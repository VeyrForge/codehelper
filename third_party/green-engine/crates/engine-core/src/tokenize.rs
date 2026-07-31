//! Tokenizer load from `.green` `metadata.gguf` KV / HF `tokenizer.json` / GGUF sidecar.
//!
//! Encoding:
//! - **llama-bpe** when `tokenizer.ggml.pre` is `llama-bpe` (Llama-3 / 3.2) — regex-ish
//!   pretokenize + `Ġ` space marker + merges
//! - **BPE** when merges are present (GPT-2 style whitespace split)
//! - **greedy longest-match** otherwise (synthetic / tiny SPM-like vocabs)
//!
//! Full SentencePiece proto decoding is intentionally out of scope.

use std::collections::HashMap;
use std::fs;
use std::path::Path;

use crate::gguf_io::{read_gguf, GgufFile, GgufValue};

/// Errors from tokenizer load / encode / decode.
#[derive(Debug)]
pub enum TokenizeError {
    Io(std::io::Error),
    MissingVocab,
    EmptyInput,
    UnknownToken(String),
    BadId(u32),
    Message(String),
}

impl std::fmt::Display for TokenizeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TokenizeError::Io(e) => write!(f, "{e}"),
            TokenizeError::MissingVocab => write!(f, "tokenizer vocab missing in GGUF / sidecar"),
            TokenizeError::EmptyInput => write!(f, "encode called with empty text"),
            TokenizeError::UnknownToken(t) => write!(f, "unknown token {t:?}"),
            TokenizeError::BadId(id) => write!(f, "token id {id} out of range"),
            TokenizeError::Message(m) => write!(f, "{m}"),
        }
    }
}

impl std::error::Error for TokenizeError {}

