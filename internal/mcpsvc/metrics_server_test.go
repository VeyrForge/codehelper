package mcpsvc

import "testing"

func TestNormalizeMetricsAddr(t *testing.T) {
	got, err := normalizeMetricsAddr(":9090")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:9090" {
		t.Fatalf("host-less: got %q", got)
	}

	got, err = normalizeMetricsAddr("127.0.0.1:9090")
	if err != nil || got != "127.0.0.1:9090" {
		t.Fatalf("loopback: got %q err=%v", got, err)
	}

	got, err = normalizeMetricsAddr("[::1]:9090")
	if err != nil || got != "[::1]:9090" {
		t.Fatalf("IPv6 loopback: got %q err=%v", got, err)
	}

	if _, err := normalizeMetricsAddr("0.0.0.0:9090"); err == nil {
		t.Fatal("all-interfaces must be refused")
	}
	if _, err := normalizeMetricsAddr(""); err != nil {
		// empty is loopback per normalizeMCPHTTPAddr; StartMetricsServer skips empty earlier
		t.Fatalf("empty should normalize as loopback ok: %v", err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "127.0.0.2"} {
		if !IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "192.168.1.5", "example.com", ""} {
		if IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = true, want false", host)
		}
	}
}
