package cmd

import (
	"reflect"
	"testing"
)

func TestAffectedTestsMixedHunkWorkRetainsFileFallback(t *testing.T) {
	for _, mode := range []string{"deleted", "outside", "staged-missing"} {
		t.Run(mode, func(t *testing.T) {
			repo := affectedRepo(t)
			prefix := ""
			if mode == "outside" {
				prefix = "// old configuration\n"
			}
			writeFile(t, repo, "core.ts", prefix+"export function alpha() {}\nexport function beta() {}\n")
			writeFile(t, repo, "direct.test.ts", "alpha();\n")
			writeFile(t, repo, "hub.ts", "export * from './core';\n")
			writeFile(t, repo, "indirect.test.ts", "import './hub';\n")
			gitChanged(t, repo, "add", ".")
			gitChanged(t, repo, "-c", "user.name=x", "-c", "user.email=x@y.z", "commit", "-m", "typescript graph")
			args := []string{"--json"}
			switch mode {
			case "deleted":
				writeFile(t, repo, "core.ts", "export function alpha() { return 1; }\n")
			case "outside":
				writeFile(t, repo, "core.ts", "// new configuration\nexport function alpha() { return 1; }\nexport function beta() {}\n")
			case "staged-missing":
				writeFile(t, repo, "core.ts", "export function alpha() { return 1; }\nexport function stagedOnly() {}\n")
				gitChanged(t, repo, "add", "core.ts")
				writeFile(t, repo, "core.ts", "export function alpha() { return 1; }\n")
				args = append(args, "--staged")
			}
			out, _, err := affectedCLI(t, repo, nil, args...)
			if err != nil {
				t.Fatal(err)
			}
			r := affectedJSON(t, out)
			if !reflect.DeepEqual(affectedPaths(r), []string{"direct.test.ts", "indirect.test.ts"}) {
				t.Fatalf("file work lost beside live alpha: %s", out)
			}
			if r.Status != "partial" || !r.Fallback.Recommended || r.Tests[0].Confidence != "high" {
				t.Fatalf("incorrect gap/confidence: %s", out)
			}
			end := r.Tests[1]
			if len(end.ChangedSymbols) != 0 || len(end.Evidence) != 1 || end.Evidence[0].Seed.Name != "" || end.Evidence[0].Kind != "reverse_import" {
				t.Fatalf("file fallback asserted as live symbol evidence: %+v", end)
			}
		})
	}
}
