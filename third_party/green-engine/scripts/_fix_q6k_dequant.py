#!/usr/bin/env python3
"""DEPRECATED: use recover_engine_core.py (apply_q6k_dequant_fix, step 9b).

Standalone wrapper kept for one-off bisect scripts; prefer::

    python scripts/recover_engine_core.py --full
"""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]


def _load_recover():
    spec = importlib.util.spec_from_file_location(
        "recover_engine_core", REPO / "scripts" / "recover_engine_core.py"
    )
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


def main() -> None:
    print("DEPRECATED: _fix_q6k_dequant.py — calling recover_engine_core.apply_q6k_dequant_fix", flush=True)
    recover = _load_recover()
    recover.apply_q6k_dequant_fix()


if __name__ == "__main__":
    main()
    sys.exit(0)
