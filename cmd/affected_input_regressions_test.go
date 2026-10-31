package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAffectedTestsInitialIndexReadFailureDoesNotEmitJSON(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "unreadable.go", "package p\nfunc Hidden() {}\n")
	p := filepath.Join(repo, "unreadable.go")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0600) })
	if _, err := os.ReadFile(p); err == nil {
		t.Skip("process can read files without read permission")
	}
	out, _, err := affectedCLI(t, repo, nil, "code.go", "--json")
	if err == nil || out != "" || !strings.Contains(err.Error(), "initializing index") {
		t.Fatalf("initial read failure emitted success: out=%q err=%v", out, err)
	}
}

func TestAffectedTestsExplicitUnsupportedInputReason(t *testing.T) {
	repo := affectedRepo(t)
	for _, p := range []string{"opaque.dat", "config.json"} {
		t.Run(p, func(t *testing.T) {
			writeFile(t, repo, p, "{}\n")
			out, _, err := affectedCLI(t, repo, nil, p, "--json")
			if err != nil {
				t.Fatal(err)
			}
			r := affectedJSON(t, out)
			found := false
			for _, l := range r.Limitations {
				if l.Code == "unsupported_input" && len(l.Paths) == 1 && l.Paths[0] == p {
					found = true
				}
			}
			if !found || r.Status != "unavailable" || !r.Fallback.Recommended {
				t.Fatalf("unsupported input reason lost: %s", out)
			}
		})
	}
}

func TestAffectedTestsExplicitSymlinkIsNotChangedTarget(t *testing.T) {
	repo := affectedRepo(t)
	if err := os.Symlink("code.go", filepath.Join(repo, "alias.go")); err != nil {
		t.Fatal(err)
	}
	out, _, err := affectedCLI(t, repo, nil, "alias.go", "--json")
	if err != nil {
		t.Fatal(err)
	}
	r := affectedJSON(t, out)
	found := false
	for _, l := range r.Limitations {
		if l.Code == "unsupported_input" && len(l.Paths) == 1 && l.Paths[0] == "alias.go" {
			found = true
		}
	}
	if !found || len(r.Tests) != 0 || r.Status != "unavailable" {
		t.Fatalf("symlink treated as changed target: %s", out)
	}
}

func TestAffectedTestsExplicitUnreadableInputReason(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "private.dat", "private data\n")
	p := filepath.Join(repo, "private.dat")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0600) })
	if _, err := os.ReadFile(p); err == nil {
		t.Skip("process can read files without read permission")
	}
	out, _, err := affectedCLI(t, repo, nil, "private.dat", "--json")
	if err != nil {
		t.Fatal(err)
	}
	r := affectedJSON(t, out)
	found := false
	for _, l := range r.Limitations {
		if l.Code == "unreadable_input" && len(l.Paths) == 1 && l.Paths[0] == "private.dat" {
			found = true
		}
	}
	if !found || r.Status != "unavailable" || !r.Fallback.Recommended {
		t.Fatalf("unreadable input reason lost: %s", out)
	}
}

func TestAffectedTestsExplicitShebangInputIsSupported(t *testing.T) {
	repo := affectedRepo(t)
	writeFile(t, repo, "tool", "#!/usr/bin/env python3\ndef run():\n    pass\n")
	writeFile(t, repo, "test_script.py", "run()\n")
	out, _, err := affectedCLI(t, repo, nil, "tool", "--json")
	if err != nil {
		t.Fatal(err)
	}
	r := affectedJSON(t, out)
	for _, l := range r.Limitations {
		if l.Code == "unsupported_input" {
			t.Fatalf("parseable shebang rejected: %s", out)
		}
	}
	if len(r.Tests) != 1 || r.Tests[0].Path != "test_script.py" || len(r.Tests[0].ChangedSymbols) != 1 || r.Tests[0].ChangedSymbols[0].Name != "run" {
		t.Fatalf("shebang input was not indexed: %s", out)
	}
}
