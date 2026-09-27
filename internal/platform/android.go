package platform

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// Android (Termux) support.
//
// The Android release is this module built with GOOS=android and no cgo: a
// position-independent executable naming Android's system linker as its
// interpreter. That is the one shape Termux can start where the app may not
// execute the files of its own data directory - Android 10 and later, for a
// Termux that targets them, such as the Google Play build. There termux-exec
// runs every program as `/system/bin/linker64 <path> args`, and the linker
// refuses the static Linux build with `has unexpected e_type: 2`.
//
// Termux patches its own Go packages for what a Go program meets on Android.
// A binary built outside Termux carries none of those patches, so this file
// makes the same adjustments at run time:
//
//   - Started through the linker, a Go program reads the linker's arguments:
//     Bionic hides the extra one from main, but Go does not go through
//     Bionic. The android init drops it (linkerLaunch) and remembers the path
//     of the binary, which /proc/self/exe no longer names (Executable).
//   - Go starts a program with the execve system call, which termux-exec
//     cannot intercept. AdaptCommand does what it would have done.
//   - Without cgo the Go resolver reads /etc/resolv.conf, which Android does
//     not have, and asks 127.0.0.1:53, where nothing listens. The resolver is
//     pointed at the nameservers of Termux's resolv.conf (useNameservers).
//   - Root certificates and the temporary directory are looked up where
//     Termux keeps them (useTermuxFiles).

const (
	// termuxDefaultPrefix is where the Termux app keeps its packages when the
	// environment names no other prefix.
	termuxDefaultPrefix = "/data/data/com.termux/files/usr"

	// envTermuxProcSelfExe is the path of the program a process was started
	// for, set by termux-exec (and by AdaptCommand) when /proc/self/exe names
	// the system linker instead.
	envTermuxProcSelfExe = "TERMUX_EXEC__PROC_SELF_EXE"

	androidLinker64 = "/system/bin/linker64"
	androidLinker32 = "/system/bin/linker"

	// shebangMax is how much of a file the kernel reads for its #! line.
	shebangMax = 256
)

// fallbackNameservers are the servers Termux's resolv-conf package ships with.
var fallbackNameservers = []string{"8.8.8.8:53", "8.8.4.4:53"}

// Set by the android init when the system linker started this process.
var (
	androidSelf       string // the path of the running binary
	androidLinkerExec bool   // programs in the app data directory go through the linker too
)

var (
	androidHostOnce  sync.Once
	androidHostValue androidHost
)

// Executable returns the path of the running binary: os.Executable, except
// on Android when the system linker started this process, since /proc/self/exe
// names the linker then.
func Executable() (string, error) {
	if androidSelf != "" {
		return androidSelf, nil
	}
	return os.Executable()
}

// AdaptCommand fits cmd to the way this host starts programs. Every place that
// builds an exec.Cmd calls it once Path, Args, Dir and Env are final and
// before Start. It does nothing anywhere but Android.
//
// On Android it does what termux-exec does for Termux's own programs:
//   - a program or a script interpreter named by its Linux path, /bin/... or
//     /usr/bin/..., is taken from the Termux prefix, so a script starting
//     with #!/usr/bin/env runs;
//   - when this process was started through the system linker, a program in
//     the app data directory is started that way too, and a script there
//     through its interpreter, because Android refuses to execute either
//     directly for this app. The child finds its own path in
//     TERMUX_EXEC__PROC_SELF_EXE.
func AdaptCommand(cmd *exec.Cmd) {
	if runtime.GOOS != "android" || cmd == nil {
		return
	}
	currentAndroidHost().adapt(cmd)
}

// androidHost is what AdaptCommand needs to know about the device.
type androidHost struct {
	prefix     string   // the Termux prefix
	dataDirs   []string // the app data directory, under both its names
	linkerExec bool     // this process was started through the system linker
}

func currentAndroidHost() androidHost {
	androidHostOnce.Do(func() {
		prefix := termuxPrefix(os.Getenv)
		androidHostValue = androidHost{
			prefix:     prefix,
			dataDirs:   termuxAppDataDirs(os.Getenv, prefix),
			linkerExec: androidLinkerExec,
		}
	})
	return androidHostValue
}

// termuxPrefix returns the Termux prefix: TERMUX__PREFIX, which the app
// exports from 0.119.0 on, then PREFIX, then the app's default.
func termuxPrefix(getenv func(string) string) string {
	for _, name := range []string{"TERMUX__PREFIX", "PREFIX"} {
		if v := getenv(name); filepath.IsAbs(v) {
			return filepath.Clean(v)
		}
	}
	return termuxDefaultPrefix
}

