#!/usr/bin/env python3
"""Green Engine UI — local dashboard for setup, run, compress, bench, and servers.

  ge ui serve
  open http://127.0.0.1:8780

Stdlib only. Shells out to `ge` and runner scripts; binds loopback by default.
See docs/GE_UI.md for the full plan and API contract.
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

# Sibling module (runner/)
sys.path.insert(0, str(Path(__file__).resolve().parent))
from hf_catalog import (  # noqa: E402
    RELIABILITY_HELP,
    USE_CASE_HELP,
    enrich_repo,
    infer_use_cases,
    score_for_hardware,
    search_hf,
)

GE_HOME = Path(os.environ.get("GE_HOME", Path.home() / ".green"))
GE_BIN = os.environ.get("GE_BIN", "")
DEFAULT_HOST = os.environ.get("GE_UI_HOST", "127.0.0.1")
DEFAULT_PORT = int(os.environ.get("GE_UI_PORT", "8780"))
EMBED_PORT = int(os.environ.get("GE_EMBED_PORT", "8766"))
CHAT_PORT = int(os.environ.get("GE_CHAT_PORT", "8767"))
CHAT_PROXY_TIMEOUT = int(os.environ.get("GE_CHAT_PROXY_TIMEOUT", "7200"))
TRANSLATE_PORT = int(os.environ.get("GE_TRANSLATE_PORT", "8768"))
COMPRESS_DIR = "green-compress"
UI_DIR = Path(__file__).resolve().parent / "ui"
UI_LOGS = GE_HOME / "ui-logs"
UI_PIDS = GE_HOME / "ui-pids"
# Embed/chat must not bind the dashboard port (8780).
RESERVED_PORTS = {8780}

_jobs_lock = threading.Lock()
_jobs: dict[str, dict[str, Any]] = {}
_server_lock = threading.Lock()


def _find_ge_bin() -> str | None:
    if GE_BIN and Path(GE_BIN).is_file():
        return GE_BIN
    for name in ("ge",):
        try:
            out = subprocess.run(
                ["sh", "-c", f"command -v {name}"],
                capture_output=True,
                text=True,
                check=False,
            )
            p = out.stdout.strip()
            if p:
                return p
        except OSError:
            pass
    root = Path(__file__).resolve().parent.parent
    cand = root / "target/release/ge"
    if cand.is_file():
        return str(cand)
    return None


def _find_greencompress() -> str | None:
    for c in [
        GE_HOME / COMPRESS_DIR / "bin/greencompress",
        Path("bin/greencompress"),
    ]:
        if c.is_file():
            return str(c)
    try:
        out = subprocess.run(
            ["sh", "-c", "command -v greencompress"],
            capture_output=True,
            text=True,
            check=False,
        )
        p = out.stdout.strip()
        if p:
            return p
    except OSError:
        pass
    sibling = Path(__file__).resolve().parent.parent.parent / "green-compress/bin/greencompress"
    if sibling.is_file():
        return str(sibling)
    return None


def _find_compress_script() -> Path | None:
    for base in [
        GE_HOME / COMPRESS_DIR,
        Path(__file__).resolve().parent.parent.parent / "green-compress",
    ]:
        cand = base / "scripts/compress_model.py"
        if cand.is_file():
            return cand
    return None


def _find_runner(name: str) -> Path | None:
    env_map = {
        "green_chat.py": "GE_CHAT_SCRIPT",
        "green_embed.py": "GE_EMBED_SCRIPT",
        "green_translate.py": "GE_TRANSLATE_SCRIPT",
    }
    env_key = env_map.get(name, f"GE_{name.upper().replace('.PY', '')}_SCRIPT")
    if os.environ.get(env_key):
        p = Path(os.environ[env_key])
        if p.is_file():
            return p
    root = os.environ.get("GE_ENGINE_ROOT")
    if root:
        cand = Path(root) / "runner" / name
        if cand.is_file():
            return cand
    here = Path(__file__).resolve().parent
    for cand in (here / name, here / "runner" / name):
        if cand.is_file():
            return cand
    ge = _find_ge_bin()
    if ge:
        d = Path(ge).resolve().parent
        for _ in range(8):
            cand = d / "runner" / name
            if cand.is_file():
                return cand
            parent = d.parent
            if parent == d:
                break
            d = parent
    return None


def _validate_chat_model(path: str) -> str | None:
    p = Path(_expand_home(path))
    if not p.is_file():
        return f"model not found: {p}"
    name = p.name.lower()
    if re.search(r"\d{5}-of-\d{5}", name):
        return (
            "GGUF shard (not a full model) — click Fresh Q4 on the catalog card "
            "to download the complete Q4_K_M file"
        )
    if ("fp16" in name or "-f16" in name) and "q4" not in name:
        return "FP16 file is not chat-ready — use Fresh Q4 to pull Q4_K_M instead"
    return None


def _chat_runtime_snapshot_path() -> Path:
    return GE_HOME / "ui" / "chat-runtime.json"


def _write_chat_runtime_snapshot(model: str, ctx: int, gpu_layers: int) -> None:
    snap = {
        "model_path": model,
        "model_name": Path(model).name,
        "ctx": ctx,
        "gpu_layers": gpu_layers,
        "started_at": time.time(),
    }
    path = _chat_runtime_snapshot_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(snap), encoding="utf-8")


def _read_chat_runtime_snapshot() -> dict[str, Any]:
    path = _chat_runtime_snapshot_path()
    if not path.is_file():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def _chat_log_text(max_bytes: int = 512_000) -> str:
    """Read chat log — whole file when small; tail when huge (CUDA spam fills recent tail)."""
    path = _log_file("chat")
    if not path.is_file():
        return ""
    try:
        size = path.stat().st_size
        if size <= max_bytes:
            return path.read_text(encoding="utf-8", errors="replace")
        return _read_tail(path, max_bytes)
    except OSError:
        return ""


def _chat_log_last_match(pattern: str, log: str) -> re.Match[str] | None:
    matches = list(re.finditer(pattern, log, re.I))
    return matches[-1] if matches else None


def _chat_log_excerpt(max_bytes: int = 2000) -> str:
    return _chat_log_text(max_bytes).strip()


def _chat_runtime_info() -> dict[str, Any]:
    """Parse the running chat server from snapshot + ui-chat.log."""
    log = _chat_log_text(512_000)
    snap = _read_chat_runtime_snapshot()
    info: dict[str, Any] = {
        "loaded": False,
        "model_name": "",
        "model_path": "",
        "ctx": 0,
        "gpu_layers": 0,
        "backend": "gguf",
        "note": "Chat uses the .gguf file via llama.cpp — not the Green Compress manifest",
    }
    if snap.get("model_name"):
        info["loaded"] = True
        info["model_name"] = snap["model_name"]
        info["model_path"] = snap.get("model_path") or ""
        info["ctx"] = int(snap.get("ctx") or 0)
        info["gpu_layers"] = int(snap.get("gpu_layers") or 0)
    if log:
        m = _chat_log_last_match(r"model=([^\s]+)", log)
        c = _chat_log_last_match(r"ctx=(\d+)", log)
        g = _chat_log_last_match(
            r"gpu_layers=(\d+)|--n_gpu_layers\s+(\d+)|--gpu-layers\s+(\d+)|-ngl\s+(\d+)",
            log,
        )
        path_m = _chat_log_last_match(r"--model\s+(/[^\s]+|~/.+?\.gguf)", log)
        if m:
            info["loaded"] = True
            info["model_name"] = m.group(1)
        if path_m:
            info["model_path"] = _expand_home(path_m.group(1))
        elif info["model_name"] and not info["model_path"]:
            for row in _list_models():
                if row["name"] == info["model_name"]:
                    info["model_path"] = row["path"]
                    break
        if c:
            info["ctx"] = int(c.group(1))
        if g:
            info["gpu_layers"] = int(next(g for g in g.groups() if g is not None))
        off = _chat_log_last_match(r"offloaded\s+(\d+)/(\d+)\s+layers", log)
        if off:
            info["gpu_layers"] = int(off.group(1))
            info["gpu_layers_total"] = int(off.group(2))
            info["loaded"] = True
        if info["gpu_layers"] == 0 and re.search(r"ggml_cuda|CUDA Graph|cuda", log, re.I):
            info["cuda_active"] = True
            if off:
                info["gpu_layers"] = int(off.group(1))
    if info["model_path"]:
        comp = _is_model_compressed(info["model_path"])
        info["compressed_on_disk"] = comp
    return info


def _chat_server_healthy() -> bool:
    return _server_status("chat", CHAT_PORT, ["/v1/models", "/health", "/"]).get("up", False)


def _chat_venv_python() -> Path:
    return GE_HOME / "chat-venv" / "bin/python"


def _embed_venv_python() -> Path:
    return GE_HOME / "embed-venv" / "bin/python"


_SERVICE_PORTS: dict[str, int] = {
    "embed": EMBED_PORT,
    "chat": CHAT_PORT,
    "translate": TRANSLATE_PORT,
}


def _kill_pid_tree(pid: int) -> None:
    """Stop a background server and its children (llama_cpp.server)."""
    try:
        pgid = os.getpgid(pid)
        os.killpg(pgid, signal.SIGTERM)
    except (OSError, ProcessLookupError):
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError:
            return
    time.sleep(0.4)
    try:
        if _is_running(pid):
            try:
                os.killpg(os.getpgid(pid), signal.SIGKILL)
            except (OSError, ProcessLookupError):
                os.kill(pid, signal.SIGKILL)
    except OSError:
        pass


def _pkill_pattern(pattern: str) -> None:
    try:
        subprocess.run(["pkill", "-f", pattern], check=False, timeout=5)
    except (OSError, subprocess.TimeoutExpired):
        pass


def _expand_home(s: str) -> str:
    if s.startswith("~/"):
        return str(Path.home() / s[2:])
    return s


def _compressed_dir_for(gguf_path: str | Path) -> Path | None:
    stem = Path(gguf_path).stem
    cand = GE_HOME / "compressed" / stem
    if (cand / "model_manifest.json").is_file():
        return cand
    return None


def _is_model_compressed(gguf_path: str | Path) -> bool:
    return _compressed_dir_for(gguf_path) is not None


def _list_models() -> list[dict[str, Any]]:
    models_dir = GE_HOME / "models"
    out: list[dict[str, Any]] = []
    if not models_dir.is_dir():
        return out
    for p in sorted(models_dir.glob("*.gguf")):
        try:
            st = p.stat()
            comp = _compressed_dir_for(p)
            row: dict[str, Any] = {
                "name": p.name,
                "path": str(p),
                "size": st.st_size,
                "compressed": comp is not None,
            }
            if comp:
                row["compressed_path"] = str(comp)
            out.append(row)
        except OSError:
            continue
    return out


def _remove_model_files(path: str) -> dict[str, Any]:
    p = Path(_expand_home(path)).resolve()
    models_dir = (GE_HOME / "models").resolve()
    try:
        p.relative_to(models_dir)
    except ValueError:
        return {"ok": False, "error": "path must be under ~/.green/models"}
    if not p.is_file():
        return {"ok": False, "error": "file not found"}
    comp = GE_HOME / "compressed" / p.stem
    chat_log = _log_file("chat")
    if chat_log.is_file():
        try:
            if p.name in chat_log.read_text(encoding="utf-8", errors="replace"):
                _stop_service("chat")
        except OSError:
            pass
    p.unlink()
    comp_removed = None
    if comp.is_dir():
        shutil.rmtree(comp)
        comp_removed = str(comp)
    return {"ok": True, "removed": str(p), "compressed_removed": comp_removed}


def _find_pulled_model(repo: str, file_pattern: str) -> str | None:
    """Best-effort match for a freshly pulled GGUF under ~/.green/models."""
    slug = repo.split("/")[-1].lower().replace("-", "")
    needle = file_pattern.strip("*").lower()
    for m in reversed(_list_models()):
        name = m["name"].lower()
        norm = name.replace("-", "").replace("_", "")
        if slug.replace("-", "") in norm or (needle and needle in name):
            return m["path"]
    local = _list_models()
    return local[-1]["path"] if local else None


def _attach_local_downloads(entries: list[dict[str, Any]], local: list[dict[str, Any]]) -> None:
    """Mark catalog/HF rows that already exist under ~/.green/models."""
    for row in entries:
        if row.get("local_path"):
            continue
        repo = (row.get("repo") or "").lower()
        slug = repo.split("/")[-1] if repo else ""
        needle = (row.get("id") or "").replace("--", "-").lower()
        want_q4 = "q4" in (row.get("file") or "").lower()
        best: dict[str, Any] | None = None
        best_score = -1
        for m in local:
            name = m["name"].lower()
            norm = name.replace("-", "").replace("_", "")
            matched = False
            if slug and slug.replace("-", "") in norm:
                matched = True
            elif needle and needle[:8] in norm.replace("-", ""):
                matched = True
            if not matched:
                continue
            score = 0
            if "q4_k_m" in name:
                score += 10
            elif "q4" in name:
                score += 5
            if "fp16" in name or "f16" in name:
                score -= 5
            if "00001-of" in name:
                score -= 3
            if want_q4 and "q4" in name:
                score += 3
            if score > best_score:
                best_score = score
                best = m
        if best:
            row["downloaded"] = True
            row["local_path"] = best["path"]
            row["local_size"] = best["size"]
            row["compressed"] = best.get("compressed", False)
            if best.get("compressed_path"):
                row["compressed_path"] = best["compressed_path"]


def _probe_url(url: str, timeout: float = 0.8) -> bool:
    try:
        req = urllib.request.Request(url, method="GET")
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return 200 <= resp.status < 500
    except (urllib.error.URLError, OSError, ValueError):
        return False


def _probe_port_identity(host: str, port: int) -> str | None:
    """Return service id if something responds: green-ui, green-embed, green-chat, or unknown."""
    url = f"http://{host}:{port}/"
    try:
        req = urllib.request.Request(url, method="GET")
        with urllib.request.urlopen(req, timeout=0.8) as resp:
            server = resp.headers.get("Server", "")
            body = resp.read(512).decode("utf-8", errors="replace")
            if "green-ui" in server.lower():
                return "green-ui"
            if "green-embed" in server.lower():
                return "green-embed"
            if "green-chat" in server.lower() or "llama" in server.lower():
                return "green-chat"
            if body.lstrip().startswith("<!DOCTYPE") or body.lstrip().startswith("<html"):
                return "green-ui"
            if "granite-embedding" in body or '"object": "list"' in body:
                return "green-embed"
            return "unknown"
    except (urllib.error.URLError, OSError, ValueError):
        return None


def _pid_on_port(port: int) -> int | None:
    try:
        out = subprocess.run(
            ["ss", "-tlnp", f"sport = :{port}"],
            capture_output=True,
            text=True,
            timeout=3,
            check=False,
        )
        m = re.search(r"pid=(\d+)", out.stdout)
        if m:
            return int(m.group(1))
    except (OSError, subprocess.TimeoutExpired):
        pass
    return None


def _kill_port(port: int) -> bool:
    pid = _pid_on_port(port)
    if pid is None:
        return False
    try:
        os.kill(pid, signal.SIGTERM)
        time.sleep(0.4)
        if _is_running(pid):
            os.kill(pid, signal.SIGKILL)
        return True
    except OSError:
        return False


def _ensure_ui_port(host: str, port: int, kill_conflict: bool) -> int:
    """Pick a free port for the dashboard; optionally kill wrong service on requested port."""
    identity = _probe_port_identity(host, port)
    if identity is None:
        return port
    if identity == "green-ui":
        print(
            f"green-ui: already running on {host}:{port} — open http://{host}:{port}",
            file=sys.stderr,
        )
        raise SystemExit(0)
    print(
        f"green-ui: port {port} is used by '{identity}' (not the dashboard).",
        file=sys.stderr,
    )
    if kill_conflict:
        if _kill_port(port):
            print(f"green-ui: stopped process on :{port}", file=sys.stderr)
            time.sleep(0.3)
            return port
        print("green-ui: could not free port — try --port 8790", file=sys.stderr)
        raise SystemExit(1)
    alt = 8790 if port == 8780 else port + 1
    print(f"green-ui: use --kill-conflict or try --port {alt}", file=sys.stderr)
    raise SystemExit(1)


def _detect_gpu() -> str | None:
    hw = _detect_hardware()
    return hw.get("gpu_name")


def _detect_hardware() -> dict[str, Any]:
    cores = os.cpu_count() or 1
    ram_gb = 8.0
    try:
        with Path("/proc/meminfo").open(encoding="utf-8") as f:
            for line in f:
                if line.startswith("MemTotal:"):
                    kb = int(line.split()[1])
                    ram_gb = round(kb / (1024 * 1024), 1)
                    break
    except (OSError, ValueError):
        pass
    gpu_name: str | None = None
    vram_gb = 0.0
    try:
        out = subprocess.run(
            ["nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits"],
            capture_output=True,
            text=True,
            timeout=3,
            check=False,
        )
        if out.returncode == 0 and out.stdout.strip():
            line = out.stdout.strip().split("\n")[0]
            parts = [p.strip() for p in line.rsplit(",", 1)]
            if len(parts) == 2:
                gpu_name, vram_s = parts
                try:
                    vram_gb = round(float(vram_s) / 1024, 1)
                except ValueError:
                    vram_gb = 0.0
            else:
                gpu_name = line
    except (OSError, subprocess.TimeoutExpired):
        pass
    return {
        "cores": cores,
        "ram_gb": ram_gb,
        "gpu_name": gpu_name,
        "gpu": gpu_name,
        "vram_gb": vram_gb,
        "has_gpu": bool(gpu_name),
        "cuda_available": bool(gpu_name),
    }


def _load_catalog() -> list[dict[str, Any]]:
    cat_path = UI_DIR / "models.json"
    if not cat_path.is_file():
        return []
    try:
        return json.loads(cat_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return []


def _local_model_for_catalog(entry: dict[str, Any], local: list[dict[str, Any]]) -> dict[str, Any] | None:
    row = {**entry}
    _attach_local_downloads([row], local)
    if row.get("local_path"):
        for m in local:
            if m["path"] == row["local_path"]:
                return m
    return None


def _curated_repo_ids() -> set[str]:
    return {e.get("repo", "").lower() for e in _load_catalog() if e.get("repo")}


def _score_model(entry: dict[str, Any], hw: dict[str, Any], compute: str) -> dict[str, Any]:
    if entry.get("repo", "").lower() in _curated_repo_ids():
        entry = {**entry, "reliability": "curated"}
    return score_for_hardware(entry, hw, compute, resolve_gpu_layers)


def resolve_gpu_layers(compute: str, model_size_gb: float, hw: dict[str, Any]) -> int:
    vram = float(hw.get("vram_gb", 0))
    has_gpu = hw.get("has_gpu", False)
    if compute == "cpu":
        return 0
    if compute == "gpu":
        return 99 if has_gpu else 0
    if not has_gpu:
        return 0
    if vram >= model_size_gb * 1.15:
        return 99
    if vram >= model_size_gb * 0.45:
        return max(8, min(60, int(vram / max(model_size_gb, 0.5) * 24)))
    return 0


def _hf_search_scored(
    query: str,
    compute: str,
    *,
    fits_only: bool = False,
    use_case: str = "",
    reliability: str = "",
    limit: int = 25,
) -> dict[str, Any]:
    hw = _detect_hardware()
    curated = _curated_repo_ids()
    raw = search_hf(query, limit=limit)

    def rel_for(r: dict[str, Any]) -> str:
        if r.get("repo", "").lower() in curated:
            return "curated"
        return r.get("reliability", "unknown")

    scored = [_score_model({**r, "reliability": rel_for(r)}, hw, compute) for r in raw]
    if fits_only:
        scored = [m for m in scored if m.get("fits")]
    if use_case:
        scored = [m for m in scored if use_case in (m.get("use_cases") or [])]
    if reliability:
        scored = [m for m in scored if m.get("reliability") == reliability]
    scored.sort(key=lambda x: x["score"], reverse=True)
    _attach_local_downloads(scored, _list_models())
    return {
        "hardware": hw,
        "compute": compute,
        "query": query,
        "results": scored,
        "filters": {"use_cases": USE_CASE_HELP, "reliability": RELIABILITY_HELP},
    }


def _recommend_models(compute: str = "auto") -> dict[str, Any]:
    hw = _detect_hardware()
    local = _list_models()
    catalog = _load_catalog()
    scored: list[dict[str, Any]] = []
    for entry in catalog:
        enriched = {
            **entry,
            "reliability": "curated",
            "use_cases": entry.get("use_cases") or infer_use_cases(entry.get("repo", entry.get("name", ""))),
        }
        row = _score_model(enriched, hw, compute)
        loc = _local_model_for_catalog(entry, local)
        row["downloaded"] = loc is not None
        row["local_path"] = loc["path"] if loc else None
        row["local_size"] = loc["size"] if loc else None
        scored.append(row)
    scored.sort(key=lambda x: x["score"], reverse=True)
    return {
        "hardware": hw,
        "compute": compute,
        "recommended": scored,
        "top_pick": scored[0] if scored else None,
        "filters": {"use_cases": USE_CASE_HELP, "reliability": RELIABILITY_HELP},
    }


def _read_tail(path: Path, max_bytes: int = 64_000) -> str:
    if not path.is_file():
        return ""
    try:
        size = path.stat().st_size
        with path.open("rb") as f:
            if size > max_bytes:
                f.seek(size - max_bytes)
            return f.read().decode("utf-8", errors="replace")
    except OSError:
        return ""


def _pid_file(service: str) -> Path:
    return UI_PIDS / f"{service}.pid"


def _log_file(service: str) -> Path:
    return GE_HOME / f"ui-{service}.log"


def _is_running(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def _stop_service(service: str) -> None:
    pf = _pid_file(service)
    if pf.is_file():
        try:
            pid = int(pf.read_text().strip())
            _kill_pid_tree(pid)
        except (ValueError, OSError):
            pass
        pf.unlink(missing_ok=True)
    port = _SERVICE_PORTS.get(service)
    if port:
        _kill_port(port)
    if service == "chat":
        _chat_runtime_snapshot_path().unlink(missing_ok=True)
        _pkill_pattern("green_chat.py")
        _pkill_pattern("llama_cpp.server")
    elif service == "embed":
        _pkill_pattern("green_embed.py")
    elif service == "translate":
        _pkill_pattern("green_translate.py")


def _start_process(service: str, argv: list[str], env: dict[str, str] | None = None) -> int:
    _stop_service(service)
    UI_PIDS.mkdir(parents=True, exist_ok=True)
    log_path = _log_file(service)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    merged = os.environ.copy()
    if env:
        merged.update(env)
    merged["GE_HOME"] = str(GE_HOME)
    with log_path.open("w", encoding="utf-8") as logf:
        proc = subprocess.Popen(
            argv,
            stdout=logf,
            stderr=subprocess.STDOUT,
            env=merged,
            start_new_session=True,
        )
    _pid_file(service).write_text(str(proc.pid), encoding="utf-8")
    return proc.pid


def _server_status(name: str, port: int, health_paths: list[str]) -> dict[str, Any]:
    pf = _pid_file(name)
    pid = None
    if pf.is_file():
        try:
            pid = int(pf.read_text().strip())
            if not _is_running(pid):
                pid = None
        except ValueError:
            pid = None
    up = any(_probe_url(f"http://127.0.0.1:{port}{p}") for p in health_paths)
    if up and pid is None:
        pid = _pid_on_port(port)
    return {"port": port, "pid": pid, "up": up}


def _run_job(job_id: str, action: str, params: dict[str, Any]) -> None:
    log_path = UI_LOGS / f"{job_id}.log"
    UI_LOGS.mkdir(parents=True, exist_ok=True)
    ge = _find_ge_bin()
    if not ge and action not in ("compress",):
        _finish_job(job_id, 1, "ge binary not found")
        return

    argv: list[str] = []
    try:
        if action == "install":
            argv = [ge, "install"]
        elif action == "stack_setup":
            argv = [ge, "stack", "setup"]
        elif action == "embed_install":
            argv = [ge, "embed", "install"]
        elif action == "chat_install":
            argv = [ge, "chat", "install"]
        elif action == "pull":
            argv = [ge, "pull", params.get("repo", ""), "--file", params.get("file", "*Q4_K_M.gguf")]
        elif action == "bench":
            argv = [ge, "bench", params.get("name", "portable_bench")]
        elif action == "test_mcp":
            argv = [ge, "test", "mcp"]
        elif action == "setup_mcp":
            _run_setup_mcp(job_id, ge, params)
            return
        elif action == "setup_chat":
            _run_setup_chat(job_id, ge, params)
            return
        elif action == "use_model":
            _run_use_model(job_id, ge, params)
            return
        elif action == "remove_model":
            path = params.get("path") or params.get("model") or ""
            if not path:
                _finish_job(job_id, 1, "no path")
                return
            r = _remove_model_files(path)
            _append_job_log(job_id, f"\n{json.dumps(r)}\n")
            _finish_job(job_id, 0 if r.get("ok") else 1, None if r.get("ok") else r.get("error"))
            return
        elif action == "reinstall_model":
            _run_reinstall_model(job_id, ge, params)
            return
        elif action == "run":
            argv = [
                ge,
                "run",
                params["model"],
                "--prompt",
                params.get("prompt", "Hello"),
                "--gpu-layers",
                str(int(params.get("gpu_layers", 0))),
                "--ctx",
                str(int(params.get("ctx", 4096))),
            ]
        elif action == "compress":
            script = _find_compress_script()
            gc = _find_greencompress()
            if not script:
                _finish_job(job_id, 1, "compress_model.py not found — run ge install")
                return
            if not gc:
                _finish_job(job_id, 1, "greencompress not found — run ge install")
                return
            gguf = params["gguf"]
            out = _expand_home(params.get("out") or str(GE_HOME / "compressed" / Path(gguf).stem))
            methods = params.get("methods") or "green_optimal,green_adaptive"
            layers = params.get("layers", "")
            argv = [
                sys.executable,
                str(script),
                "--gguf",
                gguf,
                "--out",
                out,
                "--methods",
                methods,
                "--bin",
                gc,
            ]
            if layers.strip():
                argv.extend(["--layers", layers.strip()])
        else:
            _finish_job(job_id, 1, f"unknown action: {action}")
            return

        with log_path.open("w", encoding="utf-8") as logf:
            logf.write(f"$ {' '.join(argv)}\n\n")
            logf.flush()
            proc = subprocess.Popen(
                argv,
                stdout=logf,
                stderr=subprocess.STDOUT,
                env={**os.environ, "GE_HOME": str(GE_HOME), "GE_BIN": ge or ""},
            )
            code = proc.wait()
        _finish_job(job_id, code, None)
    except Exception as exc:  # noqa: BLE001 — job runner must not crash server
        with log_path.open("a", encoding="utf-8") as logf:
            logf.write(f"\n[error] {exc}\n")
        _finish_job(job_id, 1, str(exc))


def _append_job_log(job_id: str, text: str) -> None:
    log_path = UI_LOGS / f"{job_id}.log"
    UI_LOGS.mkdir(parents=True, exist_ok=True)
    with log_path.open("a", encoding="utf-8") as logf:
        logf.write(text)


def _run_compress_gguf(job_id: str, gguf: str, ge: str) -> int:
    script = _find_compress_script()
    gc = _find_greencompress()
    if not script:
        _append_job_log(job_id, "\ncompress_model.py not found — run ge install\n")
        return 1
    if not gc:
        _append_job_log(job_id, "\ngreencompress not found — run ge install\n")
        return 1
    out = str(GE_HOME / "compressed" / Path(gguf).stem)
    argv = [
        sys.executable,
        str(script),
        "--gguf",
        gguf,
        "--out",
        out,
        "--methods",
        "green_optimal,green_adaptive",
        "--bin",
        gc,
        "--layers",
        "0,1",
    ]
    return _run_argv_logged(job_id, argv, ge)


def _run_argv_logged(job_id: str, argv: list[str], ge: str) -> int:
    _append_job_log(job_id, f"\n$ {' '.join(argv)}\n")
    proc = subprocess.Popen(
        argv,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env={**os.environ, "GE_HOME": str(GE_HOME), "GE_BIN": ge},
    )
    assert proc.stdout is not None
    for line in proc.stdout:
        _append_job_log(job_id, line)
    return proc.wait()


def _run_setup_mcp(job_id: str, ge: str, params: dict[str, Any]) -> None:
    repo = params.get("repo") or "bartowski/Llama-3.2-1B-Instruct-GGUF"
    file = params.get("file") or "*Q4_K_M.gguf"
    try:
        code = _run_argv_logged(job_id, [ge, "stack", "setup"], ge)
        if code != 0:
            _finish_job(job_id, code, "stack setup failed")
            return
        code = _run_argv_logged(job_id, [ge, "pull", repo, "--file", file], ge)
        if code != 0:
            _finish_job(job_id, code, "pull failed")
            return
        with _server_lock:
            r = _handle_servers({"service": "mcp_stack", "cmd": "start", "mcp": True})
        _append_job_log(job_id, f"\nMCP stack: {json.dumps(r)}\n")
        _finish_job(job_id, 0 if r.get("ok") else 1, None if r.get("ok") else r.get("error"))
    except Exception as exc:  # noqa: BLE001
        _append_job_log(job_id, f"\n[error] {exc}\n")
        _finish_job(job_id, 1, str(exc))


def _resolve_model_path(params: dict[str, Any]) -> str:
    model_path = params.get("model") or params.get("path") or ""
    if model_path:
        return model_path
    local = _list_models()
    return local[-1]["path"] if local else ""


def _run_setup_chat(job_id: str, ge: str, params: dict[str, Any]) -> None:
    repo = params.get("repo", "")
    file = params.get("file") or "*Q4_K_M.gguf"
    gpu_layers = int(params.get("gpu_layers", 0))
    auto_compress = bool(params.get("auto_compress", True))
    try:
        code = _run_argv_logged(job_id, [ge, "chat", "install"], ge)
        if code != 0:
            _finish_job(job_id, code, "chat install failed")
            return
        if repo:
            code = _run_argv_logged(job_id, [ge, "pull", repo, "--file", file], ge)
            if code != 0:
                _finish_job(job_id, code, "pull failed")
                return
        model_path = _resolve_model_path(params)
        if not model_path:
            _finish_job(job_id, 1, "no model to serve")
            return
        if auto_compress and not _is_model_compressed(model_path):
            _append_job_log(job_id, f"\nAuto-compressing {model_path} …\n")
            code = _run_compress_gguf(job_id, model_path, ge)
            if code != 0:
                _finish_job(job_id, code, "compress failed")
                return
        with _server_lock:
            r = _handle_servers({
                "service": "chat",
                "cmd": "start",
                "model": model_path,
                "gpu_layers": gpu_layers,
                "mcp": bool(params.get("mcp", False)),
            })
        _append_job_log(job_id, f"\nChat server: {json.dumps(r)}\n")
        _finish_job(job_id, 0 if r.get("ok") else 1, None if r.get("ok") else r.get("error"))
    except Exception as exc:  # noqa: BLE001
        _append_job_log(job_id, f"\n[error] {exc}\n")
        _finish_job(job_id, 1, str(exc))


def _run_use_model(job_id: str, ge: str, params: dict[str, Any]) -> None:
    """Pull (optional), auto-compress, and restart chat on a model path."""
    repo = params.get("repo", "")
    file = params.get("file") or "*Q4_K_M.gguf"
    gpu_layers = int(params.get("gpu_layers", 0))
    auto_compress = bool(params.get("auto_compress", True))
    restart_chat = bool(params.get("restart_chat", True))
    try:
        if repo:
            code = _run_argv_logged(job_id, [ge, "pull", repo, "--file", file], ge)
            if code != 0:
                _finish_job(job_id, code, "pull failed")
                return
        model_path = _resolve_model_path(params)
        if not model_path:
            _finish_job(job_id, 1, "no model selected")
            return
        if auto_compress and not _is_model_compressed(model_path):
            _append_job_log(job_id, f"\nAuto-compressing {model_path} …\n")
            code = _run_compress_gguf(job_id, model_path, ge)
            if code != 0:
                _finish_job(job_id, code, "compress failed")
                return
        if restart_chat:
            with _server_lock:
                r = _handle_servers({
                    "service": "chat",
                    "cmd": "start",
                    "model": model_path,
                    "gpu_layers": gpu_layers,
                    "mcp": bool(params.get("mcp", False)),
                })
            _append_job_log(job_id, f"\nChat server: {json.dumps(r)}\n")
            _finish_job(job_id, 0 if r.get("ok") else 1, None if r.get("ok") else r.get("error"))
        else:
            _finish_job(job_id, 0, None)
    except Exception as exc:  # noqa: BLE001
        _append_job_log(job_id, f"\n[error] {exc}\n")
        _finish_job(job_id, 1, str(exc))


def _run_reinstall_model(job_id: str, ge: str, params: dict[str, Any]) -> None:
    """Remove local GGUF + compressed dir, pull fresh Q4, compress."""
    repo = params.get("repo", "")
    file = params.get("file") or "*Q4_K_M.gguf"
    path = params.get("path") or params.get("model") or ""
    auto_compress = bool(params.get("auto_compress", True))
    restart_chat = bool(params.get("restart_chat", False))
    gpu_layers = int(params.get("gpu_layers", 0))
    try:
        if path:
            r = _remove_model_files(path)
            _append_job_log(job_id, f"\nremove: {json.dumps(r)}\n")
            if not r.get("ok"):
                _finish_job(job_id, 1, r.get("error"))
                return
        if not repo:
            _finish_job(job_id, 1, "reinstall needs repo (pick a catalog model)")
            return
        code = _run_argv_logged(job_id, [ge, "pull", repo, "--file", file], ge)
        if code != 0:
            _finish_job(job_id, code, "pull failed")
            return
        model_path = _find_pulled_model(repo, file) or _resolve_model_path(params)
        if not model_path:
            _finish_job(job_id, 1, "pull finished but no GGUF found")
            return
        if auto_compress:
            _append_job_log(job_id, f"\nCompressing {model_path} …\n")
            code = _run_compress_gguf(job_id, model_path, ge)
            if code != 0:
                _finish_job(job_id, code, "compress failed")
                return
        if restart_chat:
            with _server_lock:
                r = _handle_servers({
                    "service": "chat",
                    "cmd": "start",
                    "model": model_path,
                    "gpu_layers": gpu_layers,
                    "mcp": bool(params.get("mcp", False)),
                })
            _append_job_log(job_id, f"\nChat server: {json.dumps(r)}\n")
            _finish_job(job_id, 0 if r.get("ok") else 1, None if r.get("ok") else r.get("error"))
        else:
            _append_job_log(job_id, f"\nready: {model_path}\n")
            _finish_job(job_id, 0, None)
    except Exception as exc:  # noqa: BLE001
        _append_job_log(job_id, f"\n[error] {exc}\n")
        _finish_job(job_id, 1, str(exc))


def _parse_qs(path: str) -> dict[str, str]:
    if "?" not in path:
        return {}
    out: dict[str, str] = {}
    for part in path.split("?", 1)[1].split("&"):
        if "=" in part:
            k, v = part.split("=", 1)
            out[k] = urllib.parse.unquote(v)
    return out


def _finish_job(job_id: str, code: int, err: str | None) -> None:
    with _jobs_lock:
        if job_id in _jobs:
            _jobs[job_id]["state"] = "done" if code == 0 else "failed"
            _jobs[job_id]["exit_code"] = code
            _jobs[job_id]["finished"] = time.time()
            if err:
                _jobs[job_id]["error"] = err


def _start_job(action: str, params: dict[str, Any]) -> str:
    job_id = uuid.uuid4().hex[:12]
    with _jobs_lock:
        _jobs[job_id] = {
            "id": job_id,
            "action": action,
            "state": "running",
            "started": time.time(),
            "finished": None,
            "params": params,
        }
    threading.Thread(
        target=_run_job,
        args=(job_id, action, params),
        name=f"ge-ui-job-{job_id}",
        daemon=True,
    ).start()
    return job_id


def _build_status() -> dict[str, Any]:
    ge = _find_ge_bin()
    gc = _find_greencompress()
    embed_venv = (GE_HOME / "embed-venv" / "bin/python").is_file()
    chat_venv = (GE_HOME / "chat-venv" / "bin/python").is_file()
    models = _list_models()
    chat_model = ""
    chat_log = _log_file("chat")
    if chat_log.is_file():
        try:
            log = _chat_log_text(64_000)
            m = _chat_log_last_match(r"model=([^\s]+)", log)
            if m:
                chat_model = m.group(1)
        except OSError:
            pass
    return {
        "ok": True,
        "service": "green-ui",
        "ui_port": int(os.environ.get("GE_UI_ACTIVE_PORT", DEFAULT_PORT)),
        "ge_home": str(GE_HOME),
        "ge_bin": ge,
        "greencompress": gc,
        "embed_venv": embed_venv,
        "chat_venv": chat_venv,
        "compress_script": str(_find_compress_script() or ""),
        "model_count": len(models),
        "models": models,
        "chat_model": chat_model,
        "chat_runtime": _chat_runtime_info(),
        "hardware": _detect_hardware(),
        "servers": {
            "embed": _server_status("embed", EMBED_PORT, ["/health", "/v1/models", "/"]),
            "chat": _server_status("chat", CHAT_PORT, ["/health", "/v1/models", "/"]),
            "translate": _server_status(
                "translate", TRANSLATE_PORT, ["/v1/routes", "/health", "/"]
            ),
        },
    }


def _handle_servers(body: dict[str, Any]) -> dict[str, Any]:
    service = body.get("service", "")
    cmd = body.get("cmd", "start")
    ge = _find_ge_bin()
    if not ge:
        return {"ok": False, "error": "ge binary not found"}

    if cmd == "stop":
        if service == "mcp_stack":
            _stop_service("embed")
            _stop_service("chat")
            return {"ok": True, "stopped": ["embed", "chat"]}
        _stop_service(service)
        return {"ok": True, "stopped": service}

    if service == "chat":
        script = _find_runner("green_chat.py")
        if not script:
            return {"ok": False, "error": "green_chat.py not found — run ge ui install from checkout"}
        py = _chat_venv_python()
        if not py.is_file():
            return {"ok": False, "error": "chat venv missing — run ge chat install"}
        model = body.get("model") or ""
        if not model:
            models = _list_models()
            if models:
                model = models[0]["path"]
            else:
                return {"ok": False, "error": "no model — pull one first"}
        gpu = int(body.get("gpu_layers", 0))
        ctx = int(body.get("ctx", 8192 if Path(model).stat().st_size > 2_000_000_000 else 4096))
        mcp = bool(body.get("mcp", False))
        err = _validate_chat_model(model)
        if err:
            return {"ok": False, "error": err}
        argv = [
            str(py),
            str(script),
            "--model",
            model,
            "--port",
            str(CHAT_PORT),
            "--gpu-layers",
            str(gpu),
            "--ctx",
            str(ctx),
        ]
        if mcp:
            argv.append("--mcp")
        pid = _start_process("chat", argv)
        _write_chat_runtime_snapshot(model, ctx, gpu)
        for _ in range(24):
            time.sleep(0.5)
            if _chat_server_healthy():
                return {"ok": True, "pid": pid, "port": CHAT_PORT, "model": model, "gpu_layers": gpu, "ctx": ctx}
            pf = _pid_file("chat")
            if pf.is_file():
                try:
                    if not _is_running(int(pf.read_text().strip())):
                        break
                except ValueError:
                    break
            else:
                break
        excerpt = _chat_log_excerpt()
        hint = excerpt.splitlines()[-1] if excerpt else "chat process exited"
        if "Failed to load model" in excerpt:
            hint = (
                "Model failed to load in llama.cpp — use Fresh Q4 for a complete "
                f"Q4_K_M.gguf (not an FP16 shard). Log: {hint}"
            )
        return {"ok": False, "error": hint, "log": excerpt[-500:]}

    if service == "embed":
        script = _find_runner("green_embed.py")
        if not script:
            return {"ok": False, "error": "green_embed.py not found — run ge ui install from checkout"}
        py = _embed_venv_python()
        if not py.is_file():
            return {"ok": False, "error": "embed venv missing — run ge embed install"}
        mcp = bool(body.get("mcp", True))
        port = int(body.get("port", EMBED_PORT))
        if port in RESERVED_PORTS:
            return {"ok": False, "error": f"port {port} is reserved for ge ui — embed uses {EMBED_PORT}"}
        argv = [str(py), str(script), "--port", str(port)]
        if mcp:
            argv.append("--mcp")
        pid = _start_process("embed", argv)
        return {"ok": True, "pid": pid, "port": port}

    if service == "translate":
        script = _find_runner("green_translate.py")
        if not script:
            return {"ok": False, "error": "green_translate.py not found"}
        manifest = GE_HOME / "hymt2-7b-green" / "model_manifest.json"
        if not manifest.is_file():
            return {
                "ok": False,
                "error": "Hy-MT2 manifest missing — compress via translate or compress tab",
            }
        gpu = int(body.get("gpu_layers", 0))
        router = GE_HOME / "translate-router.json"
        argv = [
            sys.executable,
            str(script),
            "--port",
            str(TRANSLATE_PORT),
            "--router",
            str(router),
            "--gpu-layers",
            str(gpu),
        ]
        pid = _start_process("translate", argv)
        return {"ok": True, "pid": pid, "port": TRANSLATE_PORT}

    if service == "mcp_stack":
        r1 = _handle_servers({"service": "embed", "cmd": "start", "mcp": True})
        if not r1.get("ok"):
            return r1
        time.sleep(0.5)
        r2 = _handle_servers({"service": "chat", "cmd": "start", "mcp": True, **body})
        return {"ok": r2.get("ok", False), "embed": r1, "chat": r2}

    return {"ok": False, "error": f"unknown service: {service}"}


def _estimate_prompt_tokens(messages: list[dict[str, Any]]) -> int:
    chars = sum(len(str(m.get("content", ""))) for m in messages)
    return max(256, chars // 3)


def _clamp_chat_max_tokens(max_tokens: int, messages: list[dict[str, Any]], codegen: bool = False) -> int:
    """Keep completion budget inside the running server's context window."""
    rt = _chat_runtime_info()
    ctx = int(rt.get("ctx") or 4096)
    prompt_est = _estimate_prompt_tokens(messages)
    cap = max(512, ctx - prompt_est - 128)
    out = max(256, min(int(max_tokens), cap))
    if codegen:
        out = max(out, min(4096, cap))
    return out


