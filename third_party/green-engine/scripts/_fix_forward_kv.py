#!/usr/bin/env python3
from pathlib import Path

FWD = Path(__file__).resolve().parents[1] / "crates/engine-core/src/forward.rs"
text = FWD.read_text(encoding="utf-8")

text = text.replace(
    "        let mut token_pos = 0usize;\n"
    "        for &id in prompt_ids {\n"
    "            self.embed_token(id, &mut scratch.hidden)?;\n"
    "            self.forward_token(token_pos, kv, &mut scratch)?;",
    "        let mut token_pos = 0usize;\n"
    "        for &id in prompt_ids {\n"
    "            self.embed_token(id, &mut scratch.hidden)?;\n"
    "            self.forward_token(token_pos, &mut kv, &mut scratch)?;",
    1,
)

text = text.replace(
    "        Ok(GreedyGenerateOut {\n"
    "            new_tokens: generated,\n"
    "            kv_metrics: kv.metrics(),\n"
    "            kv_seq_len: kv.seq_len(),\n"
    "            kv_hot_cap: kv.hot_cap(),\n"
    "            kv_hot_bytes: kv.hot_bytes(),",
    "        Ok(GreedyGenerateOut {\n"
    "            new_tokens: generated,\n"
    "            kv_metrics: session.kv.metrics(),\n"
    "            kv_seq_len: session.kv.seq_len(),\n"
    "            kv_hot_cap: session.kv.hot_cap(),\n"
    "            kv_hot_bytes: session.kv.hot_bytes(),",
    1,
)

FWD.write_bytes(text.encode("utf-8"))
print("forward.rs: fixed prefill kv + session metrics")
