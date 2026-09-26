package session

// The configured MCP servers of a session are dialed here: the trust-gated
// target list, the concurrent dial with one timeout per server, the log
// lines and the warnings a settings reload reports. Session creation, the
// subagent spawn, the console's background connect and the hot reloads all
// reach a spawn through these helpers, so none of them can start a server
// the trust gate did not admit, and none of them waits on one server longer
// than its own bound, whatever its transport: a stdio command that never
// answers initialize and a remote URL that accepts the connection and stays
// silent cost the same.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/EvilFreelancer/coddy-agent/internal/config"
	"github.com/EvilFreelancer/coddy-agent/internal/mcp"
)

// defaultMCPConnectTimeout bounds one server's spawn (or request) and
// handshake. It is what a server that starts and never answers initialize
// costs the session: the others keep connecting beside it, and a turn
// waiting for the tool list waits at most this long. It stays below
// mcpStartTimeout and mcpReloadTimeout, the deadlines a whole dial shares,
// so a hung server fails on its own bound instead of running the shared
// deadline out: a server the deadline cut short is parked and dialed again
// at the session's next turn, while one that failed on its own bound is not,
// and a server that never answers would otherwise cost every turn the full
// deadline.
const defaultMCPConnectTimeout = 20 * time.Second

// mcpDialTarget is one server to connect: its declaration and the connect
// call, which goes through the trust gate for configured servers so nothing
// spawns that the gate did not admit right before the spawn.
type mcpDialTarget struct {
	Server  mcp.ManagedServer
	Connect func(ctx context.Context) (*mcp.Client, error)
}

// mcpDialResult is what one target's dial ended with.
type mcpDialResult struct {
	Target mcpDialTarget
	Client *mcp.Client
	Err    error
}

// mcpHeldServer is a configured server the trust gate refused to start: a
// project declaration the operator has not approved for this workspace.
type mcpHeldServer struct {
	Server  mcp.ManagedServer
	Blocked *mcp.BlockedError
}

// connectTimeout is the per-server dial budget; tests shorten it.
func (m *Manager) connectTimeout() time.Duration {
	if m.mcpConnectTimeout > 0 {
		return m.mcpConnectTimeout
	}
	return defaultMCPConnectTimeout
}

// configuredTarget is the dial of one configured server, through the gate.
func (m *Manager) configuredTarget(gate *mcp.TrustGate, srv mcp.ManagedServer, cwd string) mcpDialTarget {
	return mcpDialTarget{
		Server: srv,
		Connect: func(ctx context.Context) (*mcp.Client, error) {
			return gate.Connect(ctx, srv, cwd, m.log)
		},
	}
}

// configuredTargets lists the enabled configured servers (config.yaml merged
// with the two mcp.json levels) that the trust gate admits for cwd, and the
// ones it holds. The gate is evaluated here and again inside Connect, so a
// project-local .coddy/mcp.json stays cold until its exact declaration is
// approved, whichever path reaches the spawn.
func (m *Manager) configuredTargets(cfg *config.Config, cwd string) ([]mcpDialTarget, []mcpHeldServer) {
	gate := mcp.NewTrustGate(cfg)
	managed := mcp.ListManagedServersTolerant(cfg, cwd, m.log)
	targets := make([]mcpDialTarget, 0, len(managed))
	var held []mcpHeldServer
	for _, srv := range managed {
		if srv.Config.Disabled {
			continue
		}
		if err := gate.Check(cwd, srv); err != nil {
			var blocked *mcp.BlockedError
			if errors.As(err, &blocked) {
				held = append(held, mcpHeldServer{Server: srv, Blocked: blocked})
				continue
			}
			held = append(held, mcpHeldServer{Server: srv})
			continue
		}
		targets = append(targets, m.configuredTarget(gate, srv, cwd))
	}
	return targets, held
}

// dialOne connects one target under its own copy of the per-server timeout.
// A server that did not answer within it fails with an error that names the
// bound; the caller's ctx ending is reported as that ctx's error, so a
// caller can tell a dial it cut short from a server that failed.
func (m *Manager) dialOne(ctx context.Context, target mcpDialTarget) (*mcp.Client, error) {
	timeout := m.connectTimeout()
	srvCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := target.Connect(srvCtx)
	if err != nil && ctx.Err() == nil && errors.Is(srvCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("no answer to initialize within %s: %w", timeout, err)
	}
	return client, err
}

