#!/usr/bin/env python3
"""Light regression: MoE CAP8/10 (global pool), GE_GAME_MODE 1B/8B hello + RSS, GPU util gate."""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT = REPO / "scripts" / "bench" / "results"
KERNELS = REPO / "crates" / "kernels"
CUDA_BIN = r"C:\Program Files\NVIDIA GPU Computing Toolkit\CUDA\v13.3\bin\x64"
PAGED = REPO / "target" / "release" / "paged_bench.exe"

BENCH_CANDIDATES = [
    REPO / "target-gpu-rebuild" / "release" / "decode_1b_bench.exe",
    REPO / "target" / "release" / "decode_1b_bench.exe",
]

MODEL_1B = Path.home() / ".green" / "models" / "Llama-3.2-1B.green"
MODEL_8B = Path.home() / ".green" / "models" / "Meta-Llama-3.1-8B-Instruct.green"

CAP8_MIN = 46.9
CAP10_MIN = 52.2
RSS_8B_MAX_MIB = 3800.0
UTIL_LIMIT = 75.0

WARM_RE = re.compile(r"warm:.*?\| decode=([\d.]+) tok/s", re.S)
TEXT_RE = re.compile(r'text_warm="((?:\\.|[^"\\])*)"')
HIT_RE = re.compile(r"F32\s+\d+\.\d+M\s+\d+\.\d+M\s+\d+\.\d+%\s+([\d.]+)%")


def git_commit() -> str:
    try:
        r = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=str(REPO),
            capture_output=True,
            text=True,
            timeout=10,
        )
        return (r.stdout or "").strip() or "unknown"
    except Exception:
        return "unknown"


def pick_bench() -> Path:
    for p in BENCH_CANDIDATES:
        if p.is_file():
            return p
    raise SystemExit("no decode_1b_bench.exe (build target-gpu-rebuild or --features gpu)")


