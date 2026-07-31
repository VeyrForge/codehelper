#!/usr/bin/env python3
"""Compress a whole GGUF model with Green Compress and emit a manifest."""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)

if HERE not in sys.path:
    sys.path.insert(0, HERE)
from tensor_policy import benchmark_cli_args, load_policy, resolve_method_type  # noqa: E402

MIN_QUALITY_PCT = 99.50
ESCALATION_TYPES = ("green_smart", "green_spqr_svd")


def parse_benchmark_metrics(text: str) -> dict[str, float]:
    out: dict[str, float] = {}
    for line in text.splitlines():
        parts = line.strip().split(None, 1)
        if len(parts) == 2:
            try:
                out[parts[0]] = float(parts[1])
            except ValueError:
                pass
    return out


def run_benchmark_cmd(cmd: list[str]) -> tuple[int, str, str]:
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode, r.stdout, r.stderr


def run(cmd: list[str]) -> str:
    code, out, err = run_benchmark_cmd(cmd)
    if code != 0:
        sys.stderr.write(f"FAILED: {' '.join(cmd)}\n{out}\n{err}\n")
        raise SystemExit(1)
    return out


def compress_one_tensor(rec, methods, mx_dir, npy_dir, acts, tdir, bin_path, policy, min_quality=MIN_QUALITY_PCT):
    name = rec["name"]
    safe = name.replace("/", "_")
    mx = os.path.join(mx_dir, safe + ".mx")
    if not os.path.isfile(mx):
        run([bin_path, "import-npy", "--in", os.path.join(npy_dir, rec["npy"]), "--out", mx])

    policy_args = benchmark_cli_args(name, policy) if policy else []
    dirs = {}
    used_types: dict[str, str] = {}
    for m in methods:
        outd = os.path.join(tdir, safe, m)
        if os.path.isdir(outd):
            shutil.rmtree(outd)
        os.makedirs(outd, exist_ok=True)
        tensor_type = resolve_method_type(name, m, policy) if policy else m

        types_to_try: list[str] = []
        for t in (tensor_type, *ESCALATION_TYPES):
            if t not in types_to_try:
                types_to_try.append(t)

        stdout = ""
        chosen_type = tensor_type
        quality = 0.0
        for attempt, try_type in enumerate(types_to_try):
            if os.path.isdir(outd):
                shutil.rmtree(outd)
            os.makedirs(outd, exist_ok=True)
            extra = policy_args if attempt == 0 else []
            cmd = [
                bin_path, "benchmark",
                "--method-id", m,
                "--type", try_type,
                "--in", mx,
                "--activations", acts[rec["rows"]],
                "--out-dir", outd,
                "--tensor-name", name,
                "--min-quality-pct", "0",
            ] + extra
            code, out, err = run_benchmark_cmd(cmd)
            if code != 0:
                sys.stderr.write(f"WARN {name} {try_type}: {err.strip()}\n")
                continue
            metrics = parse_benchmark_metrics(out)
            q = metrics.get("quality_accuracy_pct", 0.0)
            stdout = out
            chosen_type = try_type
            quality = q
            if q >= min_quality:
                break

        if not stdout:
            sys.stderr.write(f"FAILED all types for {name}\n")
            raise SystemExit(1)
        if quality < min_quality:
            sys.stderr.write(
                f"WARN {name}: best quality {quality:.2f}% below floor {min_quality:.2f}% "
                f"(type={chosen_type})\n"
            )

        with open(os.path.join(outd, "benchmark.txt"), "w", encoding="utf-8") as fh:
            fh.write(stdout)
        dirs[m] = os.path.abspath(outd)
        used_types[m] = chosen_type

    return {
        "name": name,
        "rows": rec["rows"],
        "cols": rec["cols"],
        "ggml_type": rec["ggml_type"],
        "ref": os.path.abspath(mx),
        "dirs": dirs,
        "policy_args": policy_args,
        "tensor_type": used_types.get(methods[0], methods[0]),
    }


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--gguf", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--methods", default="green_optimal,green_adaptive")
    ap.add_argument("--layers", default="")
    ap.add_argument("--include", default="")
    ap.add_argument("--exclude", default="")
    ap.add_argument("--with-embd", action="store_true")
    ap.add_argument("--tensor-policy", default=os.path.join(REPO, "config", "tensor_policy.json"))
    ap.add_argument("--policy-preset", choices=["default", "green_optimal", "none"], default="default")
    ap.add_argument("--jobs", type=int, default=max(1, min(4, (os.cpu_count() or 2))))
    ap.add_argument("--bin", default=os.path.join(REPO, "bin", "greencompress"))
    ap.add_argument("--python", default=os.environ.get("GREEN_PYTHON", "python3"))
    ap.add_argument("--tokens", type=int, default=32)
    ap.add_argument("--skip-extract", action="store_true")
    a = ap.parse_args()

    methods = [m.strip() for m in a.methods.split(",") if m.strip()]
    npy_dir = os.path.join(a.out, "npy")
    mx_dir = os.path.join(a.out, "mx")
    acts_dir = os.path.join(a.out, "acts")
    tdir = os.path.join(a.out, "tensors")
    for d in (npy_dir, mx_dir, acts_dir, tdir):
        os.makedirs(d, exist_ok=True)

    policy: dict = {}
    if a.policy_preset == "green_optimal":
        a.tensor_policy = os.path.join(REPO, "config", "tensor_policy.json")

    tensors_json = os.path.join(npy_dir, "tensors.json")
    if a.skip_extract and os.path.isfile(tensors_json):
        print("[extract] skipped (reuse existing npy/)")
    else:
        extract = [a.python, os.path.join(HERE, "extract_gguf.py"), a.gguf, npy_dir]
        if a.layers:
            extract += ["--layers", a.layers]
        if a.include:
            extract += ["--include", a.include]
        if a.exclude:
            extract += ["--exclude", a.exclude]
        if a.with_embd:
            extract += ["--with-embd"]
        print("[extract]")
        print(run(extract))

    with open(tensors_json, encoding="utf-8") as fh:
        index = json.load(fh)

    if a.policy_preset != "none" and os.path.isfile(a.tensor_policy):
        policy = load_policy(a.tensor_policy)

    acts = {}
    for rec in index["tensors"]:
        rows = rec["rows"]
        if rows not in acts:
            p = os.path.join(acts_dir, f"acts_{rows}.mx")
            if not os.path.isfile(p):
                run([a.bin, "gen-activations", "--out", p,
                     "--rows", str(a.tokens), "--cols", str(rows), "--seed", "7"])
            acts[rows] = p

    for rec in index["tensors"]:
        safe = rec["name"].replace("/", "_")
        mx = os.path.join(mx_dir, safe + ".mx")
        if not os.path.isfile(mx):
            run([a.bin, "import-npy", "--in", os.path.join(npy_dir, rec["npy"]), "--out", mx])

    manifest_tensors = []
    jobs = max(1, a.jobs)
    if jobs == 1:
        for rec in index["tensors"]:
            entry = compress_one_tensor(rec, methods, mx_dir, npy_dir, acts, tdir, a.bin, policy)
            manifest_tensors.append({k: v for k, v in entry.items() if k != "policy_args"})
            extra = " ".join(entry.get("policy_args") or [])
            print(f"[compress] {rec['name']:28} ({rec['rows']}x{rec['cols']}) -> {', '.join(methods)} {extra}")
    else:
        with ThreadPoolExecutor(max_workers=jobs) as pool:
            futures = {
                pool.submit(compress_one_tensor, rec, methods, mx_dir, npy_dir, acts, tdir, a.bin, policy): rec
                for rec in index["tensors"]
            }
            for fut in as_completed(futures):
                rec = futures[fut]
                entry = fut.result()
                manifest_tensors.append({k: v for k, v in entry.items() if k != "policy_args"})
                extra = " ".join(entry.get("policy_args") or [])
                print(f"[compress] {rec['name']:28} ({rec['rows']}x{rec['cols']}) -> {', '.join(methods)} {extra}")

    manifest_tensors.sort(key=lambda t: t["name"])
    manifest = {
        "model": index["model"],
        "arch": index["arch"],
        "methods": methods,
        "tokens": a.tokens,
        "tensor_policy": os.path.abspath(a.tensor_policy) if policy else None,
        "tensors": manifest_tensors,
    }
    mpath = os.path.join(a.out, "model_manifest.json")
    with open(mpath, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2)
    print(f"\nmanifest: {mpath}  ({len(manifest['tensors'])} tensors, methods={methods}, jobs={jobs})")


if __name__ == "__main__":
    main()
