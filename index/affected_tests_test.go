package index

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/1broseidon/cymbal/symbols"
)

type affectedFixtureFile struct{ path, source string }
type affectedFixture struct{ root, db string }

// All selection fixtures use the production parser and SQLite index. Incremental
// indexing lets the determinism test change database insertion order, not just
// the order in which already-sorted files are written.
func newAffectedFixture(t *testing.T, files []affectedFixtureFile, incremental bool) affectedFixture {
	t.Helper()
	t.Setenv("CYMBAL_CACHE_DIR", t.TempDir())
	root := t.TempDir()
	runPhase3Git(t, root, "init")
	db, err := RepoDBPath(root)
	if err != nil {
		t.Fatal(err)
	}
	build := func() {
		t.Helper()
		stats, err := Index(root, db, Options{Workers: 1})
		if err != nil {
			t.Fatal(err)
		}
		if stats.Errors != 0 {
			t.Fatalf("fixture indexing errors: %+v", stats)
		}
	}
	for _, f := range files {
		writePhase3File(t, root, f.path, f.source)
		if incremental {
			build()
		}
	}
	if !incremental {
		build()
	}
	t.Cleanup(CloseAll)
	return affectedFixture{root, db}
}

func (f affectedFixture) seed(t *testing.T, p, name string) AffectedSymbol {
	t.Helper()
	outline, err := FileOutline(f.db, filepath.Join(f.root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range outline {
		if s.Name == name {
			return AffectedSymbol{Path: filepath.ToSlash(s.RelPath), Name: s.Name, Kind: s.Kind, Language: s.Language, StartLine: s.StartLine, EndLine: s.EndLine}
		}
	}
	t.Fatalf("fixture has no definition %s in %s: %+v", name, p, outline)
	return AffectedSymbol{}
}

func affectedSelect(t *testing.T, db string, input AffectedInput, opts AffectedOptions) *AffectedResult {
	t.Helper()
	r, err := FindAffectedTests(db, input, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("nil result without error")
	}
	if r.SchemaVersion != 1 {
		t.Errorf("schema version = %d, want 1", r.SchemaVersion)
	}
	if r.Tests == nil || r.Limitations == nil {
		t.Errorf("collections must be empty arrays, not null: %+v", r)
	}
	if !strings.Contains(strings.ToLower(r.Advisory), "safe to skip") || r.StaticAnalysisLimit == "" {
		t.Errorf("missing static analysis advisory: %+v", r)
	}
	for i, test := range r.Tests {
		if test.Rank != i+1 {
			t.Errorf("rank at %d = %d", i, test.Rank)
		}
		if test.ChangedSymbols == nil || len(test.Evidence) == 0 {
			t.Errorf("missing collections for %+v", test)
		}
	}
	return r
}

func affectedHunk(seeds ...AffectedSymbol) AffectedInput {
	return AffectedInput{Precision: "hunk", Symbols: seeds}
}
func affectedPaths(r *AffectedResult) []string {
	out := []string{}
	for _, test := range r.Tests {
		out = append(out, test.Path)
	}
	return out
}
func affectedWantPaths(t *testing.T, r *AffectedResult, want ...string) {
	t.Helper()
	if got := affectedPaths(r); !reflect.DeepEqual(got, append([]string{}, want...)) {
		t.Fatalf("selected paths = %v, want %v; result=%+v", got, want, r)
	}
}
func affectedHasCode(r *AffectedResult, code string) bool {
	for _, l := range r.Limitations {
		if l.Code == code {
			return true
		}
	}
	return false
}
func affectedWantCode(t *testing.T, r *AffectedResult, code string, want bool) {
	t.Helper()
	if got := affectedHasCode(r, code); got != want {
		t.Errorf("limitation %s present = %v, want %v: %+v", code, got, want, r.Limitations)
	}
}
func affectedWantStatus(t *testing.T, r *AffectedResult, status string) {
	t.Helper()
	if r.Status != status {
		t.Errorf("status = %s, want %s; limitations=%+v", r.Status, status, r.Limitations)
	}
	if r.Fallback.Recommended != (status != "complete") {
		t.Errorf("fallback = %+v for status %s", r.Fallback, status)
	}
	if status != "complete" && !strings.Contains(strings.ToLower(r.Fallback.Reason), "full suite") {
		t.Errorf("missing full-suite advice: %+v", r.Fallback)
	}
}
func affectedWantDistance(t *testing.T, got *int, want int) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("distance = %v, want %d", got, want)
	}
}
func affectedEvidence(t *testing.T, test AffectedTest, kind string) []AffectedEvidence {
	t.Helper()
	out := []AffectedEvidence{}
	for _, e := range test.Evidence {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s evidence for %+v", kind, test)
	}
	return out
}

func TestAffectedTestsDirectUnique(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"calc.go", "package calc\nfunc Calculate() {}\n"},
		{"direct_test.go", "package calc\nfunc TestCalculate() {\n Calculate()\n}\n"},
		{"unrelated_test.go", "package calc\nfunc TestUnrelated() {}\n"},
	}, false)
	seed := f.seed(t, "calc.go", "Calculate")
	r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, r, "direct_test.go")
	affectedWantStatus(t, r, "complete")
	test := r.Tests[0]
	if test.Confidence != "high" {
		t.Errorf("confidence = %s, want high", test.Confidence)
	}
	affectedWantDistance(t, test.Distance, 1)
	if !reflect.DeepEqual(test.ChangedSymbols, []AffectedSymbol{seed}) {
		t.Errorf("changed symbols = %+v", test.ChangedSymbols)
	}
	evidence := affectedEvidence(t, test, "direct_symbol")
	want := []AffectedStep{{Path: "calc.go", Name: "Calculate", Kind: seed.Kind, Line: 2, EndLine: 2}, {Path: "direct_test.go", Name: "Calculate", Kind: "call", Line: 3}}
	if len(evidence) != 1 || !reflect.DeepEqual(evidence[0].Steps, want) {
		t.Fatalf("evidence=%+v, want steps %+v", evidence, want)
	}
	refs, err := FindReferences(f.db, "Calculate", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].RelPath != "direct_test.go" || refs[0].Line != 3 {
		t.Fatalf("real stored reference = %+v", refs)
	}
}

