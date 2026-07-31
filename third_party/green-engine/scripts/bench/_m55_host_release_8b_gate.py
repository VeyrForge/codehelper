"""M5.5 host WS release gate — 8B ngl=24 under gaming caps.

Measures peak vs steady/last Working Set with GE_HOST_RELEASE=1.
Gate: last/steady RSS much less than on-disk weight file (dense.gguf).
Hello quality smoke. Gaming: threads<=6, BelowNormal, ctx/kv=512.
No commit.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT = REPO / "scripts" / "bench" / "results"
KERNELS = REPO / "crates" / "kernels"
CUDA_BIN = r"C:\Program Files\NVIDIA GPU Computing Toolkit\CUDA\v13.3\bin\x64"
MODEL = Path.home() / ".green" / "models" / "Meta-Llama-3.1-8B-Instruct.green"
WEIGHT = MODEL / "dense.gguf"

GE_CANDIDATES = [
    REPO / "target" / "release" / "ge.exe",
    REPO / "target-gpu-rebuild" / "release" / "ge.exe",
]
BENCH_CANDIDATES = [
    REPO / "target" / "m55" / "release" / "decode_1b_bench.exe",
    REPO / "target" / "release" / "decode_1b_bench.exe",
    REPO / "target-gpu-rebuild" / "release" / "decode_1b_bench.exe",
]

NGL = 24
THREADS = 6
HELLO_N = 24
BENCH_N = 16
# Steady/last must be well below weight file size (<< means <50%).
WEIGHT_FRAC_LIMIT = 0.50
MIN_FREE_MIB = 4096


def pick(paths: list[Path]) -> Path | None:
    return next((p for p in paths if p.is_file()), None)


def weight_mib() -> float:
    if not WEIGHT.is_file():
        raise SystemExit(f"missing weight file {WEIGHT}")
    return WEIGHT.stat().st_size / (1024 * 1024)


def nvidia_mem() -> tuple[int | None, int | None, int | None]:
    try:
        r = subprocess.run(
            [
                "nvidia-smi",
                "--query-gpu=memory.total,memory.used,memory.free",
                "--format=csv,noheader,nounits",
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if r.returncode == 0 and r.stdout.strip():
            parts = [p.strip() for p in r.stdout.strip().splitlines()[0].split(",")]
            return int(parts[0]), int(parts[1]), int(parts[2])
    except Exception:
        pass
    return None, None, None


def kill_stale() -> None:
    for im in (
        "decode_1b_bench.exe",
        "ge_soak_decode_8b.exe",
        "rss_probe.exe",
        "r5_quiet_decode_bench.exe",
        "r4_quiet_decode_bench.exe",
    ):
        subprocess.run(["taskkill", "/F", "/IM", im], capture_output=True)
    time.sleep(1.0)


def set_below_normal(pid: int) -> bool:
    try:
        import ctypes

        handle = ctypes.windll.kernel32.OpenProcess(0x0200, False, pid)
        if not handle:
            return False
        ok = bool(ctypes.windll.kernel32.SetPriorityClass(handle, 0x00004000))
        ctypes.windll.kernel32.CloseHandle(handle)
        return ok
    except Exception:
        return False


def base_env(host_release: bool) -> dict[str, str]:
    env = os.environ.copy()
    env["GE_GPU_LAYERS"] = str(NGL)
    env["GE_HOST_RELEASE"] = "1" if host_release else "0"
    env["GE_CTX"] = "512"
    env["GE_GPU_KV_MAX_SEQ"] = "512"
    env["GE_THREADS"] = str(THREADS)
    env["GE_REPACK"] = "0"
    env["GE_GEMV_Q8"] = "0"
    env["GE_CUDA_GRAPH"] = "0"
    env["GE_GPU_ATTN_8B"] = "0"
    env["GE_BENCH_IGNORE_EOS"] = "1"
    env.pop("GE_GPU_ACT_F32", None)
    env.pop("GE_LAYER_STREAM", None)
    env["GREEN_ENGINE_KERNELS_DIR"] = str(KERNELS)
    bench = pick(BENCH_CANDIDATES)
    ge = pick(GE_CANDIDATES)
    path_bits = [str(KERNELS), CUDA_BIN, env.get("PATH", "")]
    if bench:
        path_bits.insert(0, str(bench.parent))
    if ge:
        path_bits.insert(0, str(ge.parent))
    env["PATH"] = os.pathsep.join(path_bits)
    return env


def poll_rss_vram(
    pid: int,
    rss: list[int],
    free_s: list[int],
    vram_s: list[int],
    stop: threading.Event,
) -> None:
    try:
        import psutil

        proc = psutil.Process(pid)
    except Exception:
        proc = None
    while not stop.is_set():
        if proc is not None:
            try:
                rss.append(proc.memory_info().rss)
            except Exception:
                pass
        _t, used, free = nvidia_mem()
        if used is not None:
            vram_s.append(used)
        if free is not None:
            free_s.append(free)
        time.sleep(0.05)


def summarize_rss(rss: list[int]) -> dict:
    if not rss:
        return {
            "peak_rss_mib": None,
            "last_rss_mib": None,
            "steady_rss_mib": None,
            "samples": 0,
        }
    peak = max(rss) / (1024 * 1024)
    last = rss[-1] / (1024 * 1024)
    # Post-release window: last 20% of samples (after H2D peak).
    tail_n = max(1, len(rss) // 5)
    post = rss[-tail_n:]
    post_avg = (sum(post) / len(post)) / (1024 * 1024)
    mid = len(rss) // 2
    mid_avg = (sum(rss[mid:]) / len(rss[mid:])) / (1024 * 1024)
    return {
        "peak_rss_mib": round(peak, 1),
        "last_rss_mib": round(last, 1),
        "steady_rss_mib": round(post_avg, 1),
        "midhalf_avg_rss_mib": round(mid_avg, 1),
        "samples": len(rss),
    }


def hello_ok(text: str | None, blob: str) -> bool:
    hay = ((text or "") + "\n" + (blob or "")).lower()
    return "hello" in hay or "\nhi" in hay or "greet" in hay


def run_bench(host_release: bool) -> dict:
    bench = pick(BENCH_CANDIDATES)
    if bench is None:
        return {"available": False, "error": "decode_1b_bench.exe missing"}
    kill_stale()
    time.sleep(0.5)
    idle_t, idle_u, idle_f = nvidia_mem()
    env = base_env(host_release)
    rss: list[int] = []
    free_s: list[int] = []
    vram_s: list[int] = []
    stop = threading.Event()
    proc = subprocess.Popen(
        [str(bench), str(MODEL), str(BENCH_N)],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        cwd=str(REPO),
    )
    prio = set_below_normal(proc.pid)
    th = threading.Thread(
        target=poll_rss_vram, args=(proc.pid, rss, free_s, vram_s, stop), daemon=True
    )
    th.start()
    try:
        out, _ = proc.communicate(timeout=900)
    except subprocess.TimeoutExpired:
        proc.kill()
        out, _ = proc.communicate()
        out = (out or "") + "\nTIMEOUT"
    stop.set()
    th.join(timeout=2)
    summ = summarize_rss(rss)
    warm = None
    m = re.search(r"warm:.*?\| decode=([\d.]+)\s*tok/s", out or "", re.S | re.I)
    if m:
        warm = float(m.group(1))
    text = None
    tm = re.search(r'text_warm="((?:\\.|[^"\\])*)"', out or "")
    if tm:
        try:
            text = tm.group(1).encode("utf-8").decode("unicode_escape")
        except Exception:
            text = tm.group(1)
    layers = None
    lm = re.search(r"CUDA decode:\s*(\d+)\s*layer", out or "", re.I)
    if lm:
        layers = int(lm.group(1))
    max_v = max(vram_s) if vram_s else None
    min_f = min(free_s) if free_s else None
    delta = (max_v - idle_u) if (max_v is not None and idle_u is not None) else None
    return {
        "available": True,
        "mode": "bench",
        "host_release": host_release,
        "exe": str(bench),
        "exit": proc.returncode,
        "below_normal": prio,
        "threads": THREADS,
        "ngl": NGL,
        "layers_on_gpu": layers,
        "warm_decode_tok_s": warm,
        "text_warm": text,
        "hello_ok": hello_ok(text, out or ""),
        "host_ws_logged": "host WS release" in (out or ""),
        "idle_vram_mib": idle_u,
        "idle_free_mib": idle_f,
        "delta_vram_mib": delta,
        "min_free_mib": min_f,
        "vram_free_gate_ok": min_f is not None and min_f >= MIN_FREE_MIB,
        **summ,
        "stdout_tail": "\n".join((out or "").splitlines()[-16:]),
        "out": out or "",
    }


def run_ge_hello(host_release: bool) -> dict:
    ge = pick(GE_CANDIDATES)
    if ge is None:
        return {"available": False, "error": "ge.exe missing"}
    kill_stale()
    time.sleep(0.5)
    idle_t, idle_u, idle_f = nvidia_mem()
    env = base_env(host_release)
    env.pop("GE_BENCH_IGNORE_EOS", None)
    rss: list[int] = []
    free_s: list[int] = []
    vram_s: list[int] = []
    stop = threading.Event()
    cmd = [
        str(ge),
        "run",
        str(MODEL),
        "--prompt",
        "Hello",
        "-n",
        str(HELLO_N),
        "--gpu-layers",
        str(NGL),
        "--ctx",
        "512",
        "--threads",
        str(THREADS),
    ]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        cwd=str(REPO),
    )
    prio = set_below_normal(proc.pid)
    th = threading.Thread(
        target=poll_rss_vram, args=(proc.pid, rss, free_s, vram_s, stop), daemon=True
    )
    th.start()
    try:
        out, _ = proc.communicate(timeout=600)
    except subprocess.TimeoutExpired:
        proc.kill()
        out, _ = proc.communicate()
        out = (out or "") + "\nTIMEOUT"
    stop.set()
    th.join(timeout=2)
    summ = summarize_rss(rss)
    toks = None
    m = re.search(r"decode=([\d.]+)\s*tok/s", out or "", re.I)
    if m:
        toks = float(m.group(1))
    reply = None
    for line in (out or "").splitlines():
        if "Hello" in line or "hello" in line.lower():
            reply = line.strip()
            break
    max_v = max(vram_s) if vram_s else None
    min_f = min(free_s) if free_s else None
    delta = (max_v - idle_u) if (max_v is not None and idle_u is not None) else None
    return {
        "available": True,
        "mode": "ge_hello",
        "host_release": host_release,
        "exe": str(ge),
        "exit": proc.returncode,
        "below_normal": prio,
        "threads": THREADS,
        "ngl": NGL,
        "tok_s": toks,
        "reply": reply,
        "hello_ok": hello_ok(reply, out or ""),
        "host_ws_logged": "host WS release" in (out or ""),
        "idle_vram_mib": idle_u,
        "idle_free_mib": idle_f,
        "delta_vram_mib": delta,
        "min_free_mib": min_f,
        "vram_free_gate_ok": min_f is not None and min_f >= MIN_FREE_MIB,
        **summ,
        "stdout_tail": "\n".join((out or "").splitlines()[-16:]),
        "out": out or "",
    }


def gate_check(row: dict, w_mib: float) -> dict:
    limit = w_mib * WEIGHT_FRAC_LIMIT
    # Prefer last (post-exit sample) then post-tail steady.
    steady = row.get("last_rss_mib")
    if steady is None:
        steady = row.get("steady_rss_mib")
    peak = row.get("peak_rss_mib")
    checks = {
        "exit_ok": row.get("exit") == 0,
        "rss_sampled": steady is not None,
        "steady_lt_half_weight": steady is not None and steady < limit,
        "steady_lt_weight": steady is not None and steady < w_mib,
        "peak_reported": peak is not None,
        "hello_ok": bool(row.get("hello_ok")),
        "host_ws_logged": bool(row.get("host_ws_logged")),
        "vram_free_ge_4gib": row.get("vram_free_gate_ok") is True,
        "below_normal": bool(row.get("below_normal")),
    }
    # Required for PASS: exit, RSS gate, hello, host WS log, VRAM free.
    required = (
        "exit_ok",
        "rss_sampled",
        "steady_lt_half_weight",
        "hello_ok",
        "host_ws_logged",
        "vram_free_ge_4gib",
    )
    ok = all(checks[k] for k in required)
    return {
        "ok": ok,
        "weight_mib": round(w_mib, 1),
        "weight_half_limit_mib": round(limit, 1),
        "gate_steady_rss_mib": steady,
        "gate_peak_rss_mib": peak,
        "checks": checks,
    }


def main() -> int:
    OUT.mkdir(parents=True, exist_ok=True)
    w_mib = weight_mib()
    ge = pick(GE_CANDIDATES)
    bench = pick(BENCH_CANDIDATES)
    print(f"M5.5 8B host-WS gate ngl={NGL} threads={THREADS} BelowNormal")
    print(f"weight={WEIGHT} size={w_mib:.1f} MiB  half_limit={w_mib * WEIGHT_FRAC_LIMIT:.1f} MiB")
    print(f"ge={ge}")
    print(f"bench={bench}")
    assert MODEL.is_dir(), f"missing model {MODEL}"
    assert bench is not None, "missing decode_1b_bench.exe"

    print("=== HOST_RELEASE=1 bench ngl=24 ===", flush=True)
    r_on = run_bench(True)
    print(
        {
            "exit": r_on.get("exit"),
            "peak_rss_mib": r_on.get("peak_rss_mib"),
            "last_rss_mib": r_on.get("last_rss_mib"),
            "steady_rss_mib": r_on.get("steady_rss_mib"),
            "midhalf_avg_rss_mib": r_on.get("midhalf_avg_rss_mib"),
            "tok_s": r_on.get("warm_decode_tok_s"),
            "min_free_mib": r_on.get("min_free_mib"),
            "delta_vram_mib": r_on.get("delta_vram_mib"),
            "hello_ok": r_on.get("hello_ok"),
            "host_ws_logged": r_on.get("host_ws_logged"),
        },
        flush=True,
    )
    for line in (r_on.get("out") or "").splitlines():
        if "host WS" in line or "CUDA decode" in line:
            print(line, flush=True)

    print("gap 15s ...", flush=True)
    time.sleep(15)

    print("=== HOST_RELEASE=0 bench ngl=24 (A/B) ===", flush=True)
    r_off = run_bench(False)
    print(
        {
            "exit": r_off.get("exit"),
            "peak_rss_mib": r_off.get("peak_rss_mib"),
            "last_rss_mib": r_off.get("last_rss_mib"),
            "steady_rss_mib": r_off.get("steady_rss_mib"),
            "tok_s": r_off.get("warm_decode_tok_s"),
            "host_ws_logged": r_off.get("host_ws_logged"),
        },
        flush=True,
    )

    print("gap 15s ...", flush=True)
    time.sleep(15)

    print("=== Hello smoke ge run ngl=24 HOST_RELEASE=1 ===", flush=True)
    hello = run_ge_hello(True) if ge else {"available": False, "error": "ge missing"}
    if hello.get("available"):
        print(
            {
                "exit": hello.get("exit"),
                "peak_rss_mib": hello.get("peak_rss_mib"),
                "last_rss_mib": hello.get("last_rss_mib"),
                "steady_rss_mib": hello.get("steady_rss_mib"),
                "tok_s": hello.get("tok_s"),
                "hello_ok": hello.get("hello_ok"),
                "reply": (hello.get("reply") or "")[:100],
                "host_ws_logged": hello.get("host_ws_logged"),
                "min_free_mib": hello.get("min_free_mib"),
            },
            flush=True,
        )
        for line in (hello.get("out") or "").splitlines():
            if "host WS" in line or "CUDA decode" in line:
                print(line, flush=True)
    else:
        print(hello, flush=True)
        # Fall back: use bench text_warm as Hello smoke.
        hello = {
            **{k: r_on.get(k) for k in (
                "exit", "peak_rss_mib", "last_rss_mib", "steady_rss_mib",
                "hello_ok", "host_ws_logged", "min_free_mib", "vram_free_gate_ok",
                "below_normal",
            )},
            "available": True,
            "mode": "bench_text_warm_fallback",
            "reply": r_on.get("text_warm"),
            "tok_s": r_on.get("warm_decode_tok_s"),
            "source": "decode_1b_bench",
        }

    g_on = gate_check(r_on, w_mib)
    g_hello = gate_check(hello, w_mib) if hello.get("available") else {"ok": False}

    print("=== GATE ===", flush=True)
    print(
        f"weight={w_mib:.1f} MiB  half_limit={w_mib * WEIGHT_FRAC_LIMIT:.1f} MiB",
        flush=True,
    )
    print(
        f"RELEASE=1 peak={r_on.get('peak_rss_mib')} last={r_on.get('last_rss_mib')} "
        f"steady_tail={r_on.get('steady_rss_mib')} midhalf={r_on.get('midhalf_avg_rss_mib')} MiB",
        flush=True,
    )
    print(
        f"RELEASE=0 peak={r_off.get('peak_rss_mib')} last={r_off.get('last_rss_mib')} "
        f"steady_tail={r_off.get('steady_rss_mib')} MiB",
        flush=True,
    )
    if r_on.get("last_rss_mib") and r_off.get("last_rss_mib"):
        print(
            f"A/B last RSS delta OFF-ON = "
            f"{r_off['last_rss_mib'] - r_on['last_rss_mib']:.1f} MiB",
            flush=True,
        )
    print(f"bench gate: {g_on}", flush=True)
    print(f"hello gate: {g_hello}", flush=True)

    ok = g_on["ok"] and bool(hello.get("hello_ok")) and (
        hello.get("exit") == 0 if hello.get("exit") is not None else True
    )
    if g_on["ok"]:
        print(
            f"PASS: steady/last RSS {g_on['gate_steady_rss_mib']} << weight {w_mib:.1f} "
            f"(<{g_on['weight_half_limit_mib']}) peak={g_on['gate_peak_rss_mib']}",
            flush=True,
        )
    else:
        print("FAIL: bench host-release RSS / quality / VRAM gate", flush=True)
        for k, v in g_on["checks"].items():
            if not v:
                print(f"  fail check: {k}", flush=True)

    if hello.get("hello_ok"):
        print("PASS: Hello smoke", flush=True)
    else:
        print("FAIL: Hello smoke", flush=True)
        ok = False

    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    report = {
        "suite": "m55_host_release_8b_ngl24",
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "stamp": stamp,
        "model": str(MODEL),
        "weight_file": str(WEIGHT),
        "weight_mib": round(w_mib, 1),
        "weight_half_limit_mib": round(w_mib * WEIGHT_FRAC_LIMIT, 1),
        "constraints": {
            "ngl": NGL,
            "threads": THREADS,
            "priority": "BelowNormal",
            "ge_ctx": 512,
            "ge_gpu_kv_max_seq": 512,
            "min_free_vram_mib": MIN_FREE_MIB,
            "weight_frac_limit": WEIGHT_FRAC_LIMIT,
        },
        "release_on": {k: v for k, v in r_on.items() if k != "out"},
        "release_off": {k: v for k, v in r_off.items() if k != "out"},
        "hello": {k: v for k, v in hello.items() if k != "out"},
        "gate_bench": g_on,
        "gate_hello": g_hello if isinstance(g_hello, dict) else g_hello,
        "verdict": {"pass": ok},
    }
    out_path = OUT / f"m55_host_release_8b_ngl24_{stamp}.json"
    out_path.write_text(json.dumps(report, indent=2), encoding="utf-8")
    print("WROTE", out_path, flush=True)
    kill_stale()
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