impl From<std::io::Error> for TokenizeError {
    fn from(value: std::io::Error) -> Self {
        TokenizeError::Io(value)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PreTokenizer {
    /// Llama-3 / 3.2 BPE (`tokenizer.ggml.pre = llama-bpe`).
    LlamaBpe,
    /// Whitespace-split GPT-2-ish.
    Whitespace,
    None,
}

/// Loaded tokenizer ready for a forward slice.
#[derive(Clone, Debug)]
pub struct Tokenizer {
    pub model: String,
    pub tokens: Vec<String>,
    pub scores: Vec<f32>,
    pub merges: Vec<(String, String)>,
    pub bos_id: Option<u32>,
    pub eos_id: Option<u32>,
    pub unk_id: Option<u32>,
    pub pad_id: Option<u32>,
    pub add_bos: bool,
    pub add_eos: bool,
    pre: PreTokenizer,
    token_to_id: HashMap<String, u32>,
    bpe_ranks: HashMap<(String, String), u32>,
}

impl Tokenizer {
    /// Load from `.green` package paths.
    ///
    /// Order: GGUF tokenizer sidecar → HF / pack `tokenizer.json` (merges optional) →
    /// `metadata.gguf`. When JSON lacks merges, falls through to metadata GGUF which
    /// usually carries the full `tokenizer.ggml.merges` list.
    pub fn load_from_green(
        metadata_gguf: Option<&Path>,
        tokenizer_sidecar: Option<&Path>,
    ) -> Result<Self, TokenizeError> {
        if let Some(side) = tokenizer_sidecar {
            if side.is_file() {
                let ext = side
                    .extension()
                    .and_then(|e| e.to_str())
                    .unwrap_or("")
                    .to_ascii_lowercase();
                if ext == "gguf" {
                    return Self::load_gguf(side);
                }
                if ext == "json" {
                    match Self::load_json(side) {
                        Ok(tok) if !tok.merges.is_empty() || tok.pre == PreTokenizer::None => {
                            return Ok(tok);
                        }
                        Ok(partial) => {
                            // Pack sidecars often omit merges — merge with metadata GGUF.
                            if let Some(meta) = metadata_gguf {
                                if meta.is_file() {
                                    let mut full = Self::load_gguf(meta)?;
                                    // Prefer JSON special-id overrides when present.
                                    if partial.bos_id.is_some() {
                                        full.bos_id = partial.bos_id;
                                    }
                                    if partial.eos_id.is_some() {
                                        full.eos_id = partial.eos_id;
                                    }
                                    if partial.unk_id.is_some() {
                                        full.unk_id = partial.unk_id;
                                    }
                                    return Ok(full);
                                }
                            }
                            return Ok(partial);
                        }
                        Err(_) => { /* fall through */ }
                    }
                }
            }
        }
        let meta = metadata_gguf.ok_or(TokenizeError::MissingVocab)?;
        Self::load_gguf(meta)
    }

    /// Load `tokenizer.ggml.*` from a GGUF (typically `metadata.gguf`).
    pub fn load_gguf(path: &Path) -> Result<Self, TokenizeError> {
        let g = read_gguf(path, false)?;
        Self::from_gguf(&g)
    }

    /// Load HuggingFace `tokenizer.json` or pack-model sidecar JSON.
    pub fn load_json(path: &Path) -> Result<Self, TokenizeError> {
        let raw = fs::read_to_string(path)?;
        let v: serde_json::Value = serde_json::from_str(&raw)
            .map_err(|e| TokenizeError::Message(format!("tokenizer.json: {e}")))?;
        Self::from_json_value(&v)
    }

    pub fn from_json_value(v: &serde_json::Value) -> Result<Self, TokenizeError> {
        // Pack-model embedded sidecar: { model, tokens, bos_token_id, ... } (no merges).
        if let Some(arr) = v.get("tokens").and_then(|t| t.as_array()) {
            let tokens: Vec<String> = arr
                .iter()
                .filter_map(|x| x.as_str().map(str::to_string))
                .collect();
            if tokens.is_empty() {
                return Err(TokenizeError::MissingVocab);
            }
            let model = v
                .get("model")
                .and_then(|m| m.as_str())
                .unwrap_or("unknown")
                .to_string();
            let pre = if model.contains("llama") || model == "gpt2" {
                PreTokenizer::LlamaBpe
            } else {
                PreTokenizer::None
            };
            return Ok(Self::from_parts_with_pre(
                model,
                tokens,
                vec![0.0; arr.len()],
                vec![],
                json_u32(v, "bos_token_id"),
                json_u32(v, "eos_token_id"),
                json_u32(v, "unk_token_id"),
                None,
                true,
                false,
                pre,
            ));
        }

        // HuggingFace tokenizers JSON: { model: { type, vocab, merges }, ... }
        let model_obj = v
            .get("model")
            .ok_or(TokenizeError::MissingVocab)?;
        let vocab = model_obj
            .get("vocab")
            .and_then(|x| x.as_object())
            .ok_or(TokenizeError::MissingVocab)?;
        let mut pairs: Vec<(u32, String)> = Vec::with_capacity(vocab.len());
        let mut max_id = 0u32;
        for (tok, idv) in vocab {
            let id = idv.as_u64().unwrap_or(0) as u32;
            max_id = max_id.max(id);
            pairs.push((id, tok.clone()));
        }
        let mut tokens = vec![String::new(); (max_id as usize) + 1];
        for (id, tok) in pairs {
            tokens[id as usize] = tok;
        }
        let merge_lines: Vec<String> = model_obj
            .get("merges")
            .and_then(|m| m.as_array())
            .map(|a| {
                a.iter()
                    .filter_map(|x| {
                        if let Some(s) = x.as_str() {
                            Some(s.to_string())
                        } else if let Some(arr) = x.as_array() {
                            // HF sometimes stores merges as ["a","b"]
                            let a = arr.first()?.as_str()?;
                            let b = arr.get(1)?.as_str()?;
                            Some(format!("{a} {b}"))
                        } else {
                            None
                        }
                    })
                    .collect()
            })
            .unwrap_or_default();
        let merges = parse_merges(&merge_lines);
        let model_type = model_obj
            .get("type")
            .and_then(|t| t.as_str())
            .unwrap_or("BPE")
            .to_string();
        let pre = detect_hf_pre(v);
        Ok(Self::from_parts_with_pre(
            model_type,
            tokens,
            vec![0.0; (max_id as usize) + 1],
            merges,
            json_u32(v, "bos_token_id").or_else(|| added_token_id(v, "bos_token")),
            json_u32(v, "eos_token_id").or_else(|| added_token_id(v, "eos_token")),
            json_u32(v, "unk_token_id").or_else(|| added_token_id(v, "unk_token")),
            json_u32(v, "pad_token_id"),
            v.get("add_bos_token").and_then(|b| b.as_bool()).unwrap_or(true),
            v.get("add_eos_token").and_then(|b| b.as_bool()).unwrap_or(false),
            pre,
        ))
    }

    pub fn from_gguf(g: &GgufFile) -> Result<Self, TokenizeError> {
        let tokens = g
            .get("tokenizer.ggml.tokens")
            .and_then(GgufValue::as_string_array)
            .ok_or(TokenizeError::MissingVocab)?;
        if tokens.is_empty() {
            return Err(TokenizeError::MissingVocab);
        }
        let model = g
            .get("tokenizer.ggml.model")
            .and_then(GgufValue::as_str)
            .unwrap_or("unknown")
            .to_string();
        let scores = g
            .get("tokenizer.ggml.scores")
            .and_then(GgufValue::as_f32_array)
            .unwrap_or_else(|| vec![0.0; tokens.len()]);
        let merge_lines = g
            .get("tokenizer.ggml.merges")
            .and_then(GgufValue::as_string_array)
            .unwrap_or_default();
        let merges = parse_merges(&merge_lines);
        let pre_str = g
            .get("tokenizer.ggml.pre")
            .and_then(GgufValue::as_str)
            .unwrap_or("");
        let pre = match pre_str {
            "llama-bpe" | "llama3" | "llama-3" => PreTokenizer::LlamaBpe,
            "" if !merges.is_empty() && model.contains("gpt") => PreTokenizer::Whitespace,
            "" if !merges.is_empty() => PreTokenizer::LlamaBpe, // safe default for modern GGUF BPE
            _ if !merges.is_empty() => PreTokenizer::Whitespace,
            _ => PreTokenizer::None,
        };
        // Llama-3 GGUF often omits add_bos; default true matches llama.cpp for llama-bpe.
        let add_bos = kv_bool(g, "tokenizer.ggml.add_bos_token").unwrap_or(true);
        let add_eos = kv_bool(g, "tokenizer.ggml.add_eos_token").unwrap_or(false);
        Ok(Self::from_parts_with_pre(
            model,
            tokens,
            scores,
            merges,
            kv_u32(g, "tokenizer.ggml.bos_token_id"),
            kv_u32(g, "tokenizer.ggml.eos_token_id"),
            kv_u32(g, "tokenizer.ggml.unknown_token_id"),
            kv_u32(g, "tokenizer.ggml.padding_token_id"),
            add_bos,
            add_eos,
            pre,
        ))
    }

    /// Build from explicit parts (tests / loader hooks).
    pub fn from_parts(
        model: String,
        tokens: Vec<String>,
        scores: Vec<f32>,
        merges: Vec<(String, String)>,
        bos_id: Option<u32>,
        eos_id: Option<u32>,
        unk_id: Option<u32>,
        pad_id: Option<u32>,
        add_bos: bool,
        add_eos: bool,
    ) -> Self {
        let pre = if !merges.is_empty() {
            PreTokenizer::Whitespace
        } else {
            PreTokenizer::None
        };
        Self::from_parts_with_pre(
            model, tokens, scores, merges, bos_id, eos_id, unk_id, pad_id, add_bos, add_eos, pre,
        )
    }

    fn from_parts_with_pre(
        model: String,
        tokens: Vec<String>,
        scores: Vec<f32>,
        merges: Vec<(String, String)>,
        bos_id: Option<u32>,
        eos_id: Option<u32>,
        unk_id: Option<u32>,
        pad_id: Option<u32>,
        add_bos: bool,
        add_eos: bool,
        pre: PreTokenizer,
    ) -> Self {
        let mut token_to_id = HashMap::with_capacity(tokens.len());
        for (i, t) in tokens.iter().enumerate() {
            token_to_id.entry(t.clone()).or_insert(i as u32);
        }
        let mut bpe_ranks = HashMap::with_capacity(merges.len());
        for (i, pair) in merges.iter().enumerate() {
            bpe_ranks.insert(pair.clone(), i as u32);
        }
        Tokenizer {
            model,
            tokens,
            scores,
            merges,
            bos_id,
            eos_id,
            unk_id,
            pad_id,
            add_bos,
            add_eos,
            pre,
            token_to_id,
            bpe_ranks,
        }
    }

    pub fn vocab_size(&self) -> usize {
        self.tokens.len()
    }

    /// Look up a vocab string (exact match, including special tokens).
    pub fn lookup_token(&self, token: &str) -> Option<u32> {
        self.token_to_id.get(token).copied()
    }

    /// Encode text → token ids (optional BOS/EOS per GGUF flags).
    pub fn encode(&self, text: &str) -> Result<Vec<u32>, TokenizeError> {
        if text.is_empty() {
            return Err(TokenizeError::EmptyInput);
        }
        // Prefer whole special tokens (e.g. <|begin_of_text|>) when present as substrings.
        let mut ids = if !self.merges.is_empty() {
            match self.pre {
                PreTokenizer::LlamaBpe => self.encode_llama_bpe(text)?,
                PreTokenizer::Whitespace | PreTokenizer::None => self.encode_bpe_whitespace(text)?,
            }
        } else {
            self.encode_greedy(text)?
        };
        if self.add_bos {
            if let Some(bos) = self.bos_id {
                if ids.first().copied() != Some(bos) {
                    ids.insert(0, bos);
                }
            }
        }
        if self.add_eos {
            if let Some(eos) = self.eos_id {
                ids.push(eos);
            }
        }
        Ok(ids)
    }

    /// Decode token ids → string (undo GPT-2 byte-level map: `Ġ`→space, `Ċ`→newline, …).
    pub fn decode(&self, ids: &[u32]) -> Result<String, TokenizeError> {
        let mut out = String::new();
        for &id in ids {
            let t = self
                .tokens
                .get(id as usize)
                .ok_or(TokenizeError::BadId(id))?;
            if t.starts_with("<|") && t.ends_with("|>") {
                continue;
            }
            out.push_str(&byte_unicode_to_utf8(t));
        }
        Ok(out)
    }

    fn encode_greedy(&self, text: &str) -> Result<Vec<u32>, TokenizeError> {
        let bytes = text.as_bytes();
        let mut i = 0;
        let mut ids = Vec::new();
        while i < bytes.len() {
            let mut matched = None;
            for len in (1..=bytes.len() - i).rev() {
                if let Ok(s) = std::str::from_utf8(&bytes[i..i + len]) {
                    if let Some(&id) = self.token_to_id.get(s) {
                        matched = Some((len, id));
                        break;
                    }
                }
            }
            match matched {
                Some((len, id)) => {
                    ids.push(id);
                    i += len;
                }
                None => {
                    let ch = text[i..]
                        .chars()
                        .next()
                        .map(|c| c.to_string())
                        .unwrap_or_else(|| "?".into());
                    if let Some(unk) = self.unk_id {
                        ids.push(unk);
                        i += ch.len();
                    } else {
                        return Err(TokenizeError::UnknownToken(ch));
                    }
                }
            }
        }
        Ok(ids)
    }

    fn encode_bpe_whitespace(&self, text: &str) -> Result<Vec<u32>, TokenizeError> {
        let mut ids = Vec::new();
        for (wi, word) in split_words(text).into_iter().enumerate() {
            let piece = if wi > 0 && !self.token_to_id.contains_key(word) {
                let spaced = format!(" {word}");
                if self.token_to_id.contains_key(&spaced) {
                    spaced
                } else {
                    let g = format!("Ġ{word}");
                    if self.token_to_id.contains_key(&g) {
                        g
                    } else {
                        word.to_string()
                    }
                }
            } else {
                word.to_string()
            };
            ids.extend(self.bpe_word(&piece)?);
        }
        Ok(ids)
    }

    /// Llama-3 BPE: regex-ish split, GPT-2 byte-level map, then merges.
    ///
    /// Special tokens (`<|…|>`) are always atomic: never swallowed by the
    /// punctuation-piece regex (which would otherwise eat `<|eot_id|>` after `sentence.`).
    fn encode_llama_bpe(&self, text: &str) -> Result<Vec<u32>, TokenizeError> {
        let mut ids = Vec::new();
        let mut rest = text;
        while !rest.is_empty() {
            if let Some((len, id)) = self.match_special_prefix(rest) {
                ids.push(id);
                rest = &rest[len..];
                continue;
            }
            // Cap the next BPE window so pieces cannot cross a special token.
            let window = if let Some(at) = self.find_special_token_start(rest) {
                &rest[..at]
            } else {
                rest
            };
            if window.is_empty() {
                // Should be unreachable (special at 0 handled above); skip one char.
                let ch = rest.chars().next().unwrap();
                rest = &rest[ch.len_utf8()..];
                continue;
            }
            let (piece, advance) = next_llama_bpe_piece(window);
            if piece.is_empty() || advance == 0 {
                let ch = rest.chars().next().unwrap();
                rest = &rest[ch.len_utf8()..];
                continue;
            }
            rest = &rest[advance..];
            let marked = utf8_to_byte_unicode(&piece);
            ids.extend(self.bpe_word(&marked)?);
        }
        Ok(ids)
    }

    /// Byte offset of the next `<|…|>` special token in `text`, if any.
    fn find_special_token_start(&self, text: &str) -> Option<usize> {
        let mut base = 0usize;
        let mut search = text;
        while let Some(rel) = search.find("<|") {
            let abs = base + rel;
            if self.match_special_prefix(&text[abs..]).is_some() {
                return Some(abs);
            }
            base = abs + 2;
            search = &text[base..];
        }
        None
    }

    fn match_special_prefix(&self, text: &str) -> Option<(usize, u32)> {
        // Longest special token that is a prefix of `text`.
        let mut best: Option<(usize, u32)> = None;
        for (tok, &id) in &self.token_to_id {
            if tok.starts_with("<|") && tok.ends_with("|>") && text.starts_with(tok.as_str()) {
                let len = tok.len();
                if best.map(|(l, _)| len > l).unwrap_or(true) {
                    best = Some((len, id));
                }
            }
        }
        best
    }

    fn bpe_word(&self, word: &str) -> Result<Vec<u32>, TokenizeError> {
        if word.is_empty() {
            return Ok(vec![]);
        }
        if let Some(&id) = self.token_to_id.get(word) {
            return Ok(vec![id]);
        }
        let mut symbols: Vec<String> = word.chars().map(|c| c.to_string()).collect();
        if symbols.is_empty() {
            return Ok(vec![]);
        }
        loop {
            let mut best: Option<(usize, u32)> = None;
            for i in 0..symbols.len().saturating_sub(1) {
                let pair = (symbols[i].clone(), symbols[i + 1].clone());
                if let Some(&rank) = self.bpe_ranks.get(&pair) {
                    if best.map(|(_, r)| rank < r).unwrap_or(true) {
                        best = Some((i, rank));
                    }
                }
            }
            let Some((i, _)) = best else { break };
            let merged = format!("{}{}", symbols[i], symbols[i + 1]);
            symbols[i] = merged;
            symbols.remove(i + 1);
        }
        let mut ids = Vec::with_capacity(symbols.len());
        for s in symbols {
            match self.token_to_id.get(&s) {
                Some(&id) => ids.push(id),
                None => {
                    if let Some(unk) = self.unk_id {
                        ids.push(unk);
                    } else {
                        return Err(TokenizeError::UnknownToken(s));
                    }
                }
            }
        }
        Ok(ids)
    }
}

/// Approximate Llama-3 pretokenizer piece starting at `text`.
///
/// Pattern intent (HF / llama.cpp llama-bpe):
/// `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`
fn next_llama_bpe_piece(text: &str) -> (String, usize) {
    let bytes = text.as_bytes();
    if bytes.is_empty() {
        return (String::new(), 0);
    }

    // Contractions suffixes.
    let lower = text.to_ascii_lowercase();
    for suf in ["'s", "'t", "'re", "'ve", "'m", "'ll", "'d"] {
        if lower.starts_with(suf) {
            let len = suf.len();
            return (text[..len].to_string(), len);
        }
    }

    let mut chars = text.char_indices().peekable();
    let Some((0, c0)) = chars.next() else {
        return (String::new(), 0);
    };

    // Newlines / whitespace runs.
    if c0 == '\n' || c0 == '\r' {
        let mut end = c0.len_utf8();
        while let Some(&(i, c)) = chars.peek() {
            if c == '\n' || c == '\r' || c.is_whitespace() {
                end = i + c.len_utf8();
                chars.next();
            } else {
                break;
            }
        }
        return (text[..end].to_string(), end);
    }
    if c0.is_whitespace() {
        let mut end = c0.len_utf8();
        while let Some(&(i, c)) = chars.peek() {
            if c.is_whitespace() && c != '\n' && c != '\r' {
                end = i + c.len_utf8();
                chars.next();
            } else {
                break;
            }
        }
        // Prefer single leading space attached to following word when possible —
        // handled by falling through only when next is not letter; otherwise return space run.
        if let Some(&(_, n)) = chars.peek() {
            if n.is_alphabetic() || is_punct_like(n) {
                // one space + continue in caller? HF keeps " word" as one piece.
                // Consume optional single space already in end; if end is one space, attach letters.
                if end == c0.len_utf8() {
                    let mut end2 = end;
                    if is_punct_like(n) && !n.is_alphabetic() {
                        // " ?[^\s\p{L}\p{N}]+"
                        while let Some(&(i, c)) = chars.peek() {
                            if !c.is_whitespace() && !c.is_alphabetic() && !c.is_numeric() {
                                end2 = i + c.len_utf8();
                                chars.next();
                            } else {
                                break;
                            }
                        }
                        while let Some(&(i, c)) = chars.peek() {
                            if c == '\n' || c == '\r' {
                                end2 = i + c.len_utf8();
                                chars.next();
                            } else {
                                break;
                            }
                        }
                        return (text[..end2].to_string(), end2);
                    }
                    // space + letters
                    while let Some(&(i, c)) = chars.peek() {
                        if c.is_alphabetic() {
                            end2 = i + c.len_utf8();
                            chars.next();
                        } else {
                            break;
                        }
                    }
                    return (text[..end2].to_string(), end2);
                }
            }
        }
        return (text[..end].to_string(), end);
    }

    // Optional non-alnum prefix + letters: [^\r\n\p{L}\p{N}]?\p{L}+
    if !c0.is_alphabetic() && !c0.is_numeric() && c0 != '\n' && c0 != '\r' {
        if let Some(&(_, n)) = chars.peek() {
            if n.is_alphabetic() {
                let mut end = c0.len_utf8();
                while let Some(&(i, c)) = chars.peek() {
                    if c.is_alphabetic() {
                        end = i + c.len_utf8();
                        chars.next();
                    } else {
                        break;
                    }
                }
                return (text[..end].to_string(), end);
            }
        }
        // Punctuation cluster: ?[^\s\p{L}\p{N}]+
        let mut end = c0.len_utf8();
        while let Some(&(i, c)) = chars.peek() {
            if !c.is_whitespace() && !c.is_alphabetic() && !c.is_numeric() {
                end = i + c.len_utf8();
                chars.next();
            } else {
                break;
            }
        }
        while let Some(&(i, c)) = chars.peek() {
            if c == '\n' || c == '\r' {
                end = i + c.len_utf8();
                chars.next();
            } else {
                break;
            }
        }
        return (text[..end].to_string(), end);
    }

    // Letters: \p{L}+
    if c0.is_alphabetic() {
        let mut end = c0.len_utf8();
        while let Some(&(i, c)) = chars.peek() {
            if c.is_alphabetic() {
                end = i + c.len_utf8();
                chars.next();
            } else {
                break;
            }
        }
        return (text[..end].to_string(), end);
    }

    // Digits: \p{N}{1,3}
    if c0.is_numeric() {
        let mut end = c0.len_utf8();
        let mut count = 1usize;
        while count < 3 {
            if let Some(&(i, c)) = chars.peek() {
                if c.is_numeric() {
                    end = i + c.len_utf8();
                    chars.next();
                    count += 1;
                    continue;
                }
            }
            break;
        }
        return (text[..end].to_string(), end);
    }

    // Fallback: one char.
    let len = c0.len_utf8();
    (text[..len].to_string(), len)
}

fn is_punct_like(c: char) -> bool {
    !c.is_whitespace() && !c.is_alphabetic() && !c.is_numeric()
}

/// GPT-2 / Llama-3 byte↔unicode map (printable stand-ins for raw bytes).
fn bytes_to_unicode_table() -> &'static [char; 256] {
    use std::sync::OnceLock;
    static TABLE: OnceLock<[char; 256]> = OnceLock::new();
    TABLE.get_or_init(|| {
        let mut table = ['\0'; 256];
        let mut cs: Vec<u32> = Vec::with_capacity(256);
        for b in (b'!'..=b'~').chain(0xA1..=0xAC).chain(0xAE..=0xFF) {
            cs.push(b as u32);
        }
        let mut n = 0u32;
        let mut bs: Vec<u8> = cs.iter().map(|&c| c as u8).collect();
        for b in 0u8..=255 {
            if !bs.contains(&b) {
                bs.push(b);
                cs.push(256 + n);
                n += 1;
            }
        }
        for (i, &b) in bs.iter().enumerate() {
            table[b as usize] = char::from_u32(cs[i]).unwrap_or('\u{FFFD}');
        }
        table
    })
}

fn utf8_to_byte_unicode(s: &str) -> String {
    let table = bytes_to_unicode_table();
    s.as_bytes().iter().map(|&b| table[b as usize]).collect()
}

fn byte_unicode_to_utf8(s: &str) -> String {
    let table = bytes_to_unicode_table();
    let mut inv = std::collections::HashMap::with_capacity(256);
    for (b, ch) in table.iter().enumerate() {
        inv.insert(*ch, b as u8);
    }
    let mut bytes = Vec::with_capacity(s.len());
    for ch in s.chars() {
        if ch == '▁' {
            bytes.push(b' ');
            continue;
        }
        if let Some(&b) = inv.get(&ch) {
            bytes.push(b);
        } else {
            let mut buf = [0u8; 4];
            bytes.extend_from_slice(ch.encode_utf8(&mut buf).as_bytes());
        }
    }
    String::from_utf8_lossy(&bytes).into_owned()
}

fn kv_u32(g: &GgufFile, key: &str) -> Option<u32> {
    g.get(key).and_then(GgufValue::as_u64).map(|v| v as u32)
}

fn kv_bool(g: &GgufFile, key: &str) -> Option<bool> {
    match g.get(key)? {
        GgufValue::Bool(b) => Some(*b),
        other => other.as_u64().map(|v| v != 0),
    }
}

fn json_u32(v: &serde_json::Value, key: &str) -> Option<u32> {
    v.get(key).and_then(|x| x.as_u64()).map(|n| n as u32)
}

fn added_token_id(v: &serde_json::Value, key: &str) -> Option<u32> {
    let name = v.get(key)?.as_str()?;
    let added = v.get("added_tokens")?.as_array()?;
    for t in added {
        if t.get("content").and_then(|c| c.as_str()) == Some(name) {
            return t.get("id").and_then(|i| i.as_u64()).map(|n| n as u32);
        }
    }
    None
}

fn detect_hf_pre(v: &serde_json::Value) -> PreTokenizer {
    // Look for Split pretokenizer with Llama-3-ish regex, else default llama-bpe for BPE.
    if let Some(pre) = v.get("pre_tokenizer") {
        let s = pre.to_string();
        if s.contains("\\\\p{L}") || s.contains("'s|'t|'re") || s.contains("llama") {
            return PreTokenizer::LlamaBpe;
        }
    }
    PreTokenizer::LlamaBpe
}

fn parse_merges(lines: &[String]) -> Vec<(String, String)> {
    let mut out = Vec::with_capacity(lines.len());
    for line in lines {
        let mut parts = line.split_whitespace();
        let Some(a) = parts.next() else { continue };
        let Some(b) = parts.next() else { continue };
        out.push((a.to_string(), b.to_string()));
    }
    out
}

fn split_words(text: &str) -> Vec<&str> {
    text.split_whitespace().collect()
}

/// Hook the loader can call once paths are known from [`green_format::ModelMetadata`].
pub fn load_for_package(
    metadata_gguf: Option<&Path>,
    tokenizer_sidecar: Option<&Path>,
) -> Result<Tokenizer, TokenizeError> {
    Tokenizer::load_from_green(metadata_gguf, tokenizer_sidecar)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gguf_io::{write_test_gguf, TestKv};
    use tempfile::TempDir;

    #[test]
    fn greedy_encode_decode_roundtrip() {
        let tok = Tokenizer::from_parts(
            "llama".into(),
            vec!["<s>".into(), "hel".into(), "lo".into(), "world".into()],
            vec![0.0; 4],
            vec![],
            Some(0),
            None,
            None,
            None,
            true,
            false,
        );
        let ids = tok.encode("helloworld").unwrap();
        assert_eq!(ids, vec![0, 1, 2, 3]);
        assert_eq!(tok.decode(&ids[1..]).unwrap(), "helloworld");
    }

    #[test]
    fn bpe_merges_apply() {
        let tok = Tokenizer::from_parts(
            "gpt2".into(),
            vec!["a".into(), "b".into(), "c".into(), "ab".into(), "abc".into()],
            vec![0.0; 5],
            vec![("a".into(), "b".into()), ("ab".into(), "c".into())],
            None,
            None,
            None,
            None,
            false,
            false,
        );
        assert_eq!(tok.encode("abc").unwrap(), vec![4]);
    }

    #[test]
    fn load_from_metadata_gguf() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("metadata.gguf");
        write_test_gguf(
            &path,
            &[
                ("tokenizer.ggml.model", TestKv::String("llama")),
                (
                    "tokenizer.ggml.tokens",
                    TestKv::StringArray(&["<s>", "hi", "!"]),
                ),
                ("tokenizer.ggml.scores", TestKv::F32Array(&[0.0, 0.0, 0.0])),
                ("tokenizer.ggml.bos_token_id", TestKv::U32(0)),
                ("tokenizer.ggml.add_bos_token", TestKv::Bool(true)),
                ("tokenizer.ggml.add_eos_token", TestKv::Bool(false)),
            ],
            &[],
        )
        .unwrap();
        let tok = Tokenizer::load_gguf(&path).unwrap();
        assert_eq!(tok.vocab_size(), 3);
        assert_eq!(tok.encode("hi!").unwrap(), vec![0, 1, 2]);
    }

