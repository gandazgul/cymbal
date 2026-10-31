package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/1broseidon/cymbal/index"
	"github.com/spf13/cobra"
)

// These tests change cwd and process streams. Keep them serial.
func affectedCLI(t *testing.T, cwd string, input io.Reader, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cymbal", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().String("db", "", "")
	root.AddCommand(newAffectedTestsCmd())
	root.SetIn(input)
	root.SetArgs(append([]string{"affected-tests"}, args...))
	var stdout, stderr string
	var err error
	withWorkingDir(t, cwd, func() {
		stdout, stderr, err = captureProcessOutput(t, root.Execute)
	})
	return stdout, stderr, err
}

func affectedRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("CYMBAL_CACHE_DIR", t.TempDir())
	t.Setenv("CYMBAL_DB", "")
	t.Cleanup(index.CloseAll)
	repo := t.TempDir()
	writeFile(t, repo, "code.go", "package p\n\nfunc Alpha() int { return 1 }\n\nfunc Beta() int { return 2 }\n")
	writeFile(t, repo, "alpha_test.go", "package p\n\nfunc TestAlpha() { Alpha() }\n")
	writeFile(t, repo, "beta_test.go", "package p\n\nfunc TestBeta() { Beta() }\n")
	writeFile(t, repo, "unrelated_test.go", "package p\n\nfunc TestUnrelated() {}\n")
	gitChanged(t, repo, "init")
	gitChanged(t, repo, "add", ".")
	gitChanged(t, repo, "-c", "user.name=x", "-c", "user.email=x@y.z", "commit", "-m", "initial")
	return repo
}

func affectedJSON(t *testing.T, output string) index.AffectedResult {
	t.Helper()
	var envelope struct {
		Version string               `json:"version"`
		Results index.AffectedResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output)
	}
	if envelope.Version != "0.1" || envelope.Results.SchemaVersion != 1 {
		t.Fatalf("unexpected envelope: %s", output)
	}
	if envelope.Results.Tests == nil || envelope.Results.Limitations == nil {
		t.Fatalf("null collections: %s", output)
	}
	return envelope.Results
}

func affectedPaths(result index.AffectedResult) []string {
	paths := []string{}
	for _, test := range result.Tests {
		paths = append(paths, test.Path)
	}
	return paths
}

func TestAffectedTestsRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"affected-tests"})
	if err != nil || cmd != affectedTestsCmd {
		t.Fatalf("affected-tests is not registered: %v", err)
	}
}

func TestAffectedTestsDiffModesSelectCorrectBlob(t *testing.T) {
	repo := affectedRepo(t)
	// Stage Alpha, then edit Beta without changing Alpha's staged body.
	writeFile(t, repo, "code.go", "package p\n\nfunc Alpha() int { return 111 }\n\nfunc Beta() int { return 2 }\n")
	gitChanged(t, repo, "add", "code.go")
	writeFile(t, repo, "code.go", "package p\n\nfunc Alpha() int { return 111 }\n\nfunc Beta() int { return 222 }\n")
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"unstaged", nil, []string{"beta_test.go"}},
		{"staged", []string{"--staged"}, []string{"alpha_test.go"}},
		{"base", []string{"--base", "HEAD"}, []string{"alpha_test.go", "beta_test.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := affectedCLI(t, repo, nil, append(tc.args, "--json")...)
			if err != nil {
				t.Fatal(err)
			}
			result := affectedJSON(t, out)
			if got := affectedPaths(result); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
			if result.InputPrecision != "hunk" {
				t.Fatalf("precision = %s", result.InputPrecision)
			}
			for i, test := range result.Tests {
				if test.Rank != i+1 || test.Confidence != "high" || test.Distance == nil || *test.Distance != 1 {
					t.Fatalf("wrong rank/confidence/distance: %+v", test)
				}
				if len(test.ChangedSymbols) != 1 || len(test.Evidence) == 0 {
					t.Fatalf("missing attribution: %+v", test)
				}
				steps := test.Evidence[0].Steps
				if steps[len(steps)-1].Path != test.Path || steps[len(steps)-1].Line != 3 {
					t.Fatalf("wrong reference site: %+v", steps)
				}
			}
		})
	}
}

