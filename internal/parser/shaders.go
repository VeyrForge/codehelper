package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Shader/material languages are core to every game engine (Unity, Unreal, Godot,
// and raw GL/Vulkan/Metal/WebGPU) but ship no go-tree-sitter grammar, so — like
// GDScript — we extract symbols with anchored line patterns. They are all
// C-family (HLSL, GLSL, Godot Shading Language, Metal/MSL, WGSL) plus Unity's
// ShaderLab wrapper, so one extractor covers them: struct/cbuffer/function/define
// declarations, uniforms/varyings, WGSL fn/var, and the ShaderLab `Shader "name"`.
// Goal is searchability (find the shader and its key symbols), not a full parse.
var shaderDecls = []struct {
	re   *regexp.Regexp
	kind types.SymbolKind
}{
	{regexp.MustCompile(`^\s*#\s*define\s+(\w+)`), types.SymbolKindVariable},
	{regexp.MustCompile(`^\s*struct\s+(\w+)`), types.SymbolKindClass},
	{regexp.MustCompile(`^\s*cbuffer\s+(\w+)`), types.SymbolKindClass},
	{regexp.MustCompile(`^\s*ConstantBuffer\s*<[^>]*>\s+(\w+)`), types.SymbolKindClass},
	{regexp.MustCompile(`^\s*Shader\s+"([^"]+)"`), types.SymbolKindClass},                              // ShaderLab
	{regexp.MustCompile(`^\s*fn\s+(\w+)`), types.SymbolKindFunction},                                   // WGSL
	{regexp.MustCompile(`^\s*(?:@\w+\([^)]*\)\s*)*var(?:<[^>]*>)?\s+(\w+)`), types.SymbolKindVariable}, // WGSL
	{regexp.MustCompile(`^\s*uniform\s+[\w<>,\s]+?\s+(\w+)`), types.SymbolKindVariable},
	{regexp.MustCompile(`^\s*varying\s+[\w<>,\s]+?\s+(\w+)`), types.SymbolKindVariable},
	// Generic C-family function definition: a type token, the name, a paren arg
	// list with no ';' (excludes calls/prototypes), optional HLSL `: SEMANTIC`,
	// optional opening brace. Anchored to the whole line to avoid matching calls.
	{regexp.MustCompile(`^\s*[A-Za-z_][\w\s\*&:<>,\.\[\]]*?\s+([A-Za-z_]\w*)\s*\([^;{}]*\)\s*(?::\s*[\w\[\]]+\s*)?(?:\{.*)?$`), types.SymbolKindFunction},
}

// shaderLang maps a shader file extension to a friendly language name.
func shaderLang(relPath string) string {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".shader":
		return "shaderlab"
	case ".gdshader", ".gdshaderinc":
		return "gdshader"
	case ".hlsl", ".hlsli", ".fx", ".fxh", ".cginc", ".compute", ".usf", ".ush":
		return "hlsl"
	case ".metal":
		return "metal"
	case ".wgsl":
		return "wgsl"
	default:
		return "glsl"
	}
}

// parseShaderLite extracts shader declarations by line across the shader languages.
func parseShaderLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	lang := shaderLang(relPath)
	line := 0
	for _, ln := range strings.Split(string(buf), "\n") {
		line++
		for _, d := range shaderDecls {
			m := d.re.FindStringSubmatch(ln)
			if m == nil || m[1] == "" {
				continue
			}
			sym := symbol(repoID, relPath, m[1], d.kind, line, line, lang, "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			break // first matching pattern wins; one declaration per line
		}
	}
	return out, nil
}
