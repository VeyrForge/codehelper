package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// FlowLayer is a coarse architecture role on a request path.
type FlowLayer string

const (
	FlowLayerRoute      FlowLayer = "route"
	FlowLayerController FlowLayer = "controller"
	FlowLayerService    FlowLayer = "service"
	FlowLayerQuery      FlowLayer = "query"
	FlowLayerOther      FlowLayer = "other"
)

const (
	flowMaxEntrypoints = 400
	flowMaxDepth       = 8
	flowMaxFanout      = 12
	flowMaxSteps       = 24
)

var (
	syntheticRouteRe = regexp.MustCompile(`(?i)^(route|fastapi|express|flask|fiber|gin|nest|axum|ktor|sinatra)_(get|post|put|patch|delete|head|options|any|use|match)_\d+$`)
	controllerNameRe = regexp.MustCompile(`(?i)Controller$`)
	serviceNameRe    = regexp.MustCompile(`(?i)(Service|Interactor|UseCase|Handler)$`)
	queryNameRe      = regexp.MustCompile(`(?i)(Repository|Repo|Query|Store|Dao|Mapper|Model)$`)
)

// ClassifyFlowLayer assigns route→controller→service→query (or other) from
// path/name/signature heuristics used across Go/PHP/TS/Python stacks.
func ClassifyFlowLayer(sym types.Symbol) FlowLayer {
	sig := strings.ToLower(sym.Signature)
	name := sym.Name
	path := filepath.ToSlash(strings.ToLower(sym.Path))

	if strings.Contains(sig, "role=entrypoint") || syntheticRouteRe.MatchString(name) {
		return FlowLayerRoute
	}
	if strings.Contains(path, "/routes/") || strings.Contains(path, "/route/") ||
		strings.HasSuffix(path, "routes.go") || strings.HasSuffix(path, "router.go") ||
		strings.HasSuffix(path, "routes.php") || strings.HasSuffix(path, "routes.ts") ||
		strings.HasSuffix(path, "routes.js") || strings.Contains(path, "/routing/") {
		return FlowLayerRoute
	}
	switch name {
	case "ServeHTTP", "ServeHTTP2", "Handle", "HandleFunc":
		return FlowLayerRoute
	}

	if strings.Contains(path, "/controllers/") || strings.Contains(path, "/controller/") ||
		strings.Contains(path, "/http/controllers/") || controllerNameRe.MatchString(name) ||
		strings.HasSuffix(path, "controller.php") || strings.HasSuffix(path, "controller.ts") ||
		strings.HasSuffix(path, "controller.go") {
		return FlowLayerController
	}

	if strings.Contains(path, "/services/") || strings.Contains(path, "/service/") ||
		strings.Contains(path, "/usecases/") || strings.Contains(path, "/use_cases/") ||
		strings.Contains(path, "/interactors/") || serviceNameRe.MatchString(name) {
		return FlowLayerService
	}
	// handlers/ without Controller suffix often sit between route and service
	if strings.Contains(path, "/handlers/") || strings.Contains(path, "/handler/") {
		if controllerNameRe.MatchString(name) {
			return FlowLayerController
		}
		return FlowLayerService
	}

	if strings.Contains(path, "/repositories/") || strings.Contains(path, "/repository/") ||
		strings.Contains(path, "/queries/") || strings.Contains(path, "/query/") ||
		strings.Contains(path, "/models/") || strings.Contains(path, "/dao/") ||
		strings.Contains(path, "/persistence/") || strings.Contains(path, "/store/") ||
		strings.Contains(path, "/stores/") || queryNameRe.MatchString(name) {
		return FlowLayerQuery
	}
	return FlowLayerOther
}

