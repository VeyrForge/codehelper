package retrieval

import "strings"

// Programming-verb synonym clusters. Lexical search misses the (very common)
// case where a query's verb differs from the symbol's verb — "fetch the graph"
// vs func Open, "shut down" vs Close, "obtain a lock" vs Acquire. Expanding the
// query with the cluster's other members (weighted BELOW the original terms, and
// only useful when those members actually occur in the corpus) closes that gap
// with no embedding model — the no-model form of SIRA-style query vocabulary
// enrichment (arXiv:2605.06647). Clusters are deliberately tight and verb-/
// noun-focused to avoid diluting precision.
var synonymClusters = [][]string{
	{"get", "fetch", "retrieve", "load", "read", "lookup", "query"},
	{"create", "build", "make", "new", "construct", "init", "initialize", "generate", "alloc", "allocate"},
	{"delete", "remove", "destroy", "drop", "clear", "purge", "erase", "prune"},
	{"update", "modify", "set", "change", "edit", "patch", "mutate"},
	{"open", "connect", "begin", "start", "dial"},
	{"acquire", "obtain", "grab", "claim", "hold", "take"},
	{"close", "shutdown", "stop", "end", "release", "disconnect", "teardown", "dispose", "shut"},
	{"save", "store", "persist", "write", "commit", "flush", "put"},
	{"check", "validate", "verify", "ensure", "assert", "test", "inspect"},
	{"send", "post", "publish", "emit", "dispatch", "push", "broadcast"},
	{"receive", "consume", "subscribe", "pull", "recv"},
	{"parse", "decode", "deserialize", "unmarshal", "scan"},
	{"encode", "serialize", "marshal", "format", "render"},
	{"find", "search", "locate", "match", "filter"},
	{"run", "execute", "invoke", "exec", "call", "perform"},
	{"list", "enumerate", "all", "collect", "gather"},
	{"add", "append", "insert", "register", "attach", "include"},
	{"merge", "combine", "join", "fuse", "union"},
	{"lock", "mutex", "guard", "synchronize"},
	// Concept-alias (NOUN) clusters: bridge the "the word I typed isn't in the
	// symbol name" gap deterministically (no embeddings). A query noun expands to
	// the vocabulary the code actually uses — "username/password" -> auth/basic,
	// "timeout" -> deadline, "collision" -> collide/overlap. Expansions are scored
	// at synonymWeight, so they surface a missed symbol without overriding a strong
	// literal hit.
	{"auth", "authentication", "authenticate", "login", "logout", "signin", "signup", "credential", "credentials", "password", "username", "basic", "bearer"},
	{"authorize", "authorization", "permission", "permissions", "role", "roles", "access", "policy", "grant"},
	{"timeout", "deadline", "expire", "expiry", "ttl", "elapsed"},
	{"cache", "memoize", "lru", "cached", "memo"},
	{"retry", "backoff", "reattempt", "redrive"},
	{"collision", "collide", "overlap", "intersect", "contact", "hit"},
	{"animation", "animate", "sprite", "frame", "tween", "keyframe"},
	{"render", "draw", "paint", "blit", "rasterize"},
	{"route", "router", "endpoint", "handler", "controller", "path"},
	{"middleware", "interceptor", "filter", "hook"},
	{"payment", "pay", "pays", "paid", "paying", "charge", "checkout", "billing", "invoice", "transaction"},
	{"upload", "attachment", "multipart", "file"},
	{"websocket", "socket", "ws", "realtime"},
	{"config", "configuration", "settings", "options", "preferences"},
	{"log", "logger", "logging", "trace", "telemetry"},
	{"kickoff", "starter", "bootstrap", "orient"},
}

// synonymsOf maps each word to the OTHER members of its cluster (built once).
var synonymsOf = func() map[string][]string {
	m := map[string][]string{}
	for _, cl := range synonymClusters {
		for _, w := range cl {
			for _, other := range cl {
				if other != w {
					m[w] = append(m[w], other)
				}
			}
		}
	}
	return m
}()

// abbreviations maps the shorthand/slang people actually type to the real word
// the code uses, expanded like a synonym (at synonymWeight). Deterministic, no
// model — closes part of the vague/"dumb" phrasing gap ("let ppl pay", "add a btn").
var abbreviations = map[string][]string{
	"ppl": {"people", "users"}, "usr": {"user"}, "btn": {"button"}, "msg": {"message"},
	"img": {"image"}, "pwd": {"password"}, "pw": {"password"}, "cfg": {"config", "configuration"},
	"cfgs": {"config"}, "repo": {"repository"}, "fn": {"function"}, "func": {"function"},
	"val": {"value"}, "idx": {"index"}, "ctx": {"context"}, "db": {"database"},
	"req": {"request"}, "res": {"response", "result"}, "resp": {"response"}, "err": {"error"},
	"auth": {"authentication", "authorization"}, "init": {"initialize"}, "calc": {"calculate"},
	"len": {"length"}, "num": {"number"}, "addr": {"address"}, "ctrl": {"controller", "control"},
	"mw": {"middleware"}, "ws": {"websocket"}, "ui": {"interface"}, "perms": {"permissions"},
}

// singular returns a crude singular of a word for synonym lookup, so a plural
// query token ("routes", "matches") still finds its cluster keyed on the singular
// ("route", "match"). No real stemmer — just the common English plural endings.
func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "es") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

// synonymWeight discounts an expansion term's contribution relative to a term
// the user actually typed: a synonym match is worth ~40% of a literal match, so
// it can break a tie or surface an otherwise-missed symbol without letting a
// weak synonym hit outrank a strong literal one.
const synonymWeight = 0.4

// expandSynonyms returns the search token set (original + corpus-agnostic
// synonym expansions) and a per-token weight (1.0 for typed terms, synonymWeight
// for expansions). A term the user typed always keeps weight 1.0 even if it is
// also some other term's synonym.
func expandSynonyms(toks []string) ([]string, map[string]float64) {
	weight := map[string]float64{}
	var out []string
	add := func(t string, w float64) {
		t = strings.ToLower(t)
		if t == "" {
			return
		}
		if cur, ok := weight[t]; ok {
			if w > cur {
				weight[t] = w // upgrade a synonym to literal weight if also typed
			}
			return
		}
		weight[t] = w
		out = append(out, t)
	}
	for _, t := range toks {
		add(t, 1.0)
	}
	for _, t := range toks {
		lt := strings.ToLower(t)
		for _, syn := range synonymsOf[lt] {
			add(syn, synonymWeight)
		}
		// Plural query token → expand via its singular cluster ("routes" → route's
		// cluster), so plural phrasing isn't a dead end.
		if s := singular(lt); s != lt {
			add(s, synonymWeight)
			for _, syn := range synonymsOf[s] {
				add(syn, synonymWeight)
			}
		}
		// Shorthand/slang → the real word.
		for _, full := range abbreviations[lt] {
			add(full, synonymWeight)
			for _, syn := range synonymsOf[full] {
				add(syn, synonymWeight)
			}
		}
	}
	return out, weight
}
