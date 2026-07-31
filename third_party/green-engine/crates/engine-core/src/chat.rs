//! Instruct chat templates for native generate (P1.4).
//!
//! Resolves a template from (in order):
//! 1. `tokenizer_config.json` `chat_template` in the package root
//! 2. GGUF `tokenizer.chat_template` (metadata.gguf / dense sidecar)
//! 3. Built-in family from model / template fingerprint (Llama-3 / ChatML)
//!
//! Full Jinja is not executed; Llama-3.2 Instruct / ChatML are rendered via
//! built-ins that match the non-tools path of the HF templates.

use std::fs;
use std::path::Path;

use crate::gguf_io::read_gguf;

/// One chat turn.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChatMessage {
    pub role: String,
    pub content: String,
}

impl ChatMessage {
    pub fn user(content: impl Into<String>) -> Self {
        ChatMessage {
            role: "user".into(),
            content: content.into(),
        }
    }

    pub fn assistant(content: impl Into<String>) -> Self {
        ChatMessage {
            role: "assistant".into(),
            content: content.into(),
        }
    }

    pub fn system(content: impl Into<String>) -> Self {
        ChatMessage {
            role: "system".into(),
            content: content.into(),
        }
    }
}

/// Known built-in families (and `Raw` when the prompt is already templated).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ChatTemplateFamily {
    Llama3,
    ChatMl,
    /// Leave text unchanged (completion / already marked).
    None,
}

/// How to treat a bare string prompt on generate.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ChatApplyMode {
    /// Instruct / templated packages → wrap; already marked → skip.
    Auto,
    Force,
    Off,
}

impl Default for ChatApplyMode {
    fn default() -> Self {
        ChatApplyMode::Auto
    }
}

/// True when the string already contains Llama-3 / ChatML special markers.
pub fn looks_templated(prompt: &str) -> bool {
    prompt.contains("<|start_header_id|>")
        || prompt.contains("<|im_start|>")
        || prompt.contains("<|eot_id|>")
}

/// Heuristic: package looks like an Instruct / chat model.
pub fn looks_instruct(model_name: &str, source: &str, template: Option<&str>) -> bool {
    let blob = format!("{model_name} {source}").to_ascii_lowercase();
    if blob.contains("instruct") || blob.contains("chat") {
        return true;
    }
    if let Some(t) = template {
        if t.contains("<|start_header_id|>") || t.contains("<|im_start|>") {
            return true;
        }
    }
    false
}

/// Detect family from a Jinja / marker string or model name.
pub fn detect_family(model_name: &str, template: Option<&str>) -> ChatTemplateFamily {
    if let Some(t) = template {
        if t.contains("<|start_header_id|>") || t.contains("<|eot_id|>") {
            return ChatTemplateFamily::Llama3;
        }
        if t.contains("<|im_start|>") || t.contains("<|im_end|>") {
            return ChatTemplateFamily::ChatMl;
        }
    }
    let n = model_name.to_ascii_lowercase();
    if n.contains("llama-3") || n.contains("llama3") || n.contains("llama-3.2") {
        return ChatTemplateFamily::Llama3;
    }
    if n.contains("qwen") || n.contains("chatml") {
        return ChatTemplateFamily::ChatMl;
    }
    ChatTemplateFamily::None
}

/// Load `chat_template` from package `tokenizer_config.json` if present.
pub fn load_tokenizer_config_template(package_root: &Path) -> Option<String> {
    let path = package_root.join("tokenizer_config.json");
    let raw = fs::read_to_string(path).ok()?;
    let v: serde_json::Value = serde_json::from_str(&raw).ok()?;
    v.get("chat_template")
        .and_then(|t| t.as_str())
        .map(|s| s.to_string())
}

/// Load `tokenizer.chat_template` from a GGUF (typically metadata.gguf).
pub fn load_gguf_chat_template(gguf: &Path) -> Option<String> {
    let g = read_gguf(gguf, false).ok()?;
    g.get("tokenizer.chat_template")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string())
}

/// Resolve template string from package root + optional metadata GGUF.
pub fn resolve_template_string(
    package_root: &Path,
    metadata_gguf: Option<&Path>,
) -> Option<String> {
    if let Some(t) = load_tokenizer_config_template(package_root) {
        return Some(t);
    }
    if let Some(meta) = metadata_gguf {
        if let Some(t) = load_gguf_chat_template(meta) {
            return Some(t);
        }
    }
    let dense = package_root.join("dense.gguf");
    if dense.is_file() {
        return load_gguf_chat_template(&dense);
    }
    None
}

