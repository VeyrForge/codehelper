#!/usr/bin/env python3
"""Create a minimal synthetic MoE GGUF for pack-model / PackageExpertStore smoke tests.

Tiny dims (not a real model). Per-expert 2D tensors so pack-model can emit a real
raw-f32 experts-000.greenpack. Dense attn/embd kept for a valid .green package.
"""
from __future__ import annotations

import argparse
import os

import numpy as np
from gguf import GGMLQuantizationType, GGUFWriter


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="scripts/fixtures/mini-moe-test.gguf")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--experts", type=int, default=2)
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
    n_expert = max(2, int(a.experts))
    n_expert_used = 2
    f32 = GGMLQuantizationType.F32

    def rand(*shape: int) -> np.ndarray:
        return rng.standard_normal(shape, dtype=np.float32)

    w = GGUFWriter(a.out, "qwen2moe")
    w.add_uint32("qwen2moe.context_length", n_ctx)
    w.add_uint32("qwen2moe.embedding_length", n_embd)
    w.add_uint32("qwen2moe.block_count", n_layer)
    w.add_uint32("qwen2moe.feed_forward_length", n_ff)
    w.add_uint32("qwen2moe.attention.head_count", n_head)
    w.add_uint32("qwen2moe.attention.head_count_kv", n_head_kv)
    w.add_uint32("qwen2moe.expert_count", n_expert)
    w.add_uint32("qwen2moe.expert_used_count", n_expert_used)
    w.add_float32("qwen2moe.rope.freq_base", 10000.0)
    w.add_uint32("qwen2moe.rope.dimension_count", n_embd // n_head)

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

    # Dense / shared tensors (ggml [out, in] for 2D mats).
    w.add_tensor("token_embd.weight", rand(n_vocab, n_embd), raw_dtype=f32)
    w.add_tensor("output_norm.weight", rand(n_embd), raw_dtype=f32)
    w.add_tensor("output.weight", rand(n_vocab, n_embd), raw_dtype=f32)

    w.add_tensor("blk.0.attn_norm.weight", rand(n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_q.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_k.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_v.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.attn_output.weight", rand(n_embd, n_embd), raw_dtype=f32)
    w.add_tensor("blk.0.ffn_norm.weight", rand(n_embd), raw_dtype=f32)
    # Router logits weight (dense): [n_expert, n_embd] ggml.
    w.add_tensor("blk.0.ffn_gate_inp.weight", rand(n_expert, n_embd), raw_dtype=f32)

    # Per-expert MoE FFN — names match pack-model expert_index / engine classify.
    # ggml shapes: gate/up [inter, hidden], down [hidden, inter].
    for e in range(n_expert):
        w.add_tensor(f"blk.0.ffn_gate_exps.{e}.weight", rand(n_ff, n_embd), raw_dtype=f32)
        w.add_tensor(f"blk.0.ffn_up_exps.{e}.weight", rand(n_ff, n_embd), raw_dtype=f32)
        w.add_tensor(f"blk.0.ffn_down_exps.{e}.weight", rand(n_embd, n_ff), raw_dtype=f32)

    w.write_header_to_file()
    w.write_kv_data_to_file()
    w.write_tensors_to_file()
    w.close()
    print(a.out)


if __name__ == "__main__":
    main()
