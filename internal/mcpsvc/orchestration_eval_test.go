//go:build !windows

package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/orchestrator/eval"
	"github.com/VeyrForge/codehelper/internal/registry"
)

// TestOrchestrationVariantsEval compares TOON vs JSON manual chain on one repo.
func TestOrchestrationVariantsEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping orchestration variant eval in -short mode")
	}
	reg, repo := liveRegistryWithIndexedRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, v := range []eval.NamedConfig{
		{Name: "fresh_index_toon", Config: eval.Config{ManualFormat: "toon", OrchestrateFormat: "toon"}},
		{Name: "fresh_index_manual_json", Config: eval.Config{ManualFormat: "json", OrchestrateFormat: "toon"}},
	} {
		runner := EvalRunner(reg)
		runner.Config = v.Config
		runner.RefreshIndex = true
		pr, err := runner.RunProject(ctx, reg, repo)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: orch=%.2f manual=%.2f agent=%d manual_tok=%d",
			v.Name, pr.Summary.OrchestrateAvg, pr.Summary.ManualAvg,
			pr.Summary.OrchestrateAgentTokens, pr.Summary.ManualTokens)
	}
}

// TestOrchestrationBenchmark runs the 3-way eval on every indexed project.
// go test ./internal/mcpsvc -run TestOrchestrationBenchmark -v -timeout 30m
func TestOrchestrationBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full cross-project benchmark in -short mode")
	}
	reg, err := registry.Load()
	if err != nil {
		t.Skip(err)
	}
	runner := EvalRunner(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	rep, err := runner.RunAll(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Projects) == 0 {
		t.Skip("no indexed projects in registry")
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	t.Log(string(b))
	for _, p := range rep.Projects {
		t.Logf("PROJECT %s: orch=%.2f manual=%.2f baseline=%.2f tokens(orch_internal/agent/manual/base)=%d/%d/%d/%d wins=%d/%d/%d",
			p.Repo, p.Summary.OrchestrateAvg, p.Summary.ManualAvg, p.Summary.BaselineAvg,
			p.Summary.OrchestrateTokens, p.Summary.OrchestrateAgentTokens,
			p.Summary.ManualTokens, p.Summary.BaselineTokens,
			p.Summary.WinsOrchestrate, p.Summary.WinsManual, p.Summary.WinsBaseline)
		for _, c := range p.Cases {
			if len(c.Orchestrate.Gaps) > 0 {
				t.Logf("  %s/%s gaps(orch): %v", p.Repo, c.Task.Name, c.Orchestrate.Gaps)
			}
		}
	}
	_ = os.WriteFile("/tmp/codehelper-orchestration-eval.json", b, 0o644)
}

// TestOrchestrationEval is a quick single-repo smoke of the benchmark harness.
func TestOrchestrationEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping orchestration eval in -short mode")
	}
	reg, repo := liveRegistryWithIndexedRepo(t)
	runner := EvalRunner(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pr, err := runner.RunProject(ctx, reg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Cases) == 0 {
		t.Fatal("expected cases")
	}
	t.Logf("%s: orch=%.2f manual=%.2f baseline=%.2f", pr.Repo, pr.Summary.OrchestrateAvg, pr.Summary.ManualAvg, pr.Summary.BaselineAvg)
}
