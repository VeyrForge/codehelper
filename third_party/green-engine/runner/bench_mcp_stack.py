#!/usr/bin/env python3
"""Benchmark green-embed / green-chat MCP stack vs baselines.

Measures embed latency (cold, warm, cache hit), optional Ollama chat ping,
and prints a markdown-friendly table. Results are for local comparison only.

  python3 runner/bench_mcp_stack.py
  python3 runner/bench_mcp_stack.py --embed-url http://127.0.0.1:8766
  python3 runner/bench_mcp_stack.py --ollama http://127.0.0.1:11434
"""

from __future__ import annotations

import argparse
import json
import statistics
import sys
import time
import urllib.error
import urllib.request
from typing import Any

SAMPLE_QUERIES = [
    "MoE expert scheduler prefetch cache",
    "planificador de expertos caché",
    "find_greencompress MCP orchestration",
    "expert eviction LRU admit",
]


def _post_json(url: str, body: dict[str, Any], timeout: float = 120.0) -> tuple[float, dict[str, Any]]:
    raw = json.dumps(body).encode()
    req = urllib.request.Request(
        url,
        data=raw,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as r:
        data = json.loads(r.read())
    ms = (time.perf_counter() - t0) * 1000.0
    return ms, data


def _get_json(url: str, timeout: float = 10.0) -> dict[str, Any]:
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return json.loads(r.read())


def bench_embed(url: str, repeats: int) -> dict[str, Any]:
    base = url.rstrip("/")
    health: dict[str, Any] = {}
    try:
        health = _get_json(f"{base}/v1/models")
    except Exception as e:
        return {"ok": False, "error": f"embed server not reachable: {e}"}

    latencies: list[float] = []
    for i in range(repeats):
        q = SAMPLE_QUERIES[i % len(SAMPLE_QUERIES)]
        ms, _ = _post_json(f"{base}/v1/embeddings", {"input": q})
        latencies.append(ms)

    # Cache hit: repeat first query
    cache_ms, _ = _post_json(f"{base}/v1/embeddings", {"input": SAMPLE_QUERIES[0]})

    return {
        "ok": True,
        "backend": health.get("backend", "?"),
        "cache_stats": health.get("cache"),
        "p50_ms": statistics.median(latencies),
        "p95_ms": sorted(latencies)[max(0, int(len(latencies) * 0.95) - 1)],
        "cache_hit_ms": cache_ms,
        "samples": len(latencies),
    }


def bench_ollama_chat(base: str, model: str) -> dict[str, Any]:
    url = f"{base.rstrip('/')}/api/chat"
    body = {
        "model": model,
        "messages": [{"role": "user", "content": "Reply with one word: ok"}],
        "stream": False,
        "options": {"num_predict": 8},
    }
    try:
        ms, resp = _post_json(url, body, timeout=180.0)
        content = resp.get("message", {}).get("content", "")[:40]
        return {"ok": True, "latency_ms": ms, "reply": content}
    except Exception as e:
        return {"ok": False, "error": str(e)}


def bench_green_chat(base: str) -> dict[str, Any]:
    url = f"{base.rstrip('/')}/v1/chat/completions"
    body = {
        "model": "green-local",
        "messages": [{"role": "user", "content": "Reply with one word: ok"}],
        "max_tokens": 8,
        "temperature": 0,
    }
    try:
        ms, resp = _post_json(url, body, timeout=180.0)
        content = resp.get("choices", [{}])[0].get("message", {}).get("content", "")[:40]
        return {"ok": True, "latency_ms": ms, "reply": content}
    except Exception as e:
        return {"ok": False, "error": str(e)}


def main() -> None:
    ap = argparse.ArgumentParser(description="Benchmark MCP embed/chat stack")
    ap.add_argument("--embed-url", default="http://127.0.0.1:8766")
    ap.add_argument("--chat-url", default="http://127.0.0.1:8767")
    ap.add_argument("--ollama", default="http://127.0.0.1:11434")
    ap.add_argument("--ollama-model", default="llama3.2:1b")
    ap.add_argument("--repeats", type=int, default=8)
    args = ap.parse_args()

    print("# MCP stack benchmark\n")
    embed = bench_embed(args.embed_url, args.repeats)
    if embed.get("ok"):
        print("## green-embed")
        print(f"- backend: {embed['backend']}")
        print(f"- p50: {embed['p50_ms']:.1f} ms ({embed['samples']} queries)")
        print(f"- p95: {embed['p95_ms']:.1f} ms")
        print(f"- cache repeat: {embed['cache_hit_ms']:.1f} ms")
        if embed.get("cache_stats"):
            print(f"- cache: {embed['cache_stats']}")
    else:
        print(f"## green-embed — SKIP ({embed.get('error')})")
        print("  start: ge embed serve --mcp")

    print()
    chat = bench_green_chat(args.chat_url)
    if chat.get("ok"):
        print("## green-chat")
        print(f"- short completion: {chat['latency_ms']:.0f} ms")
        print(f"- reply: {chat['reply']!r}")
    else:
        print(f"## green-chat — SKIP ({chat.get('error')})")
        print("  start: ge chat serve --mcp")

    print()
    ollama = bench_ollama_chat(args.ollama, args.ollama_model)
    if ollama.get("ok"):
        print("## ollama chat")
        print(f"- model: {args.ollama_model}")
        print(f"- short completion: {ollama['latency_ms']:.0f} ms")
        print(f"- reply: {ollama['reply']!r}")
    else:
        print(f"## ollama chat — SKIP ({ollama.get('error')})")

    print("\n---")
    print("Compare p50 embed + cache_hit; lower is better.")
    print("Re-run after: ge embed install && ge embed serve --mcp")


if __name__ == "__main__":
    main()
