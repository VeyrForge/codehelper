#!/usr/bin/env python3
"""Quality gate: Q4_K_M prefill top token + 5-prompt IF harness score."""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

PROMPTS = [
    "Say hello in one short sentence.",
    "What is 2+2? Answer with one number.",
    "Name one primary color.",
    "Complete: The sky is",
    "Reply with only the word OK.",
]

REPO = Path(__file__).resolve().parents[1]
DEFAULT_MODEL = Path.home() / ".green" / "models" / "Llama-3.2-1B.green"
DEFAULT_Q4_GGUF = Path.home() / ".green" / "models" / "Llama-3.2-1B-Instruct-Q4_K_M.gguf"
EXPECTED_TOP = 48590


def score_if(prompt: str, text: str) -> int:
    t = text.strip().lower()
    if "2+2" in prompt:
        return 5 if t in ("4", "4.", "four") or t.startswith("4") else 1
    if "primary color" in prompt:
        return 5 if any(c in t for c in ("red", "blue", "yellow")) else 2
    if "sky is" in prompt:
        return 5 if "blue" in t or "not" in t else 3
    if "only the word ok" in prompt.lower():
        return 5 if t == "ok" else (0 if not t else 2)
    if "hello" in prompt.lower():
        return 5 if "hello" in t and len(t) < 80 else 3
    return 3


def release_exe(name: str) -> Path | None:
    for suffix in (".exe", ""):
        p = REPO / "target" / "release" / f"{name}{suffix}"
        if p.is_file():
            return p
    return None


