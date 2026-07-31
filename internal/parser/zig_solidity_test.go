package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseZigLiteKindsImportsCalls(t *testing.T) {
	src := []byte("const helpers = @import(\"helpers.zig\");\r\n" +
		"\r\n" +
		"pub const NameSlice = []const u8;\r\n" +
		"\r\n" +
		"pub const Tone = enum {\r\n" +
		"    casual,\r\n" +
		"    formal,\r\n" +
		"};\r\n" +
		"\r\n" +
		"pub const Greeter = struct {\r\n" +
		"    tone: Tone,\r\n" +
		"\r\n" +
		"    pub fn greet(self: Greeter, name: []const u8) []const u8 {\r\n" +
		"        _ = self;\r\n" +
		"        return format(name);\r\n" +
		"    }\r\n" +
		"\r\n" +
		"    pub fn shout(self: Greeter, name: []const u8) []const u8 {\r\n" +
		"        _ = self;\r\n" +
		"        return helpers.upper(format(name));\r\n" +
		"    }\r\n" +
		"\r\n" +
		"    pub const Stats = struct {\r\n" +
		"        count: usize,\r\n" +
		"    };\r\n" +
		"};\r\n" +
		"\r\n" +
		"pub fn format(s: []const u8) []const u8 {\r\n" +
		"    return s;\r\n" +
		"}\r\n")
	res, err := parseZigLite(context.Background(), "repo", "src/greeter.zig", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	parents := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		parents[s.Name] = s.ParentID
	}
	if found["Greeter"] != types.SymbolKindClass {
		t.Errorf("Greeter kind=%q", found["Greeter"])
	}
	if found["Tone"] != types.SymbolKindEnum {
		t.Errorf("Tone kind=%q", found["Tone"])
	}
	if found["NameSlice"] != types.SymbolKindTypeAlias {
		t.Errorf("NameSlice kind=%q", found["NameSlice"])
	}
	if found["greet"] != types.SymbolKindMethod || found["shout"] != types.SymbolKindMethod {
		t.Errorf("methods missing/wrong kind: %v", found)
	}
	if found["format"] != types.SymbolKindFunction {
		t.Errorf("top-level format kind=%q", found["format"])
	}
	if found["tone"] != types.SymbolKindVariable || parents["tone"] != "Greeter" {
		t.Errorf("field tone kind=%q parent=%q", found["tone"], parents["tone"])
	}
	if found["casual"] != types.SymbolKindVariable || parents["casual"] != "Tone" {
		t.Errorf("variant casual kind=%q parent=%q", found["casual"], parents["casual"])
	}
	if found["Stats"] != types.SymbolKindClass || parents["Stats"] != "Greeter" {
		t.Errorf("nested Stats kind=%q parent=%q", found["Stats"], parents["Stats"])
	}
	if found["count"] != types.SymbolKindVariable || parents["count"] != "Stats" {
		t.Errorf("nested field count kind=%q parent=%q", found["count"], parents["count"])
	}
	if parents["greet"] != "Greeter" {
		t.Errorf("greet ParentID=%q, want Greeter", parents["greet"])
	}
	if parents["shout"] != "Greeter" {
		t.Errorf("shout ParentID=%q, want Greeter", parents["shout"])
	}
	if parents["format"] != "" {
		t.Errorf("top-level format ParentID=%q, want empty", parents["format"])
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "helpers.zig") {
		t.Fatalf("imports=%v", res.Imports)
	}
	// Honesty: @import must not emit a calls edge to "import".
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":import") {
			t.Fatalf("unexpected @import call edge: %+v", e)
		}
	}
	calls := map[string]bool{}
	reads := map[string]bool{}
	for _, e := range res.Edges {
		leaf := e.TargetID
		if i := strings.LastIndex(leaf, ":"); i >= 0 {
			leaf = leaf[i+1:]
		}
		switch e.Kind {
		case types.RefKindCalls:
			calls[leaf] = true
		case types.RefKindReads:
			reads[leaf] = true
		}
	}
	if !calls["format"] {
		t.Fatal("expected greet/shout → format call edge")
	}
	if !calls["upper"] {
		t.Fatal("expected shout → helpers.upper call edge")
	}
	if !calls["helpers.upper"] || !reads["helpers"] {
		t.Fatalf("expected helpers.upper densify; calls=%v reads=%v", calls, reads)
	}
}

func TestParseSolidityLiteKindsImportsCalls(t *testing.T) {
	src := []byte("// SPDX-License-Identifier: MIT\r\n" +
		"pragma solidity ^0.8.19;\r\n" +
		"\r\n" +
		"import \"./Helpers.sol\";\r\n" +
		"\r\n" +
		"interface IGreeter {\r\n" +
		"    function greet(string memory name) external pure returns (string memory);\r\n" +
		"}\r\n" +
		"\r\n" +
		"contract Greeter is IGreeter {\r\n" +
		"    function greet(string memory name) public pure returns (string memory) {\r\n" +
		"        return Helpers.format(name);\r\n" +
		"    }\r\n" +
		"}\r\n" +
		"\r\n" +
		"library Helpers {\r\n" +
		"    function format(string memory s) internal pure returns (string memory) {\r\n" +
		"        return s;\r\n" +
		"    }\r\n" +
		"}\r\n")
	res, err := parseSolidityLite(context.Background(), "repo", "contracts/Greeter.sol", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	parents := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		parents[s.Name] = s.ParentID
	}
	if found["Greeter"] != types.SymbolKindClass {
		t.Errorf("Greeter kind=%q", found["Greeter"])
	}
	if found["IGreeter"] != types.SymbolKindInterface {
		t.Errorf("IGreeter kind=%q", found["IGreeter"])
	}
	if found["Helpers"] != types.SymbolKindClass {
		t.Errorf("Helpers kind=%q", found["Helpers"])
	}
	if found["greet"] != types.SymbolKindFunction || found["format"] != types.SymbolKindFunction {
		t.Errorf("functions missing: %v", found)
	}
	if parents["format"] != "Helpers" {
		t.Errorf("format ParentID=%q, want Helpers", parents["format"])
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "Helpers.sol") {
		t.Fatalf("imports=%v", res.Imports)
	}
	calls := 0
	var sawImplements, sawHelpersFormat, sawHelpersRead bool
	for _, e := range res.Edges {
		leaf := e.TargetID
		if i := strings.LastIndex(leaf, ":"); i >= 0 {
			leaf = leaf[i+1:]
		}
		switch e.Kind {
		case types.RefKindCalls:
			calls++
			if leaf == "Helpers.format" || leaf == "format" {
				sawHelpersFormat = true
			}
		case types.RefKindImplements:
			if leaf == "IGreeter" {
				sawImplements = true
			}
		case types.RefKindReads:
			if leaf == "Helpers" {
				sawHelpersRead = true
			}
		}
	}
	if calls == 0 {
		t.Fatal("expected Solidity call edges")
	}
	if !sawImplements {
		t.Fatal("expected Greeter implements IGreeter")
	}
	if !sawHelpersFormat || !sawHelpersRead {
		t.Fatalf("expected Helpers.format densify; format=%v helpersRead=%v", sawHelpersFormat, sawHelpersRead)
	}
}

func TestExtractorForZigAndSolidity(t *testing.T) {
	if ExtractorForExt(".zig") == nil {
		t.Fatal("expected .zig extractor")
	}
	if ExtractorForExt(".sol") == nil {
		t.Fatal("expected .sol extractor")
	}
}