/// Llama-3 / 3.2 Instruct built-in (non-tools path; BOS left to tokenizer `add_bos`).
pub fn render_llama3_instruct(messages: &[ChatMessage], add_generation_prompt: bool) -> String {
    let mut rest = messages;
    let system = if rest.first().map(|m| m.role == "system").unwrap_or(false) {
        let s = rest[0].content.trim().to_string();
        rest = &rest[1..];
        s
    } else {
        String::new()
    };
    let mut out = String::new();
    // Match HF Llama-3.2 Instruct headers. Omit static knowledge/today date lines —
    // they dominate greedy logits on 1B ("##"/month tokens).
    out.push_str("<|start_header_id|>system<|end_header_id|>\n\n");
    if !system.is_empty() {
        out.push_str(&system);
        if !system.ends_with('\n') {
            out.push('\n');
        }
    }
    out.push_str("<|eot_id|>");
    for m in rest {
        out.push_str("<|start_header_id|>");
        out.push_str(&m.role);
        out.push_str("<|end_header_id|>\n\n");
        out.push_str(m.content.trim());
        out.push_str("<|eot_id|>");
    }
    if add_generation_prompt {
        out.push_str("<|start_header_id|>assistant<|end_header_id|>\n\n");
    }
    out
}

/// ChatML built-in (`<|im_start|>role\n…<|im_end|>`).
pub fn render_chatml(messages: &[ChatMessage], add_generation_prompt: bool) -> String {
    let mut out = String::new();
    for m in messages {
        out.push_str("<|im_start|>");
        out.push_str(&m.role);
        out.push('\n');
        out.push_str(m.content.trim());
        out.push_str("<|im_end|>\n");
    }
    if add_generation_prompt {
        out.push_str("<|im_start|>assistant\n");
    }
    out
}

pub fn render_messages(
    family: ChatTemplateFamily,
    messages: &[ChatMessage],
    add_generation_prompt: bool,
) -> String {
    match family {
        ChatTemplateFamily::Llama3 => render_llama3_instruct(messages, add_generation_prompt),
        ChatTemplateFamily::ChatMl => render_chatml(messages, add_generation_prompt),
        ChatTemplateFamily::None => messages
            .iter()
            .map(|m| format!("{}: {}", m.role, m.content))
            .collect::<Vec<_>>()
            .join("\n"),
    }
}

/// Wrap a bare user prompt for Instruct generate, or return unchanged.
pub fn prepare_prompt(
    prompt: &str,
    mode: ChatApplyMode,
    family: ChatTemplateFamily,
) -> String {
    if mode == ChatApplyMode::Off {
        return prompt.to_string();
    }
    if looks_templated(prompt) {
        return prompt.to_string();
    }
    if mode == ChatApplyMode::Auto && family == ChatTemplateFamily::None {
        return prompt.to_string();
    }
    let family = if family == ChatTemplateFamily::None {
        ChatTemplateFamily::Llama3
    } else {
        family
    };
    render_messages(family, &[ChatMessage::user(prompt)], true)
}

/// Resolve family for a loaded package.
pub fn family_for_package(
    model_name: &str,
    source_model: &str,
    package_root: &Path,
    metadata_gguf: Option<&Path>,
) -> ChatTemplateFamily {
    let tmpl = resolve_template_string(package_root, metadata_gguf);
    let name_blob = if model_name.is_empty() {
        source_model
    } else {
        model_name
    };
    if !looks_instruct(name_blob, source_model, tmpl.as_deref())
        && tmpl.is_none()
    {
        // Still detect llama-3 from name even without "Instruct".
        return detect_family(name_blob, tmpl.as_deref());
    }
    detect_family(name_blob, tmpl.as_deref())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn llama3_wraps_user_and_assistant_header() {
        let s = render_llama3_instruct(&[ChatMessage::user("Hi")], true);
        assert!(s.contains("<|start_header_id|>user<|end_header_id|>"));
        assert!(s.contains("Hi<|eot_id|>"));
        assert!(s.ends_with("<|start_header_id|>assistant<|end_header_id|>\n\n"));
        assert!(s.contains("<|start_header_id|>system<|end_header_id|>"));
        assert!(!s.contains("Cutting Knowledge Date"));
        assert!(!s.contains("Today Date:"));
    }

    #[test]
    fn prepare_skips_already_templated() {
        let raw = "<|start_header_id|>user<|end_header_id|>\n\nHi<|eot_id|>";
        let out = prepare_prompt(raw, ChatApplyMode::Auto, ChatTemplateFamily::Llama3);
        assert_eq!(out, raw);
    }

    #[test]
    fn detect_llama3_from_template() {
        let t = "{% for m %}<|start_header_id|>{% endfor %}";
        assert_eq!(
            detect_family("x", Some(t)),
            ChatTemplateFamily::Llama3
        );
    }

    #[test]
    fn chatml_roundtrip_shape() {
        let s = render_chatml(&[ChatMessage::user("a")], true);
        assert!(s.contains("<|im_start|>user\na<|im_end|>"));
        assert!(s.ends_with("<|im_start|>assistant\n"));
    }
}
