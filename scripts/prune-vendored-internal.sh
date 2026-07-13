#!/usr/bin/env bash
# Remove vendored internal paths and non-published docs from git tracking.
# Run from codehelper root after `git subtree pull` if upstream re-added them.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

prune() {
  local p
  for p in "$@"; do
    if git ls-files --error-unmatch "$p" >/dev/null 2>&1; then
      git rm -r --cached "$p"
      echo "untracked: $p"
    fi
  done
}

prune \
  third_party/green-engine/docs \
  third_party/green-engine/experiments \
  third_party/green-engine/idea.md \
  third_party/green-compress/docs \
  third_party/green-compress/experiments \
  third_party/green-compress/TEST_REPORT.md \
  third_party/green-compress/scripts/vps-deploy.sh \
  third_party/green-compress/scripts/vps-setup.sh \
  third_party/green-compress/scripts/install-github-runner.sh \
  third_party/green-compress/scripts/fix-github-runner.sh \
  third_party/green-compress/.github/workflows/deploy.yml \
  third_party/green-compress/scripts/backfill_benchmark_txt.py \
  third_party/green-compress/scripts/codec_compare.py \
  third_party/green-compress/scripts/compare_frontier.py \
  third_party/green-compress/scripts/compare_runtime_stacks.py \
  third_party/green-compress/scripts/e2e_mixed_precision.py \
  third_party/green-compress/scripts/estimate_model_ram.py \
  third_party/green-compress/scripts/mixed_precision_analysis.py \
  third_party/green-compress/scripts/perplexity_mixed_precision.py

prune docs/public docs/qrels

for p in docs/EVAL.md docs/RELEASE.md docs/AGENT_API.md docs/SETUP.md docs/SEMANTIC.md docs/LOCAL_LLM_BENCH.md; do
  if git ls-files --error-unmatch "$p" >/dev/null 2>&1; then
    git rm --cached "$p"
    echo "untracked: $p"
  fi
done

if git ls-files --error-unmatch scripts/publish-public-mirrors.sh >/dev/null 2>&1; then
  git rm --cached scripts/publish-public-mirrors.sh
  echo "untracked: scripts/publish-public-mirrors.sh"
fi

echo "Done. Commit .gitignore + index changes."
