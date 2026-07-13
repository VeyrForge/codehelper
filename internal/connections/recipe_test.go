package connections

import "testing"

func TestExpandRecipe_SubstitutesParams(t *testing.T) {
	r := Recipe{
		Name: "tail", Argv: []string{"tail", "-n", "{lines}", "{path}"}, Params: []string{"lines", "path"},
	}
	argv, err := ExpandRecipe(r, map[string]string{"lines": "100", "path": "/var/log/app.log"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tail", "-n", "100", "/var/log/app.log"}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, argv[i], want[i])
		}
	}
}

func TestExpandRecipe_RejectsUnsafeParam(t *testing.T) {
	r := Recipe{Name: "tail", Argv: []string{"tail", "{path}"}}
	if _, err := ExpandRecipe(r, map[string]string{"path": "/tmp/x; rm -rf /"}); err == nil {
		t.Fatal("expected unsafe param rejected")
	}
}

func TestAddRecipe_UpsertsOnHost(t *testing.T) {
	var c Config
	if err := c.AddSSHHost(SSHHost{Name: "prod", Hostname: "prod.example.com", AllowedCommands: []string{"tail"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddRecipe("prod", Recipe{Name: "tail-log", Argv: []string{"tail", "-n", "{lines}", "{path}"}, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	h, r := c.FindRecipe("prod", "tail-log")
	if h == nil || r == nil {
		t.Fatal("recipe not found")
	}
	if err := c.AddRecipe("prod", Recipe{Name: "tail-log", Argv: []string{"tail", "-n", "50", "/var/log/syslog"}, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	_, r2 := c.FindRecipe("prod", "tail-log")
	if len(r2.Argv) != 4 || r2.Argv[2] != "50" {
		t.Fatalf("upsert failed: %+v", r2.Argv)
	}
}
