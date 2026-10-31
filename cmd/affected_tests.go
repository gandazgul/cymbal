package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/1broseidon/cymbal/index"
	"github.com/1broseidon/cymbal/lang"
	"github.com/spf13/cobra"
)

var affectedTestsCmd = newAffectedTestsCmd()

func newAffectedTestsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "affected-tests [files...]",
		Short: "Select ranked, explainable test paths affected by changes",
		Long: `Select advisory test paths from the current repository index.
By default, analyze unstaged changes. Use --staged or --base REF for other
Git comparisons, or supply files and/or --stdin for whole-file analysis.
Stdin is newline-separated paths, not symbols or comments. Spaces and leading
# are preserved; CRLF is accepted. Newlines in filenames cannot be represented.
Both slash and backslash are treated as path separators. Paths resolve from cwd.
Selection does not prove that unselected tests are safe to skip.`,
		RunE: runAffectedTests,
	}
	defaults := index.DefaultAffectedOptions()
	cmd.Flags().Bool("staged", false, "analyze staged changes versus HEAD")
	cmd.Flags().String("base", "", "analyze working-tree changes versus one Git ref")
	cmd.Flags().Bool("stdin", false, "read newline-separated file paths from stdin (may combine with arguments)")
	cmd.Flags().Bool("quiet", false, "write only selected paths to stdout; warnings go to stderr")
	cmd.Flags().IntP("depth", "D", defaults.Depth, "maximum dependency depth (1–5)")
	cmd.Flags().IntP("limit", "n", defaults.Limit, "maximum selected test files (positive)")
	cmd.Flags().Int("max-symbols", defaults.MaxSymbols, "maximum seed definitions (0 = unlimited)")
	cmd.Flags().Int("max-evidence", defaults.MaxEvidence, "maximum examined dependency records (0 = unlimited)")
	cmd.Flags().StringArray("test-path", nil, "additional test path classification pattern (repeatable)")
	addResolveScopeFlag(cmd)
	return cmd
}

func init() { rootCmd.AddCommand(affectedTestsCmd) }

