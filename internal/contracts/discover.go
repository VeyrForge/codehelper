package contracts

import (
	"path/filepath"
	"sort"
	"strings"
)

// Bundle is the full contract discovery result for one repo root.
type Bundle struct {
	Root     string             `json:"root"`
	Repo     string             `json:"repo,omitempty"`
	OpenAPI  []OpenAPIContract  `json:"openapi,omitempty"`
	GraphQL  []GraphQLContract  `json:"graphql,omitempty"`
	Events   []EventContract    `json:"events,omitempty"`
	Protobuf []ProtobufContract `json:"protobuf,omitempty"`
}

// ContractOccurrence locates one shared contract key in a repo.
type ContractOccurrence struct {
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"path"`
	SameGroup bool   `json:"same_group,omitempty"`
}

// ContractLink ties a shared identifier across repos (or files).
type ContractLink struct {
	Kind        string               `json:"kind"` // api_path | graphql_type | graphql_op | event | channel | protobuf_service | protobuf_message | protobuf_rpc
	Key         string               `json:"key"`
	Occurrences []ContractOccurrence `json:"occurrences"`
}

// DiscoverAll runs OpenAPI + GraphQL + event + Protobuf discovery for one root.
// Discovery is shallow (candidate paths + common dirs) — see WORKSPACE_GROUPS / LANGUAGE_MATRIX.
func DiscoverAll(root string) Bundle {
	root = strings.TrimSpace(root)
	if root == "" {
		return Bundle{}
	}
	return Bundle{
		Root:     root,
		OpenAPI:  DiscoverOpenAPI(root),
		GraphQL:  DiscoverGraphQL(root),
		Events:   DiscoverEvents(root),
		Protobuf: DiscoverProtobuf(root),
	}
}

// Count returns total discovered contract files.
func (b Bundle) Count() int {
	return len(b.OpenAPI) + len(b.GraphQL) + len(b.Events) + len(b.Protobuf)
}

// AnnotateRepo sets Repo / RepoHint on the bundle and nested contracts.
func (b Bundle) AnnotateRepo(name string) Bundle {
	name = strings.TrimSpace(name)
	b.Repo = name
	for i := range b.OpenAPI {
		b.OpenAPI[i].RepoHint = name
	}
	for i := range b.GraphQL {
		b.GraphQL[i].RepoHint = name
	}
	for i := range b.Events {
		b.Events[i].RepoHint = name
	}
	for i := range b.Protobuf {
		b.Protobuf[i].RepoHint = name
	}
	return b
}

// LinkOptions controls cross-repo contract linking.
type LinkOptions struct {
	// SameGroupRepos lists repo names that share a workspace group with the primary.
	// When empty, SameGroup is left false on all occurrences.
	SameGroupRepos map[string]struct{}
}

type contractHit struct {
	kind, key, repo, path string
}

// LinkAcrossBundles finds shared API paths, GraphQL types/ops, event/channel names,
// and Protobuf services/messages/RPCs across bundles. A link is emitted when a key
// appears in ≥2 distinct repos, or in ≥2 files within one repo. Channel names that
// match event names across repos also link as kind "event" (AsyncAPI ↔ CloudEvents).
func LinkAcrossBundles(bundles []Bundle, opts LinkOptions) []ContractLink {
	var hits []contractHit
	for _, b := range bundles {
		repo := strings.TrimSpace(b.Repo)
		for _, c := range b.OpenAPI {
			for _, p := range c.APIPaths {
				hits = append(hits, contractHit{"api_path", p, repo, c.Path})
			}
		}
		for _, c := range b.GraphQL {
			for _, t := range c.Types {
				hits = append(hits, contractHit{"graphql_type", t, repo, c.Path})
			}
			for _, op := range c.Operations {
				hits = append(hits, contractHit{"graphql_op", op, repo, c.Path})
			}
		}
		for _, c := range b.Events {
			for _, ch := range c.Channels {
				hits = append(hits, contractHit{"channel", ch, repo, c.Path})
			}
			for _, ev := range c.EventNames {
				hits = append(hits, contractHit{"event", ev, repo, c.Path})
			}
		}
		for _, c := range b.Protobuf {
			for _, s := range c.Services {
				hits = append(hits, contractHit{"protobuf_service", s, repo, c.Path})
			}
			for _, m := range c.Messages {
				hits = append(hits, contractHit{"protobuf_message", m, repo, c.Path})
			}
			for _, rpc := range c.RPCs {
				hits = append(hits, contractHit{"protobuf_rpc", rpc, repo, c.Path})
			}
		}
	}

	// Cross-kind: treat matching channel + event keys as shared event identifiers.
	for _, h := range append([]contractHit(nil), hits...) {
		if h.kind == "channel" {
			hits = append(hits, contractHit{"event", h.key, h.repo, h.path})
		}
	}

	grouped := map[string][]contractHit{}
	for _, h := range hits {
		k := h.kind + "\x00" + h.key
		grouped[k] = append(grouped[k], h)
	}

	var out []ContractLink
	for _, group := range grouped {
		if link, ok := buildLink(group, opts); ok {
			out = append(out, link)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func buildLink(group []contractHit, opts LinkOptions) (ContractLink, bool) {
	if len(group) == 0 {
		return ContractLink{}, false
	}
	repos := map[string]struct{}{}
	paths := map[string]struct{}{}
	for _, h := range group {
		if h.repo != "" {
			repos[h.repo] = struct{}{}
		}
		paths[filepath.ToSlash(h.path)] = struct{}{}
	}
	if len(repos) < 2 && len(paths) < 2 {
		return ContractLink{}, false
	}
	kind := group[0].kind
	key := group[0].key
	seenOcc := map[string]struct{}{}
	var occ []ContractOccurrence
	for _, h := range group {
		okey := h.repo + "\x00" + filepath.ToSlash(h.path)
		if _, dup := seenOcc[okey]; dup {
			continue
		}
		seenOcc[okey] = struct{}{}
		same := false
		if opts.SameGroupRepos != nil && h.repo != "" {
			_, same = opts.SameGroupRepos[h.repo]
		}
		occ = append(occ, ContractOccurrence{
			Repo:      h.repo,
			Path:      h.path,
			SameGroup: same,
		})
	}
	sort.SliceStable(occ, func(i, j int) bool {
		if occ[i].Repo != occ[j].Repo {
			return occ[i].Repo < occ[j].Repo
		}
		return occ[i].Path < occ[j].Path
	})
	return ContractLink{Kind: kind, Key: key, Occurrences: occ}, true
}