func TestAffectedTestsTransitiveChain(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() {}\n"},
		{"first.go", "package app\nfunc First() {\n Core()\n}\n"},
		{"second.go", "package app\nfunc Second() {\n First()\n}\n"},
		{"branch.go", "package app\nfunc DeadEnd() { Core() }\n"},
		{"chain_test.go", "package app\nfunc TestChain() {\n Second()\n}\n"},
	}, false)
	seed := f.seed(t, "core.go", "Core")
	first := f.seed(t, "first.go", "First")
	second := f.seed(t, "second.go", "Second")
	opts := DefaultAffectedOptions()
	opts.Depth = 3
	r := affectedSelect(t, f.db, affectedHunk(seed), opts)
	affectedWantPaths(t, r, "chain_test.go")
	affectedWantStatus(t, r, "complete")
	test := r.Tests[0]
	affectedWantDistance(t, test.Distance, 3)
	if test.Confidence != "medium" {
		t.Errorf("confidence = %s", test.Confidence)
	}
	evidence := affectedEvidence(t, test, "transitive_symbol")
	want := []AffectedStep{
		{Path: seed.Path, Name: seed.Name, Kind: seed.Kind, Line: seed.StartLine, EndLine: seed.EndLine},
		{Path: "first.go", Name: "Core", Kind: "call", Line: 3},
		{Path: first.Path, Name: first.Name, Kind: first.Kind, Line: first.StartLine, EndLine: first.EndLine},
		{Path: "second.go", Name: "First", Kind: "call", Line: 3},
		{Path: second.Path, Name: second.Name, Kind: second.Kind, Line: second.StartLine, EndLine: second.EndLine},
		{Path: "chain_test.go", Name: "Second", Kind: "call", Line: 3},
	}
	if len(evidence) != 1 || !reflect.DeepEqual(evidence[0].Steps, want) {
		t.Fatalf("chain = %+v, want %+v", evidence, want)
	}
}

func TestAffectedTestsTopLevelJavaScript(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.js", "export function core() {}\n"},
		{"top.test.js", "core();\n"},
		{"unrelated.test.js", "unrelated();\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.js", "core")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "top.test.js")
	affectedWantStatus(t, r, "complete")
	test := r.Tests[0]
	if test.Confidence != "high" {
		t.Errorf("confidence = %s", test.Confidence)
	}
	e := affectedEvidence(t, test, "direct_symbol")[0]
	affectedWantDistance(t, e.Distance, 1)
	if terminal := e.Steps[len(e.Steps)-1]; terminal.Path != "top.test.js" || terminal.Line != 1 || terminal.Kind != "call" {
		t.Errorf("top-level site = %+v", terminal)
	}
	outline, err := FileOutline(f.db, filepath.Join(f.root, "top.test.js"))
	if err != nil {
		t.Fatal(err)
	}
	if len(outline) != 0 {
		t.Fatalf("test requires no enclosing symbol, got %+v", outline)
	}
}

func TestAffectedTestsUnknownHelpersAndTerminalTestHelpers(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "export function core() {}\n"},
		{"testutil/shared.ts", "export function shared() { core(); }\n"},
		{"tests/helpers.ts", "export function terminalHelper() { shared(); }\n"},
		{"downstream.test.ts", "terminalHelper();\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.Depth = 5
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), opts)
	affectedWantPaths(t, r, "tests/helpers.ts")
	affectedWantStatus(t, r, "complete")
	affectedWantDistance(t, r.Tests[0].Distance, 2)
	e := affectedEvidence(t, r.Tests[0], "transitive_symbol")[0]
	if len(e.Steps) != 4 || e.Steps[1].Path != "testutil/shared.ts" || e.Steps[3].Path != "tests/helpers.ts" {
		t.Errorf("helper chain = %+v", e.Steps)
	}
	// Changed classified helpers are selected themselves, never their consumers.
	r = affectedSelect(t, f.db, affectedHunk(f.seed(t, "tests/helpers.ts", "terminalHelper")), opts)
	affectedWantPaths(t, r, "tests/helpers.ts")
	affectedWantStatus(t, r, "complete")
	affectedWantDistance(t, r.Tests[0].Distance, 0)
	if r.Tests[0].Confidence != "high" {
		t.Errorf("changed test confidence = %s", r.Tests[0].Confidence)
	}
	affectedEvidence(t, r.Tests[0], "changed_test")
}

func TestAffectedTestsIndependentSupportAndDuplicateInput(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Alpha() {}\nfunc Beta() {}\n"},
		{"a_test.go", "package app\nfunc TestOne() { Alpha() }\n"},
		{"z_test.go", "package app\nfunc TestMany() {\n Alpha()\n Alpha()\n Beta()\n}\n"},
	}, false)
	alpha, beta := f.seed(t, "core.go", "Alpha"), f.seed(t, "core.go", "Beta")
	input := affectedHunk(alpha, beta)
	r := affectedSelect(t, f.db, input, DefaultAffectedOptions())
	affectedWantPaths(t, r, "z_test.go", "a_test.go")
	affectedWantStatus(t, r, "complete")
	e := affectedEvidence(t, r.Tests[0], "direct_symbol")
	if len(e) != 3 || len(r.Tests[0].ChangedSymbols) != 2 {
		t.Fatalf("independent support = %+v", r.Tests[0])
	}
	sites := map[string]bool{}
	for _, v := range e {
		site := v.Steps[len(v.Steps)-1]
		key := fmt.Sprintf("%s:%d", v.Seed.Name, site.Line)
		if sites[key] {
			t.Errorf("duplicate support %s", key)
		}
		sites[key] = true
	}
	if !sites["Alpha:3"] || !sites["Alpha:4"] || !sites["Beta:5"] {
		t.Errorf("support sites = %+v", sites)
	}
	input.Symbols = []AffectedSymbol{beta, alpha, alpha, beta}
	input.Files = []AffectedFile{{Path: "core.go"}, {Path: "core.go"}}
	duplicate := affectedSelect(t, f.db, input, DefaultAffectedOptions())
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(duplicate)
	if string(a) != string(b) {
		t.Fatalf("duplicate inputs change support/ranking:\n%s\n%s", a, b)
	}
}

