//go:build android && !cgo

package platform

import (
	"net"
	"os"
)

// init gives the Go resolver the nameservers of Termux's resolv.conf. A cgo
// build resolves through Bionic, which asks Android itself, and needs none of
// this; so does a device that has an /etc/resolv.conf after all.
func init() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}
	var dialer net.Dialer
	useNameservers(net.DefaultResolver, termuxNameservers(termuxPrefix(os.Getenv)), dialer.DialContext)
}
