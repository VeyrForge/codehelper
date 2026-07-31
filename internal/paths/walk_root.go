package paths

import (
	"os"
	"path/filepath"
)

// ResolveWalkRoot follows Windows junctions / symlinks so filepath.WalkDir can
// descend. On Windows, junctions often Lstat as ModeIrregular + !IsDir (not
// ModeSymlink); EvalSymlinks also leaves them unresolved. Readlink works.
// Without this, walks from a junction cwd (e.g. .testbeds/active/laravel →
// .stub-src/laravel) see 0 files.
func ResolveWalkRoot(path string) string {
	fi, err := os.Lstat(path)
	if err != nil {
		return path
	}
	// Plain directory — nothing to resolve.
	if fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 && fi.Mode()&os.ModeIrregular == 0 {
		return path
	}
	// Symlink, junction, or other reparse: Stat sees a dir, Lstat does not.
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return path
	}
	target, err := os.Readlink(path)
	if err != nil || target == "" {
		return path
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return target
}
