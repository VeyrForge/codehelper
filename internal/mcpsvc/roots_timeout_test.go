package mcpsvc

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// blockingRootsSession's ListRoots blocks until the context is cancelled,
// simulating a client that advertises the roots capability but never answers the
// server→client request — the exact failure that hung tool calls.
type blockingRootsSession struct {
	id     string
	called atomic.Bool
}

func (s *blockingRootsSession) Initialize()       {}
func (s *blockingRootsSession) Initialized() bool { return true }
func (s *blockingRootsSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return make(chan mcp.JSONRPCNotification, 1)
}
func (s *blockingRootsSession) SessionID() string {
	if s.id == "" {
		s.id = "blocking-session-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
	}
	return s.id
}
func (s *blockingRootsSession) ListRoots(ctx context.Context, _ mcp.ListRootsRequest) (*mcp.ListRootsResult, error) {
	s.called.Store(true)
	<-ctx.Done()
	return nil, ctx.Err()
}

func ctxForSession(sess server.ClientSession) context.Context {
	srv := server.NewMCPServer("codehelper-test", "0")
	return srv.WithContext(context.Background(), sess)
}

func TestListRootsTimesOutInsteadOfHanging(t *testing.T) {
	t.Setenv("CODEHELPER_ROOTS_TIMEOUT_MS", "100")
	sess := &blockingRootsSession{}
	ctx := ctxForSession(sess)
	sid := sess.SessionID()
	sessionRootsCap.Store(sid, true) // client advertised roots
	t.Cleanup(func() { sessionRootsCap.Delete(sid); rootsCache.Delete(sid) })

	start := time.Now()
	roots, ok := mcpWorkspaceRoots(ctx)
	elapsed := time.Since(start)

	if ok || len(roots) != 0 {
		t.Fatalf("expected no roots on timeout, got ok=%v roots=%v", ok, roots)
	}
	if !sess.called.Load() {
		t.Fatal("expected ListRoots to be attempted for a roots-capable client")
	}
	if elapsed > time.Second {
		t.Fatalf("ListRoots should have timed out near 100ms, took %v", elapsed)
	}
}

func TestListRootsSkippedWhenCapabilityNotAdvertised(t *testing.T) {
	sess := &blockingRootsSession{}
	ctx := ctxForSession(sess)
	sid := sess.SessionID()
	sessionRootsCap.Store(sid, false) // client did NOT advertise roots
	t.Cleanup(func() { sessionRootsCap.Delete(sid); rootsCache.Delete(sid) })

	roots, ok := mcpWorkspaceRoots(ctx)
	if ok || len(roots) != 0 {
		t.Fatalf("expected roots skipped, got ok=%v roots=%v", ok, roots)
	}
	if sess.called.Load() {
		t.Fatal("ListRoots must not be issued when the client never advertised roots")
	}
}
