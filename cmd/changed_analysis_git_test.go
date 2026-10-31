package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/1broseidon/cymbal/index"
)

func changedGitOutput(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func changedLooseBlob(t *testing.T, repo, spec string) string {
	t.Helper()
	oid := strings.TrimSpace(string(changedGitOutput(t, repo, "rev-parse", spec)))
	p := filepath.Join(repo, ".git", "objects", oid[:2], oid[2:])
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture blob is not loose: %v", err)
	}
	return p
}

func TestChangedAnalysisRetainsGitBlobError(t *testing.T) {
	for _, staged := range []bool{false, true} {
		name := "old-index"
		if staged {
			name = "new-index"
		}
		t.Run(name, func(t *testing.T) {
			repo := affectedRepo(t)
			writeFile(t, repo, "code.go", "package p\n\nfunc Alpha() int { return 111 }\n\nfunc Beta() int { return 2 }\n")
			if staged {
				gitChanged(t, repo, "add", "code.go")
			}
			args, _, err := changedDiffArgs(staged, "")
			if err != nil {
				t.Fatal(err)
			}
			diff := changedGitOutput(t, repo, args...)
			if err := os.Remove(changedLooseBlob(t, repo, ":code.go")); err != nil {
				t.Fatal(err)
			}
			a := analyzeChangedDiff(repo, staged, "", diff)
			if a.GitError == nil || !strings.Contains(a.GitError.Error(), "git cat-file") {
				t.Fatalf("operational Git failure was hidden: %+v", a)
			}
			found := false
			for _, l := range a.Limitations {
				if l.Code == "unreadable_input" {
					found = true
				}
			}
			if !found {
				t.Fatal("side diagnostics lost after Git failure")
			}
		})
	}
}

func TestChangedAnalysisLocalUnreadableIsNotGitError(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "code.go", "package p\n\nfunc Alpha() int { return 111 }\n\nfunc Beta() int { return 2 }\n")
	args, _, err := changedDiffArgs(false, "")
	if err != nil {
		t.Fatal(err)
	}
	diff := changedGitOutput(t, repo, args...)
	if err := os.Remove(filepath.Join(repo, "code.go")); err != nil {
		t.Fatal(err)
	}
	a := analyzeChangedDiff(repo, false, "", diff)
	if a.GitError != nil {
		t.Fatalf("local file read misclassified as Git error: %v", a.GitError)
	}
	found := false
	for _, l := range a.Limitations {
		if l.Code == "unreadable_input" {
			found = true
		}
	}
	if !found {
		t.Fatal("local read diagnostic lost")
	}
}

func TestAffectedTestsGitBlobFailureDoesNotRefreshOrEmitJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("boundary timing fixture uses a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, staged := range []bool{false, true} {
		name := "old-index"
		if staged {
			name = "new-index"
		}
		t.Run(name, func(t *testing.T) {
			repo := affectedRepo(t)
			db, err := index.RepoDBPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if !staged {
				stats, err := index.Index(repo, db, index.Options{})
				if err != nil || stats.Errors != 0 {
					t.Fatalf("initial fixture index: %+v, %v", stats, err)
				}
			}
			writeFile(t, repo, "code.go", "package p\n\nfunc FreshAlpha() int { return 111 }\n\nfunc Beta() int { return 2 }\n")
			if staged {
				gitChanged(t, repo, "add", "code.go")
			}
			blob := changedLooseBlob(t, repo, ":code.go")
			// Removing a needed blob before diff also makes real Git diff fail. This
			// boundary wrapper only controls timing: execute the real diff, then remove
			// the real loose blob before the next real Git command. No output is faked.
			bin := t.TempDir()
			script := `#!/bin/sh
is_diff=false
for arg in "$@"; do
 if [ "$arg" = diff ]; then is_diff=true; fi
done
"$CYMBAL_TEST_REAL_GIT" "$@"
status=$?
if $is_diff && [ "$status" = 0 ]; then
 rm "$CYMBAL_TEST_LOOSE_BLOB" || exit 1
fi
exit "$status"
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CYMBAL_TEST_REAL_GIT", realGit)
			t.Setenv("CYMBAL_TEST_LOOSE_BLOB", blob)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			args := []string{"--json"}
			if staged {
				args = append(args, "--staged")
			}
			out, _, err := affectedCLI(t, repo, nil, args...)
			if err == nil || out != "" || !strings.Contains(err.Error(), "git cat-file") {
				t.Fatalf("blob failure emitted success: out=%q err=%v", out, err)
			}
			if _, err := os.Stat(blob); !os.IsNotExist(err) {
				t.Fatalf("real diff did not finish and remove the blob: %v", err)
			}
			if _, err := exec.Command(realGit, "-C", repo, "cat-file", "blob", ":code.go").Output(); err == nil {
				t.Fatal("real cat-file unexpectedly read the removed blob")
			}
			if staged {
				if _, err := os.Stat(db); !os.IsNotExist(err) {
					t.Fatalf("index initialized before Git failure: %v", err)
				}
			} else {
				fresh, err := index.SymbolsByName(db, "FreshAlpha")
				if err != nil || len(fresh) != 0 {
					t.Fatalf("index refreshed before Git failure: %+v, %v", fresh, err)
				}
				old, err := index.SymbolsByName(db, "Alpha")
				if err != nil || len(old) != 1 {
					t.Fatalf("original index changed: %+v, %v", old, err)
				}
			}
		})
	}
}
