package connections

import "testing"

func TestAddAndLoad_MultipleDBAndSSH(t *testing.T) {
	root := t.TempDir()
	var c Config
	if err := c.AddSSHHost(SSHHost{Name: "bastion", Hostname: "bastion.example.com", User: "deploy"}); err != nil {
		t.Fatalf("AddSSHHost: %v", err)
	}
	if err := c.AddDatabase(DBConn{Name: "staging_pg", Driver: "postgresql", Host: "127.0.0.1", PasswordRef: "env:PG", SSHTunnel: "bastion"}); err != nil {
		t.Fatalf("AddDatabase: %v", err)
	}
	if err := c.AddDatabase(DBConn{Name: "analytics", Driver: "mysql"}); err != nil {
		t.Fatalf("AddDatabase: %v", err)
	}
	if got := c.Databases[1].Driver; got != "postgres" {
		t.Fatalf("driver alias not canonicalized: got %q", got)
	}
	if err := Save(root, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Databases) != 2 || len(got.SSHHosts) != 1 {
		t.Fatalf("round trip lost profiles: %+v", got)
	}
}

func TestAddDatabase_UpsertByName(t *testing.T) {
	var c Config
	_ = c.AddDatabase(DBConn{Name: "db", Driver: "mysql", Host: "old"})
	_ = c.AddDatabase(DBConn{Name: "DB", Driver: "mysql", Host: "new"}) // case-insensitive same name
	if len(c.Databases) != 1 {
		t.Fatalf("expected upsert to one entry, got %d", len(c.Databases))
	}
	if c.Databases[0].Host != "new" {
		t.Fatalf("upsert did not replace: %+v", c.Databases[0])
	}
}

func TestAddDatabase_RejectsInlineSecret(t *testing.T) {
	var c Config
	err := c.AddDatabase(DBConn{Name: "db", Driver: "mysql", PasswordRef: "hunter2"})
	if err == nil {
		t.Fatal("expected inline secret to be rejected")
	}
}

func TestAddDatabase_RejectsUnknownDriverAndTunnel(t *testing.T) {
	var c Config
	if err := c.AddDatabase(DBConn{Name: "db", Driver: "nope-sql"}); err == nil {
		t.Fatal("expected unknown driver rejected")
	}
	if err := c.AddDatabase(DBConn{Name: "db", Driver: "mysql", SSHTunnel: "ghost"}); err == nil {
		t.Fatal("expected unknown ssh_tunnel rejected")
	}
}

func TestLoad_AbsentIsEmptyNotError(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("absent config should not error: %v", err)
	}
	if !got.Empty() {
		t.Fatalf("expected empty config, got %+v", got)
	}
}

func TestRemove(t *testing.T) {
	var c Config
	_ = c.AddSSHHost(SSHHost{Name: "h", Hostname: "x"})
	_ = c.AddDatabase(DBConn{Name: "d", Driver: "mysql"})
	if !c.Remove("h") || !c.Remove("d") {
		t.Fatal("Remove should report matches")
	}
	if !c.Empty() {
		t.Fatalf("expected empty after removals, got %+v", c)
	}
	if c.Remove("missing") {
		t.Fatal("Remove of missing name should report false")
	}
}