    #[test]
    fn decode_maps_g_space() {
        let tok = Tokenizer::from_parts_with_pre(
            "gpt2".into(),
            vec!["Hello".into(), "Ġworld".into()],
            vec![0.0; 2],
            vec![],
            None,
            None,
            None,
            None,
            false,
            false,
            PreTokenizer::LlamaBpe,
        );
        assert_eq!(tok.decode(&[0, 1]).unwrap(), "Hello world");
    }

    #[test]
    fn llama_bpe_piece_splits_words() {
        let (p, n) = next_llama_bpe_piece("Hello world");
        assert_eq!(&p, "Hello");
        assert_eq!(n, 5);
        let (p2, _) = next_llama_bpe_piece(" world");
        assert_eq!(&p2, " world");
    }

    #[test]
    fn llama_bpe_does_not_swallow_special_after_punct() {
        let tok = Tokenizer::from_parts_with_pre(
            "llama".into(),
            vec![
                "<|eot_id|>".into(),
                "<|start_header_id|>".into(),
                ".".into(),
                "sentence".into(),
            ],
            vec![0.0; 4],
            vec![],
            None,
            None,
            None,
            None,
            false,
            false,
            PreTokenizer::LlamaBpe,
        );
        let ids = tok
            .encode("sentence.<|eot_id|><|start_header_id|>")
            .unwrap();
        assert_eq!(ids, vec![3, 2, 0, 1], "got {ids:?}");
    }
}
