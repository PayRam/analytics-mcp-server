package listen

import (
	"net"
	"strings"
	"testing"
)

// Each server passes its own setting (empty when unset) and default port.
func TestAddrDefaultsToLoopbackForEveryServer(t *testing.T) {
	cases := []struct {
		server string
		port   string
		want   string
	}{
		{server: "mcp-server (MCP_HTTP_ADDR / -http)", port: "3333", want: "127.0.0.1:3333"},
		{server: "chat-api (CHAT_API_ADDR, port from CHAT_API_PORT)", port: "2358", want: "127.0.0.1:2358"},
		{server: "agent (PAYRAM_AGENT_LISTEN_ADDR)", port: "9900", want: "127.0.0.1:9900"},
		{server: "port written with a colon", port: ":3333", want: "127.0.0.1:3333"},
	}
	for _, tc := range cases {
		got := Addr("", tc.port)
		if got != tc.want {
			t.Fatalf("%s: Addr(\"\", %q) = %q, want %q", tc.server, tc.port, got, tc.want)
		}
		if !IsLoopback(got) {
			t.Fatalf("%s: default %q is not loopback", tc.server, got)
		}
	}
}

func TestAddrKeepsAnExplicitChoice(t *testing.T) {
	if got := Addr(" 0.0.0.0:3333 ", "3333"); got != "0.0.0.0:3333" {
		t.Fatalf("explicit address not kept: %q", got)
	}
}

func TestDefaultAddrBindsOnlyLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", Addr("", "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected addr type %T", ln.Addr())
	}
	if !tcp.IP.IsLoopback() {
		t.Fatalf("default listener bound to %s, want a loopback IP", tcp.IP)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:3333":  true,
		"127.0.0.2:3333":  true,
		"[::1]:3333":      true,
		"localhost:3333":  true,
		":3333":           false,
		"0.0.0.0:3333":    false,
		"[::]:3333":       false,
		"172.17.0.2:3333": false,
		"10.0.0.5:3333":   false,
		"example.com:80":  false,
		"3333":            false,
		"":                false,
	}
	for addr, want := range cases {
		if got := IsLoopback(addr); got != want {
			t.Fatalf("IsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestExposureWarning(t *testing.T) {
	if w := ExposureWarning("MCP server", "127.0.0.1:3333"); w != "" {
		t.Fatalf("loopback address must not warn, got %q", w)
	}
	for _, addr := range []string{":3333", "0.0.0.0:3333", "172.17.0.2:3333"} {
		w := ExposureWarning("MCP server", addr)
		if !strings.Contains(w, "MCP server listens on "+addr) {
			t.Fatalf("warning for %q does not name the server and address: %q", addr, w)
		}
	}
}

func TestLocalDialAddr(t *testing.T) {
	cases := map[string]string{
		":9900":          "127.0.0.1:9900",
		"0.0.0.0:9900":   "127.0.0.1:9900",
		"[::]:9900":      "127.0.0.1:9900",
		"127.0.0.1:9900": "127.0.0.1:9900",
		"10.0.0.5:9900":  "10.0.0.5:9900",
		"9900":           "127.0.0.1:9900",
	}
	for addr, want := range cases {
		if got := LocalDialAddr(addr); got != want {
			t.Fatalf("LocalDialAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}