func runAffectedTests(cmd *cobra.Command, args []string) error {
	// Validate all options before repository discovery, Git, or index refresh.
	opts := index.DefaultAffectedOptions()
	opts.Depth, _ = cmd.Flags().GetInt("depth")
	opts.Limit, _ = cmd.Flags().GetInt("limit")
	opts.MaxSymbols, _ = cmd.Flags().GetInt("max-symbols")
	opts.MaxEvidence, _ = cmd.Flags().GetInt("max-evidence")
	opts.TestPaths, _ = cmd.Flags().GetStringArray("test-path")
	var err error
	opts.Scope, err = resolveScopeOrError(cmd)
	if err != nil {
		return err
	}
	if opts.Depth < 1 || opts.Depth > 5 {
		return fmt.Errorf("--depth must be between 1 and 5")
	}
	if opts.Limit < 1 {
		return fmt.Errorf("--limit must be positive")
	}
	if opts.MaxSymbols < 0 {
		return fmt.Errorf("--max-symbols must be nonnegative")
	}
	if opts.MaxEvidence < 0 {
		return fmt.Errorf("--max-evidence must be nonnegative")
	}
	staged, _ := cmd.Flags().GetBool("staged")
	base, _ := cmd.Flags().GetString("base")
	stdin, _ := cmd.Flags().GetBool("stdin")
	quiet, _ := cmd.Flags().GetBool("quiet")
	jsonOut := getJSONFlag(cmd)
	if quiet && jsonOut {
		return fmt.Errorf("--json and --quiet are mutually exclusive")
	}
	fileMode := len(args) > 0 || stdin
	if fileMode && (cmd.Flags().Changed("staged") || cmd.Flags().Changed("base")) {
		return fmt.Errorf("file inputs cannot be combined with --staged or --base")
	}
	diffArgs, _, err := changedDiffArgs(staged, base)
	if err != nil {
		return err
	}
	paths := append([]string{}, args...)
	if stdin {
		lines, err := readAffectedPaths(cmd.InOrStdin())
		if err != nil {
			return err
		}
		paths = append(paths, lines...)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoRoot, err := gitRepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}
	repoRoot = canonicalExistingPath(repoRoot)
	input := index.AffectedInput{Precision: "hunk", Files: []index.AffectedFile{}, Symbols: []index.AffectedSymbol{}, Limitations: []index.AffectedLimitation{}}
	if fileMode {
		input.Precision = "file"
		seen := map[string]bool{}
		for _, p := range paths {
			rel, missing, err := normalizeAffectedPath(cwd, repoRoot, p)
			if err != nil {
				return err
			}
			if seen[rel] {
				continue
			}
			seen[rel] = true
			input.Files = append(input.Files, index.AffectedFile{Path: rel})
			if missing {
				input.Limitations = append(input.Limitations, index.AffectedLimitation{Code: "missing_input", Message: "Input file does not exist in the working tree.", Paths: []string{rel}, Seeds: []index.AffectedSymbol{}})
			} else if limitation := affectedFileLimitation(repoRoot, rel); limitation != nil {
				input.Limitations = append(input.Limitations, *limitation)
			}
		}
		sort.Slice(input.Files, func(i, j int) bool { return input.Files[i].Path < input.Files[j].Path })
	}
	dbPath := getDBPath(cmd)
	newDB, err := checkAffectedDBRoot(dbPath, repoRoot)
	if err != nil {
		return err
	}
	if !fileMode {
		out, err := exec.Command("git", append([]string{"-C", repoRoot}, diffArgs...)...).Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
				return fmt.Errorf("git diff: %s", strings.TrimSpace(string(exitErr.Stderr)))
			}
			return fmt.Errorf("git diff: %w", err)
		}
		analysis := analyzeChangedDiff(repoRoot, staged, base, out)
		if analysis.GitError != nil {
			return analysis.GitError
		}
		input.Files, input.Symbols, input.Limitations = analysis.Files, analysis.Symbols, analysis.Limitations
	}
	if newDB {
		stats, err := index.Index(repoRoot, dbPath, index.Options{})
		if err != nil {
			return fmt.Errorf("initializing index: %w", err)
		}
		if stats.Errors > 0 {
			return fmt.Errorf("initializing index: incomplete index: %d parse/read errors and %d write errors", stats.ParseErrors, stats.WriteErrors)
		}
	} else if err := ensureFresh(dbPath); err != nil {
		return err
	}
	result, err := index.FindAffectedTests(dbPath, input, opts)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(result)
	}
	if quiet {
		for _, test := range result.Tests {
			fmt.Fprintln(cmd.OutOrStdout(), test.Path)
		}
		writeAffectedAdvice(cmd.ErrOrStderr(), result)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Affected tests: %s (%s precision)\n", result.Status, result.InputPrecision)
	for _, test := range result.Tests {
		fmt.Fprintf(cmd.OutOrStdout(), "%d. %s [%s confidence", test.Rank, test.Path, test.Confidence)
		if test.Distance != nil {
			fmt.Fprintf(cmd.OutOrStdout(), ", distance %d", *test.Distance)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "]")
		for _, evidence := range test.Evidence {
			steps := make([]string, 0, len(evidence.Steps))
			for _, step := range evidence.Steps {
				loc := step.Path
				if step.Line > 0 {
					loc += fmt.Sprintf(":%d", step.Line)
				}
				if step.Name != "" {
					loc += " (" + step.Name + ")"
				}
				if step.RawImport != "" {
					loc += fmt.Sprintf(" (import %q)", step.RawImport)
				}
				steps = append(steps, loc)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "   %s: %s\n", evidence.Kind, strings.Join(steps, " -> "))
		}
	}
	if len(result.Tests) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No test paths selected.")
	}
	writeAffectedAdvice(cmd.OutOrStdout(), result)
	return nil
}

func writeAffectedAdvice(w io.Writer, result *index.AffectedResult) {
	for _, limitation := range result.Limitations {
		fmt.Fprintf(w, "Warning [%s]: %s", limitation.Code, limitation.Message)
		if len(limitation.Paths) > 0 {
			fmt.Fprintf(w, " (%s)", strings.Join(limitation.Paths, ", "))
		}
		fmt.Fprintln(w)
	}
	if result.Fallback.Recommended {
		fmt.Fprintln(w, result.Fallback.Reason)
	}
	fmt.Fprintln(w, result.Advisory)
	fmt.Fprintln(w, result.StaticAnalysisLimit)
}

