// Package listen decides where this repo's HTTP servers listen. Every server
// (MCP, chat API, agent) holds or can use the merchant's PayRam dashboard
// login, so each one listens on loopback unless the operator names another
// address, and then it warns at startup.
package listen

import (
	"fmt"
	"net"
	"strings"
)

// LoopbackHost is the host every listener in this repo uses by default.
const LoopbackHost = "127.0.0.1"

// Addr returns explicit when the operator set one, otherwise the loopback
// address for port. port may be written "3333" or ":3333".
func Addr(explicit, port string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	return net.JoinHostPort(LoopbackHost, strings.TrimPrefix(strings.TrimSpace(port), ":"))
}

// IsLoopback reports whether a listen address accepts connections from this
// machine only. An empty host (":3333"), 0.0.0.0, :: and every other address
// are reachable from outside, so they are not loopback.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExposureWarning returns the startup warning for a server whose address is
// not loopback, or "" when it is.
func ExposureWarning(service, addr string) string {
	if IsLoopback(addr) {
		return ""
	}
	return fmt.Sprintf("%s listens on %s: other machines, and other containers on the same Docker network, can call it. "+
		"Use a 127.0.0.1 address unless something outside this host or container must reach it.", service, addr)
}

// LocalDialAddr turns a listen address into one a process on the same host
// can dial: ":9900", "0.0.0.0:9900" and "[::]:9900" become "127.0.0.1:9900".
func LocalDialAddr(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return Addr("", addr)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = LoopbackHost
	}
	return net.JoinHostPort(host, port)
}
