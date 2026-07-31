package main

import "testing"

// TestValidateServeLoopbackAddr guards the loopback-only contract for
// `codehelper serve`: the Agent HTTP API exposes workspace read/write and LLM
// credentials behind a required bearer token on mutating routes, so it must
// never accept a non-loopback bind address. IPv6 loopback ([::1]) is accepted,
// matching MCP HTTP isLoopbackHost.
func TestValidateServeLoopbackAddr(t *testing.T) {
	valid := []string{
		"127.0.0.1:0", "127.0.0.1:8080", "localhost:0", "localhost:3000",
		"[::1]:0", "[::1]:8080",
	}
	for _, addr := range valid {
		if err := validateServeLoopbackAddr(addr); err != nil {
			t.Errorf("validateServeLoopbackAddr(%q) = %v, want nil", addr, err)
		}
	}

	invalid := []string{"0.0.0.0:0", "0.0.0.0:8080", ":8080", "192.168.1.5:8080", "::1:8080", ""}
	for _, addr := range invalid {
		if err := validateServeLoopbackAddr(addr); err == nil {
			t.Errorf("validateServeLoopbackAddr(%q) = nil, want error", addr)
		}
	}
}
