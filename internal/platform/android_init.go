//go:build android

package platform

import "os"

// init runs before main parses a flag: a process the system linker started
// sees its own arguments from here on, and Executable names the binary. The
// Termux files replace the Linux paths before anything opens a TLS
// connection or a temporary file.
func init() {
	exe, _ := os.Readlink("/proc/self/exe")
	cwd, _ := os.Getwd()
	if args, self, ok := linkerLaunch(exe, os.Args, cwd); ok {
		os.Args = args
		androidSelf = self
		androidLinkerExec = true
	}
	useTermuxFiles(termuxPrefix(os.Getenv), os.Getenv, os.Setenv)
}