def _chat_debug_log(tag: str, detail: dict[str, Any]) -> None:
    """Append codegen/chat diagnostics to ~/.green/ui/chat-debug.log (for agent/user triage)."""
    try:
        log_path = GE_HOME / "ui" / "chat-debug.log"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        row = {"t": time.strftime("%Y-%m-%dT%H:%M:%S"), "tag": tag, **detail}
        with log_path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(row, ensure_ascii=False)[:4000] + "\n")
    except OSError:
        pass


def _chat_payload(body: dict[str, Any]) -> tuple[dict[str, Any], str | None]:
    messages = body.get("messages", [])
    if not messages:
        return {}, "no messages"
    codegen = bool(body.get("codegen"))
    max_tokens = _clamp_chat_max_tokens(int(body.get("max_tokens", 8192)), messages, codegen)
    temp = float(body.get("temperature", 0.12 if codegen else 0.7))
    payload = {
        "model": body.get("model", "green-local"),
        "messages": messages,
        "max_tokens": max_tokens,
        "stream": bool(body.get("stream", False)),
        "temperature": temp,
    }
    tag = body.get("debug_tag") or ("codegen" if codegen else "chat")
    sys_msgs = [m for m in messages if m.get("role") == "system"]
    user_msgs = [m for m in messages if m.get("role") == "user"]
    prefill = messages[-1].get("content", "")[:40] if messages and messages[-1].get("role") == "assistant" else ""
    _chat_debug_log(
        tag,
        {
            "codegen": codegen,
            "temp": temp,
            "n_messages": len(messages),
            "has_system": bool(sys_msgs),
            "system_preview": (sys_msgs[0].get("content", "")[:120] if sys_msgs else ""),
            "user_preview": (user_msgs[-1].get("content", "")[:120] if user_msgs else ""),
            "prefill": prefill,
            "max_tokens": max_tokens,
        },
    )
    return payload, None


