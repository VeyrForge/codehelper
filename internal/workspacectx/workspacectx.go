// Package workspacectx builds MCP workspace-scoped contexts for multi-repo tooling.
package workspacectx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"runtime"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// WithRoots returns a context whose MCP session advertises the given workspace roots.
func WithRoots(roots ...string) context.Context {
	srv := server.NewMCPServer("codehelper-workspace", "0")
	mcpRoots := make([]mcp.Root, 0, len(roots))
	for _, root := range roots {
		mcpRoots = append(mcpRoots, mcp.Root{URI: fileURI(root)})
	}
	id := sessionIDForRoots(roots...)
	return srv.WithContext(context.Background(), &rootsSession{roots: mcpRoots, id: id})
}

func sessionIDForRoots(roots ...string) string {
	h := sha256.New()
	for _, r := range roots {
		h.Write([]byte(filepath.Clean(r)))
		h.Write([]byte{0})
	}
	return "ws-" + hex.EncodeToString(h.Sum(nil))[:16]
}

type rootsSession struct {
	roots []mcp.Root
	ch    chan mcp.JSONRPCNotification
	id    string
}

func (s *rootsSession) Initialize()       {}
func (s *rootsSession) Initialized() bool { return true }
func (s *rootsSession) SessionID() string {
	if s.id != "" {
		return s.id
	}
	return "workspace-session"
}
func (s *rootsSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if s.ch == nil {
		s.ch = make(chan mcp.JSONRPCNotification, 1)
	}
	return s.ch
}
func (s *rootsSession) ListRoots(_ context.Context, _ mcp.ListRootsRequest) (*mcp.ListRootsResult, error) {
	return &mcp.ListRootsResult{Roots: s.roots}, nil
}

func fileURI(p string) string {
	slash := filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" && len(slash) >= 2 && slash[1] == ':' {
		slash = "/" + slash
	}
	return (&url.URL{Scheme: "file", Path: slash}).String()
}
