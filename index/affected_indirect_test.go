package index

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/1broseidon/cymbal/symbols"
)

func TestAffectedTestsTerminalImplementsRetainsDispatchGap(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"base.ts", "export class Base {}\n"},
		{"consumer.test.ts", "class Check extends Base {}\n"},
	}, false)
	seed := f.seed(t, "base.ts", "Base")
	r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, r, "consumer.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "indirect_dispatch", true)
	e := affectedEvidence(t, r.Tests[0], "direct_symbol")[0]
	if len(e.Steps) != 2 || e.Steps[1].Kind != "implements" || e.Seed != seed {
		t.Fatalf("implements evidence lost: %+v", e)
	}
}

func TestAffectedTestsTerminalTypeUseRemainsComplete(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"base.swift", "class Base {}\n"},
		{"tests/consumer.swift", "func check(value: Base) {}\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "base.swift", "Base")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "tests/consumer.swift")
	affectedWantStatus(t, r, "complete")
	affectedWantCode(t, r, "indirect_dispatch", false)
	e := affectedEvidence(t, r.Tests[0], "direct_symbol")[0]
	if len(e.Steps) != 2 || e.Steps[1].Kind != "use" {
		t.Fatalf("type-use evidence lost: %+v", e)
	}
}

func TestAffectedTestsVisitedFrontierUseRetainsDispatchGap(t *testing.T) {
	// Bare callback uses are not recorded by the JS parser. Use the public
	// Store API to supply that supported reference kind in a closed cycle.
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	store, err := OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Cleanup(CloseAll)
	for _, f := range []struct{ path, name, target, kind string }{
		{"core.js", "core", "bridge", symbols.RefKindUse},
		{"bridge.js", "bridge", "core", symbols.RefKindCall},
	} {
		p := filepath.Join(root, f.path)
		err := store.InsertFileAll(p, f.path, "javascript", "fixture", time.Unix(1, 0), 10,
			[]symbols.Symbol{{File: p, Name: f.name, Kind: "function", Language: "javascript", StartLine: 1, EndLine: 3}}, nil,
			[]symbols.Ref{{Name: f.target, Kind: f.kind, Line: 2, Language: "javascript"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultAffectedOptions()
	opts.Depth = 1
	seed := AffectedSymbol{Path: "core.js", Name: "core", Kind: "function", Language: "javascript", StartLine: 1, EndLine: 3}
	r := affectedSelect(t, db, affectedHunk(seed), opts)
	affectedWantPaths(t, r)
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "indirect_dispatch", true)
	affectedWantCode(t, r, "depth_truncation", false)
	affectedWantCode(t, r, "evidence_truncation", false)
}