func readAffectedPaths(r io.Reader) ([]string, error) {
	paths := []string{}
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" {
			paths = append(paths, line)
		}
		if err == io.EOF {
			return paths, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading input paths: %w", err)
		}
	}
}

// Resolve the nearest existing ancestor too: EvalSymlinks on a missing leaf
// alone would miss an outside-root symlink in its parent directories.
func normalizeAffectedPath(cwd, repoRoot, raw string) (string, bool, error) {
	p := filepath.FromSlash(strings.ReplaceAll(raw, "\\", "/"))
	if p == "" || strings.ContainsRune(p, 0) {
		return "", false, fmt.Errorf("invalid input path %q", raw)
	}
	// A Windows drive path must not become a relative Unix filename.
	if filepath.VolumeName(p) == "" && len(p) >= 2 && p[1] == ':' {
		return "", false, fmt.Errorf("path %q is outside repository", raw)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	ancestor := p
	tail := []string{}
	missing := false
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("input path %q: %w", raw, err)
		}
		missing = true
		tail = append(tail, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", false, fmt.Errorf("cannot resolve input path %q", raw)
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", false, fmt.Errorf("resolving input path %q: %w", raw, err)
	}
	for i := len(tail) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, tail[i])
	}
	rel, err := filepath.Rel(repoRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("path %q is outside repository %s", raw, repoRoot)
	}
	if !missing {
		info, err := os.Stat(resolved)
		if err != nil {
			return "", false, fmt.Errorf("input path %q: %w", raw, err)
		}
		if info.IsDir() {
			return "", false, fmt.Errorf("input path %q is a directory; supply files", raw)
		}
	}
	// Resolve links to check containment, not to change the input's identity.
	// The walker excludes symbolic links, so a link must not select its target
	// as though the target file itself had changed.
	if lexicalRel, err := filepath.Rel(repoRoot, p); err == nil && lexicalRel != ".." && !strings.HasPrefix(lexicalRel, ".."+string(filepath.Separator)) {
		rel = lexicalRel
	}
	return filepath.ToSlash(rel), missing, nil
}

// File-mode inputs need the same explicit diagnostics as diff-mode inputs.
// Read only the shebang prefix; actual parsing remains owned by index refresh.
func affectedFileLimitation(root, rel string) *index.AffectedLimitation {
	limitation := func(code, message string) *index.AffectedLimitation {
		return &index.AffectedLimitation{Code: code, Message: message, Paths: []string{rel}, Seeds: []index.AffectedSymbol{}}
	}
	p := root
	for _, part := range strings.Split(filepath.FromSlash(rel), string(filepath.Separator)) {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if err != nil {
			return limitation("unreadable_input", fmt.Sprintf("Cannot inspect input file: %v", err))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return limitation("unsupported_input", "Symbolic-link inputs are not indexed.")
		}
		if p == filepath.Join(root, filepath.FromSlash(rel)) && !info.Mode().IsRegular() {
			return limitation("unsupported_input", "Input is not a regular source file.")
		}
	}
	file, err := os.Open(p)
	if err != nil {
		return limitation("unreadable_input", fmt.Sprintf("Cannot read input file: %v", err))
	}
	defer file.Close()
	head, err := io.ReadAll(io.LimitReader(file, lang.ShebangMaxBytes))
	if err != nil {
		return limitation("unreadable_input", fmt.Sprintf("Cannot read input file: %v", err))
	}
	l := lang.Default.ForFile(rel)
	if l == nil && filepath.Ext(rel) == "" {
		l = lang.Default.ForShebang(head)
	}
	if l == nil || !l.Parseable() {
		return limitation("unsupported_input", "Input source language is not supported by the parser.")
	}
	return nil
}

// Read metadata with its error intact, before ensureFresh can use that root.
func checkAffectedDBRoot(dbPath, repoRoot string) (bool, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	store, err := index.OpenStore(dbPath)
	if err != nil {
		return false, err
	}
	root, metaErr := store.GetMeta("repo_root")
	closeErr := store.Close()
	if metaErr != nil {
		return false, fmt.Errorf("reading index repository root: %w", metaErr)
	}
	if closeErr != nil {
		return false, closeErr
	}
	if root == "" || !pathsEqual(root, repoRoot) {
		return false, fmt.Errorf("database repository root %q differs from current repository %q", root, repoRoot)
	}
	return false, nil
}
