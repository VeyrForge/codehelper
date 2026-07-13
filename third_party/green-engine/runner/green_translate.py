#!/usr/bin/env python3
"""Green Translate — Hy-MT2 translation API (Green Engine + Green Compress).

OpenAI ``/v1/chat/completions``, Ollama ``/api/chat`` + ``/api/generate``, and Green
``/v1/translate`` on one server. Usage and cost roll up globally and per ``session_id``
(send in JSON body, ``options.session_id`` for Ollama, OpenAI ``user``, or ``X-Session-Id``).

  ge translate serve --port 8768
  curl -s http://127.0.0.1:8768/v1/usage?session_id=my-app-run-1
  curl -s http://127.0.0.1:8768/api/chat -d '{"model":"hy-mt2-7b-green-ultra","session_id":"job-1","messages":[{"role":"user","content":"Translate into French: Hello"}]}'
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlparse

GE_HOME = Path(os.environ.get("GE_HOME", Path.home() / ".green"))
DEFAULT_GGUF = GE_HOME / "models" / "Hy-MT2-7B-Q4_K_M.gguf"
DEFAULT_WORK = GE_HOME / "hymt2-7b-green"
DEFAULT_MANIFEST = DEFAULT_WORK / "model_manifest.json"
DEFAULT_PORT = int(os.environ.get("GE_TRANSLATE_PORT", "8768"))
DEFAULT_HOST = os.environ.get("GE_TRANSLATE_HOST", "127.0.0.1")
PRICING_PATH = GE_HOME / "translate-pricing.json"
USAGE_PATH = GE_HOME / "translate-usage.json"
ROUTER_PATH = GE_HOME / "translate-router.json"

MODEL_ID = os.environ.get("GE_TRANSLATE_MODEL", "hy-mt2-7b-green-ultra")
COMPRESS_METHOD = os.environ.get("GE_COMPRESS_METHOD", "green_ultra")

# Hy-MT2-7B published FLORES-200 XCOMET-XXL (hy-mt2 paper); Green Compress ~99.5% weight fidelity.
QUALITY_FLORES_XCOMET = 86.89
QUALITY_COMPRESS_PCT = 99.51

HYMT2_LANG_CODES = frozenset({
    "zh", "en", "fr", "pt", "es", "ja", "tr", "ru", "ar", "ko", "th", "it", "de", "vi", "ms", "id",
    "tl", "hi", "zh-hant", "pl", "cs", "nl", "km", "my", "fa", "gu", "ur", "te", "mr", "he", "bn",
    "ta", "uk", "bo", "kk", "mn", "ug", "yue",
})

LANG_ALIASES: dict[str, str] = {
    "english": "en", "french": "fr", "german": "de", "spanish": "es", "italian": "it",
    "portuguese": "pt", "japanese": "ja", "korean": "ko", "chinese": "zh", "arabic": "ar",
    "russian": "ru", "turkish": "tr", "vietnamese": "vi", "indonesian": "id", "malay": "ms",
    "hindi": "hi", "polish": "pl", "czech": "cs", "dutch": "nl", "ukrainian": "uk",
    "hebrew": "he", "bengali": "bn", "tamil": "ta", "thai": "th", "persian": "fa",
    "filipino": "tl", "tagalog": "tl", "traditional chinese": "zh-hant", "cantonese": "yue",
    "slovenian": "sl", "slovene": "sl", "slovenščina": "sl", "slovenscina": "sl", "slovenski": "sl",
}

SLOVENIAN_ALIASES = frozenset({"sl", "slovenian", "slovene", "slovenščina", "slovenscina", "slovenski"})

_llm_lock = threading.Lock()
_usage_lock = threading.Lock()
_bench_tok_s: float | None = None


def default_router_config() -> dict[str, Any]:
    return {
        "backend": "green-engine+green-compress",
        "default_route": "hymt2-7b",
        "routes": [
            {
                "id": "hymt2-7b",
                "model_id": "hy-mt2-7b-green-ultra",
                "family": "Hy-MT2-7B",
                "gguf": "models/Hy-MT2-7B-Q4_K_M.gguf",
                "work": "hymt2-7b-green",
                "prompt_style": "hymt2",
                "languages": sorted(HYMT2_LANG_CODES),
            },
            {
                "id": "gams-sl",
                "model_id": "gams-9b-green-ultra",
                "family": "GaMS-9B-SFT-Translator",
                "gguf": "models/GaMS-9B-SFT-Translator.Q4_K_M.gguf",
                "work": "gams-9b-green",
                "prompt_style": "gams_sl",
                "languages": sorted(SLOVENIAN_ALIASES),
            },
        ],
    }


class Route:
    def __init__(self, raw: dict[str, Any]) -> None:
        self.id = raw["id"]
        self.model_id = raw["model_id"]
        self.family = raw.get("family", self.id)
        self.gguf = GE_HOME / raw["gguf"]
        self.work = GE_HOME / raw["work"]
        self.manifest = self.work / "model_manifest.json"
        self.prompt_style = raw.get("prompt_style", "hymt2")
        self.lang_keys = frozenset(x.lower() for x in raw.get("languages", []))

    def manifest_ok(self) -> bool:
        return self.manifest.is_file()

    def gguf_ok(self) -> bool:
        return self.gguf.is_file()


class ModelRouter:
    def __init__(self, gpu_layers: int, ctx: int) -> None:
        self.gpu_layers = gpu_layers
        self.ctx = ctx
        self.routes: dict[str, Route] = {}
        self.default_route_id = "hymt2-7b"
        self._llm: Any = None
        self._active: Route | None = None
        self._manifest_cache: dict[str, dict[str, Any]] = {}

    def load_config(self, path: Path) -> None:
        if path.is_file():
            cfg = json.loads(path.read_text())
        else:
            cfg = default_router_config()
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(cfg, indent=2))
        self.default_route_id = cfg.get("default_route", "hymt2-7b")
        self.routes = {r["id"]: Route(r) for r in cfg.get("routes", [])}

    def manifest_for(self, route: Route) -> dict[str, Any]:
        if route.id not in self._manifest_cache:
            if not route.manifest_ok():
                raise RuntimeError(
                    f"Green Compress manifest missing for {route.id}: {route.manifest}\n"
                    f"  Run: ge translate compress --model {route.id}"
                )
            self._manifest_cache[route.id] = json.loads(route.manifest.read_text())
        return self._manifest_cache[route.id]

    def normalize_lang(self, target: str) -> str:
        t = target.strip().lower().rstrip(":")
        if t in LANG_ALIASES:
            return LANG_ALIASES[t]
        return t

    def pick_route(self, target_lang: str, force_route: str | None = None) -> Route:
        if force_route:
            rid = force_route.strip()
            if rid in self.routes:
                return self.routes[rid]
            for route in self.routes.values():
                if route.model_id == rid:
                    return route
            raise RuntimeError(f"unknown route/model: {force_route}")
        lang = self.normalize_lang(target_lang)
        if lang in SLOVENIAN_ALIASES:
            if "gams-sl" in self.routes and self.routes["gams-sl"].gguf_ok():
                return self.routes["gams-sl"]
        for route in self.routes.values():
            if route.id == "gams-sl":
                continue
            if lang in route.lang_keys or target_lang.strip().lower() in route.lang_keys:
                return route
        return self.routes.get(self.default_route_id) or next(iter(self.routes.values()))

    def ensure_loaded(self, route: Route) -> None:
        if not route.gguf_ok():
            raise RuntimeError(f"GGUF missing for {route.id}: {route.gguf}\n  Run: ge translate pull gams")
        with _llm_lock:
            if self._active and self._active.id == route.id and self._llm is not None:
                return
            if self._llm is not None:
                print(f"green-translate: swapping {self._active.id if self._active else '?'} -> {route.id}", file=sys.stderr, flush=True)
                self._llm = None
                self._active = None
                try:
                    import gc
                    gc.collect()
                except Exception:
                    pass
            try:
                from llama_cpp import Llama
            except ImportError:
                sys.exit("green-translate: llama-cpp-python missing — run: ge translate install")
            self.manifest_for(route)
            print(
                f"green-translate: loading {route.family} ({route.gguf.name}, green manifest ok) ...",
                file=sys.stderr,
                flush=True,
            )
            self._llm = Llama(
                model_path=str(route.gguf),
                n_ctx=self.ctx,
                n_gpu_layers=self.gpu_layers,
                n_threads=max(1, (os.cpu_count() or 4) // 2),
                verbose=False,
            )
            self._active = route

    def generate(self, route: Route, prompt: str, max_tokens: int, temperature: float, source: str = "") -> tuple[str, int]:
        self.ensure_loaded(route)
        assert self._llm is not None
        stops = ["<end_of_turn>", "<eos>", "<|eos|>", "<|endoftext|>", "</s>"]
        with _llm_lock:
            if route.prompt_style == "gams_sl":
                user = f"Prevedi naslednje besedilo v slovenščino.\n{source or prompt}"
                out = self._llm.create_chat_completion(
                    messages=[{"role": "user", "content": user}],
                    max_tokens=min(max_tokens, 256),
                    temperature=temperature,
                    stop=stops,
                )
                text = out["choices"][0]["message"]["content"].strip()
            else:
                out = self._llm(prompt, max_tokens=max_tokens, temperature=temperature, stop=stops)
                text = out["choices"][0]["text"].strip()
        comp = out.get("usage", {}).get("completion_tokens") or estimate_tokens(text)
        return text, int(comp)


_router: ModelRouter | None = None


def empty_usage_totals() -> dict[str, Any]:
    return {
        "requests": 0,
        "prompt_tokens": 0,
        "completion_tokens": 0,
        "total_tokens": 0,
        "estimated_cost_usd": 0.0,
    }


def format_cost_usd(amount: float | int) -> str:
    return f"{float(amount):.8f}"


def merge_usage(into: dict[str, Any], prompt_tokens: int, completion_tokens: int, charge: float) -> None:
    total_tokens = prompt_tokens + completion_tokens
    into["requests"] = into.get("requests", 0) + 1
    into["prompt_tokens"] = into.get("prompt_tokens", 0) + prompt_tokens
    into["completion_tokens"] = into.get("completion_tokens", 0) + completion_tokens
    into["total_tokens"] = into.get("total_tokens", 0) + total_tokens
    into["estimated_cost_usd"] = round(float(into.get("estimated_cost_usd", 0.0)) + charge, 8)


def sum_session_totals(sessions: dict[str, Any]) -> dict[str, Any]:
    totals = empty_usage_totals()
    for sess in sessions.values():
        totals["requests"] += int(sess.get("requests", 0))
        totals["prompt_tokens"] += int(sess.get("prompt_tokens", 0))
        totals["completion_tokens"] += int(sess.get("completion_tokens", 0))
        totals["total_tokens"] += int(sess.get("total_tokens", 0))
        totals["estimated_cost_usd"] = round(
            float(totals["estimated_cost_usd"]) + float(sess.get("estimated_cost_usd", 0.0)), 8
        )
    return totals


def format_usage_totals(raw: dict[str, Any]) -> dict[str, Any]:
    return {
        "requests": int(raw.get("requests", 0)),
        "prompt_tokens": int(raw.get("prompt_tokens", 0)),
        "completion_tokens": int(raw.get("completion_tokens", 0)),
        "total_tokens": int(raw.get("total_tokens", 0)),
        "estimated_cost_usd": format_cost_usd(raw.get("estimated_cost_usd", 0.0)),
    }


def format_session_entry(data: dict[str, Any]) -> dict[str, Any]:
    out = format_usage_totals(data)
    if "first_seen" in data:
        out["first_seen"] = data["first_seen"]
    if "last_seen" in data:
        out["last_seen"] = data["last_seen"]
    return out


def recompute_usage(usage: dict[str, Any]) -> dict[str, Any]:
    sessions = usage.setdefault("sessions", {})
    totals = sum_session_totals(sessions)
    usage["requests"] = totals["requests"]
    usage["prompt_tokens"] = totals["prompt_tokens"]
    usage["completion_tokens"] = totals["completion_tokens"]
    usage["total_tokens"] = totals["total_tokens"]
    usage["estimated_cost_usd"] = totals["estimated_cost_usd"]
    if "since" not in usage:
        usage["since"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    return usage


def fresh_usage() -> dict[str, Any]:
    return {
        **empty_usage_totals(),
        "since": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "sessions": {},
    }


def clear_usage() -> None:
    with _usage_lock:
        save_usage(fresh_usage())


def resolve_session_id(payload: dict[str, Any], headers: Any) -> str:
    """App-supplied id merges requests; omitted id => one session per request."""
    opts = payload.get("options") or {}
    for key in ("session_id", "session"):
        val = payload.get(key) or opts.get(key)
        if val is not None and str(val).strip():
            return str(val).strip()
    user = payload.get("user")
    if user is not None and str(user).strip():
        return str(user).strip()
    header = headers.get("X-Session-Id") or headers.get("X-Green-Session-Id")
    if header and str(header).strip():
        return str(header).strip()
    return f"sess-{uuid.uuid4()}"


def usage_block(prompt_tokens: int, completion_tokens: int, charge: float, elapsed: float, session_id: str) -> dict[str, Any]:
    comp = completion_tokens
    return {
        "session_id": session_id,
        "prompt_tokens": prompt_tokens,
        "completion_tokens": comp,
        "total_tokens": prompt_tokens + comp,
        "estimated_cost_usd": format_cost_usd(charge),
        "generation_tok_s": round(comp / elapsed, 2) if elapsed > 0 else 0.0,
    }


def green_home() -> Path:
    return GE_HOME


def load_pricing() -> dict[str, Any]:
    if PRICING_PATH.is_file():
        return json.loads(PRICING_PATH.read_text())
    # Default: quality-adjusted local rate vs commercial MT (~$10/1M chars ≈ $2.5/1M tokens).
    # Hy-MT2-7B tier: premium quality, local cost basis.
    return {
        "model_id": MODEL_ID,
        "backend": "green-engine+green-compress",
        "base_model": "tencent/Hy-MT2-7B",
        "compress_method": COMPRESS_METHOD,
        "quality": {
            "compress_weight_fidelity_pct": QUALITY_COMPRESS_PCT,
            "flores_xcomet_xxl_reference": QUALITY_FLORES_XCOMET,
            "languages": 33,
        },
        "currency": "USD",
        "input_per_1m_tokens": 0.40,
        "output_per_1m_tokens": 1.20,
        "minimum_charge_usd": 0.000001,
        "notes": (
            "Local Hy-MT2-7B translation tier. Weights validated via Green Compress manifest; "
            "generation uses Hy-MT2 hunyuan-dense runtime. Rates are configurable in "
            f"{PRICING_PATH}."
        ),
    }


def estimate_from_text(text: str, target_lang: str, output_tokens: int | None = None) -> dict[str, int]:
    is_sl = target_lang.strip().lower() in SLOVENIAN_ALIASES or (
        _router is not None and _router.normalize_lang(target_lang) in SLOVENIAN_ALIASES
    )
    if is_sl:
        prompt = f"Prevedi naslednje besedilo v slovenščino.\n{text}"
        messages = [{"role": "user", "content": prompt}]
    else:
        messages = [{"role": "user", "content": f"Translate into {target_lang}: {text}"}]
        prompt = build_translate_prompt(messages, style="hymt2")
    prompt_tokens = count_message_tokens(messages) + estimate_tokens(prompt)
    completion_tokens = output_tokens if output_tokens is not None else max(1, estimate_tokens(text))
    return {"prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens}


def pricing_estimate(
    prompt_tokens: int, completion_tokens: int, pricing: dict[str, Any] | None = None
) -> dict[str, Any]:
    p = pricing or load_pricing()
    charge = cost_usd(prompt_tokens, completion_tokens, p)
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": prompt_tokens + completion_tokens,
        "estimated_cost_usd": format_cost_usd(charge),
        "currency": p.get("currency", "USD"),
    }


def get_pricing_view(
    prompt_tokens: int | None = None,
    completion_tokens: int | None = None,
) -> dict[str, Any]:
    p = load_pricing()
    currency = p.get("currency", "USD")
    inp_rate = float(p.get("input_per_1m_tokens", 0.40))
    out_rate = float(p.get("output_per_1m_tokens", 1.20))
    min_charge = float(p.get("minimum_charge_usd", 0.000001))
    examples = [
        (50, 10, "short"),
        (500, 100, "medium"),
        (5000, 1000, "long"),
    ]
    body: dict[str, Any] = {
        "currency": currency,
        "model_id": p.get("model_id", MODEL_ID),
        "backend": p.get("backend", "green-engine+green-compress"),
        "base_model": p.get("base_model", "tencent/Hy-MT2-7B"),
        "compress_method": p.get("compress_method", COMPRESS_METHOD),
        "rates": {
            "input_per_1m_tokens": inp_rate,
            "output_per_1m_tokens": out_rate,
            "minimum_charge_usd": format_cost_usd(min_charge),
        },
        "quality": p.get("quality", {}),
        "capacity": {
            "manifest_tensors": sum(
                len(_router.manifest_for(r).get("tensors", []))
                for r in (_router.routes.values() if _router else [])
                if r.manifest_ok()
            ) if _router else 0,
            "observed_matmul_tok_s": _bench_tok_s,
            "active_route": _router._active.id if _router and _router._active else None,
            "routes": [
                {
                    "id": r.id,
                    "model_id": r.model_id,
                    "manifest_tensors": len(_router.manifest_for(r).get("tensors", [])) if r.manifest_ok() and _router else 0,
                    "loaded": _router._active.id == r.id if _router and _router._active else False,
                }
                for r in (_router.routes.values() if _router else [])
            ],
        },
        "examples": [
            {
                "label": label,
                "prompt_tokens": pt,
                "completion_tokens": ct,
                **pricing_estimate(pt, ct, p),
            }
            for pt, ct, label in examples
        ],
        "config_path": str(PRICING_PATH),
        "notes": p.get("notes", ""),
    }
    if prompt_tokens is not None and completion_tokens is not None:
        body["estimate"] = pricing_estimate(prompt_tokens, completion_tokens, p)
    return body


def load_usage() -> dict[str, Any]:
    if USAGE_PATH.is_file():
        try:
            data = json.loads(USAGE_PATH.read_text())
            data.setdefault("sessions", {})
            return recompute_usage(data)
        except json.JSONDecodeError:
            pass
    return fresh_usage()


def save_usage(u: dict[str, Any]) -> None:
    USAGE_PATH.parent.mkdir(parents=True, exist_ok=True)
    USAGE_PATH.write_text(json.dumps(recompute_usage(u), indent=2))


def record_usage(session_id: str, prompt_tokens: int, completion_tokens: int) -> tuple[int, float]:
    pricing = load_pricing()
    charge = cost_usd(prompt_tokens, completion_tokens, pricing)
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    with _usage_lock:
        usage = load_usage()
        sessions = usage.setdefault("sessions", {})
        sess = sessions.setdefault(session_id, {**empty_usage_totals(), "first_seen": now})
        if "first_seen" not in sess:
            sess["first_seen"] = now
        sess["last_seen"] = now
        merge_usage(sess, prompt_tokens, completion_tokens, charge)
        save_usage(usage)
    return prompt_tokens + completion_tokens, charge


def get_usage_view(session_id: str | None = None) -> dict[str, Any]:
    usage = load_usage()
    currency = load_pricing().get("currency", "USD")
    if session_id:
        sess = usage.get("sessions", {}).get(session_id)
        if not sess:
            return {
                "currency": currency,
                "session_id": session_id,
                "found": False,
                **format_usage_totals(empty_usage_totals()),
            }
        return {
            "currency": currency,
            "session_id": session_id,
            "found": True,
            **format_session_entry(sess),
        }
    sessions = usage.get("sessions", {})
    return {
        "currency": currency,
        "since": usage.get("since"),
        "totals": format_usage_totals(usage),
        "session_count": len(sessions),
        "sessions": {sid: format_session_entry(data) for sid, data in sessions.items()},
    }


def load_manifest(path: Path) -> dict[str, Any]:
    if not path.is_file():
        sys.exit(
            f"green-translate: manifest missing: {path}\n"
            "  Run: ge translate compress   # Hy-MT2-7B -> Green Compress\n"
            "  Or: ge translate serve --manifest PATH"
        )
    return json.loads(path.read_text())


def run_weights_bench(manifest: Path) -> float | None:
    """Run green-weights-bench; return green_ultra tok/s if available."""
    ge_root = Path(__file__).resolve().parent.parent
    bench = ge_root / "target" / "release" / "green-weights-bench"
    if not bench.is_file():
        return None
    try:
        out = subprocess.run(
            [str(bench), "--manifest", str(manifest), "--tokens", "32", "--iters", "10"],
            capture_output=True,
            text=True,
            timeout=600,
        )
        if out.returncode != 0:
            return None
        for line in out.stdout.splitlines():
            if COMPRESS_METHOD in line and "tok/s" not in line:
                parts = line.split()
                for i, p in enumerate(parts):
                    if p.replace(".", "").isdigit() and i + 1 < len(parts):
                        try:
                            val = float(p)
                            if 10 < val < 100000:
                                return val
                        except ValueError:
                            pass
        m = re.search(rf"{re.escape(COMPRESS_METHOD)}\s+[\d.]+\%\s+[\d.]+\s+[\d.]+x\s+(\d+)", out.stdout)
        if m:
            return float(m.group(1))
    except (subprocess.TimeoutExpired, OSError):
        return None
    return None


def estimate_tokens(text: str) -> int:
    return max(1, len(text) // 3)


def count_message_tokens(messages: list[dict[str, Any]]) -> int:
    total = 0
    for m in messages:
        total += estimate_tokens(str(m.get("content", "")))
    return total


def build_translate_prompt(messages: list[dict[str, Any]], style: str = "hymt2") -> str:
    user_text = ""
    target = "the target language"
    for m in messages:
        if m.get("role") == "user":
            user_text = str(m.get("content", "")).strip()
    m = re.search(
        r"(?:into|to|v)\s+([A-Za-z\u0080-\uFFFF][A-Za-z\u0080-\uFFFF\s\-]*)",
        user_text,
        re.I,
    )
    if m:
        target = m.group(1).strip().rstrip(":")
        body = re.sub(r"^translate[^:]*:\s*", "", user_text, flags=re.I).strip()
        body = re.sub(r"^prevedi[^:]*:\s*", "", body, flags=re.I).strip()
        source = body if body and body != user_text else user_text
    else:
        source = user_text
    if style == "gams_sl":
        return f"Prevedi naslednje besedilo v slovenščino.\n{source}"
    return (
        f"Translate the following text into {target}. Note that you should only output "
        f"the translated result without any additional explanation:\n\n{source}"
    )


def build_prompt_for_route(route: Route, source: str, target_lang: str) -> str:
    if route.prompt_style == "gams_sl":
        return f"Prevedi naslednje besedilo v slovenščino.\n{source}"
    messages = [{"role": "user", "content": f"Translate into {target_lang}: {source}"}]
    return build_translate_prompt(messages, style="hymt2")


def parse_force_route(payload: dict[str, Any], headers: Any) -> str | None:
    for key in ("route", "model", "backend_model"):
        val = payload.get(key)
        if val is not None and str(val).strip():
            return str(val).strip()
    hdr = headers.get("X-Green-Route") or headers.get("X-Model-Route")
    if hdr and str(hdr).strip():
        return str(hdr).strip()
    return None


def run_translation(
    source: str,
    target_lang: str,
    max_tokens: int,
    temperature: float,
    force_route: str | None = None,
) -> tuple[str, int, int, float, Route]:
    assert _router is not None
    route = _router.pick_route(target_lang, force_route)
    prompt = build_prompt_for_route(route, source, target_lang)
    messages = [{"role": "user", "content": prompt}]
    prompt_tokens = count_message_tokens(messages) + estimate_tokens(prompt)
    t0 = time.perf_counter()
    text, completion_tokens = _router.generate(route, prompt, max_tokens, temperature, source=source)
    elapsed = time.perf_counter() - t0
    return text, prompt_tokens, completion_tokens, elapsed, route


def cost_usd(prompt_t: int, completion_t: int, pricing: dict[str, Any]) -> float:
    inp = prompt_t * pricing["input_per_1m_tokens"] / 1_000_000
    out = completion_t * pricing["output_per_1m_tokens"] / 1_000_000
    return max(pricing.get("minimum_charge_usd", 0.0), inp + out)


class Handler(BaseHTTPRequestHandler):
    server_version = "green-translate/0.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"{self.address_string()} - {fmt % args}", file=sys.stderr)

    def _active_model_id(self) -> str:
        if _router and _router._active:
            return _router._active.model_id
        return MODEL_ID

    def _json(self, code: int, body: dict[str, Any], extra_headers: dict[str, str] | None = None) -> None:
        raw = json.dumps(body, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("X-Green-Backend", "green-engine+green-compress")
        self.send_header("X-Green-Model", self._active_model_id())
        if _router and _router._active:
            self.send_header("X-Green-Route", _router._active.id)
        if extra_headers:
            for k, v in extra_headers.items():
                self.send_header(k, v)
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:
        if self.path in ("/", "/health"):
            routes_info = []
            if _router:
                for r in _router.routes.values():
                    routes_info.append({
                        "id": r.id,
                        "model_id": r.model_id,
                        "family": r.family,
                        "gguf": r.gguf.is_file(),
                        "manifest": r.manifest_ok(),
                        "loaded": _router._active.id == r.id if _router._active else False,
                    })
            self._json(
                200,
                {
                    "status": "ok",
                    "backend": "green-engine+green-compress",
                    "active_route": _router._active.id if _router and _router._active else None,
                    "active_model": self._active_model_id(),
                    "routes": routes_info,
                    "bench_tok_s_matmul": _bench_tok_s,
                },
            )
            return
        if self.path == "/v1/routes":
            self._json(
                200,
                {
                    "routes": [
                        {
                            "id": r.id,
                            "model_id": r.model_id,
                            "family": r.family,
                            "languages": sorted(r.lang_keys)[:20],
                            "manifest_ok": r.manifest_ok(),
                            "gguf_ok": r.gguf_ok(),
                        }
                        for r in (_router.routes.values() if _router else [])
                    ]
                },
            )
            return
        if self.path == "/v1/models":
            self._json(
                200,
                {
                    "object": "list",
                    "data": [
                        {
                            "id": r.model_id,
                            "object": "model",
                            "owned_by": "green-engine",
                            "meta": {
                                "route": r.id,
                                "family": r.family,
                                "compress": COMPRESS_METHOD,
                            },
                        }
                        for r in (_router.routes.values() if _router else [])
                    ],
                },
            )
            return
        if self.path == "/v1/pricing" or self.path.startswith("/v1/pricing?") or self.path == "/pricing" or self.path.startswith("/pricing?"):
            qs = parse_qs(urlparse(self.path).query)
            pt = qs.get("prompt_tokens", [None])[0]
            ct = qs.get("completion_tokens", [None])[0]
            prompt_tokens = int(pt) if pt is not None else None
            completion_tokens = int(ct) if ct is not None else None
            self._json(200, get_pricing_view(prompt_tokens, completion_tokens))
            return
        if self.path == "/v1/usage" or self.path.startswith("/v1/usage?"):
            qs = parse_qs(urlparse(self.path).query)
            sid = (qs.get("session_id") or qs.get("session") or [None])[0]
            self._json(200, get_usage_view(sid))
            return
        if self.path == "/api/tags":
            self._json(
                200,
                {
                    "models": [
                        {
                            "name": MODEL_ID,
                            "model": MODEL_ID,
                            "modified_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                            "size": 0,
                            "digest": "green-compress",
                            "details": {
                                "family": "hunyuan-dense",
                                "parameter_size": "7B",
                                "quantization_level": "green_ultra",
                            },
                        }
                    ]
                },
            )
            return
        self._json(404, {"error": "not found"})

    def _read_json(self) -> tuple[dict[str, Any] | None, int]:
        n = int(self.headers.get("Content-Length", "0"))
        try:
            return json.loads(self.rfile.read(n)), 0
        except json.JSONDecodeError:
            return None, 400

    def do_DELETE(self) -> None:
        if self.path == "/v1/usage" or self.path.startswith("/v1/usage?"):
            clear_usage()
            self._json(200, {"cleared": True, **get_usage_view()})
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path == "/v1/pricing/estimate" or self.path == "/pricing/estimate":
            self._handle_pricing_estimate()
            return
        if self.path == "/v1/translate/batch":
            self._handle_translate_batch()
            return
        if self.path == "/v1/translate":
            self._handle_translate()
            return
        if self.path == "/v1/chat/completions":
            self._handle_chat_completion()
            return
        if self.path == "/api/chat":
            self._handle_ollama_chat()
            return
        if self.path == "/api/generate":
            self._handle_ollama_generate()
            return
        self._json(404, {"error": "not found"})

    def _run_translation(
        self,
        source: str,
        target_lang: str,
        max_tokens: int,
        temperature: float,
        force_route: str | None = None,
    ) -> tuple[str, int, int, float, Route]:
        return run_translation(source, target_lang, max_tokens, temperature, force_route)

    def _record_usage(self, session_id: str, prompt_tokens: int, completion_tokens: int) -> tuple[int, float]:
        return record_usage(session_id, prompt_tokens, completion_tokens)

    def _handle_pricing_estimate(self) -> None:
        payload, err = self._read_json()
        if payload is None:
            self._json(err, {"error": "invalid json"})
            return
        if "prompt_tokens" in payload or "completion_tokens" in payload:
            prompt_tokens = int(payload.get("prompt_tokens", 0))
            completion_tokens = int(payload.get("completion_tokens", 0))
        elif payload.get("text"):
            target = payload.get("target_lang", payload.get("target", "French"))
            out_t = payload.get("estimated_output_tokens")
            out_t = int(out_t) if out_t is not None else None
            tok = estimate_from_text(str(payload["text"]), target, out_t)
            prompt_tokens = tok["prompt_tokens"]
            completion_tokens = tok["completion_tokens"]
        else:
            self._json(
                400,
                {
                    "error": "provide prompt_tokens+completion_tokens, or text+target_lang",
                },
            )
            return
        est = pricing_estimate(prompt_tokens, completion_tokens)
        self._json(
            200,
            {
                "model_id": load_pricing().get("model_id", MODEL_ID),
                **est,
                "rates": get_pricing_view()["rates"],
            },
        )

    def _handle_translate(self) -> None:
        payload, err = self._read_json()
        if payload is None:
            self._json(err, {"error": "invalid json"})
            return
        session_id = resolve_session_id(payload, self.headers)
        text_in = payload.get("text", "")
        target = payload.get("target_lang", payload.get("target", "French"))
        if not text_in:
            self._json(400, {"error": "missing text"})
            return
        max_tokens = int(payload.get("max_tokens", 512))
        temperature = float(payload.get("temperature", 0.7))
        force = parse_force_route(payload, self.headers)
        try:
            translated, prompt_t, comp_t, elapsed, route = self._run_translation(
                text_in, target, max_tokens, temperature, force
            )
        except Exception as e:
            self._json(500, {"error": str(e)})
            return
        _, charge = self._record_usage(session_id, prompt_t, comp_t)
        self._json(
            200,
            {
                "model": route.model_id,
                "route": route.id,
                "session_id": session_id,
                "target_lang": target,
                "text": text_in,
                "translation": translated,
                "usage": usage_block(prompt_t, comp_t, charge, elapsed, session_id),
            },
            extra_headers={
                "X-Session-Id": session_id,
                "X-Green-Route": route.id,
                "X-Generation-Tok-S": f"{comp_t / elapsed:.2f}" if elapsed > 0 else "0",
                "X-Estimated-Cost-USD": f"{charge:.8f}",
            },
        )

    def _handle_translate_batch(self) -> None:
        payload, err = self._read_json()
        if payload is None:
            self._json(err, {"error": "invalid json"})
            return
        batch_session = resolve_session_id(payload, self.headers)
        items = payload.get("items", [])
        if not items:
            self._json(400, {"error": "missing items"})
            return
        max_tokens = int(payload.get("max_tokens", 512))
        temperature = float(payload.get("temperature", 0.7))
        force = parse_force_route(payload, self.headers)
        results = []
        batch_prompt = batch_comp = 0
        batch_charge = 0.0
        for i, item in enumerate(items):
            text_in = item.get("text", "")
            target = item.get("target_lang", item.get("target", "French"))
            item_session = batch_session
            for key in ("session_id", "session"):
                if item.get(key):
                    item_session = str(item[key]).strip()
                    break
            item_force = force
            for key in ("route", "model"):
                if item.get(key):
                    item_force = str(item[key]).strip()
                    break
            if not text_in:
                results.append({"index": i, "error": "missing text"})
                continue
            try:
                translated, prompt_t, comp_t, elapsed, route = self._run_translation(
                    text_in, target, max_tokens, temperature, item_force
                )
                _, charge = self._record_usage(item_session, prompt_t, comp_t)
                batch_prompt += prompt_t
                batch_comp += comp_t
                batch_charge += charge
                results.append(
                    {
                        "index": i,
                        "session_id": item_session,
                        "route": route.id,
                        "model": route.model_id,
                        "target_lang": target,
                        "text": text_in,
                        "translation": translated,
                        "usage": usage_block(prompt_t, comp_t, charge, elapsed, item_session),
                    }
                )
            except Exception as e:
                results.append({"index": i, "error": str(e)})
        self._json(
            200,
            {
                "model": MODEL_ID,
                "session_id": batch_session,
                "count": len(results),
                "results": results,
                "batch_usage": {
                    **usage_block(batch_prompt, batch_comp, batch_charge, 0.0, batch_session),
                    "generation_tok_s": None,
                },
            },
            extra_headers={"X-Session-Id": batch_session},
        )

    def _translate_from_messages(
        self,
        messages: list[dict[str, Any]],
        max_tokens: int,
        temperature: float,
        force_route: str | None,
    ) -> tuple[str, int, int, float, Route]:
        assert _router is not None
        target = "French"
        for m in messages:
            if m.get("role") == "user":
                um = re.search(
                    r"(?:into|to|v)\s+([A-Za-z\u0080-\uFFFF][A-Za-z\u0080-\uFFFF\s\-]*)",
                    str(m.get("content", "")),
                    re.I,
                )
                if um:
                    target = um.group(1).strip().rstrip(":")
        route = _router.pick_route(target, force_route)
        prompt = build_translate_prompt(messages, style=route.prompt_style)
        source = ""
        for m in messages:
            if m.get("role") == "user":
                um = re.search(
                    r"(?:into|to|v)\s+[^:\n]+:\s*(.+)$",
                    str(m.get("content", "")),
                    re.I | re.S,
                )
                if um:
                    source = um.group(1).strip()
                elif route.prompt_style == "gams_sl":
                    source = re.sub(r"(?i)^prevedi[^:]*:\s*", "", str(m.get("content", ""))).strip()
        prompt_tokens = count_message_tokens(messages) + estimate_tokens(prompt)
        t0 = time.perf_counter()
        text, completion_tokens = _router.generate(route, prompt, max_tokens, temperature, source=source)
        elapsed = time.perf_counter() - t0
        return text, prompt_tokens, completion_tokens, elapsed, route

    def _handle_chat_completion(self) -> None:
        payload, err = self._read_json()
        if payload is None:
            self._json(err, {"error": "invalid json"})
            return
        session_id = resolve_session_id(payload, self.headers)
        messages = payload.get("messages")
        if not messages:
            self._json(400, {"error": "missing messages"})
            return
        max_tokens = int(payload.get("max_tokens", 512))
        temperature = float(payload.get("temperature", 0.7))
        force = parse_force_route(payload, self.headers)
        try:
            text, prompt_tokens, completion_tokens, elapsed, route = self._translate_from_messages(
                messages, max_tokens, temperature, force
            )
        except Exception as e:
            self._json(500, {"error": str(e)})
            return
        total_tokens, charge = self._record_usage(session_id, prompt_tokens, completion_tokens)
        self._json(
            200,
            {
                "id": f"chatcmpl-{uuid.uuid4()}",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": payload.get("model", route.model_id),
                "route": route.id,
                "session_id": session_id,
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": text},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {
                    **usage_block(prompt_tokens, completion_tokens, charge, elapsed, session_id),
                    "total_tokens": total_tokens,
                },
            },
            extra_headers={
                "X-Session-Id": session_id,
                "X-Green-Route": route.id,
                "X-Generation-Tok-S": f"{completion_tokens / elapsed:.2f}" if elapsed > 0 else "0",
                "X-Estimated-Cost-USD": f"{charge:.8f}",
            },
        )

    def _handle_ollama_chat(self) -> None:
        payload, err = self._read_json()
        if payload is None:
            self._json(err, {"error": "invalid json"})
            return
        if payload.get("stream"):
            self._json(400, {"error": "stream=true not supported; use stream=false"})
            return
        session_id = resolve_session_id(payload, self.headers)
        messages = payload.get("messages")
        if not messages:
            self._json(400, {"error": "missing messages"})
            return
        max_tokens = int(payload.get("options", {}).get("num_predict", payload.get("max_tokens", 512)))
        temperature = float(payload.get("options", {}).get("temperature", payload.get("temperature", 0.7)))
        force = parse_force_route(payload, self.headers)
        try:
            text, prompt_tokens, completion_tokens, elapsed, route = self._translate_from_messages(
                messages, max_tokens, temperature, force
            )
        except Exception as e:
            self._json(500, {"error": str(e)})
            return
        elapsed_ns = int(elapsed * 1_000_000_000)
        _, charge = self._record_usage(session_id, prompt_tokens, completion_tokens)
        self._json(
            200,
            {
                "model": payload.get("model", route.model_id),
                "route": route.id,
                "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "message": {"role": "assistant", "content": text},
                "done": True,
                "done_reason": "stop",
                "session_id": session_id,
                "prompt_eval_count": prompt_tokens,
                "eval_count": completion_tokens,
                "total_duration": elapsed_ns,
                "eval_duration": elapsed_ns,
                "green_usage": usage_block(prompt_tokens, completion_tokens, charge, elapsed, session_id),
            },
            extra_headers={
                "X-Session-Id": session_id,
                "X-Green-Route": route.id,
                "X-Estimated-Cost-USD": f"{charge:.8f}",
            },
        )

    def _handle_ollama_generate(self) -> None:
        payload, err = self._read_json()
        if payload is None:
            self._json(err, {"error": "invalid json"})
            return
        if payload.get("stream"):
            self._json(400, {"error": "stream=true not supported; use stream=false"})
            return
        session_id = resolve_session_id(payload, self.headers)
        raw_prompt = payload.get("prompt", "")
        if not raw_prompt:
            self._json(400, {"error": "missing prompt"})
            return
        max_tokens = int(payload.get("options", {}).get("num_predict", payload.get("max_tokens", 512)))
        temperature = float(payload.get("options", {}).get("temperature", payload.get("temperature", 0.7)))
        force = parse_force_route(payload, self.headers)
        if payload.get("messages"):
            messages = payload.get("messages")
            try:
                text, prompt_tokens, completion_tokens, elapsed, route = self._translate_from_messages(
                    messages, max_tokens, temperature, force
                )
            except Exception as e:
                self._json(500, {"error": str(e)})
                return
        else:
            assert _router is not None
            target = "French"
            if "sloven" in raw_prompt.lower() or "prevedi" in raw_prompt.lower():
                target = "Slovenian"
            route = _router.pick_route(target, force)
            if "translate" in raw_prompt.lower() or "prevedi" in raw_prompt.lower():
                messages = [{"role": "user", "content": raw_prompt}]
                prompt = build_translate_prompt(messages, style=route.prompt_style)
                prompt_tokens = count_message_tokens(messages) + estimate_tokens(prompt)
            else:
                prompt = raw_prompt
                prompt_tokens = estimate_tokens(prompt)
            t0 = time.perf_counter()
            try:
                text, completion_tokens = _router.generate(route, prompt, max_tokens, temperature, source=raw_prompt)
            except Exception as e:
                self._json(500, {"error": str(e)})
                return
            elapsed = time.perf_counter() - t0
        elapsed_ns = int(elapsed * 1_000_000_000)
        _, charge = self._record_usage(session_id, prompt_tokens, completion_tokens)
        self._json(
            200,
            {
                "model": payload.get("model", route.model_id),
                "route": route.id,
                "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "response": text,
                "done": True,
                "done_reason": "stop",
                "session_id": session_id,
                "prompt_eval_count": prompt_tokens,
                "eval_count": completion_tokens,
                "total_duration": elapsed_ns,
                "eval_duration": elapsed_ns,
                "green_usage": usage_block(prompt_tokens, completion_tokens, charge, elapsed, session_id),
            },
            extra_headers={
                "X-Session-Id": session_id,
                "X-Green-Route": route.id,
                "X-Estimated-Cost-USD": f"{charge:.8f}",
            },
        )


def main() -> None:
    global _router, _bench_tok_s

    ap = argparse.ArgumentParser(description="Green Translate — routed MT (Green Engine + Green Compress)")
    ap.add_argument("--host", default=DEFAULT_HOST)
    ap.add_argument("--port", type=int, default=DEFAULT_PORT)
    ap.add_argument("--router", type=Path, default=ROUTER_PATH)
    ap.add_argument("--manifest", type=Path, default=None, help=argparse.SUPPRESS)
    ap.add_argument("--gguf", type=Path, default=None, help=argparse.SUPPRESS)
    ap.add_argument("--gpu-layers", type=int, default=int(os.environ.get("GE_GPU_LAYERS", "18")))
    ap.add_argument("--ctx", type=int, default=4096)
    ap.add_argument("--skip-bench", action="store_true")
    args = ap.parse_args()

    _router = ModelRouter(args.gpu_layers, args.ctx)
    _router.load_config(args.router)
    print(f"green-translate: router {args.router} ({len(_router.routes)} routes, lazy load)", file=sys.stderr)

    for route in _router.routes.values():
        if not route.manifest_ok():
            print(f"green-translate: warning: no manifest for {route.id} — ge translate compress --model {route.id}", file=sys.stderr)
        if not route.gguf_ok():
            print(f"green-translate: warning: no GGUF for {route.id} — ge translate pull {route.id}", file=sys.stderr)

    if not args.skip_bench:
        for route in _router.routes.values():
            if route.manifest_ok():
                print(f"green-translate: bench {route.id} ...", file=sys.stderr)
                tok_s = run_weights_bench(route.manifest)
                if tok_s:
                    _bench_tok_s = tok_s
                    print(f"green-translate: {route.id} matmul ~{tok_s:.0f} tok/s", file=sys.stderr)

    pricing = load_pricing()
    PRICING_PATH.parent.mkdir(parents=True, exist_ok=True)
    if not PRICING_PATH.is_file():
        PRICING_PATH.write_text(json.dumps(pricing, indent=2))

    addr = (args.host, args.port)
    httpd = ThreadingHTTPServer(addr, Handler)
    print(f"green-translate: http://{args.host}:{args.port}/v1/translate", file=sys.stderr)
    print(f"  routes:  http://{args.host}:{args.port}/v1/routes", file=sys.stderr)
    print(f"  ollama:  http://{args.host}:{args.port}/api/chat", file=sys.stderr)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\ngreen-translate: stopped", file=sys.stderr)


if __name__ == "__main__":
    main()
