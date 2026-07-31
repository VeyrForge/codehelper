#!/usr/bin/env python3
"""Locate / document Pack Owner B's dense-only tiny.green fixture (Owner K).

Canonical path (do not invent a second package):
  <green-compress>/scripts/fixtures/tiny.green

Rebuild (from green-compress root) — see green-compress README:
  python scripts/make_test_gguf.py --out scripts/fixtures/mini-test.gguf --seed 0
  greencompress pack-model --gguf scripts/fixtures/mini-test.gguf --out scripts/fixtures/tiny.green --verify

Usage (from GreenEngine root):
  python scripts/make_tiny_green.py          # print resolved path
  python scripts/make_tiny_green.py --check  # verify manifest + checksums.json exist
"""
from __future__ import annotations

import argparse
import os
import sys


def repo_root() -> str:
    return os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))


def find_tiny_green(explicit: str | None) -> str:
    if explicit:
        return os.path.abspath(explicit)
    env = os.environ.get("GE_TINY_GREEN")
    if env:
        return os.path.abspath(env)
    here = repo_root()
    candidates = [
        os.path.join(here, "..", "green-compress", "scripts", "fixtures", "tiny.green"),
        os.path.join(
            here, "..", "codehelper", "third_party", "green-compress", "scripts", "fixtures", "tiny.green"
        ),
        r"../green-compress\scripts\fixtures\tiny.green",
    ]
    for c in candidates:
        p = os.path.abspath(c)
        if os.path.isfile(os.path.join(p, "manifest.json")):
            return p
    raise SystemExit(
        "Pack Owner B fixture not found. Expected sibling "
        "green-compress/scripts/fixtures/tiny.green (or set GE_TINY_GREEN)."
    )


def main() -> None:
    ap = argparse.ArgumentParser(description="Resolve Pack Owner B tiny.green fixture")
    ap.add_argument("--path", default=None, help="Override fixture directory")
    ap.add_argument("--check", action="store_true", help="Require dense sidecars + checksums")
    a = ap.parse_args()

    root = find_tiny_green(a.path)
    print(root)
    if a.check:
        for name in ("manifest.json", "metadata.gguf", "dense.gguf", "checksums.json"):
            p = os.path.join(root, name)
            if not os.path.isfile(p):
                raise SystemExit(f"missing {p}")
        print("check: ok (dense-only pack fixture present)", file=sys.stderr)


if __name__ == "__main__":
    main()
