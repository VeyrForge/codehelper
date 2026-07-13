package parser

import (
	"fmt"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func edgeID(repoID, src, dst, kind string) string {
	return fmt.Sprintf("e:%s:%s:%s:%s", repoID, src, kind, dst)
}

func moduleNodeID(repoID, mod string) string {
	return fmt.Sprintf("mod:%s:%s", repoID, mod)
}

func symbol(repoID, relPath, name string, kind types.SymbolKind, ls, le int, lang, sig, parent string) types.Symbol {
	id := fmt.Sprintf("sym:%s:%s:%d:%s", repoID, relPath, ls, name)
	return types.Symbol{
		ID:        id,
		RepoID:    repoID,
		Name:      name,
		Kind:      kind,
		Path:      relPath,
		LineStart: ls,
		LineEnd:   le,
		Language:  lang,
		Signature: sig,
		ParentID:  parent,
	}
}

func containsEdge(repoID, relPath, symID string) types.Reference {
	fid := FileNodeID(repoID, relPath)
	return types.Reference{
		ID:         edgeID(repoID, fid, symID, "contains"),
		RepoID:     repoID,
		Kind:       types.RefKindContains,
		SourceID:   fid,
		TargetID:   symID,
		Confidence: 1,
	}
}