func TestAffectedTestsStagedMissingLiveSeedIsReported(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "code.go", "package p\n\nfunc StagedOnly() int { return 1 }\n\nfunc Beta() int { return 2 }\n")
	gitChanged(t, repo, "add", "code.go")
	writeFile(t, repo, "code.go", "package p\n\nfunc DiskOnly() int { return 1 }\n\nfunc Beta() int { return 2 }\n")
	out, _, err := affectedCLI(t, repo, nil, "--staged", "--json")
	if err != nil {
		t.Fatal(err)
	}
	result := affectedJSON(t, out)
	found := false
	for _, limitation := range result.Limitations {
		for _, seed := range limitation.Seeds {
			if limitation.Code == "missing_live_seed" && seed.Name == "StagedOnly" {
				found = true
			}
		}
	}
	if !found || !result.Fallback.Recommended {
		t.Fatalf("missing staged/live gap: %s", out)
	}
}

func TestAffectedTestsWholeFileUsesAllDefinitions(t *testing.T) {
	repo := affectedRepo(t)
	out, _, err := affectedCLI(t, repo, nil, "code.go", "--json")
	if err != nil {
		t.Fatal(err)
	}
	result := affectedJSON(t, out)
	if got := affectedPaths(result); !reflect.DeepEqual(got, []string{"alpha_test.go", "beta_test.go"}) {
		t.Fatalf("selected %v", got)
	}
	if result.InputPrecision != "file" || result.Status != "partial" || !result.Fallback.Recommended {
		t.Fatalf("wrong whole-file status: %s", out)
	}
	for _, test := range result.Tests {
		if test.Confidence != "medium" {
			t.Fatalf("whole-file symbol confidence: %s", out)
		}
	}
}

func TestAffectedTestsPathInputsPreserveNamesAndCombine(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "sub/# spaced_test.go", "package p\nfunc TestSpaced() {}\n")
	writeFile(t, repo, "sub/ leading_test.go", "package p\nfunc TestLeading() {}\n")
	out, _, err := affectedCLI(t, filepath.Join(repo, "sub"), strings.NewReader("# spaced_test.go\r\n\n leading_test.go\r\n# spaced_test.go\n"), "--stdin", "..\\sub\\# spaced_test.go", "--json")
	if err != nil {
		t.Fatal(err)
	}
	result := affectedJSON(t, out)
	want := []string{"sub/ leading_test.go", "sub/# spaced_test.go"}
	if got := affectedPaths(result); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	for _, test := range result.Tests {
		if len(test.Evidence) != 1 || test.Evidence[0].Kind != "changed_test" {
			t.Fatalf("duplicate/missing changed-test evidence: %+v", test)
		}
	}
}

func TestAffectedTestsEmptyStdinDoesNotUseDiff(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "code.go", "package p\nfunc Alpha() int { return 111 }\n")
	out, _, err := affectedCLI(t, repo, strings.NewReader("\r\n\n"), "--stdin", "--json")
	if err != nil {
		t.Fatal(err)
	}
	result := affectedJSON(t, out)
	if len(result.Tests) != 0 || result.Status != "complete" || result.InputPrecision != "file" {
		t.Fatalf("empty stdin used diff: %s", out)
	}
}

func TestAffectedTestsMissingFileRemainsVisible(t *testing.T) {
	repo := affectedRepo(t)
	out, _, err := affectedCLI(t, repo, nil, "missing.go", "--json")
	if err != nil {
		t.Fatal(err)
	}
	result := affectedJSON(t, out)
	found := false
	for _, limitation := range result.Limitations {
		if limitation.Code == "missing_input" && reflect.DeepEqual(limitation.Paths, []string{"missing.go"}) {
			found = true
		}
	}
	if !found || result.Status != "unavailable" || !result.Fallback.Recommended {
		t.Fatalf("missing input was hidden: %s", out)
	}
}

func TestAffectedTestsQuietAndTextOutputs(t *testing.T) {
	repo := affectedRepo(t)
	out, stderr, err := affectedCLI(t, repo, nil, "code.go", "--quiet")
	if err != nil {
		t.Fatal(err)
	}
	if out != "alpha_test.go\nbeta_test.go\n" {
		t.Fatalf("quiet stdout: %q", out)
	}
	for _, want := range []string{"Run the full suite", "unselected tests are safe to skip", "file_level_precision"} {
		requireOutputContains(t, stderr, want)
	}
	out, _, err = affectedCLI(t, repo, nil, "code.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. alpha_test.go", "medium confidence, distance 1", "direct_symbol:", "alpha_test.go:3", "Run the full suite", "unselected tests are safe to skip"} {
		requireOutputContains(t, out, want)
	}
}

