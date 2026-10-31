package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/1broseidon/cymbal/index"
)

func TestChangedAnalysisBinaryHeaderRetainsUnquotedSpacesAndUnicode(t *testing.T) {
	t.Parallel()
	// An embedded " b/" must not be mistaken for the separator between paths.
	path := "目录 b/sub/a b.bin"
	repo := changedAnalysisRepo(t, map[string]string{path: "old\x00binary"})
	writeFile(t, repo, path, "new\x00binary")
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	if a.Skipped != 1 || !reflect.DeepEqual(a.Files, []index.AffectedFile{{Path: path}}) || !changedAnalysisHasLimitation(a, "binary_input", path) {
		t.Fatalf("headerless binary path must not be split on spaces: %+v", a)
	}
}

func TestChangedAnalysisContentRenameUsesEachSidesLanguage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, oldPath, newPath string
		wantSeeds              int
	}{
		{"unsupported old", "old name.txt", "new name.go", 1},
		{"unsupported new", "old name.go", "new name.txt", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := "package p\n\nfunc Changed() int { return 1 }\n\nfunc StableOne() int { return 10 }\n\nfunc StableTwo() int { return 20 }\n\nfunc StableThree() int { return 30 }\n"
			repo := changedAnalysisRepo(t, map[string]string{tc.oldPath: content})
			gitChanged(t, repo, "mv", tc.oldPath, tc.newPath)
			writeFile(t, repo, tc.newPath, strings.Replace(content, "return 1 }", "return 2 }", 1))
			gitChanged(t, repo, "add", tc.newPath)
			a := analyzeChangedDiff(repo, true, "", changedAnalysisDiff(t, repo, true, ""))
			if !reflect.DeepEqual(a.ChangedOrder, []string{"Changed"}) || len(a.Deleted) != 0 || a.Skipped != 0 || len(a.Symbols) != tc.wantSeeds {
				t.Fatalf("rename must use each path's language without false deletions: %+v", a)
			}
			unsupported := tc.oldPath
			if tc.wantSeeds == 0 {
				unsupported = tc.newPath
			}
			if !changedAnalysisHasLimitation(a, "unsupported_input", unsupported) || !changedAnalysisHasLimitation(a, "renamed_input", tc.oldPath) || !changedAnalysisHasLimitation(a, "renamed_input", tc.newPath) {
				t.Fatalf("both path and per-side gaps must remain visible: %+v", a.Limitations)
			}
			if tc.wantSeeds == 1 && (a.Symbols[0].Path != tc.newPath || a.Symbols[0].Name != "Changed" || a.Symbols[0].Deleted) {
				t.Fatalf("live seed must use the renamed new-side identity: %+v", a.Symbols)
			}
		})
	}
}

func TestChangedAnalysisOldOnlyEditUsesNewSymbolCoordinates(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{"p.go": "package p\n\nfunc Changed() {\n println(1)\n println(2)\n}\n"})
	writeFile(t, repo, "p.go", "package p\n\nfunc Changed() {\n println(2)\n}\n")
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	want := []index.AffectedSymbol{{Path: "p.go", Name: "Changed", Kind: "function", Language: "go", StartLine: 3, EndLine: 5}}
	if !reflect.DeepEqual(a.Symbols, want) || len(a.Deleted) != 0 {
		t.Fatalf("deleted body line must map to surviving new coordinates: %+v", a)
	}
}

func TestChangedAnalysisModeOnlyKeepsLegacyNoContentTreatment(t *testing.T) {
	t.Parallel()
	path := "script with space.sh"
	repo := changedAnalysisRepo(t, map[string]string{path: "#!/bin/sh\nrun() { :; }\n"})
	gitChanged(t, repo, "config", "core.fileMode", "true")
	if err := os.Chmod(filepath.Join(repo, path), 0o755); err != nil {
		t.Fatal(err)
	}
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	if a.Skipped != 0 || len(a.Changed) != 0 || len(a.Deleted) != 0 || len(a.Symbols) != 0 || !reflect.DeepEqual(a.Files, []index.AffectedFile{{Path: path}}) {
		t.Fatalf("mode-only metadata must not become a skipped file or a symbol edit: %+v", a)
	}
	if !changedAnalysisHasLimitation(a, "file_level_precision", path) {
		t.Fatalf("metadata-only gap missing: %+v", a.Limitations)
	}
}

func TestChangedAnalysisCopyDoesNotDeleteSource(t *testing.T) {
	t.Parallel()
	// Git copy detection is not enabled by changedDiffArgs, but valid incoming
	// patches can carry it. Extended paths are not prefixed with a/ or b/.
	diff := []byte("diff --git a/a/source name.go b/b/copy name.go\nsimilarity index 100%\ncopy from a/source name.go\ncopy to b/copy name.go\n")
	a := analyzeChangedDiff(t.TempDir(), false, "", diff)
	want := []index.AffectedFile{{Path: "a/source name.go"}, {Path: "b/copy name.go"}}
	if !reflect.DeepEqual(a.Files, want) || a.Skipped != 0 || len(a.Deleted) != 0 || !changedAnalysisHasLimitation(a, "copied_input", "b/copy name.go") {
		t.Fatalf("copy source must remain live: %+v", a)
	}
}

func TestChangedAnalysisBinaryPatchKeepsAdditionPath(t *testing.T) {
	t.Parallel()
	diff := []byte("diff --git \"a/tab\\t.bin\" \"b/tab\\t.bin\"\nnew file mode 100644\nindex 000..abc\nGIT binary patch\nliteral 4\nnot source\n")
	a := analyzeChangedDiff(t.TempDir(), false, "", diff)
	if a.Skipped != 1 || !reflect.DeepEqual(a.Files, []index.AffectedFile{{Path: "tab\t.bin"}}) || !changedAnalysisHasLimitation(a, "binary_input", "tab\t.bin") {
		t.Fatalf("binary patch addition must retain its new path: %+v", a)
	}
}

func TestChangedAnalysisNewPathWinsOverRemovedPath(t *testing.T) {
	t.Parallel()
	diff := []byte("diff --git a/a.go b/b.go\nsimilarity index 100%\nrename from a.go\nrename to b.go\ndiff --git a/b.go b/a.go\nsimilarity index 100%\nrename from b.go\nrename to a.go\n")
	a := analyzeChangedDiff(t.TempDir(), false, "", diff)
	want := []index.AffectedFile{{Path: "a.go"}, {Path: "b.go"}}
	if !reflect.DeepEqual(a.Files, want) {
		t.Fatalf("a path known to exist on a new side must not be marked deleted: %+v", a.Files)
	}
}