def _proxy_chat(body: dict[str, Any]) -> dict[str, Any]:
    if not _server_status("chat", CHAT_PORT, ["/v1/models", "/health", "/"]).get("up"):
        return {"ok": False, "error": "chat server not running — pick a model and click Start server"}
    url = f"http://127.0.0.1:{CHAT_PORT}/v1/chat/completions"
    payload, err = _chat_payload(body)
    if err:
        return {"ok": False, "error": err}
    payload["stream"] = False
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    last_err = ""
    for attempt in range(2):
        try:
            with urllib.request.urlopen(req, timeout=CHAT_PROXY_TIMEOUT) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            err_body = e.read().decode("utf-8", errors="replace")
            return {"ok": False, "error": err_body or str(e)}
        except (urllib.error.URLError, OSError) as e:
            last_err = str(getattr(e, "reason", e) or e)
            if attempt == 0 and "closed connection" in last_err.lower():
                time.sleep(1.0)
                if not _chat_server_healthy():
                    break
                continue
            break
    hint = last_err
    if "closed connection" in hint.lower():
        rt = _chat_runtime_info()
        hint = (
            f"{hint} — chat server dropped the connection (often OOM or crash). "
            f"Running: {rt.get('model_name') or 'unknown'} · "
            "Stop model → Start server. For 7B use Fresh Q4, not the FP16 shard."
        )
    return {"ok": False, "error": hint}


