"""Hugging Face GGUF discovery, metadata, and hardware fit scoring for ge ui."""

from __future__ import annotations

import json
import re
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

USE_CASE_HELP = {
    "chat": "General instruction-following and conversation",
    "code": "Programming, scripts, repo questions",
    "moe": "Mixture-of-experts — Green Engine scheduling shines here",
    "mcp": "Small/fast — codehelper enrich & routing (≤3B Q4)",
    "translate": "Machine translation (ge translate)",
    "embed": "Embeddings — use ge embed, not chat GGUF",
    "vision": "Image+text — needs vision-capable runner",
    "quality": "Larger models — best answers, more VRAM",
    "starter": "Tiny & fast — first pull, tests, low RAM",
}

RELIABILITY_HELP = {
    "curated": "Hand-picked in Green Engine catalog — tested with this stack",
    "official": "Publisher or official org quant",
    "community": "Trusted community quant (e.g. bartowski) — widely used",
    "popular": "High HF downloads — less vetting",
    "unknown": "New or niche — verify before production",
}


def fetch_json(url: str, timeout: float = 12.0) -> Any:
    req = urllib.request.Request(
        url,
        headers={"User-Agent": "green-engine-ui/0.3"},
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def estimate_params_b(text: str) -> float | None:
    t = text.lower().replace("_", " ")
    m = re.search(r"(\d+(?:\.\d+)?)\s*b\b", t)
    if m:
        return float(m.group(1))
    m = re.search(r"\b(\d+(?:\.\d+)?)b\b", t.replace("-", ""))
    if m:
        return float(m.group(1))
    m = re.search(r"-(\d+)b-", t)
    if m:
        return float(m.group(1))
    return None


def estimate_q4_size_gb(params_b: float | None, file_size_bytes: int | None = None) -> float:
    if file_size_bytes and file_size_bytes > 0:
        return round(file_size_bytes / (1024**3), 2)
    if params_b is None:
        return 4.0
    return round(params_b * 0.55, 2)


def infer_use_cases(repo_id: str, tags: list[str] | None = None) -> list[str]:
    s = repo_id.lower()
    cases: list[str] = []
    if any(x in s for x in ("moe", "mixtral", "olmoe", "deepseek-moe")):
        cases.append("moe")
    if any(x in s for x in ("embed", "embedding", "bge-", "e5-")):
        cases.append("embed")
    if any(x in s for x in ("translate", "hymt", "gams", "mt2", "nllb")):
        cases.append("translate")
    if any(x in s for x in ("code", "coder", "starcoder", "deepseek-coder", "codellama")):
        cases.append("code")
    if any(x in s for x in ("llava", "vision", "bakllava", "moondream")):
        cases.append("vision")
    params = estimate_params_b(s)
    if params is not None and params <= 3.5:
        cases.append("mcp")
    if params is not None and params >= 7:
        cases.append("quality")
    if params is not None and params <= 2:
        cases.append("starter")
    if "instruct" in s or "chat" in s:
        if "embed" not in cases:
            cases.append("chat")
    if not cases:
        cases.append("chat")
    out: list[str] = []
    for c in cases:
        if c not in out:
            out.append(c)
    return out


def infer_reliability(repo_id: str, downloads: int, curated_ids: set[str]) -> str:
    rid = repo_id.lower()
    if rid in curated_ids:
        return "curated"
    org = repo_id.split("/")[0].lower()
    official_orgs = {
        "meta-llama", "qwen", "mistralai", "google", "microsoft", "ibm-granite",
        "tencent", "huggingface",
    }
    community_orgs = {"bartowski", "mradermacher", "lmstudio-community"}
    if org in official_orgs:
        return "official"
    if org in community_orgs:
        return "community"
    if downloads >= 50_000:
        return "popular"
    if downloads >= 5_000:
        return "community"
    return "unknown"


def pick_default_gguf(files: list[dict[str, Any]]) -> dict[str, Any] | None:
    ggufs = [f for f in files if str(f.get("rfilename", "")).endswith(".gguf")]
    if not ggufs:
        return None
    for needle in ("Q4_K_M", "IQ4_XS", "Q4_K_S", "Q5_K_M", "Q4_0"):
        for f in ggufs:
            if needle in f["rfilename"]:
                return f
    return ggufs[0]


def search_hf(query: str, limit: int = 25) -> list[dict[str, Any]]:
    q = urllib.parse.quote(query.strip())
    lim = max(1, min(50, limit))
    url = (
        f"https://huggingface.co/api/models?search={q}&filter=gguf"
        f"&sort=downloads&direction=-1&limit={lim}"
    )
    try:
        data = fetch_json(url)
    except (urllib.error.URLError, OSError, json.JSONDecodeError, TimeoutError):
        return []
    if not isinstance(data, list):
        return []
    out: list[dict[str, Any]] = []
    for row in data:
        if not isinstance(row, dict):
            continue
        repo_id = row.get("id") or row.get("modelId")
        if not repo_id:
            continue
        downloads = int(row.get("downloads") or 0)
        tags = row.get("tags") or []
        params = estimate_params_b(repo_id)
        out.append(
            {
                "id": repo_id.replace("/", "--"),
                "repo": repo_id,
                "name": repo_id.split("/")[-1].replace("-", " "),
                "downloads": downloads,
                "tags": tags,
                "params_b": params,
                "size_gb": estimate_q4_size_gb(params),
                "file": "*Q4_K_M.gguf",
                "use_cases": infer_use_cases(repo_id, tags),
                "reliability": infer_reliability(repo_id, downloads, set()),
                "source": "huggingface",
            }
        )
    return out


def repo_gguf_files(repo_id: str) -> list[dict[str, Any]]:
    try:
        meta = fetch_json(f"https://huggingface.co/api/models/{repo_id}")
    except (urllib.error.URLError, OSError, json.JSONDecodeError, TimeoutError):
        return []
    files: list[dict[str, Any]] = []
    for s in meta.get("siblings") or []:
        name = s.get("rfilename", "")
        if not name.endswith(".gguf"):
            continue
        size = int(s.get("size") or 0)
        files.append({"rfilename": name, "size": size, "size_gb": round(size / (1024**3), 2)})
    files.sort(key=lambda x: x["size"])
    return files


def enrich_repo(repo_id: str, curated_ids: set[str]) -> dict[str, Any]:
    files = repo_gguf_files(repo_id)
    sib = [{"rfilename": f["rfilename"], "size": f["size"]} for f in files]
    best = pick_default_gguf(sib)
    params = estimate_params_b(repo_id)
    size_gb = estimate_q4_size_gb(params, best["size"] if best else None)
    downloads = 0
    tags: list[str] = []
    try:
        meta = fetch_json(f"https://huggingface.co/api/models/{repo_id}")
        downloads = int(meta.get("downloads") or 0)
        tags = meta.get("tags") or []
    except (urllib.error.URLError, OSError, json.JSONDecodeError, TimeoutError):
        pass
    return {
        "id": repo_id.replace("/", "--"),
        "repo": repo_id,
        "name": repo_id.split("/")[-1],
        "downloads": downloads,
        "params_b": params,
        "size_gb": size_gb,
        "file": best["rfilename"] if best else "*Q4_K_M.gguf",
        "gguf_files": files[:24],
        "use_cases": infer_use_cases(repo_id, tags),
        "reliability": infer_reliability(repo_id, downloads, curated_ids),
        "source": "huggingface",
    }


def score_for_hardware(
    entry: dict[str, Any],
    hw: dict[str, Any],
    compute: str,
    resolve_gpu_layers,
) -> dict[str, Any]:
    size = float(entry.get("size_gb") or 4)
    min_vram = float(entry.get("min_vram_gb") or size * 1.1)
    min_ram = float(entry.get("min_ram_gb") or size * 1.4)
    vram = float(hw.get("vram_gb") or 0)
    ram = float(hw.get("ram_gb") or 8)
    has_gpu = bool(hw.get("has_gpu"))

    if compute == "gpu":
        fits = has_gpu and vram >= min_vram
        headroom = vram - size if has_gpu else -size
    elif compute == "cpu":
        fits = ram >= min_ram
        headroom = ram - size * 1.2
    else:
        gpu_ok = has_gpu and vram >= min_vram
        cpu_ok = ram >= min_ram
        fits = gpu_ok or cpu_ok
        headroom = max(vram - size if has_gpu else -1, ram - size * 1.2)

    downloads = float(entry.get("downloads") or 0)
    score = 40.0 + min(20.0, downloads**0.15)
    if fits:
        score += 35
    score += min(15, max(0, headroom * 2))
    rel = entry.get("reliability", "unknown")
    if rel == "curated":
        score += 12
    elif rel in ("official", "community"):
        score += 6

    if fits and headroom > 4:
        verdict, label = "excellent", "Runs great"
    elif fits:
        verdict, label = "good", "Should run well"
    elif headroom > -2:
        verdict, label = "tight", "Tight — CPU or partial GPU"
    else:
        verdict, label = "poor", "Too large for this machine"

    gpu_layers = resolve_gpu_layers(compute, size, hw)
    backend = "cpu"
    if compute == "gpu" and has_gpu:
        backend = "gpu"
    elif compute == "auto" and has_gpu and vram >= min_vram:
        backend = "gpu"
    elif compute == "auto" and has_gpu and vram >= size * 0.4:
        backend = "hybrid"

    use_cases = entry.get("use_cases") or ["chat"]
    notes: list[str] = []
    if "mcp" in use_cases:
        notes.append("Good for codehelper MCP routing (small)")
    if "moe" in use_cases:
        notes.append("MoE — pair with Green Engine scheduler")
    if "embed" in use_cases:
        notes.append("Embedding model — not for ge chat")
    notes.append(RELIABILITY_HELP.get(entry.get("reliability", "unknown"), ""))

    return {
        **entry,
        "min_vram_gb": round(min_vram, 1),
        "min_ram_gb": round(min_ram, 1),
        "score": round(score, 1),
        "fits": fits,
        "verdict": verdict,
        "verdict_label": label,
        "suggested_backend": backend,
        "suggested_gpu_layers": gpu_layers,
        "use_case_help": {uc: USE_CASE_HELP.get(uc, uc) for uc in use_cases},
        "reliability_help": RELIABILITY_HELP.get(entry.get("reliability", "unknown"), ""),
        "notes": [n for n in notes if n],
    }
