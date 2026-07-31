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
// declarations, uniforms/varyings, WGSL fn/var, the ShaderLab `Shader "name"`,
// `#include` imports, and heuristic call edges inside function bodies.
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

var (
	shaderIncludeRe = regexp.MustCompile(`^\s*#\s*include\s*[<"']([^>"']+)[>"']`)
	shaderCallRe    = regexp.MustCompile(`\b([A-Za-z_][\w]*)\s*\(`)
)

// shaderCallSkip filters keywords, type constructors, and ubiquitous builtins so
// call edges stay useful (user helpers like tonemap / SampleAlbedo).
var shaderCallSkip = map[string]struct{}{
	"if": {}, "for": {}, "while": {}, "switch": {}, "return": {}, "discard": {},
	"else": {}, "do": {}, "case": {}, "default": {}, "break": {}, "continue": {},
	"struct": {}, "cbuffer": {}, "layout": {}, "in": {}, "out": {}, "inout": {},
	"uniform": {}, "varying": {}, "const": {}, "void": {}, "precision": {},
	"highp": {}, "mediump": {}, "lowp": {}, "fn": {}, "var": {}, "let": {},
	"true": {}, "false": {}, "nullptr": {}, "NULL": {},
	"float": {}, "int": {}, "uint": {}, "bool": {}, "half": {}, "double": {},
	"vec2": {}, "vec3": {}, "vec4": {}, "ivec2": {}, "ivec3": {}, "ivec4": {},
	"uvec2": {}, "uvec3": {}, "uvec4": {}, "bvec2": {}, "bvec3": {}, "bvec4": {},
	"mat2": {}, "mat3": {}, "mat4": {}, "mat2x2": {}, "mat3x3": {}, "mat4x4": {},
	"float2": {}, "float3": {}, "float4": {}, "int2": {}, "int3": {}, "int4": {},
	"uint2": {}, "uint3": {}, "uint4": {}, "half2": {}, "half3": {}, "half4": {},
	"float2x2": {}, "float3x3": {}, "float4x4": {},
	"sampler2D": {}, "samplerCube": {}, "texture": {}, "texture2D": {}, "texelFetch": {},
	"lerp": {}, "mix": {}, "saturate": {}, "mul": {}, "dot": {}, "cross": {},
	"normalize": {}, "clamp": {}, "step": {}, "smoothstep": {}, "max": {}, "min": {},
	"abs": {}, "sin": {}, "cos": {}, "pow": {}, "sqrt": {}, "fract": {}, "floor": {},
	"ceil": {}, "length": {}, "distance": {}, "reflect": {}, "refract": {},
	"atan": {}, "atan2": {}, "exp": {}, "log": {}, "fwidth": {}, "ddx": {}, "ddy": {},
	"Sample": {}, "Load": {}, "GetDimensions": {},
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

// parseShaderLite extracts shader declarations, #include imports, and call edges.
func parseShaderLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	lang := shaderLang(relPath)
	fid := FileNodeID(repoID, relPath)
	line := 0
	var currentFuncID string
	braceDepth := 0

	for _, ln := range strings.Split(string(buf), "\n") {
		line++

		if m := shaderIncludeRe.FindStringSubmatch(ln); len(m) > 1 {
			path := strings.TrimSpace(m[1])
			if path != "" {
				out.Imports = append(out.Imports, path)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, path), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, path),
					Confidence: 0.85,
				})
			}
			continue
		}

		declMatched := false
		for _, d := range shaderDecls {
			m := d.re.FindStringSubmatch(ln)
			if m == nil || m[1] == "" {
				continue
			}
			sym := symbol(repoID, relPath, m[1], d.kind, line, line, lang, "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			declMatched = true
			if d.kind == types.SymbolKindFunction {
				currentFuncID = sym.ID
				braceDepth = shaderBraceDelta(ln)
				if idx := strings.Index(ln, "{"); idx >= 0 {
					emitShaderCalls(repoID, relPath, currentFuncID, ln[idx+1:], out)
				}
				if braceDepth <= 0 || !strings.Contains(ln, "{") {
					currentFuncID = ""
					braceDepth = 0
				}
			}
			break // first matching pattern wins; one declaration per line
		}
		if declMatched {
			continue
		}

		if currentFuncID == "" {
			continue
		}
		trim := strings.TrimSpace(ln)
		if trim != "" && !strings.HasPrefix(trim, "//") && !strings.HasPrefix(trim, "#") {
			emitShaderCalls(repoID, relPath, currentFuncID, ln, out)
		}
		braceDepth += shaderBraceDelta(ln)
		if braceDepth <= 0 {
			currentFuncID = ""
			braceDepth = 0
		}
	}
	return out, nil
}

func emitShaderCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range shaderCallRe.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := shaderCallSkip[name]; skip {
			continue
		}
		if !isCallableName(name) {
			continue
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + name
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.5,
		})
	}
}

// shaderBraceDelta returns net { minus } on a line, ignoring string literals.
func shaderBraceDelta(s string) int {
	n := 0
	inStr := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == quote {
				inStr = false
			}
			continue
		}
		if c == '"' || c == '\'' {
			inStr = true
			quote = c
			continue
		}
		switch c {
		case '{':
			n++
		case '}':
			n--
		}
	}
	return n
}
