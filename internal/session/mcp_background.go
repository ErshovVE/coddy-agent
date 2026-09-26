package session

// A session's configured MCP servers can connect in the background, after
// session/new or a load from disk has returned: the progress record
// (MCPConnectUpdate), the worker that dials them, the control update the
// console renders, and the wait a turn makes for its tool list
// (WaitMCPConnect on State). Only the interactive console turns this on;
// every other surface keeps connecting a new session's servers in
// session/new and a restored session's before its first turn.

import (
	"context"

	"github.com/EvilFreelancer/coddy-agent/internal/mcp"
)

// The states of one configured MCP server while a session connects them in
// the background (MCPServerConnect.State).
const (
	MCPConnectStateConnecting = "connecting"
	MCPConnectStateConnected  = "connected"
	MCPConnectStateFailed     = "failed"
	MCPConnectStateHeld       = "held"
	// MCPConnectStateCancelled is a server whose dial a settings reload or a
	// teardown ended before it settled: the reload dials it again, so there
	// is nothing to report about it.
	MCPConnectStateCancelled = "cancelled"
)

// MCPServerConnect is one configured server's place in a background connect.
type MCPServerConnect struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Tools is how many tools the server offered once connected.
	Tools int `json:"tools,omitempty"`
	// Error says why a failed server did not connect.
	Error string `json:"error,omitempty"`
	// Hint is what the operator can do about a held server.
	Hint string `json:"hint,omitempty"`
}

// MCPConnectUpdate is the progress of a session's background MCP connect,
// sent to the surface that owns the session as a control update - never on
// the ACP wire. Done says the dial has settled for every server; the tool
// list is complete from then on.
type MCPConnectUpdate struct {
	Servers []MCPServerConnect `json:"servers"`
	Done    bool               `json:"done"`
	// Generation is the dial the snapshot describes, one higher for every
	// dial a session started and for every reload or teardown that ended
	// one. A surface drops a snapshot older than the last it applied: the
	// sender of a superseded dial may enqueue its snapshot after the
	// replacement's, and the footer would otherwise show the old count.
	Generation uint64 `json:"generation"`
}

// Counts reports how many servers are connected out of the ones that could
// be: a server the trust gate holds is not counted, since nothing is dialing
// it.
func (u MCPConnectUpdate) Counts() (connected, total int) {
	for _, s := range u.Servers {
		if s.State == MCPConnectStateHeld {
			continue
		}
		total++
		if s.State == MCPConnectStateConnected {
			connected++
		}
	}
	return connected, total
}

func (u MCPConnectUpdate) clone() MCPConnectUpdate {
	out := MCPConnectUpdate{Done: u.Done, Generation: u.Generation}
	if u.Servers != nil {
		out.Servers = append([]MCPServerConnect(nil), u.Servers...)
	}
	return out
}

// controlUpdateSender is the optional in-process surface boundary a background
// connect reports through. The console implements it; the ACP server and the
// HTTP relay do not, and they never connect in the background either.
type controlUpdateSender interface {
	SendControlUpdate(sessionID string, update any) error
}

// SetBackgroundMCPConnect makes the sessions this manager opens from now on -
// new ones and ones restored from disk - connect their configured MCP servers
// in the background: session/new or session/load returns as soon as the
// state exists, the dial runs in a worker owned by the state, and a turn
// waits for it at admission (WaitMCPConnect). Only the console turns it on:
// it draws its first frame while the servers come up, and it opens a stored
// session only to continue it. The surfaces that promise connected servers
// when session/new returns keep that promise, and the ones that load a stored
// session to read it start nothing until its first turn.
func (m *Manager) SetBackgroundMCPConnect(on bool) {
	m.backgroundMCP.Store(on)
}

// BackgroundMCPConnect reports whether the manager connects the configured
// MCP servers of the sessions it opens in the background.
func (m *Manager) BackgroundMCPConnect() bool {
	return m.backgroundMCP.Load()
}

// connectNewSessionMCPServers is the configured-server step of a new
// session: the tool filter, then the dial - in the background when the
// manager connects there, in the caller's context otherwise.
func (m *Manager) connectNewSessionMCPServers(ctx context.Context, state *State) {
	if m.backgroundMCP.Load() {
		m.installMCPFilter(state)
		m.startBackgroundMCPConnect(state)
		return
	}
	m.connectConfiguredMCPServers(ctx, state)
}

// startBackgroundMCPConnect dials the session's configured servers in a
// worker the state owns and reports each server as it settles. The worker's
// context is the state's, not the request's: session/new has returned by the
// time most servers answer. A reload or a teardown cancels it, and a result
// that lands after either is closed rather than installed. Every server is
// held to the per-server timeout and nothing is parked for a retry: a server
// that failed is reported once, and a turn starts without it instead of
// dialing it again.
func (m *Manager) startBackgroundMCPConnect(state *State) {
	cwd := state.GetCWD()
	targets, held := m.configuredTargets(m.activeCfg(), cwd)
	servers := make([]MCPServerConnect, 0, len(targets)+len(held))
	for _, t := range targets {
		servers = append(servers, MCPServerConnect{Name: t.Server.Config.Name, State: MCPConnectStateConnecting})
	}
	for _, h := range held {
		m.logHeld(cwd, h)
		entry := MCPServerConnect{Name: h.Server.Config.Name, State: MCPConnectStateHeld}
		if h.Blocked != nil {
			entry.Error = h.Blocked.Error()
			entry.Hint = "approve it with: coddy mcp trust " + h.Server.Config.Name
		}
		servers = append(servers, entry)
	}
	gen, ctx, ok := state.beginBackgroundMCP(servers)
	if !ok {
		return
	}
	if len(targets) == 0 {
		state.finishBackgroundMCP(gen, nil)
		m.sendMCPConnectUpdate(state)
		return
	}
	m.sendMCPConnectUpdate(state)
	go func() {
		results := m.dialConcurrently(ctx, targets, func(i int, r mcpDialResult) {
			entry := MCPServerConnect{Name: r.Target.Server.Config.Name, State: MCPConnectStateConnected}
			if r.Err != nil {
				if ctx.Err() != nil {
					// Superseded: the reload or the teardown that cancelled
					// the dial is what the session runs now.
					return
				}
				entry.State, entry.Error = MCPConnectStateFailed, r.Err.Error()
			} else {
				entry.Tools = len(r.Client.Tools())
			}
			m.logDial(r)
			// The targets come first in servers, in the same order.
			if state.settleBackgroundMCP(gen, i, entry) {
				m.sendMCPConnectUpdate(state)
			}
		})
		clients := make([]*mcp.Client, 0, len(results))
		for _, r := range results {
			if r.Err == nil && r.Client != nil {
				clients = append(clients, r.Client)
			}
		}
		if state.finishBackgroundMCP(gen, clients) {
			m.sendMCPConnectUpdate(state)
		}
	}()
}

// supersedeBackgroundMCP ends a background connect still running for the
// session before something else dials its configured servers - a settings
// reload - and tells the surface, whose footer would otherwise count servers
// that nobody dials any more.
func (m *Manager) supersedeBackgroundMCP(st *State) {
	if st.cancelBackgroundMCPConnect() {
		m.sendMCPConnectUpdate(st)
	}
}

// sendMCPConnectUpdate hands the current snapshot to the surface, if it
// listens for control updates.
func (m *Manager) sendMCPConnectUpdate(state *State) {
	sender, ok := m.server.(controlUpdateSender)
	if !ok {
		return
	}
	snap, recorded := state.MCPConnectSnapshot()
	if !recorded {
		return
	}
	_ = sender.SendControlUpdate(state.GetID(), snap)
}