func TestAffectedTestsCompleteQuietStillWarns(t *testing.T) {
	repo := affectedRepo(t)
	out, stderr, err := affectedCLI(t, repo, nil, "--quiet")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("empty diff stdout: %q", out)
	}
	requireOutputContains(t, stderr, "Selection does not prove that unselected tests are safe to skip.")
	if strings.Contains(stderr, "Run the full suite") {
		t.Fatalf("empty diff incorrectly has a gap: %s", stderr)
	}
}

func TestAffectedTestsValidationBeforeGitOrRefresh(t *testing.T) {
	dir := t.TempDir() // Deliberately not a Git repository.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--depth=0"}, "--depth"}, {[]string{"--depth=6"}, "--depth"},
		{[]string{"--limit=0"}, "--limit"}, {[]string{"--max-symbols=-1"}, "--max-symbols"},
		{[]string{"--max-evidence=-1"}, "--max-evidence"}, {[]string{"--resolve-scope=typo"}, "--resolve-scope"},
		{[]string{"--staged", "--base=HEAD"}, "mutually exclusive"},
		{[]string{"--base=HEAD..main"}, "single ref"}, {[]string{"--base=--help"}, "invalid base"},
		{[]string{"x.go", "--staged"}, "file inputs"}, {[]string{"--stdin", "--base=HEAD"}, "file inputs"},
		{[]string{"--json", "--quiet"}, "mutually exclusive"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "never.db")
			out, _, err := affectedCLI(t, dir, strings.NewReader(""), append(tc.args, "--db", db)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
				t.Fatalf("out=%q err=%v, want %q", out, err, tc.want)
			}
			if _, err := os.Stat(db); !os.IsNotExist(err) {
				t.Fatalf("validation touched database: %v", err)
			}
		})
	}
}

type affectedReadError struct{}

func (affectedReadError) Read([]byte) (int, error) { return 0, errors.New("broken path stream") }

func TestAffectedTestsStdinReadFailure(t *testing.T) {
	out, _, err := affectedCLI(t, t.TempDir(), affectedReadError{}, "--stdin", "--json")
	if err == nil || !strings.Contains(err.Error(), "broken path stream") || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestAffectedTestsRejectOutsideRootIncludingMissingSymlinkLeaf(t *testing.T) {
	repo := affectedRepo(t)
	outside := t.TempDir()
	writeFile(t, outside, "outside.go", "package p\n")
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(outside, "outside.go"), "escape/outside.go", "escape/missing/leaf.go", "C:\\other\\file.go"} {
		t.Run(p, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "never.db")
			out, _, err := affectedCLI(t, repo, nil, p, "--json", "--db", db)
			if err == nil || !strings.Contains(err.Error(), "outside repository") || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if _, err := os.Stat(db); !os.IsNotExist(err) {
				t.Fatalf("outside path created database: %v", err)
			}
		})
	}
}