def _which(cmd: str) -> str | None:
    return shutil.which(cmd)


def _validate_one_file(path: str, content: str) -> dict[str, Any]:
    """Run language-appropriate syntax check on one file."""
    ext = Path(path).suffix.lower()
    if not content.strip():
        return {"ok": False, "error": "empty file"}
    try:
        if ext == ".json":
            json.loads(content)
            return {"ok": True}
        if ext in (".md", ".txt", ".gitignore", ".env", ".css", ".html", ".htm", ".svg"):
            return {"ok": True, "note": "markup — no syntax checker"}
        if ext in (".yml", ".yaml"):
            try:
                import yaml  # type: ignore
                yaml.safe_load(content)
                return {"ok": True}
            except ImportError:
                return {"ok": True, "note": "yaml — install pyyaml to validate"}
            except Exception as e:
                return {"ok": False, "error": str(e)}
        if ext == ".toml":
            try:
                import tomllib
                tomllib.loads(content.encode("utf-8"))
                return {"ok": True}
            except Exception as e:
                return {"ok": False, "error": str(e)}
        if ext == ".xml":
            import xml.etree.ElementTree as ET
            try:
                ET.fromstring(content)
                return {"ok": True}
            except ET.ParseError as e:
                return {"ok": False, "error": str(e)}
        if ext == ".sql":
            return {"ok": True, "note": "sql — structure not validated"}
        with tempfile.NamedTemporaryFile(mode="w", suffix=ext, delete=False, encoding="utf-8") as f:
            f.write(content)
            tmp = f.name
        try:
            if ext in (".sh", ".bash"):
                bash = _which("bash")
                if not bash:
                    return {"ok": True, "note": "bash not installed"}
                r = subprocess.run([bash, "-n", tmp], capture_output=True, text=True, timeout=15)
                out = (r.stdout + r.stderr).strip()
                return {"ok": r.returncode == 0, "error": out if r.returncode else ""}
            if ext in (".ts", ".tsx", ".jsx", ".vue"):
                node = _which("node")
                if not node:
                    return {"ok": True, "note": "node not installed"}
                r = subprocess.run([node, "--check", tmp], capture_output=True, text=True, timeout=15)
                out = (r.stderr + r.stdout).strip()
                if r.returncode == 0:
                    return {"ok": True}
                return {"ok": False, "error": out or "syntax check failed"}
            if ext == ".php":
                php = _which("php")
                if not php:
                    return {"ok": True, "note": "php not installed"}
                r = subprocess.run([php, "-l", tmp], capture_output=True, text=True, timeout=15)
                out = (r.stdout + r.stderr).strip()
                return {"ok": r.returncode == 0, "error": out if r.returncode else ""}
            if ext == ".py":
                r = subprocess.run(
                    [sys.executable, "-m", "py_compile", tmp],
                    capture_output=True,
                    text=True,
                    timeout=15,
                )
                out = (r.stdout + r.stderr).strip()
                return {"ok": r.returncode == 0, "error": out if r.returncode else ""}
            if ext in (".js", ".mjs", ".cjs"):
                node = _which("node")
                if not node:
                    return {"ok": True, "note": "node not installed"}
                r = subprocess.run([node, "--check", tmp], capture_output=True, text=True, timeout=15)
                out = (r.stderr + r.stdout).strip()
                return {"ok": r.returncode == 0, "error": out if r.returncode else ""}
            if ext == ".rs":
                rustc = _which("rustc")
                if not rustc:
                    return {"ok": True, "note": "rustc not installed"}
                r = subprocess.run(
                    [rustc, "--crate-type", "lib", "--edition", "2021", "-o", "/dev/null", tmp],
                    capture_output=True,
                    text=True,
                    timeout=30,
                )
                out = (r.stderr + r.stdout).strip()
                return {"ok": r.returncode == 0, "error": out if r.returncode else ""}
            if ext == ".go":
                go = _which("go")
                if not go:
                    return {"ok": True, "note": "go not installed"}
                r = subprocess.run(["go", "fmt", tmp], capture_output=True, text=True, timeout=15)
                return {"ok": r.returncode == 0, "error": (r.stderr or r.stdout).strip()}
            return {"ok": True, "note": f"no checker for {ext}"}
        finally:
            Path(tmp).unlink(missing_ok=True)
    except subprocess.TimeoutExpired:
        return {"ok": False, "error": "validation timed out"}
    except json.JSONDecodeError as e:
        return {"ok": False, "error": str(e)}
    except OSError as e:
        return {"ok": False, "error": str(e)}


