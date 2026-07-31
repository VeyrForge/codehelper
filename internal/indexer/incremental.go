package indexer

import (
	"context"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/filehash"
	"github.com/VeyrForge/codehelper/internal/graph"
)

const fullReindexRatio = 0.5

// RemoveSymbolsForPaths deletes symbols and related edges for paths.
func RemoveSymbolsForPaths(ctx context.Context, st *graph.Store, repoID string, paths []string) error {
	for _, p := range paths {
		if err := deleteSymbolsPath(ctx, st, repoID, p); err != nil {
			return err
		}
	}
	return nil
}

func deleteSymbolsPath(ctx context.Context, st *graph.Store, repoID, relPath string) error {
	db := st.DB()
	rows, err := db.QueryContext(ctx, `SELECT id FROM symbols WHERE repo_id=? AND path=?`, repoID, relPath)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := db.ExecContext(ctx, `DELETE FROM edges WHERE repo_id=? AND (src_id=? OR dst_id=?)`, repoID, id, id); err != nil {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM symbols WHERE repo_id=? AND path=?`, repoID, relPath); err != nil {
		return err
	}
	return nil
}

// FileContentHash returns sha256 of file at root/rel.
func FileContentHash(root, rel string) (string, error) {
	return filehash.OfFile(filepath.Join(root, rel))
}

// ShouldSkipUnchanged returns true if file hash matches wantHash.
func ShouldSkipUnchanged(root, rel, wantHash string) (bool, error) {
	got, err := FileContentHash(root, rel)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return got == wantHash, nil
}
