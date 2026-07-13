#!/usr/bin/env python3
"""Resolve per-tensor compression overrides from config/tensor_policy.json."""
from __future__ import annotations

import json
import os
import re
from typing import Any

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
DEFAULT_POLICY = os.path.join(REPO, "config", "tensor_policy.json")


def _layer_index(tensor_name: str) -> int | None:
    m = re.search(r"(?:blk|layer)\.(\d+)", tensor_name, re.I)
    return int(m.group(1)) if m else None


def load_policy(path: str | None = None) -> dict[str, Any]:
    path = path or DEFAULT_POLICY
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def match_rule(tensor_name: str, policy: dict[str, Any]) -> dict[str, Any]:
    merged: dict[str, Any] = {}
    for rule in policy.get("rules", []):
        pattern = rule.get("match", "")
        if not pattern or not re.search(pattern, tensor_name, re.I):
            continue
        merged.update(rule.get("benchmark", {}))
        break

    late = policy.get("late_layers") or {}
    layer_min = late.get("layer_min")
    if layer_min is not None:
        idx = _layer_index(tensor_name)
        if idx is not None and idx >= int(layer_min):
            merged.update(late.get("benchmark", {}))
    return merged


def resolve_method_type(tensor_name: str, default_method: str, policy: dict[str, Any] | None = None) -> str:
    """Per-tensor method override from policy."""
    bench = match_rule(tensor_name, policy or load_policy())
    return str(bench.get("type", default_method))


def benchmark_cli_args(tensor_name: str, policy: dict[str, Any] | None = None) -> list[str]:
    """Extra greencompress benchmark flags for this tensor (not --type)."""
    policy = policy or load_policy()
    bench = match_rule(tensor_name, policy)
    args: list[str] = []
    key_map = {
        "rank": "rank",
        "sparse_frac": "sparse-frac",
        "outlier_frac": "outlier-frac",
        "repair_passes": "repair-passes",
        "sparse_mode": "sparse-mode",
        "skip_repair_quality_pct": "skip-repair-quality-pct",
        "min_quality_pct": "min-quality-pct",
        "block": "block",
        "spin_search": "spin-search",
    }
    for src, cli in key_map.items():
        if src in bench and bench[src] is not None:
            args.extend([f"--{cli}", str(bench[src])])
    return args
