package usage

import (
	"strings"
	"sync"
	"time"
)

// Recorder holds the small amount of per-session state the MCP hook needs to
// turn a tool call into an Event: which client opened the session, when each
// in-flight call started (for latency), and a cache of the resolved project root
// so repo resolution runs once per session, not once per call.
//
// It is transport-agnostic and dependency-free on purpose — the mcpsvc package
// drives it from server hooks (see internal/mcpsvc/usage_hook.go) so this package
// never imports the MCP server.
type Recorder struct {
	clients   sync.Map // session -> client name
	starts    sync.Map // callKey -> time.Time
	repoCache sync.Map // session|repoArg -> repoRoot
	once      sync.Map // "session|key" -> struct{} : one-time-per-session flags
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// SetClient records the client name advertised at initialize for a session.
func (r *Recorder) SetClient(session, client string) {
	if session == "" {
		return
	}
	r.clients.Store(session, client)
}

// Client returns the client name for a session, or "unknown".
func (r *Recorder) Client(session string) string {
	if v, ok := r.clients.Load(session); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return "unknown"
}

// Begin marks the start of an in-flight call keyed by callKey.
func (r *Recorder) Begin(callKey string, now time.Time) {
	r.starts.Store(callKey, now)
}

// Elapsed returns milliseconds since Begin for callKey and clears the entry.
// Returns 0 if the start was never recorded.
func (r *Recorder) Elapsed(callKey string, now time.Time) int64 {
	v, ok := r.starts.LoadAndDelete(callKey)
	if !ok {
		return 0
	}
	t0, _ := v.(time.Time)
	if t0.IsZero() {
		return 0
	}
	return now.Sub(t0).Milliseconds()
}

// RepoRoot returns the cached project root for (session, repoArg), resolving it
// via resolve() exactly once and caching the result so the potentially
// roots-round-tripping resolution does not run on every tool call.
func (r *Recorder) RepoRoot(session, repoArg string, resolve func() string) string {
	key := session + "|" + repoArg
	if v, ok := r.repoCache.Load(key); ok {
		s, _ := v.(string)
		return s
	}
	root := resolve()
	r.repoCache.Store(key, root)
	return root
}

// MarkOnce returns true the first time it is called for (session, key) and false
// on every call after, until the session is Forgotten. It lets a handler emit a
// constant piece of guidance once per session instead of re-sending it on every
// tool call. An empty session is treated as never-seen (always true), so a
// sessionless transport keeps the guidance rather than silently dropping it.
func (r *Recorder) MarkOnce(session, key string) bool {
	if session == "" {
		return true
	}
	_, loaded := r.once.LoadOrStore(session+"|"+key, struct{}{})
	return !loaded
}

// Forget drops cached state for a closed session.
func (r *Recorder) Forget(session string) {
	r.clients.Delete(session)
	prefix := session + "|"
	dropByPrefix := func(m *sync.Map) {
		m.Range(func(k, _ any) bool {
			if ks, _ := k.(string); strings.HasPrefix(ks, prefix) {
				m.Delete(k)
			}
			return true
		})
	}
	dropByPrefix(&r.repoCache)
	dropByPrefix(&r.once)
}