func TestAffectedTestsDeterministic(t *testing.T) {
	files := []affectedFixtureFile{
		{"root.go", "package app\nfunc Root() {}\nfunc Other() {}\n"},
		{"a.go", "package app\nfunc A() { Root() }\n"},
		{"b.go", "package app\nfunc B() { Root() }\n"},
		{"join.go", "package app\nfunc Join() {\n A()\n B()\n Cycle()\n}\n"},
		{"cycle.go", "package app\nfunc Cycle() { Join() }\n"},
		{"z_test.go", "package app\nfunc TestZ() { Join() }\n"},
		{"a_test.go", "package app\nfunc TestA() { Join() }\n"},
	}
	forward := newAffectedFixture(t, files, true)
	reversed := append([]affectedFixtureFile{}, files...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	backward := newAffectedFixture(t, reversed, true)
	opts := DefaultAffectedOptions()
	opts.Depth = 5
	var payload []byte
	for _, f := range []affectedFixture{forward, backward} {
		root, other := f.seed(t, "root.go", "Root"), f.seed(t, "root.go", "Other")
		for _, input := range []AffectedInput{affectedHunk(root, other), affectedHunk(other, root, root)} {
			r := affectedSelect(t, f.db, input, opts)
			affectedWantPaths(t, r, "a_test.go", "z_test.go")
			affectedWantStatus(t, r, "complete")
			for _, test := range r.Tests {
				e := affectedEvidence(t, test, "transitive_symbol")
				if len(e) != 1 {
					t.Fatalf("diamond paths inflated support: %+v", e)
				}
				affectedWantDistance(t, e[0].Distance, 3)
				if len(e[0].Steps) != 6 || e[0].Steps[1].Path != "a.go" || e[0].Steps[3].Path != "join.go" {
					t.Fatalf("noncanonical shortest diamond path: %+v", e[0].Steps)
				}
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if payload == nil {
				payload = b
			} else if string(payload) != string(b) {
				t.Fatalf("insertion/input order changed JSON:\n%s\n%s", payload, b)
			}
		}
	}
}

func TestAffectedTestsReExportImportsDoNotOutrankSymbols(t *testing.T) {
	files := []affectedFixtureFile{
		{"core.ts", "export function core() {}\n"},
		{"barrel.ts", "export * from './core';\n"},
		{"named.ts", "export { core as renamed } from './core';\n"},
		{"z-symbol.test.ts", "core();\n"},
	}
	for i := 0; i < 6; i++ {
		files = append(files, affectedFixtureFile{fmt.Sprintf("a-import-%d.test.ts", i), "import './barrel';\nimport './named';\n"})
	}
	f := newAffectedFixture(t, files, false)
	opts := DefaultAffectedOptions()
	opts.Depth = 3
	// Symbol-backed seeds suppress multi-hop fallback, so use a symbol-free
	// changed file alongside the live symbol to exercise both evidence classes.
	writePhase3File(t, f.root, "config.ts", "// changed configuration, no definitions\n")
	writePhase3File(t, f.root, "settings-hub.ts", "export * from './config';\n")
	writePhase3File(t, f.root, "settings-alt.ts", "export * from './config';\n")
	for i := 0; i < 6; i++ {
		writePhase3File(t, f.root, fmt.Sprintf("b-fallback-%d.test.ts", i), "import './settings-hub';\nimport './settings-alt';\n")
	}
	if _, err := Index(f.root, f.db, Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Symbols: []AffectedSymbol{f.seed(t, "core.ts", "core")}, Files: []AffectedFile{{Path: "config.ts"}}}, opts)
	if len(r.Tests) != 7 || r.Tests[0].Path != "z-symbol.test.ts" || r.Tests[0].Confidence != "high" {
		t.Fatalf("symbol must outrank import fan-out: %+v", r.Tests)
	}
	for _, test := range r.Tests[1:] {
		if test.Confidence != "low" {
			t.Errorf("import confidence = %s", test.Confidence)
		}
		evidence := affectedEvidence(t, test, "reverse_import")
		if len(evidence) != 2 {
			t.Fatalf("import candidate must have more support than the symbol candidate: %+v", evidence)
		}
		for _, e := range evidence {
			affectedWantDistance(t, e.Distance, 2)
			if len(e.Steps) != 3 || e.Steps[1].RawImport != "./config" || e.Steps[1].Target != "config.ts" || (e.Steps[2].RawImport != "./settings-hub" && e.Steps[2].RawImport != "./settings-alt") {
				t.Errorf("barrel evidence = %+v", e)
			}
		}
	}
	affectedWantCode(t, r, "heuristic_import_fallback", true)
	affectedWantStatus(t, r, "partial")
	// With no symbol-backed test, actual export-from records provide fallback.
	noDirect := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "core.ts"}}}, opts)
	if len(noDirect.Tests) != 6 {
		t.Fatalf("export-from fallback candidates = %+v", noDirect.Tests)
	}
	for _, test := range noDirect.Tests {
		e := affectedEvidence(t, test, "reverse_import")
		if len(e) != 2 {
			t.Errorf("both barrel imports should be independent evidence: %+v", e)
		}
	}
}

func TestAffectedTestsImportPathsAreLiteralAndHeuristic(t *testing.T) {
	for _, stem := range []string{"a_b", "a%b"} {
		t.Run(stem, func(t *testing.T) {
			f := newAffectedFixture(t, []affectedFixtureFile{
				{stem + ".ts", "// no symbols\n"},
				{"literal.test.ts", fmt.Sprintf("import './%s';\n", stem)},
				{"wildcard.test.ts", "import './axb';\n"},
				{"unrelated.test.ts", "import './elsewhere';\n"},
			}, false)
			r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: stem + ".ts"}}}, DefaultAffectedOptions())
			affectedWantPaths(t, r, "literal.test.ts")
			affectedWantStatus(t, r, "partial")
			if r.Tests[0].Confidence != "low" || len(r.Tests[0].ChangedSymbols) != 0 {
				t.Errorf("file import candidate = %+v", r.Tests[0])
			}
			e := affectedEvidence(t, r.Tests[0], "direct_import")[0]
			if len(e.Steps) != 2 || e.Steps[1].RawImport != "./"+stem || e.Steps[1].Target != stem+".ts" {
				t.Errorf("literal import explanation = %+v", e)
			}
		})
	}
	t.Run("basename collision", func(t *testing.T) {
		f := newAffectedFixture(t, []affectedFixtureFile{
			{"one/widget.ts", "// no symbols\n"}, {"two/widget.ts", "// same basename\n"},
			{"collision.test.ts", "import './two/widget';\n"},
		}, false)
		r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "one/widget.ts"}}}, DefaultAffectedOptions())
		affectedWantPaths(t, r, "collision.test.ts")
		affectedWantCode(t, r, "heuristic_import_fallback", true)
		if r.Tests[0].Confidence != "low" {
			t.Errorf("collision confidence = %s", r.Tests[0].Confidence)
		}
		e := affectedEvidence(t, r.Tests[0], "direct_import")[0]
		if e.Steps[1].RawImport != "./two/widget" || e.Steps[1].Target != "one/widget.ts" {
			t.Errorf("heuristic match must show actual raw/target: %+v", e)
		}
	})
}

