package connections

import "testing"

func TestAddAlias_LocalArgv(t *testing.T) {
	var c Config
	if err := c.AddAlias(CommandAlias{Name: "test", Argv: []string{"go", "test", "./..."}}); err != nil {
		t.Fatal(err)
	}
	a := c.FindAlias("test")
	if a == nil || len(a.Argv) != 3 {
		t.Fatalf("alias missing: %+v", a)
	}
}

func TestAddAlias_RemoteRequiresHostAndRecipe(t *testing.T) {
	var c Config
	if err := c.AddSSHHost(SSHHost{Name: "prod", Hostname: "prod.example.com", AllowedCommands: []string{"tail"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddRecipe("prod", Recipe{Name: "tail-log", Argv: []string{"tail", "-n", "50", "/var/log/syslog"}, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddAlias(CommandAlias{Name: "logs", RemoteHost: "prod", RemoteRecipe: "tail-log"}); err != nil {
		t.Fatal(err)
	}
	if c.FindAlias("logs") == nil {
		t.Fatal("remote alias not stored")
	}
}

func TestRemoveAlias(t *testing.T) {
	var c Config
	_ = c.AddAlias(CommandAlias{Name: "x", Argv: []string{"echo", "hi"}})
	if !c.RemoveAlias("x") {
		t.Fatal("remove failed")
	}
	if c.FindAlias("x") != nil {
		t.Fatal("alias still present")
	}
}
