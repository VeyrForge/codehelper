package mcpsvc

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/connections"
)

func TestConnectionsBriefFor_ReportsWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	var c connections.Config
	if err := c.AddSSHHost(connections.SSHHost{Name: "bastion", Hostname: "b.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddDatabase(connections.DBConn{
		Name: "pg", Driver: "postgres", Host: "127.0.0.1", Database: "app",
		PasswordRef: "env:SECRET", SSHTunnel: "bastion", ReadOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := connections.Save(root, c); err != nil {
		t.Fatal(err)
	}

	brief := connectionsBriefFor(root)
	if brief == nil {
		t.Fatal("expected a brief when profiles exist")
	}
	if len(brief.Databases) != 1 || len(brief.SSHHosts) != 1 {
		t.Fatalf("unexpected brief: %+v", brief)
	}
	db := brief.Databases[0]
	if db.Name != "pg" || db.Driver != "postgres" || !db.ReadOnly || db.SSHTunnel != "bastion" {
		t.Fatalf("db brief wrong: %+v", db)
	}
	// The brief type has no password field at all — this is the secret-safety guard.
	// (dbConnBrief intentionally omits PasswordRef.)
}

func TestConnectionsBriefFor_NilWhenUnconfigured(t *testing.T) {
	if connectionsBriefFor(t.TempDir()) != nil {
		t.Fatal("expected nil brief when no connections configured")
	}
}