def _validate_files(body: dict[str, Any]) -> dict[str, Any]:
    files = body.get("files") or {}
    if not isinstance(files, dict) or not files:
        return {"ok": False, "error": "no files"}
    results: dict[str, Any] = {}
    for path, content in files.items():
        if not isinstance(content, str):
            content = str(content)
        results[str(path)] = _validate_one_file(str(path), content)
    ok = all(r.get("ok") for r in results.values())
    return {"ok": ok, "results": results}


def _proxy_chat_stream(handler: BaseHTTPRequestHandler, body: dict[str, Any]) -> None:
    if not _server_status("chat", CHAT_PORT, ["/v1/models", "/health", "/"]).get("up"):
        handler._send_json({"ok": False, "error": "chat server not running — pick a model and click Start server"}, 400)
        return
    url = f"http://127.0.0.1:{CHAT_PORT}/v1/chat/completions"
    payload, err = _chat_payload(body)
    if err:
        handler._send_json({"ok": False, "error": err}, 400)
        return
    payload["stream"] = True
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    headers_sent = False

    def _write_client(data: bytes) -> bool:
        nonlocal headers_sent
        if not data:
            return True
        try:
            handler.wfile.write(data)
            handler.wfile.flush()
            return True
        except (BrokenPipeError, ConnectionResetError):
            return False

    try:
        with urllib.request.urlopen(req, timeout=CHAT_PROXY_TIMEOUT) as resp:
            handler.send_response(200)
            handler.send_header("Content-Type", "text/event-stream; charset=utf-8")
            handler.send_header("Cache-Control", "no-cache")
            handler.send_header("Connection", "keep-alive")
            handler.send_header("X-Green-UI", "1")
            handler.end_headers()
            headers_sent = True
            while True:
                try:
                    chunk = resp.read(8192)
                except http.client.IncompleteRead as e:
                    tail = e.partial if e.partial else b""
                    if tail and not _write_client(tail):
                        return
                    break
                if not chunk:
                    break
                if not _write_client(chunk):
                    return
    except urllib.error.HTTPError as e:
        if headers_sent:
            return
        err_body = e.read().decode("utf-8", errors="replace")
        handler._send_json({"ok": False, "error": err_body or str(e)}, 400)
    except (BrokenPipeError, ConnectionResetError):
        return
    except urllib.error.URLError as e:
        if headers_sent:
            return
        hint = str(getattr(e, "reason", e) or e)
        if "closed connection" in hint.lower():
            rt = _chat_runtime_info()
            hint = (
                f"{hint} — chat server dropped the connection (often OOM or crash). "
                f"Running: {rt.get('model_name') or 'unknown'}"
            )
        handler._send_json({"ok": False, "error": hint}, 502)
    except OSError as e:
        if headers_sent or isinstance(e, (BrokenPipeError, ConnectionResetError)):
            return
        hint = str(e)
        if "closed connection" in hint.lower():
            rt = _chat_runtime_info()
            hint = (
                f"{hint} — chat server dropped the connection (often OOM or crash). "
                f"Running: {rt.get('model_name') or 'unknown'}"
            )
        handler._send_json({"ok": False, "error": hint}, 502)


