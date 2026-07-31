package eval

import (
	_ "embed"
	"strings"
)

//go:embed testdata/golden_retrieval_suite.json
var goldenSuiteJSON string

// Golden returns the extended retrieval benchmark suite.
func Golden() (Suite, error) {
	return LoadSuite(strings.NewReader(goldenSuiteJSON))
}
