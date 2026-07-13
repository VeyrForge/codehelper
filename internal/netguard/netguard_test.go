package netguard

import (
	"net"
	"testing"
)

func TestPolicyCheckIP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ip      string
		policy  Policy
		allowed bool
	}{
		// Cloud metadata + link-local: ALWAYS blocked, regardless of policy.
		{"169.254.169.254", Policy{AllowPrivate: true, AllowLoopback: true}, false},
		{"fe80::1", Policy{AllowPrivate: true, AllowLoopback: true}, false},
		// Private ranges: blocked by default, allowed only on opt-in.
		{"10.0.0.5", Policy{}, false},
		{"192.168.1.10", Policy{}, false},
		{"172.16.0.1", Policy{}, false},
		{"10.0.0.5", Policy{AllowPrivate: true}, true},
		// Loopback: blocked by default, allowed on opt-in.
		{"127.0.0.1", Policy{}, false},
		{"127.0.0.1", Policy{AllowLoopback: true}, true},
		{"::1", Policy{AllowLoopback: true}, true},
		// Reserved/unspecified/CGNAT: always blocked.
		{"0.0.0.0", Policy{AllowLoopback: true, AllowPrivate: true}, false},
		{"100.64.0.1", Policy{AllowPrivate: true}, false},
		// Public addresses: always allowed.
		{"8.8.8.8", Policy{}, true},
		{"1.1.1.1", Policy{}, true},
		{"2606:4700:4700::1111", Policy{}, true},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		err := c.policy.CheckIP(ip)
		if (err == nil) != c.allowed {
			t.Errorf("CheckIP(%s, %+v): allowed=%v, err=%v", c.ip, c.policy, c.allowed, err)
		}
	}
}

func TestParseAllowlist(t *testing.T) {
	t.Parallel()
	nets := parseAllowlist("169.254.169.254/32, 10.0.0.0/8 192.168.1.5, garbage, ::1")
	if len(nets) != 4 {
		t.Fatalf("parsed %d nets, want 4 (garbage skipped): %v", len(nets), nets)
	}
	if !nets[0].Contains(net.ParseIP("169.254.169.254")) {
		t.Error("metadata /32 should contain the metadata IP")
	}
	if !nets[1].Contains(net.ParseIP("10.9.9.9")) {
		t.Error("10.0.0.0/8 should contain 10.9.9.9")
	}
}

// TestOperatorAllowlistOverridesBlock proves the env escape hatch lets an
// operator permit an otherwise-blocked address (e.g. the metadata endpoint).
func TestOperatorAllowlistOverridesBlock(t *testing.T) {
	// White-box: consume the sync.Once and inject an allowlist, then restore.
	operatorAllowOnce.Do(func() {})
	saved := operatorAllow
	t.Cleanup(func() { operatorAllow = saved })

	operatorAllow = parseAllowlist("169.254.169.254/32")
	if err := (Policy{}).CheckIP(net.ParseIP("169.254.169.254")); err != nil {
		t.Errorf("operator-allowlisted metadata IP should be permitted, got %v", err)
	}
	// A different link-local address is still blocked.
	if err := (Policy{}).CheckIP(net.ParseIP("169.254.1.1")); err == nil {
		t.Error("non-allowlisted link-local should still be blocked")
	}
}

// TestControlBlocksMetadata confirms the dial-time hook rejects the metadata
// endpoint even when address carries a port.
func TestControlBlocksMetadata(t *testing.T) {
	t.Parallel()
	p := Policy{AllowLoopback: true, AllowPrivate: true}
	if err := p.control("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("control allowed cloud metadata endpoint; want blocked")
	}
	if err := p.control("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("control blocked public address: %v", err)
	}
}
