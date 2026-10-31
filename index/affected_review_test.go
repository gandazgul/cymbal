package index

import (
	"strings"
	"testing"
)

func TestAffectedTestsUnseededWorkDoesNotLoseFileFallback(t *testing.T) {
	for _, missing := range []string{"deleted", "stale", "mixed", "outside", "upstream"} {
		t.Run(missing, func(t *testing.T) {
			f := newAffectedFixture(t, []affectedFixtureFile{
				{"core.ts", "export function alpha() {}\nexport function beta() {}\nexport function gamma() {}\n"},
				{"direct.test.ts", "alpha();\n"},
				{"hub.ts", "export * from './core';\n"},
				{"indirect.test.ts", "import './hub';\n"},
			}, false)
			alpha, beta := f.seed(t, "core.ts", "alpha"), f.seed(t, "core.ts", "beta")
			input := affectedHunk(alpha)
			reindex := func(source string) {
				t.Helper()
				writePhase3File(t, f.root, "core.ts", source)
				stats, err := Index(f.root, f.db, Options{Workers: 1})
				if err != nil || stats.Errors != 0 {
					t.Fatalf("updating fixture: %+v, %v", stats, err)
				}
			}
			switch missing {
			case "deleted":
				beta.Deleted = true
				input.Symbols = append(input.Symbols, beta)
				reindex("export function alpha() {}\n")
			case "stale":
				input.Symbols = append(input.Symbols, beta)
				reindex("export function alpha() {}\n" + strings.Repeat("\n", 10) + "export function beta() {}\n")
			case "mixed":
				beta.Deleted = true
				input.Symbols = append(input.Symbols, beta, f.seed(t, "core.ts", "gamma"))
				reindex("export function alpha() {}\n" + strings.Repeat("\n", 10) + "export function gamma() {}\n")
			case "outside":
				input.Limitations = []AffectedLimitation{{Code: "content_outside_symbols", Paths: []string{"core.ts"}}}
			case "upstream":
				input.Limitations = []AffectedLimitation{{Code: "missing_live_seed", Seeds: []AffectedSymbol{beta}}}
			}
			r := affectedSelect(t, f.db, input, DefaultAffectedOptions())
			affectedWantPaths(t, r, "direct.test.ts", "indirect.test.ts")
			affectedWantStatus(t, r, "partial")
			test := r.Tests[1]
			e := affectedEvidence(t, test, "reverse_import")
			if len(e) != 1 || e[0].Seed.Name != "" || e[0].Seed.Path != "core.ts" || len(test.ChangedSymbols) != 0 {
				t.Fatalf("unseeded work must use changed-file evidence, not a live definition: %+v", test)
			}
			if r.Tests[0].Confidence != "high" {
				t.Fatalf("valid direct evidence lost: %+v", r.Tests[0])
			}
		})
	}
}

func TestAffectedTestsTiedEnclosingOwners(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "export function core() {}\n"},
		{"owners.ts", "function a() {} function z() { core(); }\n"},
		{"a.test.ts", "a();\n"},
		{"z.test.ts", "z();\n"},
		{"direct.test.ts", "core();\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "direct.test.ts", "a.test.ts", "z.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "ambiguous_enclosing_symbol", true)
	if r.Tests[0].Confidence != "high" || r.Tests[0].Evidence[0].Ambiguous {
		t.Fatalf("unique direct evidence was lowered: %+v", r.Tests[0])
	}
	for _, test := range r.Tests[1:] {
		e := affectedEvidence(t, test, "transitive_symbol")[0]
		if test.Confidence != "low" || !e.Ambiguous || len(e.Steps) != 4 {
			t.Fatalf("tied owner asserted as certain: %+v", test)
		}
	}
	opts := DefaultAffectedOptions()
	opts.Depth = 1
	r = affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), opts)
	affectedWantPaths(t, r, "direct.test.ts")
	affectedWantCode(t, r, "depth_truncation", true)
}

