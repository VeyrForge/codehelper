package mcpsvc

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDemoteLiveExpressSmokeNames(t *testing.T) {
	in := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{Name: "res.send", Path: "lib/response.js", LineStart: 126}, Score: 1.509},
		{Symbol: types.Symbol{Name: "res.sendFile", Path: "lib/response.js", LineStart: 373}, Score: 1.335},
		{Symbol: types.Symbol{Name: "sendfile", Path: "lib/response.js", LineStart: 924}, Score: 1.112},
		{Symbol: types.Symbol{Name: "res.sendStatus", Path: "lib/response.js", LineStart: 323}, Score: 1.076},
		{Symbol: types.Symbol{Name: "app.path", Path: "lib/application.js", LineStart: 399}, Score: 1.039},
		{Symbol: types.Symbol{Name: "exports.compileQueryParser", Path: "lib/utils.js", LineStart: 162}, Score: 0.768},
		{Symbol: types.Symbol{Name: "parseExtendedQueryString", Path: "lib/utils.js", LineStart: 267}, Score: 0.715},
		{Symbol: types.Symbol{Name: "res.render", Path: "lib/response.js", LineStart: 897}, Score: 0.647},
	}
	got := demoteIntentMismatchedHits("N+1 query loop alloc hot path res.send", in)
	for i, h := range got {
		t.Logf("%d %s", i, h.Symbol.Name)
	}
	if got[0].Symbol.Name == "res.send" || got[0].Symbol.Name == "res.sendFile" || got[0].Symbol.Name == "sendfile" || got[0].Symbol.Name == "res.sendStatus" {
		t.Fatalf("expected HTTP friends demoted, got %+v", got[0])
	}
}
