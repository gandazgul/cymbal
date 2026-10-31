package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/1broseidon/cymbal/index"
)

func changedAnalysisRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for path, content := range files {
		writeFile(t, repo, path, content)
	}
	gitChanged(t, repo, "init")
	gitChanged(t, repo, "add", ".")
	gitChanged(t, repo, "-c", "user.name=x", "-c", "user.email=a@b.c", "commit", "-m", "initial")
	return repo
}

func changedAnalysisDiff(t *testing.T, repo string, staged bool, base string) []byte {
	t.Helper()
	args, _, err := changedDiffArgs(staged, base)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func changedAnalysisHasLimitation(a changedAnalysis, code, path string) bool {
	for _, l := range a.Limitations {
		if l.Code != code {
			continue
		}
		for _, p := range l.Paths {
			if p == path {
				return true
			}
		}
	}
	return false
}

func TestChangedAnalysisPreservesSameNameDefinitionIdentities(t *testing.T) {
	t.Parallel()
	old := "class A:\n    def shared(self):\n        return 1\n\nclass B:\n    def shared(self):\n        return 2\n"
	repo := changedAnalysisRepo(t, map[string]string{"p.py": old})
	writeFile(t, repo, "p.py", strings.ReplaceAll(old, "return ", "return 10 + "))
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	if !reflect.DeepEqual(a.ChangedOrder, []string{"shared"}) || len(a.Changed["shared"]) != 1 {
		t.Fatalf("legacy names must remain deduplicated: %+v", a)
	}
	want := []index.AffectedSymbol{
		{Path: "p.py", Name: "shared", Kind: "function", Language: "python", StartLine: 2, EndLine: 3},
		{Path: "p.py", Name: "shared", Kind: "function", Language: "python", StartLine: 6, EndLine: 7},
	}
	if !reflect.DeepEqual(a.Symbols, want) {
		t.Fatalf("definition identities = %+v, want %+v", a.Symbols, want)
	}
	if a.Skipped != 0 || len(a.Deleted) != 0 {
		t.Fatalf("modification must not become a skip or deletion: %+v", a)
	}
}

func TestChangedAnalysisUsesEachModesActualBlobs(t *testing.T) {
	t.Parallel()
	old := "package p\n\nfunc Alpha() int { return 1 }\n\nfunc Gamma() int { return 3 }\n"
	repo := changedAnalysisRepo(t, map[string]string{"p.go": old})
	staged := strings.Replace(old, "return 1", "return 111", 1)
	writeFile(t, repo, "p.go", staged)
	gitChanged(t, repo, "add", "p.go")
	writeFile(t, repo, "p.go", strings.Replace(staged, "return 3", "return 333", 1))
	for _, tc := range []struct {
		name   string
		staged bool
		base   string
		want   []string
	}{
		{"unstaged", false, "", []string{"Gamma"}},
		{"staged", true, "", []string{"Alpha"}},
		{"base", false, "HEAD", []string{"Alpha", "Gamma"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := analyzeChangedDiff(repo, tc.staged, tc.base, changedAnalysisDiff(t, repo, tc.staged, tc.base))
			var names []string
			for _, s := range a.Symbols {
				names = append(names, s.Name)
				if s.Path != "p.go" || s.Deleted || s.Language != "go" || s.Kind != "function" {
					t.Errorf("incorrect symbol identity: %+v", s)
				}
			}
			if !reflect.DeepEqual(a.ChangedOrder, tc.want) || !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("legacy names %v, precise names %v, want %v", a.ChangedOrder, names, tc.want)
			}
		})
	}
}

func TestChangedAnalysisRetainsDeletedFileAndSymbolLocation(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{"gone.go": "package p\n\nfunc Gone() {}\n"})
	if err := os.Remove(filepath.Join(repo, "gone.go")); err != nil {
		t.Fatal(err)
	}
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	if !reflect.DeepEqual(a.DeletedOrder, []string{"Gone"}) || len(a.Changed) != 0 {
		t.Fatalf("legacy deletion = %+v", a)
	}
	if want := []index.AffectedFile{{Path: "gone.go", Deleted: true}}; !reflect.DeepEqual(a.Files, want) {
		t.Errorf("files = %+v, want %+v", a.Files, want)
	}
	want := []index.AffectedSymbol{{Path: "gone.go", Name: "Gone", Kind: "function", Language: "go", StartLine: 3, EndLine: 3, Deleted: true}}
	if !reflect.DeepEqual(a.Symbols, want) {
		t.Errorf("symbols = %+v, want %+v", a.Symbols, want)
	}
}

