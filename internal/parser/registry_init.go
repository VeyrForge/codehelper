package parser

import "context"

func init() {
	tsCaps := Capabilities{Symbols: true, Imports: true, Calls: true, Inheritance: false, LanguageName: "typescript"}
	RegisterExtractor([]string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}, tsCaps, ParseTypeScript)

	RegisterExtractor([]string{".py"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "python"}, ParsePython)
	RegisterExtractor([]string{".go"}, Capabilities{Symbols: true, Imports: false, Calls: true, LanguageName: "go"}, ParseGo)
	RegisterExtractor([]string{".rs"}, Capabilities{Symbols: true, Imports: false, Calls: true, LanguageName: "rust"}, ParseRust)
	RegisterExtractor([]string{".java"}, Capabilities{Symbols: true, Imports: false, Calls: true, LanguageName: "java"}, ParseJava)
	RegisterExtractor([]string{".cs"}, Capabilities{Symbols: true, Imports: false, Calls: true, Inheritance: true, LanguageName: "csharp"}, ParseCSharp)

	RegisterExtractor([]string{".c", ".h"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "c"}, ParseC)
	RegisterExtractor([]string{".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "cpp"}, ParseCpp)
	RegisterExtractor([]string{".php"}, Capabilities{Symbols: true, Imports: true, Calls: true, Inheritance: true, LanguageName: "php"}, ParsePHP)
	RegisterExtractor([]string{".rb"}, Capabilities{Symbols: true, Imports: true, Calls: true, Inheritance: true, LanguageName: "ruby"}, ParseRuby)
	RegisterExtractor([]string{".kt", ".kts"}, Capabilities{Symbols: true, Imports: false, Calls: true, Inheritance: true, LanguageName: "kotlin"}, ParseKotlin)
	RegisterExtractor([]string{".swift"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "swift"}, ParseSwift)
	RegisterExtractor([]string{".scala", ".sc"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "scala"}, ParseScala)
	RegisterExtractor([]string{".sh", ".bash"}, Capabilities{Symbols: true, SymbolLite: true, Calls: true, LanguageName: "bash"}, ParseBash)
	RegisterExtractor([]string{".ps1", ".psm1"}, Capabilities{Symbols: true, SymbolLite: true, Calls: true, LanguageName: "powershell"}, parsePowerShellLite)
	RegisterExtractor([]string{".lua"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "lua"}, ParseLua)
	RegisterExtractor([]string{".ex", ".exs"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "elixir"}, ParseElixir)
	// Clojure / Erlang / F# / R / Perl / OCaml / Haskell: no smacker/go-tree-sitter
	// grammars — regex lite only.
	RegisterExtractor([]string{".clj", ".cljs", ".cljc"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "clojure"}, parseClojureLite)
	RegisterExtractor([]string{".erl", ".hrl"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "erlang"}, parseErlangLite)
	RegisterExtractor([]string{".fs", ".fsi", ".fsx"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "fsharp"}, parseFSharpLite)
	RegisterExtractor([]string{".r"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "r"}, parseRLite)
	RegisterExtractor([]string{".pl", ".pm", ".t"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "perl"}, parsePerlLite)
	RegisterExtractor([]string{".ml", ".mli"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "ocaml"}, parseOCamlLite)
	RegisterExtractor([]string{".hs", ".lhs"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "haskell"}, parseHaskellLite)
	RegisterExtractor([]string{".tf", ".tfvars", ".hcl"}, Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "hcl"}, ParseHCL)
	RegisterExtractor([]string{".prisma"}, Capabilities{Symbols: true, SymbolLite: true, Calls: true, LanguageName: "prisma"}, ParsePrisma)
	RegisterExtractor([]string{".proto"}, Capabilities{Symbols: true, Imports: true, Calls: false, LanguageName: "protobuf"}, ParseProtobuf)
	RegisterExtractor([]string{".gd"}, Capabilities{Symbols: true, SymbolLite: true, Calls: true, LanguageName: "gdscript"}, parseGDScriptLite)
	// Shader/material languages across engines (Unity, Unreal, Godot) and raw
	// GL/Vulkan/Metal/WebGPU — one C-family lite extractor (see shaders.go).
	RegisterExtractor([]string{
		".hlsl", ".hlsli", ".fx", ".fxh", ".cginc", ".compute", ".usf", ".ush", // HLSL (Unity/Unreal)
		".shader",                   // Unity ShaderLab
		".gdshader", ".gdshaderinc", // Godot shading language
		".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese", // GLSL stages
		".rgen", ".rchit", ".rmiss", ".rahit", ".rint", ".rcall", // GLSL ray tracing
		".metal", // Metal Shading Language
		".wgsl",  // WebGPU Shading Language
	}, Capabilities{Symbols: true, SymbolLite: true, LanguageName: "shader"}, parseShaderLite)

	lite := Capabilities{Symbols: true, SymbolLite: true, LanguageName: "symbol_lite"}
	RegisterExtractor([]string{".sql"}, lite, func(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
		return parseSQLLite(ctx, repoID, relPath, buf)
	})
	// CSS/HTML use real tree-sitter extraction (selectors, ids, custom elements,
	// custom properties) rather than the regex "lite" pass — better for web/CMS work.
	RegisterExtractor([]string{".html", ".htm"}, Capabilities{Symbols: true, LanguageName: "html"}, ParseHTML)
	RegisterExtractor([]string{".css", ".scss"}, Capabilities{Symbols: true, LanguageName: "css"}, ParseCSS)
	RegisterExtractor([]string{".dart"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "dart"}, parseDartLite)
	RegisterExtractor([]string{".zig"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "zig"}, parseZigLite)
	RegisterExtractor([]string{".sol"}, Capabilities{Symbols: true, SymbolLite: true, Imports: true, Calls: true, LanguageName: "solidity"}, parseSolidityLite)
	sfcCaps := Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "svelte"}
	RegisterExtractor([]string{".svelte"}, sfcCaps, ParseSvelte)
	vueCaps := Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "vue"}
	RegisterExtractor([]string{".vue"}, vueCaps, ParseVue)
	astroCaps := Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "astro"}
	RegisterExtractor([]string{".astro"}, astroCaps, ParseAstro)
	mdxCaps := Capabilities{Symbols: true, Imports: true, Calls: true, LanguageName: "mdx"}
	RegisterExtractor([]string{".mdx"}, mdxCaps, ParseMDX)
}
