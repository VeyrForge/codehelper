// Package types holds the shared graph and index domain models that form
// codehelper's thin public Go API under pkg/ (see pkg/README.md).
//
// Import path: github.com/VeyrForge/codehelper/pkg/types
package types

// SymbolKind identifies a code symbol category.
type SymbolKind string

const (
	SymbolKindFunction  SymbolKind = "function"
	SymbolKindMethod    SymbolKind = "method"
	SymbolKindClass     SymbolKind = "class"
	SymbolKindInterface SymbolKind = "interface"
	SymbolKindVariable  SymbolKind = "variable"
	SymbolKindTypeAlias SymbolKind = "type_alias"
	SymbolKindEnum      SymbolKind = "enum"
	SymbolKindNamespace SymbolKind = "namespace"
	SymbolKindModule    SymbolKind = "module"
	SymbolKindUnknown   SymbolKind = "unknown"
)

// Symbol is a named code unit with location.
type Symbol struct {
	ID        string     `json:"id"`
	RepoID    string     `json:"repo_id"`
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	Path      string     `json:"path"`
	LineStart int        `json:"line_start"`
	LineEnd   int        `json:"line_end"`
	Language  string     `json:"language"`
	Signature string     `json:"signature,omitempty"`
	ParentID  string     `json:"parent_id,omitempty"`
}

// ReferenceKind classifies an edge between symbols or files.
type ReferenceKind string

const (
	RefKindImports    ReferenceKind = "imports"
	RefKindCalls      ReferenceKind = "calls"
	RefKindInherits   ReferenceKind = "inherits"
	RefKindImplements ReferenceKind = "implements"
	RefKindContains   ReferenceKind = "contains"
	RefKindReads      ReferenceKind = "reads"
)

// Reference is a directed relationship.
type Reference struct {
	ID         string        `json:"id"`
	RepoID     string        `json:"repo_id"`
	Kind       ReferenceKind `json:"kind"`
	SourceID   string        `json:"source_id"`
	TargetID   string        `json:"target_id"`
	Confidence float64       `json:"confidence"`
}

// Process is a traced execution flow (entry + steps).
type Process struct {
	ID          string   `json:"id"`
	RepoID      string   `json:"repo_id"`
	Name        string   `json:"name"`
	EntrySymbol string   `json:"entry_symbol"`
	StepSymbols []string `json:"step_symbols"`
}

// Cluster is a functional community (Leiden-style grouping).
type Cluster struct {
	ID       string   `json:"id"`
	RepoID   string   `json:"repo_id"`
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Cohesion float64  `json:"cohesion"`
}

// ImpactNode is one hop in blast-radius analysis.
type ImpactNode struct {
	SymbolID   string  `json:"symbol_id"`
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Loc        string  `json:"loc,omitempty"` // path:line cite (MCP may upgrade type-like line≤1)
	Depth      int     `json:"depth"`
	Confidence float64 `json:"confidence"`
	Provenance string  `json:"provenance,omitempty"` // exact | scoped | name_only | inferred
	Kind       string  `json:"kind"`
}

// ImpactResult groups impact analysis output.
type ImpactResult struct {
	Target               string       `json:"target"`
	Direction            string       `json:"direction"`
	Nodes                []ImpactNode `json:"nodes"`
	MustUpdateCandidates []ImpactNode `json:"must_update_candidates,omitempty"`
	RiskTier             string       `json:"risk_tier"`
}

// FileMeta tracks indexed file metadata.
type FileMeta struct {
	ID       string `json:"id"`
	RepoID   string `json:"repo_id"`
	Path     string `json:"path"`
	Language string `json:"language"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"`
}