// ListEntrypointSymbols returns HTTP/route entry candidates for request-flow tracing.
func (s *Store) ListEntrypointSymbols(ctx context.Context, repoID string, limit int) ([]types.Symbol, error) {
	if limit <= 0 {
		limit = flowMaxEntrypoints
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols
WHERE repo_id=? AND kind IN ('function','method') AND (
  signature LIKE '%role=entrypoint%'
  OR name IN ('ServeHTTP','Handle','HandleFunc')
  OR LOWER(name) LIKE 'route\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'express\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'fastapi\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'flask\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'fiber\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'gin\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'nest\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'axum\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'ktor\_%' ESCAPE '\'
  OR LOWER(name) LIKE 'sinatra\_%' ESCAPE '\'
  OR LOWER(path) LIKE '%/routes/%'
  OR LOWER(path) LIKE '%/controllers/%'
)
ORDER BY path, line_start, name, id
LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	syms, err := scanSymbols(rows)
	if err != nil {
		return nil, err
	}
	var out []types.Symbol
	for _, sym := range syms {
		layer := ClassifyFlowLayer(sym)
		if layer == FlowLayerRoute || layer == FlowLayerController {
			out = append(out, sym)
			continue
		}
		// Controllers under /controllers/ still seed flows even without route edges.
		if strings.Contains(filepath.ToSlash(strings.ToLower(sym.Path)), "/controllers/") {
			out = append(out, sym)
		}
	}
	return out, nil
}

// BuildRequestFlows traces route→controller→service→query spines from entrypoints.
func (s *Store) BuildRequestFlows(ctx context.Context, repoID string) ([]types.Process, error) {
	entries, err := s.ListEntrypointSymbols(ctx, repoID, flowMaxEntrypoints)
	if err != nil {
		return nil, err
	}
	seenEntry := map[string]struct{}{}
	var procs []types.Process
	for _, entry := range entries {
		if _, ok := seenEntry[entry.ID]; ok {
			continue
		}
		seenEntry[entry.ID] = struct{}{}
		steps, layers, err := s.traceRequestSpine(ctx, repoID, entry)
		if err != nil {
			return nil, err
		}
		if len(steps) == 0 {
			continue
		}
		// Require at least a route/controller start; prefer multi-layer flows but
		// keep single-hop controller entries so Laravel-style controllers appear.
		if !hasUsefulFlow(layers) {
			continue
		}
		sum := sha256.Sum256([]byte(entry.ID + "\x00" + strings.Join(steps, ",")))
		pid := hex.EncodeToString(sum[:8])
		name := formatFlowName(entry.Name, layers)
		procs = append(procs, types.Process{
			ID:          fmt.Sprintf("proc:%s:%s", repoID, pid),
			RepoID:      repoID,
			Name:        name,
			EntrySymbol: entry.ID,
			StepSymbols: steps,
		})
	}
	sort.SliceStable(procs, func(i, j int) bool {
		if procs[i].Name != procs[j].Name {
			return procs[i].Name < procs[j].Name
		}
		return procs[i].ID < procs[j].ID
	})
	return procs, nil
}

// ClusterRequestFlows builds layer + shared-spine clusters from request flows.
func (s *Store) ClusterRequestFlows(ctx context.Context, repoID string, procs []types.Process) ([]types.Cluster, error) {
	if len(procs) == 0 {
		return nil, nil
	}
	byID := map[string]types.Symbol{}
	ids := map[string]struct{}{}
	for _, p := range procs {
		for _, sid := range p.StepSymbols {
			ids[sid] = struct{}{}
		}
	}
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	syms, err := s.SymbolsByIDs(ctx, repoID, idList)
	if err != nil {
		return nil, err
	}
	for _, sym := range syms {
		byID[sym.ID] = sym
	}

	layerMembers := map[FlowLayer][]string{}
	famMembers := map[string][]string{}
	for _, p := range procs {
		var famKey string
		for _, sid := range p.StepSymbols {
			sym, ok := byID[sid]
			if !ok {
				continue
			}
			layer := ClassifyFlowLayer(sym)
			if layer != FlowLayerOther {
				layerMembers[layer] = append(layerMembers[layer], sid)
			}
			if famKey == "" && (layer == FlowLayerController || layer == FlowLayerService) {
				famKey = strings.ToLower(sym.Name)
			}
		}
		if famKey == "" {
			famKey = "entry:" + p.EntrySymbol
		}
		famMembers[famKey] = append(famMembers[famKey], p.StepSymbols...)
	}

	var out []types.Cluster
	for _, layer := range []FlowLayer{FlowLayerRoute, FlowLayerController, FlowLayerService, FlowLayerQuery} {
		mem := dedupeStable(layerMembers[layer])
		if len(mem) == 0 {
			continue
		}
		sum := sha256.Sum256([]byte("layer:" + string(layer)))
		id := hex.EncodeToString(sum[:8])
		out = append(out, types.Cluster{
			ID:       fmt.Sprintf("clu:%s:layer:%s", repoID, id),
			RepoID:   repoID,
			Name:     "layer:" + string(layer),
			Members:  mem,
			Cohesion: layerCohesion(layer, len(mem)),
		})
	}
	fams := make([]string, 0, len(famMembers))
	for k := range famMembers {
		fams = append(fams, k)
	}
	sort.Strings(fams)
	for _, fam := range fams {
		mem := dedupeStable(famMembers[fam])
		if len(mem) < 2 {
			continue
		}
		sum := sha256.Sum256([]byte("flowfam:" + fam))
		id := hex.EncodeToString(sum[:8])
		out = append(out, types.Cluster{
			ID:       fmt.Sprintf("clu:%s:flow:%s", repoID, id),
			RepoID:   repoID,
			Name:     "flowfam:" + fam,
			Members:  mem,
			Cohesion: 0.7,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ReplaceProcesses deletes all processes for repo then upserts next.
func (s *Store) ReplaceProcesses(ctx context.Context, repoID string, procs []types.Process) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM processes WHERE repo_id=?`, repoID); err != nil {
		return err
	}
	for _, p := range procs {
		if err := s.UpsertProcess(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceClusters deletes all clusters for repo then upserts next.
func (s *Store) ReplaceClusters(ctx context.Context, repoID string, clusters []types.Cluster) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM clusters WHERE repo_id=?`, repoID); err != nil {
		return err
	}
	for _, c := range clusters {
		if err := s.UpsertCluster(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// PersistRequestFlows rebuilds processes + flow/layer clusters from the call graph.
func (s *Store) PersistRequestFlows(ctx context.Context, repoID string) (nProcs, nClusters int, err error) {
	procs, err := s.BuildRequestFlows(ctx, repoID)
	if err != nil {
		return 0, 0, err
	}
	clusters, err := s.ClusterRequestFlows(ctx, repoID, procs)
	if err != nil {
		return 0, 0, err
	}
	if err := s.ReplaceProcesses(ctx, repoID, procs); err != nil {
		return 0, 0, err
	}
	if err := s.ReplaceClusters(ctx, repoID, clusters); err != nil {
		return 0, 0, err
	}
	return len(procs), len(clusters), nil
}

func (s *Store) traceRequestSpine(ctx context.Context, repoID string, entry types.Symbol) ([]string, []FlowLayer, error) {
	type flowNode struct {
		id     string
		path   []string
		layers []FlowLayer
		score  int
	}
	better := func(cand, best flowNode) bool {
		if cand.score != best.score {
			return cand.score > best.score
		}
		if distinctLayers(cand.layers) != distinctLayers(best.layers) {
			return distinctLayers(cand.layers) > distinctLayers(best.layers)
		}
		return len(cand.path) > len(best.path)
	}
	entryLayer := ClassifyFlowLayer(entry)
	best := flowNode{id: entry.ID, path: []string{entry.ID}, layers: []FlowLayer{entryLayer}, score: layerScore(entryLayer)}
	queue := []flowNode{best}
	seen := map[string]struct{}{entry.ID: {}}

	for depth := 0; depth < flowMaxDepth && len(queue) > 0; depth++ {
		var next []flowNode
		for _, cur := range queue {
			callees, err := s.CalleesOf(ctx, repoID, cur.id)
			if err != nil {
				return nil, nil, err
			}
			if len(callees) > flowMaxFanout {
				callees = callees[:flowMaxFanout]
			}
			for _, cal := range callees {
				if _, ok := seen[cal.ID]; ok {
					continue
				}
				seen[cal.ID] = struct{}{}
				layer := ClassifyFlowLayer(cal)
				np := append(append([]string{}, cur.path...), cal.ID)
				nl := append(append([]FlowLayer{}, cur.layers...), layer)
				sc := cur.score + layerProgressScore(cur.layers, layer)
				cand := flowNode{id: cal.ID, path: np, layers: nl, score: sc}
				if better(cand, best) {
					best = cand
				}
				if len(np) < flowMaxSteps {
					next = append(next, cand)
				}
			}
		}
		queue = next
	}
	return best.path, best.layers, nil
}

func hasUsefulFlow(layers []FlowLayer) bool {
	if len(layers) == 0 {
		return false
	}
	if layers[0] == FlowLayerRoute || layers[0] == FlowLayerController {
		return true
	}
	return distinctLayers(layers) >= 2
}

func distinctLayers(layers []FlowLayer) int {
	seen := map[FlowLayer]struct{}{}
	for _, l := range layers {
		if l == FlowLayerOther {
			continue
		}
		seen[l] = struct{}{}
	}
	return len(seen)
}

func layerScore(l FlowLayer) int {
	switch l {
	case FlowLayerRoute:
		return 4
	case FlowLayerController:
		return 3
	case FlowLayerService:
		return 2
	case FlowLayerQuery:
		return 5
	default:
		return 0
	}
}

func layerProgressScore(prev []FlowLayer, next FlowLayer) int {
	base := layerScore(next)
	if next == FlowLayerOther {
		return 0
	}
	last := FlowLayerOther
	for i := len(prev) - 1; i >= 0; i-- {
		if prev[i] != FlowLayerOther {
			last = prev[i]
			break
		}
	}
	order := map[FlowLayer]int{
		FlowLayerRoute: 0, FlowLayerController: 1, FlowLayerService: 2, FlowLayerQuery: 3,
	}
	if order[next] >= order[last] {
		return base + 3
	}
	return base
}

func formatFlowName(entryName string, layers []FlowLayer) string {
	var chain []string
	seen := map[FlowLayer]struct{}{}
	for _, l := range layers {
		if l == FlowLayerOther {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		chain = append(chain, string(l))
	}
	if len(chain) == 0 {
		chain = []string{"flow"}
	}
	return fmt.Sprintf("flow:%s:%s", strings.Join(chain, "→"), entryName)
}

func layerCohesion(layer FlowLayer, n int) float64 {
	base := 0.55
	switch layer {
	case FlowLayerRoute:
		base = 0.6
	case FlowLayerController:
		base = 0.7
	case FlowLayerService:
		base = 0.75
	case FlowLayerQuery:
		base = 0.65
	}
	if n > 20 {
		return base - 0.1
	}
	return base
}

func dedupeStable(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