func TestAffectedTestsRejectForeignDatabaseBeforeRefresh(t *testing.T) {
	repo := affectedRepo(t)
	foreign := t.TempDir()
	writeFile(t, foreign, "foreign.go", "package p\nfunc Foreign() {}\n")
	db := filepath.Join(t.TempDir(), "foreign.db")
	if _, err := index.Index(foreign, db, index.Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	// A refresh would update this file in the foreign index.
	writeFile(t, foreign, "foreign.go", "package p\nfunc MustNotRefresh() {}\n")
	out, _, err := affectedCLI(t, repo, nil, "--json", "--db", db)
	if err == nil || !strings.Contains(err.Error(), "database repository root") || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	outline, err := index.FileOutline(db, filepath.Join(foreign, "foreign.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(outline) != 1 || outline[0].Name != "Foreign" {
		t.Fatalf("foreign DB was refreshed: %+v", outline)
	}
}

func TestAffectedTestsOperationalErrorsDoNotEmitJSON(t *testing.T) {
	repo := affectedRepo(t)
	badDB := filepath.Join(t.TempDir(), "corrupt.db")
	writeFile(t, filepath.Dir(badDB), filepath.Base(badDB), "not a SQLite database")
	for _, args := range [][]string{{"--base=missing-ref", "--json"}, {"--db", badDB, "--json"}} {
		out, _, err := affectedCLI(t, repo, nil, args...)
		if err == nil || out != "" {
			t.Fatalf("args=%v out=%q err=%v", args, out, err)
		}
	}
}

func TestAffectedTestsCurrentWorktreeOnly(t *testing.T) {
	repo := affectedRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitChanged(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")
	writeFile(t, sibling, "sibling_only_test.go", "package p\nfunc TestSiblingOnly() { Alpha() }\n")
	if _, err := index.Index(sibling, "", index.Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "code.go", "package p\n\nfunc Alpha() int { return 999 }\n\nfunc Beta() int { return 2 }\n")
	out, _, err := affectedCLI(t, repo, nil, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := affectedPaths(affectedJSON(t, out)); !reflect.DeepEqual(got, []string{"alpha_test.go"}) {
		t.Fatalf("sibling candidate leaked: %v", got)
	}
}

func TestAffectedTestsBoundsFlags(t *testing.T) {
	repo := affectedRepo(t)
	out, _, err := affectedCLI(t, repo, nil, "code.go", "--json", "-D", "1", "-n", "1", "--max-symbols=0", "--max-evidence=0")
	if err != nil {
		t.Fatal(err)
	}
	result := affectedJSON(t, out)
	if result.Bounds.Depth != 1 || result.Bounds.Limit != 1 || result.Bounds.MaxSymbols != 0 || result.Bounds.MaxEvidence != 0 {
		t.Fatalf("wrong effective bounds: %+v", result.Bounds)
	}
	if got := affectedPaths(result); !reflect.DeepEqual(got, []string{"alpha_test.go"}) {
		t.Fatalf("ranked limit = %v", got)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation.Code == "result_truncation" {
			found = true
		}
	}
	if !found || !result.Fallback.Recommended {
		t.Fatalf("limit gap was hidden: %s", out)
	}
}

func TestAffectedTestsClassificationAndScopeFlags(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "checks/check.go", "package p\nfunc CheckAlpha() { Alpha() }\n")
	writeFile(t, repo, "interop.test.js", "Alpha();\n")
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"same", []string{"--resolve-scope=same"}, []string{"alpha_test.go", "beta_test.go"}},
		{"custom classification", []string{"--test-path=checks/"}, []string{"alpha_test.go", "beta_test.go", "checks/check.go"}},
		{"all languages", []string{"--resolve-scope=all"}, []string{"alpha_test.go", "beta_test.go", "interop.test.js"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := affectedCLI(t, repo, nil, append(tc.args, "code.go", "--json")...)
			if err != nil {
				t.Fatal(err)
			}
			if got := affectedPaths(affectedJSON(t, out)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAffectedTestsDatabaseOverridesAndMissingMetadata(t *testing.T) {
	repo := affectedRepo(t)
	envDB := filepath.Join(t.TempDir(), "env.db")
	t.Setenv("CYMBAL_DB", envDB)
	out, _, err := affectedCLI(t, repo, nil, "code.go", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(affectedJSON(t, out).Tests) != 2 {
		t.Fatalf("environment database did not initialize: %s", out)
	}
	store, err := index.OpenStore(envDB)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.GetMeta("repo_root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !pathsEqual(root, repo) {
		t.Fatalf("initialized root %q, want %q", root, repo)
	}

	// An existing database without repository metadata must not be adopted.
	emptyDB := filepath.Join(t.TempDir(), "empty.db")
	store, err = index.OpenStore(emptyDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	out, _, err = affectedCLI(t, repo, nil, "--db", emptyDB, "--json")
	if err == nil || !strings.Contains(err.Error(), "database repository root") || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestAffectedTestsRefreshFailureDoesNotEmitJSON(t *testing.T) {
	repo := affectedRepo(t)
	db := filepath.Join(t.TempDir(), "refresh.db")
	if _, err := index.Index(repo, db, index.Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	store, err := index.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// This is persisted refresh configuration, not a query-only option.
	if err := store.SetMeta("index_exclude", "invalid JSON"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	out, _, err := affectedCLI(t, repo, nil, "--db", db, "--json")
	if err == nil || !strings.Contains(err.Error(), "refreshing index") || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestAffectedTestsUpdateNoticeSuppressedForQuiet(t *testing.T) {
	reset := stubPassiveNoticeEnv(t)
	defer reset()
	t.Setenv("CI", "")
	t.Setenv("CYMBAL_NO_UPDATE_NOTIFIER", "")
	root := &cobra.Command{Use: "cymbal"}
	root.PersistentFlags().Bool("json", false, "")
	cmd := newAffectedTestsCmd()
	root.AddCommand(cmd)
	if shouldSkipPassiveUpdateNotice(cmd) {
		t.Fatal("normal interactive command should permit notice")
	}
	if err := cmd.Flags().Set("quiet", "true"); err != nil {
		t.Fatal(err)
	}
	if !shouldSkipPassiveUpdateNotice(cmd) {
		t.Fatal("quiet command must suppress passive notice")
	}
}