// termuxAppDataDirs returns the app data directory under the names Termux
// exports (from 0.119.0 on), and the one the prefix sits in:
// <data dir>/files/usr. These are the directories termux-exec starts programs
// from through the linker.
func termuxAppDataDirs(getenv func(string) string, prefix string) []string {
	var dirs []string
	add := func(dir string) {
		if !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	add(getenv("TERMUX_APP__DATA_DIR"))
	add(getenv("TERMUX_APP__LEGACY_DATA_DIR"))
	if dir, ok := strings.CutSuffix(prefix, "/files/usr"); ok {
		add(dir)
	}
	return dirs
}

// isSystemLinker reports whether path is Android's dynamic linker, which is
// what /proc/self/exe names for a program the linker was asked to run.
func isSystemLinker(path string) bool {
	switch filepath.Base(path) {
	case "linker64", "linker":
	default:
		return false
	}
	return strings.HasPrefix(path, "/system/") || strings.HasPrefix(path, "/apex/")
}

// linkerLaunch recognises a process the system linker started as a program:
// exe is what /proc/self/exe names and args are the arguments the kernel
// handed over. termux-exec runs `/system/bin/linker64` with the caller's
// argv[0], the path of the program and the program's arguments, and a Go
// program reads all of them. linkerLaunch returns the arguments the program
// should see - its path, then its own arguments - the path, absolute, and
// whether the linker started it at all.
func linkerLaunch(exe string, args []string, cwd string) ([]string, string, bool) {
	if !isSystemLinker(exe) || len(args) < 2 || args[1] == "" {
		return args, "", false
	}
	self := args[1]
	if !filepath.IsAbs(self) {
		self = filepath.Join(cwd, self)
	}
	return args[1:], filepath.Clean(self), true
}

// useTermuxFiles points what the Go runtime looks for at Linux paths to
// Termux's copies, as Termux patches its own Go: the CA bundle of the
// ca-certificates package and the prefix's tmp directory (Go's Android default
// is /data/local/tmp, which an app cannot write). A value the user exported
// stays.
func useTermuxFiles(prefix string, getenv func(string) string, setenv func(string, string) error) {
	if getenv("SSL_CERT_FILE") == "" {
		if bundle := filepath.Join(prefix, "etc", "tls", "cert.pem"); isRegularFile(bundle) {
			_ = setenv("SSL_CERT_FILE", bundle)
		}
	}
	if getenv("TMPDIR") == "" {
		if tmp := filepath.Join(prefix, "tmp"); isDir(tmp) {
			_ = setenv("TMPDIR", tmp)
		}
	}
}

// termuxNameservers returns the nameservers of the prefix's resolv.conf, the
// file every resolver in Termux reads, or the servers that file ships with
// when it is missing or names none.
func termuxNameservers(prefix string) []string {
	if data, err := os.ReadFile(filepath.Join(prefix, "etc", "resolv.conf")); err == nil {
		if servers := parseNameservers(data); len(servers) > 0 {
			return servers
		}
	}
	return slices.Clone(fallbackNameservers)
}

// parseNameservers returns host:port for every nameserver line of a
// resolv.conf that names an IP address.
func parseNameservers(data []byte) []string {
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		if err != nil {
			continue
		}
		servers = append(servers, net.JoinHostPort(addr.String(), "53"))
	}
	return servers
}

// dialFunc is the signature of net.Resolver.Dial.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// useNameservers makes r send the queries the Go resolver addresses to its
// built-in defaults - 127.0.0.1:53 and [::1]:53, all it has without
// /etc/resolv.conf - to servers, through dial.
func useNameservers(r *net.Resolver, servers []string, dial dialFunc) {
	if len(servers) == 0 {
		return
	}
	r.PreferGo = true
	r.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dial(ctx, network, nameserverFor(address, servers))
	}
}

// nameserverFor maps one of the Go resolver's default nameservers to the
// configured one in its place; any other address stays.
func nameserverFor(address string, servers []string) string {
	switch address {
	case "127.0.0.1:53":
		return servers[0]
	case "[::1]:53":
		return servers[1%len(servers)]
	}
	return address
}