func TestChangedAnalysisFailedNewSideDoesNotCreateDeletionOrLiveSeed(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{"p.go": "package p\n\nfunc Known() {}\n"})
	writeFile(t, repo, "p.go", "package p\x00\n")
	// Force a textual patch so both blob failures, rather than Git's binary
	// block shortcut, are exercised. The actual blobs still come from Git/disk.
	out, err := exec.Command("git", "-C", repo, "diff", "--text", "--no-ext-diff", "--no-color").Output()
	if err != nil {
		t.Fatal(err)
	}
	a := analyzeChangedDiff(repo, false, "", out)
	if len(a.Deleted) != 0 || len(a.Symbols) != 0 || a.Skipped != 0 || !reflect.DeepEqual(a.ChangedOrder, []string{"Known"}) {
		t.Fatalf("failed new side must retain only legacy old-side modification: %+v", a)
	}
	if !changedAnalysisHasLimitation(a, "binary_input", "p.go") || !changedAnalysisHasLimitation(a, "missing_live_seed", "p.go") {
		t.Fatalf("new-side failure and unknown live identity must remain visible: %+v", a.Limitations)
	}
	for _, l := range a.Limitations {
		if l.Code == "missing_live_seed" && (len(l.Seeds) != 1 || l.Seeds[0].Name != "Known" || l.Seeds[0].StartLine != 3 || l.Seeds[0].Deleted) {
			t.Fatalf("unknown old-side identity was lost or marked deleted: %+v", l)
		}
	}
}

func TestChangedAnalysisRetainsBothSideReadFailures(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{"p.go": "package p\nfunc Known() {}\n"})
	writeFile(t, repo, "p.go", "package p\nfunc Changed() {}\n")
	diff := changedAnalysisDiff(t, repo, false, "")
	gitChanged(t, repo, "rm", "--cached", "p.go")
	if err := os.Remove(filepath.Join(repo, "p.go")); err != nil {
		t.Fatal(err)
	}
	a := analyzeChangedDiff(repo, false, "", diff)
	var messages []string
	for _, l := range a.Limitations {
		if l.Code == "unreadable_input" {
			messages = append(messages, l.Message)
		}
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "new-side") || !strings.Contains(messages[1], "old-side") {
		t.Fatalf("both failures must be retained: %+v", a.Limitations)
	}
	if a.Skipped != 1 || len(a.Deleted) != 0 || len(a.Symbols) != 0 || !reflect.DeepEqual(a.Files, []index.AffectedFile{{Path: "p.go"}}) {
		t.Fatalf("unreadable is not proof of deletion: %+v", a)
	}
}

func TestChangedAnalysisRetainsOldSideFailureWithLiveSeed(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{"p.go": "package p\nfunc Known() {}\n"})
	writeFile(t, repo, "p.go", "package p\nfunc Changed() {}\n")
	diff := changedAnalysisDiff(t, repo, false, "")
	gitChanged(t, repo, "rm", "--cached", "p.go")
	a := analyzeChangedDiff(repo, false, "", diff)
	if !changedAnalysisHasLimitation(a, "unreadable_input", "p.go") || a.Skipped != 0 || len(a.Deleted) != 0 || len(a.Symbols) != 1 || a.Symbols[0].Name != "Changed" {
		t.Fatalf("successful new-side attribution must survive old-side failure: %+v", a)
	}
}

func TestChangedAnalysisReportsUnsupportedAndOutsideSymbolContent(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{
		"notes.txt": "old notes\n",
		"p.go":      "package p\n\nfunc Unchanged() {}\n",
	})
	writeFile(t, repo, "notes.txt", "new notes\n")
	writeFile(t, repo, "p.go", "// package comment\npackage p\n\nfunc Unchanged() {}\n")
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	if a.Skipped != 1 || len(a.Symbols) != 0 || len(a.Changed) != 0 || len(a.Files) != 2 {
		t.Fatalf("file-only content must remain visible without invented symbols: %+v", a)
	}
	if !changedAnalysisHasLimitation(a, "unsupported_input", "notes.txt") || !changedAnalysisHasLimitation(a, "content_outside_symbols", "p.go") {
		t.Fatalf("unsupported/outside-symbol gaps missing: %+v", a.Limitations)
	}
}