func TestAffectedTestsColocatedNaming(t *testing.T) {
	cases := []struct {
		source string
		tests  []string
		body   string
	}{
		{"thing.go", []string{"thing_test.go"}, "package app\n"},
		{"thing.ts", []string{"thing.test.js", "thing.spec.tsx", "thing.test.jsx", "thing.spec.mjs", "thing.test.cjs", "thing.spec.mts", "thing.test.cts"}, "// no references\n"},
		{"thing.py", []string{"test_thing.py", "thing_test.py"}, "# no references\n"},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			files := []affectedFixtureFile{{tc.source, tc.body}, {"unrelated_test.py", "# unrelated\n"}}
			for _, p := range tc.tests {
				files = append(files, affectedFixtureFile{p, tc.body})
			}
			f := newAffectedFixture(t, files, false)
			r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: tc.source}}}, DefaultAffectedOptions())
			want := append([]string{}, tc.tests...)
			sort.Strings(want)
			affectedWantPaths(t, r, want...)
			affectedWantStatus(t, r, "partial")
			for _, test := range r.Tests {
				if test.Confidence != "low" || test.Distance != nil || len(test.ChangedSymbols) != 0 {
					t.Errorf("naming-only result = %+v", test)
				}
				e := affectedEvidence(t, test, "colocated")[0]
				if e.Distance != nil || e.Seed.Path != tc.source || e.Seed.Name != "" || len(e.Steps) != 2 || e.Steps[1].Path != test.Path {
					t.Errorf("naming evidence = %+v", e)
				}
			}
			affectedWantCode(t, r, "colocated_inference", true)
		})
	}
}

func TestAffectedTestsAmbiguousSeedLanguageScope(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "export function collide() {}\n"},
		{"other.js", "function collide() {}\n"},
		{"other.py", "def collide():\n    pass\n"},
		{"ts.test.ts", "collide();\n"}, {"js.test.js", "collide();\n"}, {"test_py.py", "collide()\n"},
	}, false)
	seed := f.seed(t, "core.ts", "collide")
	cases := []struct {
		scope ResolveScope
		paths []string
		amb   bool
	}{
		{ResolveScopeSame, []string{"ts.test.ts"}, false},
		{ResolveScopeFamily, []string{"js.test.js", "ts.test.ts"}, true},
		{ResolveScopeAll, []string{"js.test.js", "test_py.py", "ts.test.ts"}, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			opts := DefaultAffectedOptions()
			opts.Scope = tc.scope
			r := affectedSelect(t, f.db, affectedHunk(seed), opts)
			affectedWantPaths(t, r, tc.paths...)
			affectedWantCode(t, r, "ambiguous_definitions", tc.amb)
			status := "complete"
			confidence := "high"
			if tc.amb {
				status = "partial"
				confidence = "low"
			}
			affectedWantStatus(t, r, status)
			for _, test := range r.Tests {
				e := affectedEvidence(t, test, "direct_symbol")[0]
				if test.Confidence != confidence || e.Ambiguous != tc.amb {
					t.Errorf("scoped ambiguity = %+v", test)
				}
			}
		})
	}
}

func TestAffectedTestsAmbiguousIntermediateLanguageScope(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "export function core() {}\n"},
		{"bridge.ts", "function bridge() { core(); }\n"},
		{"other.js", "function bridge() {}\n"},
		{"bridge.test.ts", "bridge();\n"},
	}, false)
	for _, scope := range []ResolveScope{ResolveScopeSame, ResolveScopeFamily, ResolveScopeAll} {
		t.Run(string(scope), func(t *testing.T) {
			opts := DefaultAffectedOptions()
			opts.Scope = scope
			r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), opts)
			affectedWantPaths(t, r, "bridge.test.ts")
			amb := scope != ResolveScopeSame
			affectedWantCode(t, r, "ambiguous_definitions", amb)
			confidence, status := "medium", "complete"
			if amb {
				confidence, status = "low", "partial"
			}
			affectedWantStatus(t, r, status)
			e := affectedEvidence(t, r.Tests[0], "transitive_symbol")[0]
			if r.Tests[0].Confidence != confidence || e.Ambiguous != amb {
				t.Errorf("intermediate ambiguity = %+v", r.Tests[0])
			}
			affectedWantDistance(t, e.Distance, 2)
		})
	}
}

func TestAffectedTestsIndirectInterfaceEvidence(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"api.ts", "export interface Contract { run(): void; }\n"},
		{"worker.ts", "class Worker implements Contract { run() {} }\n"},
		{"worker.test.ts", "new Worker();\n"},
	}, false)
	seed := f.seed(t, "api.ts", "Contract")
	r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, r, "worker.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "indirect_dispatch", true)
	e := affectedEvidence(t, r.Tests[0], "transitive_symbol")[0]
	if len(e.Steps) != 4 || e.Steps[1].Kind != "implements" || e.Steps[1].Path != "worker.ts" {
		t.Fatalf("must preserve implements, not invent call: %+v", e.Steps)
	}
	refs, err := FindImplementors(f.db, "Contract", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.RelPath == "worker.ts" && ref.Implementer == "Worker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture has no implements relationship: %+v", refs)
	}
}

func TestAffectedTestsIndirectCallbackUse(t *testing.T) {
	// The JS/Go parsers do not record bare callback arguments as non-call refs.
	// Model that existing schema relationship through the public Store API only.
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	store, err := OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Cleanup(CloseAll)
	files := []struct {
		p, name string
		refs    []symbols.Ref
	}{
		{"core.js", "callback", nil},
		{"register.js", "install", []symbols.Ref{{Name: "callback", Kind: symbols.RefKindUse, Line: 2, Language: "javascript"}}},
		{"callback.test.js", "check", []symbols.Ref{{Name: "install", Kind: symbols.RefKindCall, Line: 2, Language: "javascript"}}},
	}
	for _, f := range files {
		p := filepath.Join(root, f.p)
		if err := store.InsertFileAll(p, f.p, "javascript", "fixture", time.Unix(1, 0), 10, []symbols.Symbol{{File: p, Name: f.name, Kind: "function", Language: "javascript", StartLine: 1, EndLine: 3}}, nil, f.refs); err != nil {
			t.Fatal(err)
		}
	}
	seed := AffectedSymbol{Path: "core.js", Name: "callback", Kind: "function", Language: "javascript", StartLine: 1, EndLine: 3}
	r := affectedSelect(t, db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, r, "callback.test.js")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "indirect_dispatch", true)
	e := affectedEvidence(t, r.Tests[0], "transitive_symbol")[0]
	if len(e.Steps) != 4 || e.Steps[1].Kind != "use" || e.Steps[1].Name != "callback" || e.Steps[3].Name != "install" {
		t.Fatalf("callback explanation invented dispatch: %+v", e.Steps)
	}
}