// adapt rewrites cmd for the device; see AdaptCommand. A command it cannot
// read - a lookup that failed, a missing or unreadable file, no ELF and no
// shebang - is left for Start to report as it is.
func (h androidHost) adapt(cmd *exec.Cmd) {
	if cmd.Err != nil || cmd.Path == "" {
		return
	}
	argv := cmd.Args
	if len(argv) == 0 {
		argv = []string{cmd.Path}
	}
	program := h.hostPath(absolutePath(cmd.Path, cmd.Dir))
	header, ok := readExecutableHeader(program)
	if !ok {
		return
	}

	if bytes.HasPrefix(header, []byte("\x7fELF")) {
		if h.linkerExec && h.inDataDir(program) {
			cmd.Path = linkerFor(header)
			cmd.Args = append([]string{argv[0], program}, argv[1:]...)
			setChildSelf(cmd, program)
			return
		}
		cmd.Path = program
		setChildSelf(cmd, "")
		return
	}

	shebang, ok := parseShebang(header)
	if !ok {
		return
	}
	interpreter := h.hostPath(absolutePath(shebang.interpreter, cmd.Dir))
	tail := []string{}
	if shebang.arg != "" {
		tail = append(tail, shebang.arg)
	}
	tail = append(append(tail, program), argv[1:]...)

	switch {
	case h.linkerExec && h.inDataDir(interpreter):
		interpreterHeader, ok := readExecutableHeader(interpreter)
		if !ok || !bytes.HasPrefix(interpreterHeader, []byte("\x7fELF")) {
			return
		}
		cmd.Path = linkerFor(interpreterHeader)
		cmd.Args = append([]string{shebang.interpreter, interpreter}, tail...)
		setChildSelf(cmd, program)
	case (h.linkerExec && h.inDataDir(program)) || interpreter != shebang.interpreter:
		// Android refuses to execute the script file itself, or the kernel
		// would not find the interpreter it names: start the interpreter.
		cmd.Path = interpreter
		cmd.Args = append([]string{shebang.interpreter}, tail...)
		setChildSelf(cmd, "")
	default:
		cmd.Path = program
		setChildSelf(cmd, "")
	}
}

// hostPath maps a program named by its Linux path, /bin/... or /usr/bin/..., to
// the Termux build of it in the prefix, when there is one.
func (h androidHost) hostPath(path string) string {
	for _, dir := range []string{"/usr/bin/", "/bin/"} {
		rest, ok := strings.CutPrefix(path, dir)
		if !ok || rest == "" {
			continue
		}
		if candidate := filepath.Join(h.prefix, "bin", rest); isRegularFile(candidate) {
			return candidate
		}
		return path
	}
	return path
}

func (h androidHost) inDataDir(path string) bool {
	for _, dir := range h.dataDirs {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// linkerFor picks the linker for the class of an ELF header: a 32-bit program
// on a 64-bit device needs the 32-bit linker.
func linkerFor(header []byte) string {
	if len(header) > 4 && header[4] == 1 {
		return androidLinker32
	}
	return androidLinker64
}

// setChildSelf names the child's own path in its environment when the linker
// starts it, and otherwise drops the variable, which would still name
// whatever program this process was started for.
func setChildSelf(cmd *exec.Cmd, self string) {
	env := cmd.Env
	if env == nil {
		if self == "" {
			if _, set := os.LookupEnv(envTermuxProcSelfExe); !set {
				return
			}
		}
		env = cmd.Environ()
	}
	kept := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, envTermuxProcSelfExe+"=") {
			kept = append(kept, kv)
		}
	}
	if self != "" {
		kept = append(kept, envTermuxProcSelfExe+"="+self)
	}
	if len(kept) == len(env) && self == "" {
		return
	}
	cmd.Env = kept
}

type shebangLine struct {
	interpreter string // as the script names it
	arg         string // the optional single argument after it
}

// parseShebang reads the #! line of a script the way Linux does: the
// interpreter, then everything up to the end of the line as one argument.
func parseShebang(header []byte) (shebangLine, bool) {
	rest, ok := bytes.CutPrefix(header, []byte("#!"))
	if !ok {
		return shebangLine{}, false
	}
	line, _, found := bytes.Cut(rest, []byte("\n"))
	if !found {
		return shebangLine{}, false
	}
	text := strings.TrimSpace(strings.TrimSuffix(string(line), "\r"))
	interpreter, arg, _ := strings.Cut(text, " ")
	if interpreter == "" {
		return shebangLine{}, false
	}
	return shebangLine{interpreter: interpreter, arg: strings.TrimSpace(arg)}, true
}

// readExecutableHeader returns the first bytes of a regular file someone may
// execute, or false for anything Start should be left to report.
func readExecutableHeader(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, shebangMax)
	n, err := io.ReadFull(f, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, false
	}
	return header[:n], true
}

// absolutePath resolves a relative program path the way the child will: from
// its working directory, dir, or this process's when dir is empty.
func absolutePath(path, dir string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	base := dir
	if !filepath.IsAbs(base) {
		cwd, err := os.Getwd()
		if err != nil {
			return path
		}
		base = filepath.Join(cwd, base)
	}
	return filepath.Join(base, path)
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
