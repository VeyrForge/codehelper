package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/connections"
)

func TestQueryDB_RequiresReadOnlyConnection(t *testing.T) {
	root := t.TempDir()
	cfg := connections.Config{
		Databases: []connections.DBConn{{Name: "rw", Driver: "sqlite", Database: "x.db", ReadOnly: false}},
	}
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := QueryDB(context.Background(), root, "rw", "SELECT 1", 10)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only error, got %v", err)
	}
}

func TestExecRecipe_RequiresReadOnlyRecipe(t *testing.T) {
	root := t.TempDir()
	var cfg connections.Config
	_ = cfg.AddSSHHost(connections.SSHHost{Name: "h", Hostname: "h.example.com", AllowedCommands: []string{"tail"}})
	_ = cfg.AddRecipe("h", connections.Recipe{Name: "restart", Argv: []string{"tail", "-n", "5", "/var/log/syslog"}, ReadOnly: false})
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := ExecRecipe(context.Background(), root, "h", "restart", nil, 0)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only recipe error, got %v", err)
	}
}

func TestBuildSSHArgv_ProxyJump(t *testing.T) {
	cfg := connections.Config{}
	_ = cfg.AddSSHHost(connections.SSHHost{Name: "bastion", Hostname: "bastion.example.com", User: "jump", Port: 2222})
	_ = cfg.AddSSHHost(connections.SSHHost{Name: "app", Hostname: "10.0.0.5", User: "deploy", JumpHost: "bastion"})
	h := cfg.FindSSH("app")
	argv := buildSSHArgv(cfg, *h, []string{"tail", "-n", "10", "/var/log/app.log"})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-J jump@bastion.example.com:2222") {
		t.Fatalf("missing ProxyJump: %q", joined)
	}
}

func TestReadRemoteLog_MissingRecipe(t *testing.T) {
	root := t.TempDir()
	cfg := connections.Config{
		LogSources: []connections.LogSource{{Name: "nginx", Kind: "nginx", Path: "/var/log/nginx/error.log", SSHHost: "prod"}},
		SSHHosts:   []connections.SSHHost{{Name: "prod", Hostname: "prod.example.com", AllowedCommands: []string{"tail"}}},
	}
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := ReadLog(context.Background(), root, "nginx", 50)
	if err == nil || !strings.Contains(err.Error(), "no tail recipe") {
		t.Fatalf("expected missing recipe error, got %v", err)
	}
}
