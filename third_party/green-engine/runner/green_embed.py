#!/usr/bin/env python3
"""Green Embed — local OpenAI-compatible /v1/embeddings server (CPU, multilingual).

Uses ibm-granite/granite-embedding-97m-multilingual-r2 (~195 MB, 200+ languages).
Designed for codehelper MCP semantic rerank (CODEHELPER_EMBED_URL).

MCP profile (--mcp): ONNX when available, embedding cache, bounded threads, request batching.

  python3 runner/green_embed.py --port 8766 --mcp --preload
  curl -s http://127.0.0.1:8766/v1/embeddings -H 'Content-Type: application/json' \\
    -d '{"model":"granite-embedding-97m-multilingual-r2","input":"hello"}'
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import queue
import sys
import threading
import time
from collections import OrderedDict
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

DEFAULT_MODEL = os.environ.get(
    "GE_EMBED_MODEL", "ibm-granite/granite-embedding-97m-multilingual-r2"
)
DEFAULT_PORT = int(os.environ.get("GE_EMBED_PORT", "8766"))
GE_HOME = Path(os.environ.get("GE_HOME", Path.home() / ".green"))

_model = None
_model_name = DEFAULT_MODEL
_backend = "unknown"
_encode_lock = threading.Lock()


class EmbeddingCache:
    """LRU cache keyed by sha256(text) — MCP queries repeat often."""

    def __init__(self, max_entries: int) -> None:
        self.max_entries = max(0, max_entries)
        self._data: OrderedDict[str, list[float]] = OrderedDict()
        self._lock = threading.Lock()
        self.hits = 0
        self.misses = 0

    @staticmethod
    def _key(text: str) -> str:
        return hashlib.sha256(text.encode("utf-8")).hexdigest()

    def get_many(self, texts: list[str]) -> tuple[list[int], list[str], list[list[float] | None]]:
        if self.max_entries == 0:
            return list(range(len(texts))), texts, [None] * len(texts)

        keys = [self._key(t) for t in texts]
        out: list[list[float] | None] = []
        miss_idx: list[int] = []
        miss_texts: list[str] = []
        with self._lock:
            for i, (k, t) in enumerate(zip(keys, texts)):
                if k in self._data:
                    self._data.move_to_end(k)
                    out.append(self._data[k])
                    self.hits += 1
                else:
                    out.append(None)
                    miss_idx.append(i)
                    miss_texts.append(t)
                    self.misses += 1
        return miss_idx, miss_texts, out

    def put_many(self, texts: list[str], vecs: list[list[float]]) -> None:
        if self.max_entries == 0:
            return
        with self._lock:
            for t, v in zip(texts, vecs):
                k = self._key(t)
                self._data[k] = v
                self._data.move_to_end(k)
            while len(self._data) > self.max_entries:
                self._data.popitem(last=False)


_cache: EmbeddingCache | None = None


class EmbedBatcher:
    """Coalesce concurrent single-text requests within a short window."""

    def __init__(self, window_ms: float, max_batch: int) -> None:
        self.window_s = max(0.0, window_ms / 1000.0)
        self.max_batch = max(1, max_batch)
        self._pending: list[tuple[list[str], threading.Event, list[list[float] | None] | None, list[str | None]]] = []
        self._lock = threading.Lock()
        self._cv = threading.Condition(self._lock)
        self._stop = False
        threading.Thread(target=self._worker, name="green-embed-batcher", daemon=True).start()

    def embed(self, texts: list[str]) -> list[list[float]]:
        if len(texts) > 1 or self.window_s <= 0:
            return _encode_uncached(texts)

        ev = threading.Event()
        slot: list[list[float] | None] | None = [None]
        err: list[str | None] = [None]
        with self._cv:
            self._pending.append((texts, ev, slot, err))
            self._cv.notify()
        ev.wait()
        if err[0]:
            raise RuntimeError(err[0])
        assert slot[0] is not None
        return slot[0]

    def shutdown(self) -> None:
        with self._cv:
            self._stop = True
            self._cv.notify_all()

    def _worker(self) -> None:
        while True:
            with self._cv:
                while not self._pending and not self._stop:
                    self._cv.wait()
                if self._stop and not self._pending:
                    return
                batch = self._pending[: self.max_batch]
                del self._pending[: len(batch)]
            if not batch:
                continue
            if self.window_s > 0 and len(batch) < self.max_batch:
                time.sleep(self.window_s)
                with self._cv:
                    while self._pending and len(batch) < self.max_batch:
                        batch.append(self._pending.pop(0))

            flat: list[str] = []
            spans: list[tuple[threading.Event, list[list[float] | None], list[str | None], int, int]] = []
            for texts, ev, slot, err in batch:
                start = len(flat)
                flat.extend(texts)
                spans.append((ev, slot, err, start, start + len(texts)))

            try:
                vecs = embed_texts(flat, via_batcher=False)
            except Exception as e:
                msg = str(e)
                for ev, _slot, err, _a, _b in spans:
                    err[0] = msg
                    ev.set()
                continue

            for ev, slot, err, a, b in spans:
                slot[0] = vecs[a:b]
                err[0] = None
                ev.set()


_batcher: EmbedBatcher | None = None


def _configure_threads(n: int) -> None:
    n = max(1, n)
    os.environ.setdefault("OMP_NUM_THREADS", str(n))
    os.environ.setdefault("MKL_NUM_THREADS", str(n))
    os.environ.setdefault("OPENBLAS_NUM_THREADS", str(n))
    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    try:
        import torch

        torch.set_num_threads(n)
        torch.set_num_interop_threads(1)
    except ImportError:
        pass


def load_model(prefer_onnx: bool = True) -> None:
    global _model, _backend
    if _model is not None:
        return
    t0 = time.time()
    model_id = _model_name

    try:
        from sentence_transformers import SentenceTransformer
    except ImportError as e:
        raise RuntimeError(
            "sentence-transformers not installed — run: ge embed install"
        ) from e

    if prefer_onnx and os.environ.get("GE_EMBED_ONNX", "1") == "1":
        try:
            _model = SentenceTransformer(
                model_id,
                device="cpu",
                backend="onnx",
                model_kwargs={"file_name": "onnx/model.onnx"},
            )
            _backend = "onnx-st"
            dim_fn = getattr(_model, "get_embedding_dimension", _model.get_sentence_embedding_dimension)
            print(
                f"green-embed: onnx-st ready dim={dim_fn()} in {time.time() - t0:.1f}s",
                file=sys.stderr,
                flush=True,
            )
            return
        except Exception as e:
            print(f"green-embed: ONNX unavailable ({e}), using PyTorch", file=sys.stderr, flush=True)

    _model = SentenceTransformer(model_id, device="cpu")
    _backend = "torch"
    dim_fn = getattr(_model, "get_embedding_dimension", _model.get_sentence_embedding_dimension)
    print(
        f"green-embed: torch ready dim={dim_fn()} in {time.time() - t0:.1f}s",
        file=sys.stderr,
        flush=True,
    )


def _encode_uncached(texts: list[str]) -> list[list[float]]:
    load_model()
    assert _model is not None
    with _encode_lock:
        vecs = _model.encode(  # type: ignore[union-attr]
            texts,
            normalize_embeddings=True,
            show_progress_bar=False,
            batch_size=min(32, max(1, len(texts))),
        )
        return vecs.tolist()


def embed_texts(texts: list[str], via_batcher: bool = True) -> list[list[float]]:
    if not texts:
        return []
    if via_batcher and _batcher is not None and len(texts) == 1:
        return _batcher.embed(texts)

    if _cache is None or _cache.max_entries == 0:
        return _encode_uncached(texts)

    miss_idx, miss_texts, merged = _cache.get_many(texts)
    if miss_texts:
        fresh = _encode_uncached(miss_texts)
        _cache.put_many(miss_texts, fresh)
        it = iter(fresh)
        for i in miss_idx:
            merged[i] = next(it)
    return [m for m in merged if m is not None]


class Handler(BaseHTTPRequestHandler):
    server_version = "green-embed/0.2"

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"{self.address_string()} - {fmt % args}", file=sys.stderr)

    def _json(self, code: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:
        if self.path in ("/", "/health", "/v1/models"):
            cache_stats = None
            if _cache is not None and _cache.max_entries > 0:
                cache_stats = {
                    "hits": _cache.hits,
                    "misses": _cache.misses,
                    "max_entries": _cache.max_entries,
                }
            self._json(
                200,
                {
                    "status": "ok",
                    "model": _model_name,
                    "backend": _backend,
                    "cache": cache_stats,
                    "object": "list",
                    "data": [{"id": _model_name, "object": "model"}],
                },
            )
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path != "/v1/embeddings":
            self._json(404, {"error": "not found"})
            return
        n = int(self.headers.get("Content-Length", "0"))
        try:
            payload = json.loads(self.rfile.read(n))
        except json.JSONDecodeError:
            self._json(400, {"error": "invalid json"})
            return

        inp = payload.get("input")
        if inp is None:
            self._json(400, {"error": "missing input"})
            return
        texts = inp if isinstance(inp, list) else [str(inp)]
        texts = [str(t) for t in texts]
        if not texts:
            self._json(400, {"error": "empty input"})
            return

        try:
            vecs = embed_texts(texts)
        except Exception as e:
            self._json(500, {"error": str(e)})
            return

        self._json(
            200,
            {
                "object": "list",
                "model": payload.get("model", _model_name),
                "data": [
                    {"object": "embedding", "index": i, "embedding": v}
                    for i, v in enumerate(vecs)
                ],
                "usage": {"prompt_tokens": sum(len(t.split()) for t in texts), "total_tokens": 0},
            },
        )


def _apply_mcp_defaults(args: argparse.Namespace) -> None:
    if not args.mcp:
        return
    if args.threads is None:
        args.threads = max(1, min(4, (os.cpu_count() or 4) // 2))
    if args.cache_size is None:
        args.cache_size = 2048
    if args.batch_window_ms is None:
        args.batch_window_ms = 8.0
    if args.batch_max is None:
        args.batch_max = 32
    args.preload = True


def main() -> None:
    p = argparse.ArgumentParser(description="Green Embed — local multilingual embeddings")
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=DEFAULT_PORT)
    p.add_argument("--model", default=DEFAULT_MODEL, help="HuggingFace embedding model id")
    p.add_argument("--preload", action="store_true", help="Load model before accepting requests")
    p.add_argument(
        "--mcp",
        action="store_true",
        help="MCP profile: preload, ONNX, cache, batching, bounded CPU threads",
    )
    p.add_argument("--threads", type=int, default=None, help="CPU threads for inference (default: auto)")
    p.add_argument("--cache-size", type=int, default=None, help="LRU embedding cache entries (0=off)")
    p.add_argument(
        "--batch-window-ms",
        type=float,
        default=None,
        help="Coalesce concurrent requests within this window (0=off)",
    )
    p.add_argument("--batch-max", type=int, default=None, help="Max texts per coalesced batch")
    p.add_argument("--no-onnx", action="store_true", help="Force PyTorch backend")
    args = p.parse_args()
    _apply_mcp_defaults(args)

    global _model_name, _cache, _batcher
    _model_name = args.model
    os.environ["GE_EMBED_MODEL"] = args.model
    if args.no_onnx:
        os.environ["GE_EMBED_ONNX"] = "0"

    threads = args.threads if args.threads is not None else max(1, (os.cpu_count() or 4) // 2)
    _configure_threads(threads)

    cache_size = args.cache_size if args.cache_size is not None else (512 if args.mcp else 0)
    _cache = EmbeddingCache(cache_size)

    window = args.batch_window_ms if args.batch_window_ms is not None else 0.0
    batch_max = args.batch_max if args.batch_max is not None else 32
    if window > 0:
        _batcher = EmbedBatcher(window, batch_max)

    if args.preload:
        load_model(prefer_onnx=not args.no_onnx)

    addr = (args.host, args.port)
    httpd = ThreadingHTTPServer(addr, Handler)
    print(
        f"green-embed: listening http://{args.host}:{args.port}/v1/embeddings "
        f"(mcp={args.mcp} cache={cache_size} threads={threads} backend={_backend})",
        file=sys.stderr,
        flush=True,
    )
    print(
        "  codehelper: export CODEHELPER_EMBED_URL=http://127.0.0.1:"
        f"{args.port}",
        file=sys.stderr,
    )
    print(f"  model env: CODEHELPER_EMBED_MODEL={args.model}", file=sys.stderr)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\ngreen-embed: stopped", file=sys.stderr)
    finally:
        if _batcher is not None:
            _batcher.shutdown()


if __name__ == "__main__":
    main()
