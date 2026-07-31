package indexer

import (
	"testing"
)

func BenchmarkLanguageFromExt(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = languageFromExt("pkg/foo/bar.go")
	}
}
