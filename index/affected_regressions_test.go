package index

import (
	"reflect"
	"testing"
)

func TestAffectedTestsCombinedDepthAndEvidenceCycle(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() { A() }\n"},
		{"a.go", "package app\nfunc A() {\n Core()\n A()\n}\n"},
		{"z_test.go", "package app\nfunc TestEnd() { A() }\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.Depth, opts.MaxEvidence = 1, 1
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
	affectedWantPaths(t, r)
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "evidence_truncation", true)
}

func TestAffectedTestsFallbackIsPerChangedSymbol(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "export function alpha() {}\nexport function beta() {}\n"},
		{"direct.test.ts", "alpha();\n"},
		{"hub.ts", "export * from './core';\n"},
		{"indirect.test.ts", "import './hub';\n"},
		{"import.test.ts", "import './core';\n"},
		{"core.spec.ts", "// naming-only candidate\n"},
	}, false)
	alpha, beta := f.seed(t, "core.ts", "alpha"), f.seed(t, "core.ts", "beta")
	r := affectedSelect(t, f.db, affectedHunk(alpha, beta), DefaultAffectedOptions())
	affectedWantPaths(t, r, "direct.test.ts", "import.test.ts", "core.spec.ts", "indirect.test.ts")
	for _, test := range r.Tests {
		switch test.Path {
		case "indirect.test.ts":
			e := affectedEvidence(t, test, "reverse_import")
			if len(e) != 1 || e[0].Seed != beta || !reflect.DeepEqual(test.ChangedSymbols, []AffectedSymbol{beta}) {
				t.Fatalf("fallback lost seed attribution: %+v", test)
			}
		case "import.test.ts", "core.spec.ts":
			kind := "direct_import"
			if test.Path == "core.spec.ts" {
				kind = "colocated"
			}
			e := affectedEvidence(t, test, kind)
			if len(e) != 2 || len(test.ChangedSymbols) != 2 {
				t.Fatalf("file candidate lost changed symbols: %+v", test)
			}
		}
	}
}

func TestAffectedTestsImportTerminalDoesNotDuplicateSupport(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "// no symbols\n"},
		{"a/hub.ts", "export * from '../core';\n"},
		{"b/hub.ts", "export * from '../core';\n"},
		{"end.test.ts", "import './hub';\n"},
	}, false)
	r := affectedSelect(t, f.db, AffectedInput{Precision: "hunk", Files: []AffectedFile{{Path: "core.ts"}}}, DefaultAffectedOptions())
	affectedWantPaths(t, r, "end.test.ts")
	e := affectedEvidence(t, r.Tests[0], "reverse_import")
	if len(e) != 1 {
		t.Fatalf("one raw terminal import counted more than once: %+v", e)
	}
	if len(e[0].Steps) != 3 || e[0].Steps[1].Path != "a/hub.ts" || e[0].Steps[2].Target != "a/hub.ts" {
		t.Fatalf("noncanonical import explanation: %+v", e)
	}
}

func TestAffectedTestsTerminalOwnerDoesNotCauseAmbiguity(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() {}\n"},
		{"one_test.go", "package app\nfunc TestSame() { Core() }\n"},
		{"other/two_test.go", "package other\nfunc TestSame() {}\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "one_test.go")
	affectedWantStatus(t, r, "complete")
	affectedWantCode(t, r, "ambiguous_definitions", false)
	if r.Tests[0].Confidence != "high" || affectedEvidence(t, r.Tests[0], "direct_symbol")[0].Ambiguous {
		t.Fatalf("terminal owner lowered evidence: %+v", r.Tests[0])
	}
}

func TestAffectedTestsPythonMemberDispatch(t *testing.T) {
	for _, intermediate := range []bool{false, true} {
		name := "seed"
		if intermediate {
			name = "intermediate"
		}
		t.Run(name, func(t *testing.T) {
			f := newAffectedFixture(t, []affectedFixtureFile{
				{"core.py", "def core():\n    pass\n"},
				{"worker.py", "class Worker:\n    def run(self):\n        core()\n"},
				{"test_dispatch.py", "worker.run()\n"},
			}, false)
			seed := f.seed(t, "worker.py", "run")
			if seed.Kind != "function" {
				t.Fatalf("fixture must retain parser identity: %+v", seed)
			}
			if intermediate {
				seed = f.seed(t, "core.py", "core")
			}
			r := affectedSelect(t, f.db, affectedHunk(seed), DefaultAffectedOptions())
			affectedWantPaths(t, r, "test_dispatch.py")
			affectedWantStatus(t, r, "partial")
			affectedWantCode(t, r, "indirect_dispatch", true)
		})
	}
}

func TestAffectedTestsNestedFunctionIsNotMemberDispatch(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"nested.py", "def outer():\n    def inner():\n        pass\n    return inner\n"},
		{"test_consumer.py", "inner()\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "nested.py", "inner")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "test_consumer.py")
	affectedWantStatus(t, r, "complete")
	affectedWantCode(t, r, "indirect_dispatch", false)
}

func TestAffectedTestsEvidenceBudgetCountsSharedRecordsOnce(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Alpha() {}\nfunc Beta() {}\n"},
		{"bridge.go", "package app\nfunc Bridge() {\n Alpha()\n Beta()\n}\n"},
		{"end_test.go", "package app\nfunc TestEnd() { Bridge() }\n"},
	}, false)
	opts := DefaultAffectedOptions()
	opts.MaxEvidence = 3
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Alpha"), f.seed(t, "core.go", "Beta")), opts)
	affectedWantPaths(t, r, "end_test.go")
	affectedWantStatus(t, r, "complete")
	affectedWantCode(t, r, "evidence_truncation", false)
	if len(affectedEvidence(t, r.Tests[0], "transitive_symbol")) != 2 {
		t.Fatalf("shared terminal record lost a seed: %+v", r.Tests[0])
	}
}

func TestAffectedTestsBoundaryCyclesConsumeEvidenceBudget(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.go", "package app\nfunc Core() { A() }\n"},
		{"a.go", "package app\nfunc A() {\n Core()\n A()\n}\n"},
	}, false)
	for _, budget := range []int{2, 3} {
		opts := DefaultAffectedOptions()
		opts.Depth, opts.MaxEvidence = 1, budget
		r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.go", "Core")), opts)
		affectedWantPaths(t, r)
		status := "complete"
		if budget == 2 {
			status = "partial"
		}
		affectedWantStatus(t, r, status)
		affectedWantCode(t, r, "evidence_truncation", budget == 2)
		affectedWantCode(t, r, "depth_truncation", false)
	}
}

func TestAffectedTestsImplementingClassRetainsDispatchGap(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"service.ts", "interface Worker { run(): void }\nexport class Service implements Worker { run() {} }\n"},
		{"consumer.test.ts", "new Service();\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "service.ts", "Service")), DefaultAffectedOptions())
	affectedWantPaths(t, r, "consumer.test.ts")
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "indirect_dispatch", true)
}

func TestAffectedTestsProductionTopLevelReferenceRetainsGap(t *testing.T) {
	f := newAffectedFixture(t, []affectedFixtureFile{
		{"core.ts", "export function core() {}\n"},
		{"bridge.ts", "core();\n"},
	}, false)
	r := affectedSelect(t, f.db, affectedHunk(f.seed(t, "core.ts", "core")), DefaultAffectedOptions())
	affectedWantPaths(t, r)
	affectedWantStatus(t, r, "partial")
	affectedWantCode(t, r, "unresolved_reference_boundary", true)
}