func TestAffectedTestsDeterministicLocationPathTie(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "function core() {}\n"},
		{"bridge.ts", "function first() {\n core();\n}\n\n\n\n\n\nfunction second() {\n core();\n}\nfunction end() {\n first();\n second();\n}\n"},
		{"end.test.ts", "end();\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.Depth = 3
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), opts)
	affectedWantPaths(t, r, "end.test.ts")
	e := affectedEvidence(t, r.Tests[0], "transitive_symbol")
	if len(e) != 1 || e[0].Steps[1].Line != 2 {
		t.Fatalf("location tie must order line 2 before 10: %+v", e)
	}
}

func TestAffectedTestsNativeStoredPaths(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"src/core.ts", "export function core() {}\n"},
		{"cases/a/direct.test.ts", "core();\n"},
		{"cases/a/import.test.ts", "import '../../src/core';\n"},
		{"cases/a0/later.test.ts", "core();\nimport '../../src/core';\n"},
	}, false)
	seed := f.seed(t, "src/core.ts", "core")
	// The parser owns every relationship. Only the persisted path representation
	// is changed to reproduce indexes written by the native Windows walker.
	store, err := OpenStore(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`UPDATE files SET rel_path=replace(rel_path,'/','\')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE imports SET raw_path=replace(raw_path,'/','\')`); err != nil {
		t.Fatal(err)
	}
	r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, r, "cases/a/direct.test.ts", "cases/a0/later.test.ts", "cases/a/import.test.ts")
	// Paths use slash separators, but raw import text preserves stored bytes.
	for _, test := range r.Tests {
		for _, evidence := range test.Evidence {
			if strings.Contains(evidence.Seed.Path, `\`) {
				t.Fatalf("native seed path escaped normalization: %+v", evidence)
			}
			for _, step := range evidence.Steps {
				if strings.Contains(step.Path, `\`) || strings.Contains(step.Target, `\`) {
					t.Fatalf("native step path escaped normalization: %+v", step)
				}
				if step.Kind == "import" && step.RawImport != `..\..\src\core` {
					t.Fatalf("raw import bytes were changed: %+v", step)
				}
			}
		}
	}
	opts := DefaultAffectedOptions()
	opts.MaxEvidence = 1
	r = affectedSelect(t, f.db, affectedHunk(seed), opts)
	if len(r.Tests) == 0 || r.Tests[0].Path != "cases/a/direct.test.ts" {
		t.Fatalf("query cap ran before slash path ordering: %+v", r.Tests)
	}
	affectedWantCode(t, r, "evidence_truncation", true)
	// File-only input exercises importer ordering independently of ref ordering.
	r = affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "src/core.ts"}}}, opts)
	affectedWantPaths(t, r, "cases/a/import.test.ts")
	affectedWantCode(t, r, "evidence_truncation", true)
}

func TestAffectedTestsSameLineMemberOwnersAreUncertain(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "function core() {}\n"},
		{"worker.ts", "class Worker { run() { core(); } }\n"},
		{"member.test.ts", "run();\n"},
		{"class.test.ts", "new Worker();\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "class.test.ts", "member.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "ambiguous_enclosing_symbol", true)
	for _, test := range r.Tests {
		e := affectedEvidence(t, test, "transitive_symbol")[0]
		if !e.Ambiguous || e.Confidence != "low" {
			t.Fatalf("same-line member owner asserted as certain: %+v", e)
		}
	}
}

func TestAffectedTestsTiedOwnerDepthFrontier(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "function core() { a(); } function z() {}\n"},
		{"a.ts", "function a() { core(); }\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.Depth = 1
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), opts)
	affectedWantCode(t, r, "depth_truncation", true)
	affectedWantStatus(t, r, "partial")
	opts.Depth = 2
	r = affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), opts)
	affectedWantCode(t, r, "depth_truncation", false)
	affectedWantCode(t, r, "ambiguous_enclosing_symbol", true)
}
