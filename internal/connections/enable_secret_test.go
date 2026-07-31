package connections

import "testing"

func TestEnabledDefaultsTrueAndToggles(t *testing.T) {
	var c Config
	_ = c.AddDatabase(DBConn{Name: "db", Driver: "mysql"})
	_ = c.AddSSHHost(SSHHost{Name: "h", Hostname: "x"})
	if !c.Databases[0].Enabled() || !c.SSHHosts[0].Enabled() {
		t.Fatal("new profiles should be enabled by default (Disabled absent)")
	}
	if !c.SetEnabled("db", false) || !c.SetEnabled("h", false) {
		t.Fatal("SetEnabled should report a match")
	}
	if c.Databases[0].Enabled() || c.SSHHosts[0].Enabled() {
		t.Fatal("profiles should be disabled after SetEnabled(false)")
	}
	if c.SetEnabled("missing", true) {
		t.Fatal("SetEnabled on unknown name should report false")
	}
}

func TestSecretRefAccepted(t *testing.T) {
	var c Config
	if err := c.AddDatabase(DBConn{Name: "db", Driver: "postgres", PasswordRef: SecretRef}); err != nil {
		t.Fatalf("secret-store password_ref should be accepted: %v", err)
	}
	if !c.Databases[0].UsesSecretStore() {
		t.Fatal("UsesSecretStore should be true for the secret sentinel")
	}
}

func TestAllowedCommandsPersist(t *testing.T) {
	root := t.TempDir()
	var c Config
	h := SSHHost{Name: "ops", Hostname: "ops.example.com", AllowedCommands: []string{"journalctl", "systemctl"}}
	if err := c.AddSSHHost(h); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SSHHosts) != 1 || len(got.SSHHosts[0].AllowedCommands) != 2 {
		t.Fatalf("allowed_commands not persisted: %+v", got.SSHHosts)
	}
}
