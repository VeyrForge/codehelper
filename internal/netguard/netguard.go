// Package netguard provides SSRF-resistant HTTP transport for codehelper's
// outbound fetchers (the docs engine and the web verification tool). The defense
// is applied at dial time: a Dialer.Control hook inspects the *actual* IP being
// connected to and rejects forbidden ranges. Because the check runs on every
// dial — including redirect targets — it defeats DNS rebinding and redirect-based
// SSRF that host/URL allowlists alone miss.
//
// The canonical SSRF target for an agent-driven fetcher is the cloud metadata
// endpoint (169.254.169.254, link-local); internal services live in the RFC1918
// private ranges. Those are blocked by default; loopback and private access are
// opt-in for legitimate local-dev verification.
package netguard

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Policy controls which destination IP classes a dial may reach. The zero value
// is the strictest: only public, globally-routable unicast addresses.
type Policy struct {
	AllowLoopback bool // 127.0.0.0/8, ::1 — local dev servers
	AllowPrivate  bool // RFC1918 / ULA — internal LAN services
}

// operatorAllow is an explicit allowlist of CIDRs the human operator opted into
// via CODEHELPER_NETGUARD_ALLOW (e.g. "169.254.169.254/32,10.0.0.0/8"). It is an
// intentional escape hatch for setup/self-hosting that overrides the default-deny
// — set by a person editing config/env, never reachable by the LLM through a tool
// argument. Parsed once.
var (
	operatorAllowOnce sync.Once
	operatorAllow     []*net.IPNet
)

// AllowEnvVar is the environment variable that carries the operator allowlist.
const AllowEnvVar = "CODEHELPER_NETGUARD_ALLOW"

func operatorAllowlist() []*net.IPNet {
	operatorAllowOnce.Do(func() {
		operatorAllow = parseAllowlist(os.Getenv(AllowEnvVar))
	})
	return operatorAllow
}

// parseAllowlist turns a comma/space-separated list of CIDRs or bare IPs into
// nets. Invalid entries are skipped (an operator typo must never widen access by
// accident, and must never crash the fetcher).
func parseAllowlist(s string) []*net.IPNet {
	var out []*net.IPNet
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if _, ipnet, err := net.ParseCIDR(tok); err == nil {
			out = append(out, ipnet)
			continue
		}
		if ip := net.ParseIP(tok); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

// CheckIP returns a non-nil error when the policy forbids connecting to ip.
// Link-local (incl. cloud metadata 169.254.169.254), multicast, unspecified and
// other non-global-unicast ranges are always denied — they are never a
// legitimate fetch target and are the primary SSRF exfiltration vectors — unless
// the operator has explicitly allowlisted them via CODEHELPER_NETGUARD_ALLOW.
func (p Policy) CheckIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("netguard: nil IP")
	}
	// Operator escape hatch: an explicitly allowlisted address is always permitted.
	for _, n := range operatorAllowlist() {
		if n.Contains(ip) {
			return nil
		}
	}
	switch {
	case ip.IsLoopback():
		if p.AllowLoopback {
			return nil
		}
		return fmt.Errorf("netguard: blocked loopback address %s", ip)
	case ip.IsPrivate():
		if p.AllowPrivate {
			return nil
		}
		return fmt.Errorf("netguard: blocked private address %s", ip)
	case ip.IsUnspecified(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("netguard: blocked non-routable address %s", ip)
	}
	// Catch remaining reserved IPv4 space (CGNAT, benchmarking, test-net, etc.).
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0,
			v4[0] == 100 && v4[1]&0xc0 == 64,
			v4[0] == 192 && v4[1] == 0 && v4[2] == 0,
			v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
			return fmt.Errorf("netguard: blocked reserved address %s", ip)
		}
	}
	return nil
}

// control is the Dialer.Control hook: it runs after DNS resolution with the
// concrete IP:port being dialed, so it sees the real destination even when a
// hostname rebinds to an internal address between resolution and connection.
func (p Policy) control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("netguard: unresolved dial target %q", address)
	}
	return p.CheckIP(ip)
}

// Client builds an SSRF-guarded *http.Client. insecureTLS skips certificate
// verification (local-dev only). Every dial — initial and redirect — is checked
// against the policy.
func Client(timeout time.Duration, p Policy, insecureTLS bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: p.control}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if insecureTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}