// dialConcurrently connects every target at once, each under its own copy of
// the per-server timeout, and returns the results in input order. settled is
// called once per target as it finishes, never two at a time. A ctx that is
// already done spawns nothing: an expired reload budget must not start
// processes it will only throw away.
func (m *Manager) dialConcurrently(ctx context.Context, targets []mcpDialTarget, settled func(i int, r mcpDialResult)) []mcpDialResult {
	results := make([]mcpDialResult, len(targets))
	if len(targets) == 0 {
		return results
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for i, target := range targets {
		results[i].Target = target
		if err := ctx.Err(); err != nil {
			mu.Lock()
			results[i].Err = err
			if settled != nil {
				settled(i, results[i])
			}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(i int, target mcpDialTarget) {
			defer wg.Done()
			client, err := m.dialOne(ctx, target)
			mu.Lock()
			results[i].Client, results[i].Err = client, err
			r := results[i]
			if settled != nil {
				settled(i, r)
			}
			mu.Unlock()
		}(i, target)
	}
	wg.Wait()
	return results
}

// logHeld says why a configured server was not started.
func (m *Manager) logHeld(cwd string, h mcpHeldServer) {
	if h.Blocked != nil {
		m.log.Warn("MCP server not started: project declaration is not approved for this workspace",
			"server", h.Server.Config.Name, "workspace", cwd, "state", string(h.Blocked.State),
			"digest", h.Blocked.Digest, "approve_with", "coddy mcp trust "+h.Server.Config.Name)
		return
	}
	m.log.Warn("MCP server not started", "server", h.Server.Config.Name, "workspace", cwd)
}

// logDial says how one configured server's dial ended.
func (m *Manager) logDial(r mcpDialResult) {
	name := r.Target.Server.Config.Name
	if r.Err != nil {
		m.log.Warn("failed to connect MCP server", "server", name, "error", r.Err)
		return
	}
	m.log.Info("connected MCP server", "name", name,
		"transport", mcp.EffectiveTransport(r.Target.Server.Config), "tools", len(r.Client.Tools()))
}

// dialConfiguredFor connects the enabled configured servers of cfg the trust
// gate admits for cwd, all at once, and returns the clients in configuration
// order - the tool list a model is handed keeps its order from one session
// start to the next, which the provider's prompt cache needs - with one
// warning per server that did not start. ReloadConfigForSession dials the
// configuration it is about to install, which the manager does not hold yet,
// so the config is a parameter here.
func (m *Manager) dialConfiguredFor(ctx context.Context, cfg *config.Config, cwd string) ([]*mcp.Client, []string) {
	targets, held := m.configuredTargets(cfg, cwd)
	var warnings []string
	for _, h := range held {
		m.logHeld(cwd, h)
		if h.Blocked != nil {
			warnings = append(warnings, fmt.Sprintf("connect MCP %s: %v", h.Server.Config.Name, h.Blocked))
		}
	}
	clients := make([]*mcp.Client, 0, len(targets))
	for _, r := range m.dialConcurrently(ctx, targets, nil) {
		m.logDial(r)
		if r.Err != nil {
			warnings = append(warnings, fmt.Sprintf("connect MCP %s: %v", r.Target.Server.Config.Name, r.Err))
			continue
		}
		clients = append(clients, r.Client)
	}
	return clients, warnings
}

// dialConfiguredMCPServers connects every enabled configured server the trust
// gate admits for cwd and returns the clients without attaching them to a
// session. Session creation, the subagent spawn and the settings hot reload
// all go through here, so none of them can reach a spawn without
// TrustGate.Connect: a project-local .coddy/mcp.json stays cold until its
// exact declaration is approved. The servers are dialed concurrently, so the
// call costs the slowest server, not the sum of them, and never more than
// the per-server timeout.
func (m *Manager) dialConfiguredMCPServers(ctx context.Context, cwd string) []*mcp.Client {
	clients, _ := m.dialConfiguredFor(ctx, m.activeCfg(), cwd)
	return clients
}

// connectSessionMCPServers connects the servers an ACP client sent with
// session/new or session/load, concurrently and each under the per-server
// timeout, and attaches the ones that answered to the session in the order
// the client listed them. Unlike the configured servers these survive a
// settings reload, because only that client can recreate them.
func (m *Manager) connectSessionMCPServers(ctx context.Context, st *State, decls []config.MCPServerConfig) {
	if len(decls) == 0 {
		return
	}
	targets := make([]mcpDialTarget, 0, len(decls))
	for _, srv := range decls {
		targets = append(targets, mcpDialTarget{
			Server: mcp.ManagedServer{Config: srv},
			Connect: func(ctx context.Context) (*mcp.Client, error) {
				return m.connectMCPServer(ctx, st, srv)
			},
		})
	}
	for _, r := range m.dialConcurrently(ctx, targets, nil) {
		if r.Err != nil {
			m.log.Warn("failed to connect client MCP server", "server", r.Target.Server.Config.Name, "error", r.Err)
			continue
		}
		st.AddSessionMCPClient(r.Client)
		st.RememberSessionMCPDeclaration(r.Target.Server.Config)
	}
}