class GreenUIHandler(BaseHTTPRequestHandler):
    server_version = "green-ui/0.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        if os.environ.get("GE_UI_QUIET"):
            return
        sys.stderr.write(f"green-ui: {self.address_string()} - {fmt % args}\n")

    def _send_json(self, data: dict[str, Any], code: int = 200) -> None:
        body = json.dumps(data).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_bytes(self, data: bytes, content_type: str, code: int = 200) -> None:
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-cache")
        self.send_header("X-Green-UI", "1")
        self.end_headers()
        self.wfile.write(data)

    def _read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b"{}"
        try:
            return json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            return {}

    def _serve_static(self, rel: str) -> None:
        safe = re.sub(r"\.\.+", "", rel).lstrip("/")
        if safe == "" or safe == "index.html":
            path = UI_DIR / "index.html"
        elif safe.startswith("ui/"):
            path = UI_DIR / safe[3:]
        else:
            path = UI_DIR / safe
        if not path.is_file() or not str(path.resolve()).startswith(str(UI_DIR.resolve())):
            self.send_error(HTTPStatus.NOT_FOUND)
            return
        ctype = "text/plain"
        if path.suffix == ".html":
            ctype = "text/html; charset=utf-8"
        elif path.suffix == ".css":
            ctype = "text/css; charset=utf-8"
        elif path.suffix == ".js":
            ctype = "application/javascript; charset=utf-8"
        elif path.suffix == ".json":
            ctype = "application/json; charset=utf-8"
        data = path.read_bytes()
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        if path.suffix == ".html":
            self.send_header("Cache-Control", "no-cache, must-revalidate")
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:  # noqa: N802
        path = self.path.split("?", 1)[0]
        if path in ("/", "/index.html"):
            self._serve_static("index.html")
            return
        if path.startswith("/ui/"):
            self._serve_static(path[1:])
            return
        if path == "/api/recommendations":
            qs = _parse_qs(self.path)
            compute = qs.get("compute", "auto")
            self._send_json(_recommend_models(compute))
            return
        if path == "/api/hf/search":
            qs = _parse_qs(self.path)
            q = qs.get("q", qs.get("query", "llama"))
            compute = qs.get("compute", "auto")
            fits_only = qs.get("fits_only", "") in ("1", "true", "yes")
            use_case = qs.get("use_case", "")
            reliability = qs.get("reliability", "")
            limit = int(qs.get("limit", "25") or 25)
            self._send_json(_hf_search_scored(
                q, compute, fits_only=fits_only, use_case=use_case,
                reliability=reliability, limit=limit,
            ))
            return
        if path == "/api/hf/repo":
            qs = _parse_qs(self.path)
            repo = qs.get("repo", "")
            compute = qs.get("compute", "auto")
            if not repo:
                self._send_json({"ok": False, "error": "missing repo"}, 400)
                return
            hw = _detect_hardware()
            entry = enrich_repo(repo, _curated_repo_ids())
            scored = _score_model(entry, hw, compute)
            self._send_json({"ok": True, "model": scored, "hardware": hw})
            return
        if path == "/api/filters":
            self._send_json({
                "ok": True,
                "use_cases": USE_CASE_HELP,
                "reliability": RELIABILITY_HELP,
                "compute": {
                    "auto": "GPU when VRAM fits, else CPU (recommended)",
                    "cpu": "Force CPU — gpu-layers 0",
                    "gpu": "Force GPU — all layers on GPU",
                },
            })
            return
        if path == "/api/compute":
            self._send_json({
                "ok": True,
                "modes": ["auto", "cpu", "gpu"],
                "default": "auto",
                "help": "auto picks GPU when VRAM fits, else CPU",
            })
            return
        if path == "/api/status":
            self._send_json(_build_status())
            return
        if path == "/api/models":
            self._send_json({"ok": True, "models": _list_models()})
            return
        if path == "/api/jobs":
            with _jobs_lock:
                jobs = sorted(_jobs.values(), key=lambda j: j["started"], reverse=True)
            self._send_json({"ok": True, "jobs": jobs[:50]})
            return
        m = re.match(r"^/api/jobs/([a-f0-9]+)/log$", path)
        if m:
            job_id = m.group(1)
            log_path = UI_LOGS / f"{job_id}.log"
            text = _read_tail(log_path)
            self._send_bytes(text.encode("utf-8"), "text/plain; charset=utf-8")
            return
        self.send_error(HTTPStatus.NOT_FOUND)

    def do_POST(self) -> None:  # noqa: N802
        path = self.path.split("?", 1)[0]
        body = self._read_json()
        if path == "/api/jobs":
            action = body.get("action", "")
            if not action:
                self._send_json({"ok": False, "error": "missing action"}, 400)
                return
            params = {k: v for k, v in body.items() if k != "action"}
            job_id = _start_job(action, params)
            self._send_json({"ok": True, "job_id": job_id})
            return
        if path == "/api/servers":
            with _server_lock:
                result = _handle_servers(body)
            code = 200 if result.get("ok") else 400
            self._send_json(result, code)
            return
        if path == "/api/chat":
            if body.get("stream"):
                _proxy_chat_stream(self, body)
                return
            result = _proxy_chat(body)
            code = 200 if "choices" in result else 400
            self._send_json(result, code)
            return
        if path == "/api/validate":
            self._send_json(_validate_files(body))
            return
        self.send_error(HTTPStatus.NOT_FOUND)