func TestChangedAnalysisPureRenameKeepsLegacyCountersAndRichPaths(t *testing.T) {
	t.Parallel()
	oldPath, newPath := "a/old name\t.go", "b/new name\t.go"
	repo := changedAnalysisRepo(t, map[string]string{oldPath: "package p\nfunc Known() {}\n"})
	if err := os.MkdirAll(filepath.Join(repo, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitChanged(t, repo, "mv", oldPath, newPath)
	a := analyzeChangedDiff(repo, true, "", changedAnalysisDiff(t, repo, true, ""))
	if a.Skipped != 0 || len(a.Changed) != 0 || len(a.Deleted) != 0 || len(a.Symbols) != 0 {
		t.Fatalf("pure rename must keep legacy no-content treatment: %+v", a)
	}
	want := []index.AffectedFile{{Path: oldPath, Deleted: true}, {Path: newPath}}
	if !reflect.DeepEqual(a.Files, want) || !changedAnalysisHasLimitation(a, "renamed_input", oldPath) || !changedAnalysisHasLimitation(a, "renamed_input", newPath) {
		t.Fatalf("rename paths/gap lost: %+v", a)
	}
}

func TestChangedAnalysisBinaryPathsAndDeletion(t *testing.T) {
	t.Parallel()
	path := "images/a b/name\t.bin"
	repo := changedAnalysisRepo(t, map[string]string{path: "old\x00binary"})
	if err := os.Remove(filepath.Join(repo, path)); err != nil {
		t.Fatal(err)
	}
	a := analyzeChangedDiff(repo, false, "", changedAnalysisDiff(t, repo, false, ""))
	if a.Skipped != 1 || len(a.Symbols) != 0 || !reflect.DeepEqual(a.Files, []index.AffectedFile{{Path: path, Deleted: true}}) || !changedAnalysisHasLimitation(a, "binary_input", path) {
		t.Fatalf("binary deletion metadata lost: %+v", a)
	}
}

func TestChangedAnalysisConflictKeepsQuotedPathAndLegacyCount(t *testing.T) {
	t.Parallel()
	diff := []byte("diff --cc \"a/conflicted name\\t.go\"\nindex abc,def..000\n--- a/ignored\n+++ b/ignored\n@@@ -1 -1 +1 @@@\n++conflict\n")
	a := analyzeChangedDiff(t.TempDir(), false, "", diff)
	path := "a/conflicted name\t.go"
	if a.Conflicted != 1 || a.Skipped != 0 || len(a.Symbols) != 0 || !reflect.DeepEqual(a.Files, []index.AffectedFile{{Path: path}}) || !changedAnalysisHasLimitation(a, "conflicted_input", path) {
		t.Fatalf("conflict must keep path/count without parsing combined hunks: %+v", a)
	}
}

func TestChangedAnalysisEmptyDiffHasEmptyCollections(t *testing.T) {
	a := analyzeChangedDiff(t.TempDir(), false, "", nil)
	if a.Files == nil || a.Symbols == nil || a.Limitations == nil || a.ChangedOrder == nil || a.DeletedOrder == nil || len(a.Files)+len(a.Symbols)+len(a.Limitations)+len(a.Changed)+len(a.Deleted) != 0 || a.Skipped != 0 || a.Conflicted != 0 {
		t.Fatalf("empty diff must be an empty complete input, not null collections: %+v", a)
	}
}

func TestChangedAnalysisModeSeparatesRenamedDefinitions(t *testing.T) {
	t.Parallel()
	repo := changedAnalysisRepo(t, map[string]string{"p.go": "package p\nfunc Original() {}\n"})
	writeFile(t, repo, "p.go", "package p\nfunc StagedOnly() {}\n")
	gitChanged(t, repo, "add", "p.go")
	writeFile(t, repo, "p.go", "package p\nfunc WorkingOnly() {}\n")
	for _, tc := range []struct {
		name                string
		staged              bool
		base, live, deleted string
	}{
		{"staged", true, "", "StagedOnly", "Original"},
		{"unstaged", false, "", "WorkingOnly", "StagedOnly"},
		{"base", false, "HEAD", "WorkingOnly", "Original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := analyzeChangedDiff(repo, tc.staged, tc.base, changedAnalysisDiff(t, repo, tc.staged, tc.base))
			if !reflect.DeepEqual(a.ChangedOrder, []string{tc.live}) || !reflect.DeepEqual(a.DeletedOrder, []string{tc.deleted}) {
				t.Fatalf("wrong blob version: live %v, deleted %v; want %s and %s", a.ChangedOrder, a.DeletedOrder, tc.live, tc.deleted)
			}
			var live, deleted []string
			for _, s := range a.Symbols {
				if s.Deleted {
					deleted = append(deleted, s.Name)
				} else {
					live = append(live, s.Name)
				}
			}
			if !reflect.DeepEqual(live, []string{tc.live}) || !reflect.DeepEqual(deleted, []string{tc.deleted}) {
				t.Fatalf("wrong rich blob identities: %+v", a.Symbols)
			}
		})
	}
}
