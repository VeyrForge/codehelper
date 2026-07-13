package connections

import "testing"

func TestSetPolicy_VerifyAllowlistCapsRequest(t *testing.T) {
	p := Policy{VerifyAllowlist: []string{"go", "npm", "make"}}
	got := p.EffectiveVerifyAllowlist([]string{"go", "curl", "npm"})
	if len(got) != 2 || got[0] != "go" || got[1] != "npm" {
		t.Fatalf("intersection wrong: %v", got)
	}
}

func TestSetPolicy_RejectsBlockedVerifyAllowlist(t *testing.T) {
	var c Config
	err := c.SetPolicy(Policy{VerifyAllowlist: []string{"ssh"}})
	if err == nil {
		t.Fatal("ssh must not be allowed in verify_allowlist")
	}
}

func TestAddSSHHost_RejectsDangerousAllowlist(t *testing.T) {
	var c Config
	err := c.AddSSHHost(SSHHost{
		Name: "ops", Hostname: "ops.example.com",
		AllowedCommands: []string{"journalctl", "bash"},
	})
	if err == nil {
		t.Fatal("bash must not be allowed on SSH allowlist")
	}
}

func TestAddLogSource_AndRemove(t *testing.T) {
	root := t.TempDir()
	var c Config
	if err := c.AddLogSource(LogSource{Name: "wp", Kind: "wordpress", Path: "wp-content/debug.log"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LogSources) != 1 || got.LogSources[0].Kind != "wordpress" {
		t.Fatalf("log source not persisted: %+v", got.LogSources)
	}
	if !got.RemoveLogSource("wp") || !got.Empty() {
		t.Fatalf("expected empty after log removal, got %+v", got)
	}
}

func TestPolicy_IsConfigured(t *testing.T) {
	var empty Policy
	if empty.IsConfigured() {
		t.Fatal("default policy should not count as configured")
	}
	withGit := Policy{AllowGit: true}
	if !withGit.IsConfigured() {
		t.Fatal("allow_git should count as configured")
	}
}

func TestSetPolicy_RejectsInlineGitHubToken(t *testing.T) {
	var c Config
	err := c.SetPolicy(Policy{GitHub: &GitHubPolicy{TokenRef: "ghp_secret"}})
	if err == nil {
		t.Fatal("inline github token must be rejected")
	}
}