func TestAffectedTestsEmptyAndNoCandidates(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}, {"unrelated_test.go", "package app\nfunc TestOther() {}\n"}}, false)
	for _, input := range []AffectedInput{{Precision: "hunk"}, affectedHunk(f.seed(t, "core.go", "Core"))} {
		r := affectedSelect(t, f.db, input, DefaultAffectedOptions())
		affectedWantPaths(t, r)
		affectedWantStatus(t, r, "complete")
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"tests":[]`) || !strings.Contains(string(b), `"limitations":[]`) {
			t.Errorf("empty JSON arrays: %s", b)
		}
	}
}

func TestAffectedTestsMissingAndDeletedSeeds(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"live.go", "package app\nfunc Vanished() {}\n"},
		{"live_test.go", "package app\nfunc TestLive() { Vanished() }\n"},
	}, false)
	live := f.seed(t, "live.go", "Vanished")
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			seed := live
			seed.Path = "gone.go"
			seed.Deleted = deleted
			r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
			affectedWantPaths(t, r)
			affectedWantStatus(t, r, "unavailable")
			code := "missing_live_seed"
			if deleted {
				code = "missing_deleted_seed"
			}
			affectedWantCode(t, r, code, true)
			affectedWantCode(t, r, "no_analyzable_input", true)
			found := false
			for _, l := range r.Limitations {
				for _, p := range l.Paths {
					if p == "gone.go" {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("missing path was discarded: %+v", r.Limitations)
			}
		})
	}
	t.Run("moved location is not live identity", func(t *testing.T) {
		seed := live
		seed.StartLine += 10
		seed.EndLine += 10
		r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
		// A naming match is valid weak evidence; the same-name reference is not.
		affectedWantPaths(t, r, "live_test.go")
		affectedWantCode(t, r, "missing_live_seed", true)
		if r.Tests[0].Confidence != "low" {
			t.Errorf("stale seed falsely resolved: %+v", r.Tests[0])
		}
		for _, e := range r.Tests[0].Evidence {
			if e.Kind == "direct_symbol" {
				t.Errorf("stale location produced symbol evidence: %+v", e)
			}
		}
	})
}

func TestAffectedTestsFilePrecision(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Alpha() {}\nfunc Beta() {}\n"},
		{"alpha_test.go", "package app\nfunc TestAlpha() { Alpha() }\n"},
		{"beta_test.go", "package app\nfunc TestBeta() { Beta() }\n"},
	}, false)
	r := affectedSelect(t, f.db, AffectedInput{Precision: "file", Files: []AffectedFile{{Path: "core.go"}, {Path: "core.go"}}}, DefaultAffectedOptions())
	affectedWantPaths(t, r, "alpha_test.go", "beta_test.go")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "file_level_precision", true)
	if r.InputPrecision != "file" {
		t.Errorf("precision = %s", r.InputPrecision)
	}
	for _, test := range r.Tests {
		if test.Confidence != "medium" {
			t.Errorf("whole-file direct confidence = %s", test.Confidence)
		}
		affectedWantDistance(t, test.Distance, 1)
		if len(test.ChangedSymbols) != 1 {
			t.Errorf("changed attribution = %+v", test.ChangedSymbols)
		}
	}
}

func TestAffectedTestsUpstreamInputGaps(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}, false)
	for _, code := range []string{"unsupported_input", "binary_input", "conflicted_input", "unreadable_input", "parse_failure", "rename", "content_outside_symbols"} {
		t.Run(code, func(t *testing.T) {
			l := AffectedLimitation{Code: code, Message: "Upstream input cannot be attributed precisely.", Paths: []string{"unavailable.dat"}, Seeds: []AffectedSymbol{}}
			r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "unavailable.dat"}}, Limitations: []AffectedLimitation{l}}, DefaultAffectedOptions())
			affectedWantPaths(t, r)
			affectedWantStatus(t, r, "unavailable")
			affectedWantCode(t, r, code, true)
			found := false
			for _, got := range r.Limitations {
				if reflect.DeepEqual(got, l) {
					found = true
				}
			}
			if !found {
				t.Errorf("upstream limitation not preserved: %+v", r.Limitations)
			}
		})
	}
}

func TestAffectedTestsSymbolFreeInputsAttemptFileEvidence(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"config.ts", "// configuration outside symbols\n"},
		{"config.test.ts", "import './config';\n"},
	}, false)
	input := AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "config.ts"}}, Limitations: []AffectedLimitation{{Code: "content_outside_symbols", Message: "Changed content is outside definitions.", Paths: []string{"config.ts"}}}}
	r := affectedSelect(t, f.db, input, DefaultAffectedOptions())
	affectedWantPaths(t, r, "config.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "content_outside_symbols", true)
	affectedEvidence(t, r.Tests[0], "direct_import")
	if len(r.Tests[0].ChangedSymbols) != 0 {
		t.Errorf("symbol-free input invented symbols: %+v", r.Tests[0])
	}
}

func TestAffectedTestsDepthBoundaries(t *testing.T) {
	// Bound 2: work ends one below, exactly at, and one beyond the bound.
	for _, distance := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("distance=%d", distance), func(t *testing.T) {
			files := []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}
			caller := "Core"
			for n := 1; n < distance; n++ {
				name := fmt.Sprintf("Bridge%d", n)
				files = append(files, affectedFixtureFile{fmt.Sprintf("bridge%d.go", n), fmt.Sprintf("package app\nfunc %s() { %s() }\n", name, caller)})
				caller = name
			}
			files = append(files, affectedFixtureFile{"end_test.go", fmt.Sprintf("package app\nfunc TestEnd() { %s() }\n", caller)})
			f := newAffectedFixture(t, files, false)
			opts := DefaultAffectedOptions()
			opts.Depth = 2
			r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
			beyond := distance > 2
			affectedWantCode(t, r, "depth_truncation", beyond)
			if beyond {
				affectedWantPaths(t, r)
				affectedWantStatus(t, r, "partial")
			} else {
				affectedWantPaths(t, r, "end_test.go")
				affectedWantStatus(t, r, "complete")
				affectedWantDistance(t, r.Tests[0].Distance, distance)
			}
		})
	}
}

func TestAffectedTestsEvidenceBoundaries(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("records=%d", count), func(t *testing.T) {
			source := "package app\nfunc TestSites() {\n"
			for n := 0; n < count; n++ {
				source += " Core()\n"
			}
			source += "}\n"
			f := newAffectedFixture(t, []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}, {"sites_test.go", source}}, false)
			opts := DefaultAffectedOptions()
			opts.MaxEvidence = 2
			r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
			affectedWantPaths(t, r, "sites_test.go")
			beyond := count > 2
			affectedWantCode(t, r, "evidence_truncation", beyond)
			status := "complete"
			if beyond {
				status = "partial"
			}
			affectedWantStatus(t, r, status)
			want := count
			if want > 2 {
				want = 2
			}
			if got := len(affectedEvidence(t, r.Tests[0], "direct_symbol")); got != want {
				t.Errorf("retained evidence = %d, want %d", got, want)
			}
		})
	}
}

func TestAffectedTestsSeedBoundaries(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("seeds=%d", count), func(t *testing.T) {
			files := []affectedFixtureFile{}
			for n := 0; n < count; n++ {
				files = append(files, affectedFixtureFile{fmt.Sprintf("core%d.go", n), fmt.Sprintf("package app\nfunc Core%d() {}\n", n)}, affectedFixtureFile{fmt.Sprintf("selected%d_test.go", n), fmt.Sprintf("package app\nfunc Test%d() { Core%d() }\n", n, n)})
			}
			f := newAffectedFixture(t, files, false)
			seeds := []AffectedSymbol{}
			for n := 0; n < count; n++ {
				seeds = append(seeds, f.seed(t, fmt.Sprintf("core%d.go", n), fmt.Sprintf("Core%d", n)))
			}
			opts := DefaultAffectedOptions()
			opts.MaxSymbols = 2
			r := affectedSelect(t, f.db, affectedHunk(seeds...), opts)
			beyond := count > 2
			affectedWantCode(t, r, "seed_truncation", beyond)
			want := []string{"selected0_test.go"}
			if count >= 2 {
				want = append(want, "selected1_test.go")
			}
			affectedWantPaths(t, r, want...)
			status := "complete"
			if beyond {
				status = "partial"
			}
			affectedWantStatus(t, r, status)
			if beyond {
				found := false
				for _, l := range r.Limitations {
					if l.Code == "seed_truncation" && reflect.DeepEqual(l.Seeds, []AffectedSymbol{seeds[2]}) {
						found = true
					}
				}
				if !found {
					t.Errorf("omitted seed identity not reported: %+v", r.Limitations)
				}
			}
		})
	}
}

func TestAffectedTestsResultBoundaries(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("candidates=%d", count), func(t *testing.T) {
			files := []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}
			// The lexically last candidate has most independent support. Limit must
			// follow ranking, not discovery or path ordering.
			for n := 0; n < count; n++ {
				body := fmt.Sprintf("package app\nfunc Test%d() {\n", n)
				for k := 0; k <= n; k++ {
					body += " Core()\n"
				}
				body += "}\n"
				files = append(files, affectedFixtureFile{fmt.Sprintf("%d_test.go", n), body})
			}
			f := newAffectedFixture(t, files, false)
			opts := DefaultAffectedOptions()
			opts.Limit = 2
			r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
			want := []string{}
			for n := count - 1; n >= 0 && len(want) < 2; n-- {
				want = append(want, fmt.Sprintf("%d_test.go", n))
			}
			affectedWantPaths(t, r, want...)
			beyond := count > 2
			affectedWantCode(t, r, "result_truncation", beyond)
			status := "complete"
			if beyond {
				status = "partial"
			}
			affectedWantStatus(t, r, status)
		})
	}
}

func TestAffectedTestsInvalidOptionsAndDatabaseError(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}, false)
	for _, tc := range []struct {
		name   string
		change func(*AffectedOptions)
	}{
		{"zero depth", func(o *AffectedOptions) { o.Depth = 0 }}, {"excess depth", func(o *AffectedOptions) { o.Depth = 6 }},
		{"zero limit", func(o *AffectedOptions) { o.Limit = 0 }}, {"negative seeds", func(o *AffectedOptions) { o.MaxSymbols = -1 }},
		{"negative evidence", func(o *AffectedOptions) { o.MaxEvidence = -1 }}, {"invalid scope", func(o *AffectedOptions) { o.Scope = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultAffectedOptions()
			tc.change(&opts)
			r, err := FindAffectedTests(f.db, AffectedInput{Precision: "hunk"}, opts)
			if err == nil || r != nil {
				t.Fatalf("invalid options returned success: %+v, %v", r, err)
			}
		})
	}
	if r, err := FindAffectedTests(f.db, AffectedInput{Precision: "guess"}, DefaultAffectedOptions()); err == nil || r != nil {
		t.Fatalf("invalid precision returned success: %+v, %v", r, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(bad, []byte("not a SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := FindAffectedTests(bad, AffectedInput{Precision: "hunk"}, DefaultAffectedOptions()); err == nil || r != nil {
		t.Fatalf("corrupt database returned success: %+v, %v", r, err)
	}
}

func TestAffectedTestsChangedTestFileHasPriority(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() {}\n"},
		{"a_test.go", "package app\nfunc TestA() { Core() }\n"},
		{"z_test.go", "package app\nfunc TestZ() {}\n"},
	}, false)
	r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Symbols: []AffectedSymbol{f.seed(t, "core.go", "Core")}, Files: []AffectedFile{{Path: "z_test.go"}}}, DefaultAffectedOptions())
	affectedWantPaths(t, r, "z_test.go", "a_test.go")
	affectedWantStatus(t, r, "complete")
	test := r.Tests[0]
	affectedWantDistance(t, test.Distance, 0)
	if test.Confidence != "high" || len(test.ChangedSymbols) != 0 {
		t.Errorf("changed test file = %+v", test)
	}
	e := affectedEvidence(t, test, "changed_test")[0]
	if e.Seed.Path != "z_test.go" || len(e.Steps) != 1 || e.Steps[0].Kind != "changed_file" {
		t.Errorf("changed test fact = %+v", e)
	}
}

func TestAffectedTestsCustomTestPathsStopTraversal(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "function core() {}\n"},
		{"checks/helper.ts", "function helper() { core(); }\n"},
		{"consumer.test.ts", "helper();\n"},
	}, false)
	seed := f.seed(t, "core.ts", "core")
	normal := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, normal, "consumer.test.ts")
	affectedWantDistance(t, normal.Tests[0].Distance, 2)
	opts := DefaultAffectedOptions()
	opts.TestPaths = []string{"checks/**"}
	r := affectedSelect(t, f.db, affectedHunk(seed), opts)
	affectedWantPaths(t, r, "checks/helper.ts")
	affectedWantStatus(t, r, "complete")
	affectedWantDistance(t, r.Tests[0].Distance, 1)
	if r.Tests[0].Confidence != "high" {
		t.Errorf("custom test confidence = %s", r.Tests[0].Confidence)
	}
}

func TestAffectedTestsDeletedFileRetainsOnlyHeuristicEvidence(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"other.ts", "function removed() {}\n"},
		{"same-name.test.ts", "removed();\n"},
		{"import.test.ts", "import './gone';\n"},
	}, false)
	old := f.seed(t, "other.ts", "removed")
	old.Path = "gone.ts"
	old.Deleted = true
	r := affectedSelect(t, f.db, affectedHunk(old), DefaultAffectedOptions())
	affectedWantPaths(t, r, "import.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "missing_deleted_seed", true)
	affectedWantCode(t, r, "heuristic_import_fallback", true)
	test := r.Tests[0]
	if test.Confidence != "low" || len(test.ChangedSymbols) != 0 {
		t.Errorf("deleted symbol must not acquire live identity: %+v", test)
	}
	e := affectedEvidence(t, test, "direct_import")[0]
	if !e.Seed.Deleted || e.Seed.Path != "gone.ts" || e.Seed.Name != "" {
		t.Errorf("deleted file seed = %+v", e.Seed)
	}
}

func TestAffectedTestsSameLanguageAmbiguity(t *testing.T) {
	for _, intermediate := range []bool{false, true} {
		t.Run(fmt.Sprintf("intermediate=%v", intermediate), func(t *testing.T) {
			files := []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}
			ambiguous := "Core"
			if intermediate {
				ambiguous = "Bridge"
				files = append(files, affectedFixtureFile{"bridge.go", "package app\nfunc Bridge() { Core() }\n"})
			}
			files = append(files, affectedFixtureFile{"other/duplicate.go", fmt.Sprintf("package other\nfunc %s() {}\n", ambiguous)}, affectedFixtureFile{"selected_test.go", fmt.Sprintf("package app\nfunc TestSelected() { %s() }\n", ambiguous)})
			f := newAffectedFixture(t, files, false)
			opts := DefaultAffectedOptions()
			opts.Scope = ResolveScopeSame
			r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
			affectedWantPaths(t, r, "selected_test.go")
			affectedWantStatus(t, r, "partial")
			affectedWantCode(t, r, "ambiguous_definitions", true)
			if r.Tests[0].Confidence != "low" {
				t.Errorf("ambiguous explanation = %+v", r.Tests[0])
			}
			kind, distance := "direct_symbol", 1
			if intermediate {
				kind, distance = "transitive_symbol", 2
			}
			e := affectedEvidence(t, r.Tests[0], kind)[0]
			if !e.Ambiguous {
				t.Errorf("ambiguity not propagated: %+v", e)
			}
			affectedWantDistance(t, e.Distance, distance)
		})
	}
}

func TestAffectedTestsMethodSeedReportsIndirectDispatch(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"worker.go", "package app\ntype Worker struct {}\nfunc (w Worker) Run() {}\n"},
		{"worker_test.go", "package app\nfunc TestWorker() {\n w.Run()\n}\n"},
	}, false)
	seed := f.seed(t, "worker.go", "Run")
	if seed.Kind != "method" {
		t.Fatalf("fixture requires method seed, got %+v", seed)
	}
	r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
	affectedWantPaths(t, r, "worker_test.go")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "indirect_dispatch", true)
	affectedEvidence(t, r.Tests[0], "direct_symbol")
}

func TestAffectedTestsDepthBoundaryCycleIsExhausted(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() {}\n"},
		{"a.go", "package app\nfunc A() {\n Core()\n B()\n}\n"},
		{"b.go", "package app\nfunc B() { A() }\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.Depth = 2
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
	affectedWantPaths(t, r)
	affectedWantStatus(t, r, "complete")
	affectedWantCode(t, r, "depth_truncation", false)
}

func TestAffectedTestsImportDepthBoundaries(t *testing.T) {
	for _, distance := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("distance=%d", distance), func(t *testing.T) {
			files := []affectedFixtureFile{{"config.ts", "// symbol-free configuration\n"}}
			previous := "config"
			for n := 1; n < distance; n++ {
				name := fmt.Sprintf("link%d", n)
				files = append(files, affectedFixtureFile{name + ".ts", fmt.Sprintf("export * from './%s';\n", previous)})
				previous = name
			}
			files = append(files, affectedFixtureFile{"selected.test.ts", fmt.Sprintf("import './%s';\n", previous)})
			f := newAffectedFixture(t, files, false)
			opts := DefaultAffectedOptions()
			opts.Depth = 2
			r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "config.ts"}}}, opts)
			affectedWantStatus(t, r, "partial") // Imports are always explicitly heuristic.
			affectedWantCode(t, r, "depth_truncation", distance > 2)
			if distance > 2 {
				affectedWantPaths(t, r)
			} else {
				affectedWantPaths(t, r, "selected.test.ts")
				affectedWantDistance(t, r.Tests[0].Distance, distance)
				kind := "direct_import"
				if distance > 1 {
					kind = "reverse_import"
				}
				e := affectedEvidence(t, r.Tests[0], kind)[0]
				if len(e.Steps) != distance+1 {
					t.Errorf("import chain = %+v", e.Steps)
				}
			}
		})
	}
}

func TestAffectedTestsImportEvidenceBoundaries(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("imports=%d", count), func(t *testing.T) {
			files := []affectedFixtureFile{{"config.ts", "// no definitions\n"}}
			for n := 0; n < count; n++ {
				files = append(files, affectedFixtureFile{fmt.Sprintf("%d.test.ts", n), "import './config';\n"})
			}
			f := newAffectedFixture(t, files, false)
			opts := DefaultAffectedOptions()
			opts.MaxEvidence = 2
			r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "config.ts"}}}, opts)
			affectedWantCode(t, r, "evidence_truncation", count > 2)
			want := []string{"0.test.ts"}
			if count >= 2 {
				want = append(want, "1.test.ts")
			}
			affectedWantPaths(t, r, want...)
			affectedWantStatus(t, r, "partial")
		})
	}
}

func TestAffectedTestsZeroBudgetsAreUnlimited(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n"},
		{"selected_test.go", "package app\nfunc TestSelected() {\n Alpha()\n Beta()\n Gamma()\n}\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.MaxSymbols = 0
	opts.MaxEvidence = 0
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Alpha"), f.seed(t, "core.go", "Beta"), f.seed(t, "core.go", "Gamma")), opts)
	affectedWantPaths(t, r, "selected_test.go")
	affectedWantStatus(t, r, "complete")
	if len(affectedEvidence(t, r.Tests[0], "direct_symbol")) != 3 || len(r.Tests[0].ChangedSymbols) != 3 {
		t.Errorf("zero budget discarded support: %+v", r.Tests[0])
	}
	if r.Bounds.MaxSymbols != 0 || r.Bounds.MaxEvidence != 0 {
		t.Errorf("effective bounds = %+v", r.Bounds)
	}
}

func TestAffectedTestsQueryErrorsAreNotEmptySuccess(t *testing.T) {
	for _, table := range []string{"symbols", "refs", "imports"} {
		t.Run(table, func(t *testing.T) {
			f := newAffectedFixture(t, []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}, false)
			input := affectedHunk(f.seed(t, "core.go", "Core"))
			// Warm the normal connection before damage, so schema setup cannot silently
			// recreate a dropped table. SQL is used only to inject an operational fault.
			affectedSelect(t, f.db, input, DefaultAffectedOptions())
			db, err := sql.Open("sqlite3", f.db)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			if r, err := FindAffectedTests(f.db, input, DefaultAffectedOptions()); err == nil || r != nil {
				t.Fatalf("%s query failure returned successful result: %+v, %v", table, r, err)
			}
		})
	}
}

func TestAffectedTestsDoNotSelectSiblingWorktreeTests(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{{"core.go", "package app\nfunc Core() {}\n"}}, false)
	runPhase3Git(t, f.root, "add", ".")
	runPhase3Git(t, f.root, "-c", "user.name=Cymbal Test", "-c", "user.email=cymbal@example.invalid", "commit", "-m", "fixture")
	sibling := filepath.Join(t.TempDir(), "sibling")
	runPhase3Git(t, f.root, "worktree", "add", "--detach", sibling, "HEAD")
	writePhase3File(t, sibling, "sibling_test.go", "package app\nfunc TestSibling() { Core() }\n")
	if _, err := Index(sibling, "", Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	siblingDB, err := RepoDBPath(sibling)
	if err != nil {
		t.Fatal(err)
	}
	positive := affectedSelect(t, siblingDB, affectedHunk(f.seed(t, "core.go", "Core")), DefaultAffectedOptions())
	affectedWantPaths(t, positive, "sibling_test.go")
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), DefaultAffectedOptions())
	affectedWantPaths(t, r)
	affectedWantStatus(t, r, "complete")
}

func TestAffectedTestsUnsupportedAndSymbolFreeWithoutEvidence(t *testing.T) {
	for _, tc := range []affectedFixtureFile{{"unsupported.dat", "opaque data\n"}, {"empty.ts", "// supported, but no definitions or dependencies\n"}} {
		t.Run(tc.path, func(t *testing.T) {
			f := newAffectedFixture(t, []affectedFixtureFile{tc}, false)
			input := AffectedInput{Precision: "file", Files: []AffectedFile{{Path: tc.path}}}
			if tc.path == "unsupported.dat" {
				input.Limitations = []AffectedLimitation{{Code: "unsupported_input", Message: "Unsupported source language.", Paths: []string{tc.path}}}
			}
			r := affectedSelect(t, f.db, input, DefaultAffectedOptions())
			affectedWantPaths(t, r)
			affectedWantStatus(t, r, "unavailable")
			affectedWantCode(t, r, "no_analyzable_input", true)
			affectedWantCode(t, r, "file_level_precision", true)
			if tc.path == "unsupported.dat" {
				affectedWantCode(t, r, "unsupported_input", true)
			}
		})
	}
}

func TestAffectedTestsMixedUsableAndUnavailableInput(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() {}\n"},
		{"direct_test.go", "package app\nfunc TestDirect() { Core() }\n"},
		{"opaque.dat", "unsupported content\n"},
	}, false)
	input := affectedHunk(f.seed(t, "core.go", "Core"))
	input.Files = []AffectedFile{{Path: "opaque.dat"}}
	input.Limitations = []AffectedLimitation{{Code: "unsupported_input", Message: "Unsupported source language.", Paths: []string{"opaque.dat"}}}
	r := affectedSelect(t, f.db, input, DefaultAffectedOptions())
	affectedWantPaths(t, r, "direct_test.go")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "unsupported_input", true)
	if r.Tests[0].Confidence != "high" {
		t.Errorf("an unrelated gap must not lower actual direct evidence strength: %+v", r.Tests[0])
	}
}

func TestAffectedTestsPackageDirectoryImportCandidate(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"pkg/hidden.go", "package pkg\n// no usable symbol seed\n"},
		{"package_test.go", "package app\nimport \"example.com/app/pkg\"\n"},
		{"other_test.go", "package app\nimport \"example.com/app/other\"\n"},
	}, false)
	r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "pkg/hidden.go"}}}, DefaultAffectedOptions())
	affectedWantPaths(t, r, "package_test.go")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "heuristic_import_fallback", true)
	e := affectedEvidence(t, r.Tests[0], "direct_import")[0]
	if e.Steps[1].RawImport != "example.com/app/pkg" || e.Steps[1].Target != "pkg/hidden.go" || r.Tests[0].Confidence != "low" {
		t.Errorf("package heuristic = %+v", e)
	}
}

func TestAffectedTestsImportsCannotWinWithMoreSupport(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "function core() {}\n"},
		{"z-symbol.test.ts", "core();\n"},
		{"a-import.test.ts", "import './core';\nimport './core.ts';\nimport './elsewhere/core';\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "z-symbol.test.ts", "a-import.test.ts")
	affectedWantStatus(t, r, "partial")
	if r.Tests[0].Confidence != "high" || r.Tests[1].Confidence != "low" {
		t.Errorf("evidence confidence ordering = %+v", r.Tests)
	}
	if len(affectedEvidence(t, r.Tests[0], "direct_symbol")) != 1 || len(affectedEvidence(t, r.Tests[1], "direct_import")) != 3 {
		t.Fatalf("fixture does not establish stronger independent import support: %+v", r.Tests)
	}
}
