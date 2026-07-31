#!/usr/bin/env python3
"""Locate / rebuild Pack Owner B's tiny-moe.green fixture (M4).

Canonical path:
  <green-compress>/scripts/fixtures/tiny-moe.green

Rebuild (from green-compress root):
  python scripts/make_test_moe_gguf.py --out scripts/fixtures/mini-moe-test.gguf --seed 1
  python scripts/pack_model.py --gguf scripts/fixtures/mini-moe-test.gguf --out scripts/fixtures/tiny-moe.green --verify

Usage (from GreenEngine root):
  python scripts/make_tiny_moe_green.py          # print resolved path
  python scripts/make_tiny_moe_green.py --check  # verify MoE sidecars
"""
from __future__ import annotations

import argparse
import os
import sys


def repo_root() -> str:
    return os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))


def find_tiny_moe_green(explicit: str | None) -> str:
    if explicit:
        return os.path.abspath(explicit)
    env = os.environ.get("GE_TINY_MOE_GREEN")
    if env:
        return os.path.abspath(env)
    here = repo_root()
    candidates = [
        os.path.join(here, "..", "green-compress", "scripts", "fixtures", "tiny-moe.green"),
        os.path.join(
            here,
            "..",
            "codehelper",
            "third_party",
            "green-compress",
            "scripts",
            "fixtures",
            "tiny-moe.green",
        ),
        r"../green-compress\scripts\fixtures\tiny-moe.green",
    ]
    for c in candidates:
        p = os.path.abspath(c)
        if os.path.isfile(os.path.join(p, "manifest.json")):
            return p
    raise SystemExit(
        "tiny-moe.green not found. Expected sibling "
        "green-compress/scripts/fixtures/tiny-moe.green (or set GE_TINY_MOE_GREEN)."
    )


def main() -> None:
    ap = argparse.ArgumentParser(description="Resolve tiny-moe.green fixture")
    ap.add_argument("--path", default=None, help="Override fixture directory")
    ap.add_argument("--check", action="store_true", help="Require MoE sidecars + GRNP magic")
    a = ap.parse_args()

    root = find_tiny_moe_green(a.path)
    print(root)
    if a.check:
        for name in (
            "manifest.json",
            "metadata.gguf",
            "dense.gguf",
            "experts-000.greenpack",
            "checksums.json",
        ):
            p = os.path.join(root, name)
            if not os.path.isfile(p):
                raise SystemExit(f"missing {p}")
        with open(os.path.join(root, "experts-000.greenpack"), "rb") as fh:
            magic = fh.read(4)
        if magic != b"GRNP":
            raise SystemExit(f"bad greenpack magic: {magic!r}")
        print("check: ok (MoE pack fixture present)", file=sys.stderr)


if __name__ == "__main__":
    main()
