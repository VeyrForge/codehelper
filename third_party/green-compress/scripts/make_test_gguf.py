#!/usr/bin/env python3
"""Create a minimal synthetic dense Llama GGUF for pack-model / forward smoke tests.

Dense-only (no MoE experts). Tiny dims suitable for native .green vertical slices.
"""
from __future__ import annotations

import argparse
import os

import numpy as np
from gguf import GGMLQuantizationType, GGUFWriter


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="scripts/fixtures/mini-test.gguf")
    ap.add_argument("--seed", type=int, default=0)
    a = ap.parse_args()
    os.makedirs(os.path.dirname(a.out) or ".", exist_ok=True)

    rng = np.random.default_rng(a.seed)
    n_vocab = 32
    n_embd = 64
    n_ff = 128
    n_head = 4
    n_head_kv = 4
    n_ctx = 128
    n_layer = 1
    f32 = GGMLQuantizationType.F32

    def rand(*shape: int) -> np.ndarray:
        return rng.standard_normal(shape, dtype=np.float32)

    w = GGUFWriter(a.out, "llama")
    w.add_uint32("llama.context_length", n_ctx)
    w.add_uint32("llama.embedding_length", n_embd)
    w.add_uint32("llama.block_count", n_layer)
    w.add_uint32("llama.feed_forward_length", n_ff)
    w.add_uint32("llama.attention.head_count", n_head)
    w.add_uint32("llama.attention.head_count_kv", n_head_kv)
    w.add_float32("llama.rope.freq_base", 10000.0)
    w.add_uint32("llama.rope.dimension_count", n_embd // n_head)

    tokens = ["<unk>", "<s>", "</s>"] + [f"t{i}" for i in range(3, n_vocab)]
    scores = [0.0] * n_vocab
    types = [0] * n_vocab
    w.add_tokenizer_model("llama")
    w.add_token_list(tokens)
    w.add_token_scores(scores)
    w.add_token_types(types)
    w.add_bos_token_id(1)
    w.add_eos_token_id(2)
    w.add_unk_token_id(0)

    # GGUFWriter stores row-major; shapes are (rows, cols) as written.
    w.add_tensor("token_embd.weight", rand(n_vocab, n_embd), raw_dtype=f32)
    w.add_tensor("output_norm.weight", rand(n_embd), raw_dtype=f32)
    w.add_tensor("output.weight", rand(n_vocab, n_embd), raw_dtype=f32)

    w.add_tensor("blk.0.attn_norm.weight", rand(n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_q.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_k.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_v.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_output.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.ffn_norm.weight", rand(n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.ffn_gate.weight", rand(n_ff, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.ffn_up.weight", rand(n_ff, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.ffn_down.weight", rand(n_embd, n_ff), raw_dtype=f32)

    w.write_header_to_file()
    w.write_kv_data_to_file()
    w.write_tensors_to_file()
    w.close()
    print(a.out)


if __name__ == "__main__":
    main()