def gpu_util() -> float | None:
    try:
        r = subprocess.run(
            [
                "nvidia-smi",
                "--query-gpu=utilization.gpu",
                "--format=csv,noheader,nounits",
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if r.returncode == 0 and r.stdout.strip():
            return float(r.stdout.strip().splitlines()[0])
    except Exception:
        pass
    return None


def wait_util(limit: float = UTIL_LIMIT, max_wait: float = 300.0) -> dict:
    waited = 0.0
    polls = 0
    while waited < max_wait:
        polls += 1
        u = gpu_util()
        if u is None or u <= limit:
            return {
                "ready": True,
                "gpu_util": u,
                "polls": polls,
                "waited_s": waited,
                "limit": limit,
            }
        time.sleep(5.0)
        waited += 5.0
    return {
        "ready": False,
        "gpu_util": gpu_util(),
        "polls": polls,
        "waited_s": waited,
        "limit": limit,
    }


def base_env(ngl: int) -> dict[str, str]:
    bench = pick_bench()
    env = os.environ.copy()
    env.update(
        {
            "GE_GAME_MODE": "1",
            "GE_BENCH_IGNORE_EOS": "1",
            "GE_REPACK": "0",
            "GE_GEMV_Q8": "0",
            "GE_CTX": "512",
            "GE_GPU_KV_MAX_SEQ": "512",
            "GE_HOST_RELEASE": "1",
            "GE_CUDA_GRAPH": "0",
            "GE_GPU_LAYERS": str(ngl),
            "GREEN_ENGINE_KERNELS_DIR": str(KERNELS),
        }
    )
    env.pop("GE_GPU_ACT_F32", None)
    env.pop("GE_THREADS", None)
    env.pop("GE_CPU_THREADS", None)
    env["PATH"] = os.pathsep.join(
        [
            str(bench.parent),
            str(KERNELS),
            CUDA_BIN,
            env.get("PATH", ""),
        ]
    )
    return env


def hello_ok(text: str | None) -> bool:
    if not text:
        return False
    t = text.strip().lower()
    return "hello" in t or t.startswith("hi") or "greet" in t


def run_paged_cap(cap: int) -> float:
    if not PAGED.is_file():
        raise SystemExit(f"missing {PAGED}")
    env = os.environ.copy()
    env["GE_SLOT_POOL"] = "global"
    env["GE_CAP"] = str(cap)
    p = subprocess.run(
        [str(PAGED)],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        cwd=str(REPO),
        timeout=600,
    )
    out = (p.stdout or "") + "\n" + (p.stderr or "")
    if p.returncode != 0:
        raise SystemExit(f"paged_bench cap={cap} exit {p.returncode}\n{out[-2000:]}")
    m = HIT_RE.search(out)
    if not m:
        raise SystemExit(f"paged_bench cap={cap}: could not parse F32 hit %\n{out[-2000:]}")
    return float(m.group(1))


def run_game_smoke(label: str, model: Path, ngl: int, n_new: int = 32) -> dict:
    import psutil

    bench = pick_bench()
    if not model.exists():
        raise SystemExit(f"missing model {model}")
    util_pre = gpu_util()
    rss: list[int] = []
    stop = threading.Event()

    def poll(pid: int) -> None:
        while not stop.is_set():
            try:
                rss.append(psutil.Process(pid).memory_info().rss)
            except Exception:
                pass
            time.sleep(0.25)

    env = base_env(ngl)
    proc = subprocess.Popen(
        [str(bench), str(model), str(n_new)],
        env=env,
        cwd=str(REPO),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    th = threading.Thread(target=poll, args=(proc.pid,), daemon=True)
    th.start()
    out, err = proc.communicate(timeout=600)
    stop.set()
    th.join(timeout=2)
    combined = (out or "") + "\n" + (err or "")
    if "gpu-layers" in combined.lower() and "ignored" in combined.lower():
        raise SystemExit(
            f"{label}: GPU layers ignored — use GPU-enabled decode_1b_bench ({bench})"
        )
    warm = WARM_RE.search(combined)
    decode_tps = float(warm.group(1)) if warm else None
    tm = TEXT_RE.search(combined)
    text = tm.group(1) if tm else None
    peak = max(rss) / (1024 * 1024) if rss else None
    mid = len(rss) // 2
    steady_mid = (
        sum(rss[mid:]) / len(rss[mid:]) / (1024 * 1024) if rss[mid:] else None
    )
    steady_last = rss[-1] / (1024 * 1024) if rss else None
    return {
        "label": label,
        "exit": proc.returncode,
        "hello_ok": hello_ok(text),
        "decode_tps": decode_tps,
        "rss_peak_mib": round(peak, 1) if peak is not None else None,
        "rss_steady_last_mib": round(steady_last, 1) if steady_last is not None else None,
        "rss_steady_midhalf_mib": round(steady_mid, 1) if steady_mid is not None else None,
        "gpu_util_pre": util_pre,
        "ngl": ngl,
        "bench": str(bench.relative_to(REPO)),
    }


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    util_gate = wait_util()
    cap8 = run_paged_cap(8)
    cap10 = run_paged_cap(10)
    runs = [
        run_game_smoke("1B_hello", MODEL_1B, ngl=16),
        run_game_smoke("8B_hello", MODEL_8B, ngl=24),
    ]
    rss_8b = runs[1].get("rss_peak_mib")
    moe_pass = cap8 >= CAP8_MIN and cap10 >= CAP10_MIN
    smoke_pass = all(r["exit"] == 0 and r["hello_ok"] for r in runs)
    rss_pass = (rss_8b or 99999.0) <= RSS_8B_MAX_MIB
    verdict = (
        "PASS"
        if util_gate.get("ready") and moe_pass and smoke_pass and rss_pass
        else "FAIL"
    )
    result = {
        "suite": "light_regression_bundle",
        "stamp": time.strftime("%Y%m%d_%H%M%S"),
        "commit": git_commit(),
        "util_gate": util_gate,
        "checks": {
            "moe_paged_bench": {
                "CAP8_global_hit_pct": cap8,
                "min": CAP8_MIN,
                "pass_cap8": cap8 >= CAP8_MIN,
                "CAP10_global_hit_pct": cap10,
                "min_cap10": CAP10_MIN,
                "pass_cap10": cap10 >= CAP10_MIN,
                "pass": moe_pass,
            },
            "ge_game_mode_smoke": {
                "runs": runs,
                "pass": smoke_pass,
                "rss_8b_peak_mib": rss_8b,
                "rss_8b_max_mib": RSS_8B_MAX_MIB,
                "pass_rss_8b": rss_pass,
            },
        },
        "verdict": verdict,
    }
    path = OUT / f"light_regression_bundle_{result['stamp']}.json"
    path.write_text(json.dumps(result, indent=2), encoding="utf-8")
    print(json.dumps(result, indent=2))
    if verdict != "PASS":
        raise SystemExit(2)


if __name__ == "__main__":
    main()