def run_cmd(args: list[str], *, label: str, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    print(f">> {label}", flush=True)
    proc = subprocess.run(
        args,
        cwd=cwd or REPO,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if proc.stdout:
        print(proc.stdout, end="", flush=True)
    if proc.stderr:
        print(proc.stderr, end="", file=sys.stderr, flush=True)
    return proc


def verify_q4km_prefill(*, expected_top: int, gguf: Path) -> tuple[bool, str]:
    exe = release_exe("diag_q4km_prefill")
    if not exe:
        build = run_cmd(
            ["cargo", "build", "--release", "-p", "engine-core", "--bin", "diag_q4km_prefill"],
            label="cargo build diag_q4km_prefill",
        )
        if build.returncode != 0:
            return False, "diag_q4km_prefill build failed"
        exe = release_exe("diag_q4km_prefill")
        if not exe:
            return False, "diag_q4km_prefill binary missing after build"

    if not gguf.is_file():
        return False, f"Q4_K_M GGUF not found: {gguf}"

    proc = run_cmd([str(exe), str(gguf)], label=f"diag_q4km_prefill {gguf.name}")
    if proc.returncode != 0:
        return False, f"diag_q4km_prefill exit {proc.returncode}"

    out = proc.stdout or ""
    m = re.search(r"^top (\d+)$", out, re.MULTILINE)
    if not m:
        return False, "diag_q4km_prefill: no top token line"
    top = int(m.group(1))
    if top != expected_top:
        return False, f"Q4_K_M prefill top {top} != expected {expected_top}"
    return True, f"Q4_K_M prefill top OK: {top}"


def wait_health(port: int, timeout: float = 300.0) -> bool:
    url = f"http://127.0.0.1:{port}/health"
    end = time.time() + timeout
    while time.time() < end:
        try:
            with urllib.request.urlopen(url, timeout=2) as resp:
                if resp.status == 200:
                    return True
        except Exception:
            time.sleep(0.5)
    return False


def chat(port: int, prompt: str, max_tokens: int = 32) -> str:
    body = json.dumps(
        {
            "model": "quality-gate",
            "messages": [{"role": "user", "content": prompt}],
            "max_tokens": max_tokens,
            "temperature": 0,
        }
    ).encode()
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/v1/chat/completions",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=180) as resp:
        data = json.loads(resp.read().decode())
    return data["choices"][0]["message"]["content"]


def isolated_serve_ge(ge: Path) -> tuple[Path, Path | None]:
    """Copy ge.exe to a distinct name so taskkill /IM ge.exe cannot stop this server."""
    if ge.name.lower() not in ("ge.exe", "ge"):
        return ge, None
    dest = ge.with_name(f"ge_qg_{os.getpid()}.exe")
    shutil.copy2(ge, dest)
    return dest, dest


def run_harness(*, model: Path, ge: Path, min_score: int, port: int) -> tuple[bool, str, int]:
    if not model.is_dir() or not (model / "manifest.json").is_file():
        return False, f".green model not found: {model}", 0
    if not ge.is_file():
        return False, f"ge binary not found: {ge}", 0

    env = os.environ.copy()
    env["GE_BENCH_IGNORE_EOS"] = "1"
    # Avoid PIPE deadlock while waiting on health; concurrent taskkill of ge.exe is mitigated by isolated_serve_ge (distinct binary name).
    if not env.get("GE_GPU_LAYERS", "").strip():
        env["GE_GPU_LAYERS"] = "99"
    kernels = Path(__file__).resolve().parents[1] / "crates" / "kernels"
    if kernels.is_dir() and not env.get("GREEN_ENGINE_KERNELS_DIR", "").strip():
        env["GREEN_ENGINE_KERNELS_DIR"] = str(kernels)
    err_path = Path(os.environ.get("TEMP", ".")) / f"ge_qg_serve_{port}.err.txt"
    err_f = open(err_path, "w", encoding="utf-8", errors="replace")
    serve_ge, isolated_copy = isolated_serve_ge(ge)
    proc = subprocess.Popen(
        [str(serve_ge), "chat", "serve", "--model", str(model), "--port", str(port)],
        stdout=subprocess.DEVNULL,
        stderr=err_f,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
    )
    try:
        if not wait_health(port):
            try:
                err_f.flush()
            except Exception:
                pass
            err = err_path.read_text(encoding="utf-8", errors="replace") if err_path.is_file() else ""
            return False, f"chat serve health timeout: {err[-500:]}", 0

        scores: list[int] = []
        for i, prompt in enumerate(PROMPTS):
            text = chat(port, prompt)
            s = score_if(prompt, text)
            scores.append(s)
            print(f"harness[{i}] score={s} text={text!r}", flush=True)

        total = sum(scores)
        ok = total >= min_score
        msg = f"5-prompt harness {total}/25 (min {min_score})"
        return ok, msg, total
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        try:
            err_f.close()
        except Exception:
            pass
        if isolated_copy is not None:
            try:
                isolated_copy.unlink(missing_ok=True)
            except Exception:
                pass


def main() -> int:
    parser = argparse.ArgumentParser(description="GreenEngine quality gate")
    parser.add_argument("--min-score", type=int, default=23, help="Minimum 5-prompt harness score")
    parser.add_argument("--expected-top", type=int, default=EXPECTED_TOP, help="Q4_K_M prefill top token")
    parser.add_argument("--model", type=Path, default=DEFAULT_MODEL, help=".green model for harness")
    parser.add_argument("--q4-gguf", type=Path, default=DEFAULT_Q4_GGUF, help="Q4_K_M GGUF for prefill gate")
    parser.add_argument("--ge", type=Path, default=REPO / "target" / "release" / "ge.exe")
    parser.add_argument("--port", type=int, default=18772)
    parser.add_argument("--skip-harness", action="store_true")
    parser.add_argument("--skip-q4km", action="store_true")
    args = parser.parse_args()

    results: dict[str, object] = {"pass": True, "checks": {}}

    if not args.skip_q4km:
        ok, msg = verify_q4km_prefill(expected_top=args.expected_top, gguf=args.q4_gguf)
        results["checks"]["q4km_prefill"] = {"pass": ok, "message": msg}
        print(f"q4km_prefill: {'PASS' if ok else 'FAIL'} — {msg}", flush=True)
        if not ok:
            results["pass"] = False

    if not args.skip_harness:
        ok, msg, total = run_harness(
            model=args.model, ge=args.ge, min_score=args.min_score, port=args.port
        )
        results["checks"]["harness"] = {"pass": ok, "message": msg, "score": total, "max": 25}
        print(f"harness: {'PASS' if ok else 'FAIL'} — {msg}", flush=True)
        if not ok:
            results["pass"] = False

    print(json.dumps(results, indent=2), flush=True)
    return 0 if results["pass"] else 1


if __name__ == "__main__":
    sys.exit(main())