def main() -> None:
    ap = argparse.ArgumentParser(description="Green Engine UI dashboard")
    ap.add_argument("--host", default=DEFAULT_HOST)
    ap.add_argument("--port", type=int, default=DEFAULT_PORT)
    ap.add_argument("--ge-bin", default="", help="path to ge executable")
    ap.add_argument(
        "--kill-conflict",
        action="store_true",
        help="stop wrong service (e.g. embed) if it occupies the UI port",
    )
    args = ap.parse_args()
    if args.ge_bin:
        global GE_BIN  # noqa: PLW0603
        GE_BIN = args.ge_bin
    GE_HOME.mkdir(parents=True, exist_ok=True)
    UI_LOGS.mkdir(parents=True, exist_ok=True)
    UI_PIDS.mkdir(parents=True, exist_ok=True)
    if not UI_DIR.is_dir():
        print(f"green-ui: missing UI assets at {UI_DIR}", file=sys.stderr)
        raise SystemExit(1)
    port = _ensure_ui_port(args.host, args.port, args.kill_conflict)
    os.environ["GE_UI_ACTIVE_PORT"] = str(port)
    ge = _find_ge_bin()
    print(f"green-ui: dashboard → http://{args.host}:{port}", file=sys.stderr)
    print(f"green-ui: (embed={EMBED_PORT} chat={CHAT_PORT} — not {port})", file=sys.stderr)
    if ge:
        print(f"green-ui: ge → {ge}", file=sys.stderr)
    else:
        print("green-ui: warning — ge binary not found", file=sys.stderr)
    httpd = ThreadingHTTPServer((args.host, port), GreenUIHandler)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\ngreen-ui: stopped", file=sys.stderr)


if __name__ == "__main__":
    main()
