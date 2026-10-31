package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/1broseidon/cymbal/index"
	"github.com/1broseidon/cymbal/lang"
	"github.com/1broseidon/cymbal/symbols"
)

// changedAnalysis keeps the name-scoped changed contract alongside the precise
// inputs used by affected-tests. A failed side is a limitation, not an empty
// symbol table: in particular, it must never prove a deletion.
type changedAnalysis struct {
	Changed, Deleted           map[string]map[string]struct{}
	ChangedOrder, DeletedOrder []string
	Skipped, Conflicted        int
	Files                      []index.AffectedFile
	Symbols                    []index.AffectedSymbol
	Limitations                []index.AffectedLimitation
	// GitError is operational, unlike a local unreadable working-tree file.
	// Legacy changed retains its diagnostics-only behavior; affected-tests fails.
	GitError error
}

func analyzeChangedDiff(repoRoot string, staged bool, base string, diff []byte) changedAnalysis {
	a := changedAnalysis{
		Changed: map[string]map[string]struct{}{}, Deleted: map[string]map[string]struct{}{},
		ChangedOrder: []string{}, DeletedOrder: []string{},
		Files: []index.AffectedFile{}, Symbols: []index.AffectedSymbol{},
		Limitations: []index.AffectedLimitation{},
	}
	add := func(m map[string]map[string]struct{}, order *[]string, name, path string) {
		if m[name] == nil {
			m[name] = map[string]struct{}{}
			*order = append(*order, name)
		}
		if path != "" {
			m[name][path] = struct{}{}
		}
	}
	files := map[string]bool{} // path -> deleted; a known new side wins
	addFile := func(path string, deleted bool) {
		if path != "" {
			if _, exists := files[path]; !exists || !deleted {
				files[path] = deleted
			}
		}
	}
	seeds := map[index.AffectedSymbol]bool{}
	addSymbol := func(s symbols.Symbol, path string, deleted bool) {
		seeds[affectedChangedSymbol(s, path, deleted)] = true
	}
	limitation := func(code, message string, paths ...string) {
		pathSet := map[string]struct{}{}
		for _, path := range paths {
			if path != "" {
				pathSet[path] = struct{}{}
			}
		}
		a.Limitations = append(a.Limitations, index.AffectedLimitation{
			Code: code, Message: message, Paths: sortedKeys(pathSet), Seeds: []index.AffectedSymbol{},
		})
	}

	oldRev := "" // unstaged: index versus working tree
	switch {
	case staged:
		oldRev = "HEAD"
	case base != "":
		oldRev = base
	}
	text := string(diff)
	for _, path := range changedConflictPaths(text) {
		a.Conflicted++
		addFile(path, false)
		limitation("conflicted_input", "Combined diff cannot be attributed to two blob versions.", path)
	}

	for _, f := range parseChangedFiles(text) {
		oldPath, newPath := f.MetadataOldPath, f.MetadataNewPath
		addFile(newPath, false)
		if oldPath != "" && oldPath != newPath {
			addFile(oldPath, !f.Copied)
		}
		if f.Renamed {
			limitation("renamed_input", "Path rename can change dependencies beyond the changed lines.", oldPath, newPath)
		}
		if f.Copied {
			limitation("copied_input", "Copied file dependencies need file-level analysis.", oldPath, newPath)
		}
		if f.Binary {
			a.Skipped++
			paths := []string{}
			if oldPath != "" {
				paths = append(paths, oldPath)
			}
			if newPath != "" && newPath != oldPath {
				paths = append(paths, newPath)
			}
			limitation("binary_input", "Binary diff has no attributable source lines.", paths...)
			continue
		}
		// Keep the legacy no-content treatment even though metadata now retains
		// headerless rename/copy/mode paths. Such blocks were never skips.
		if len(f.OldLines) == 0 && len(f.NewLines) == 0 {
			if !f.Renamed && !f.Copied {
				limitation("file_level_precision", "Metadata-only change has no attributable source lines.", newPath)
			}
			continue
		}

		parseSide := func(path, side string, read func() ([]byte, error)) ([]symbols.Symbol, bool) {
			blob, err := read()
			if err != nil {
				if side == "old" || staged {
					a.GitError = errors.Join(a.GitError, fmt.Errorf("git cat-file %s-side blob %q: %w", side, path, err))
				}
				limitation("unreadable_input", fmt.Sprintf("Cannot read %s-side blob: %v", side, err), path)
				return nil, false
			}
			syms, ok := parseBlobSymbols(blob, path)
			if !ok {
				code := changedParseFailure(blob, path)
				limitation(code, fmt.Sprintf("Cannot parse %s-side blob for symbol attribution.", side), path)
			}
			return syms, ok
		}
		selected := func(syms []symbols.Symbol, lines map[int]bool, path, side string) []symbols.Symbol {
			out, outside := innermostChangedDefinitions(syms, lines)
			if outside {
				limitation("content_outside_symbols", fmt.Sprintf("Changed %s-side lines occur outside navigable definitions.", side), path)
			}
			return out
		}

		fileParsed, newParsed := false, false
		newNames := map[string]bool{}
		newDefinitions := map[[4]string][]symbols.Symbol{}
		var newSyms []symbols.Symbol
		if f.NewPath != "" {
			newSyms, newParsed = parseSide(f.NewPath, "new", func() ([]byte, error) {
				return readNewSide(repoRoot, staged, f.NewPath)
			})
			if newParsed {
				fileParsed = true
				for _, s := range newSyms {
					newNames[s.Name] = true
					key := changedDefinitionKey(s)
					newDefinitions[key] = append(newDefinitions[key], s)
				}
				for _, s := range selected(newSyms, f.NewLines, f.NewPath, "new") {
					add(a.Changed, &a.ChangedOrder, s.Name, f.NewPath)
					addSymbol(s, f.NewPath, false)
				}
			}
		}
		if f.OldPath != "" && len(f.OldLines) > 0 {
			oldSyms, ok := parseSide(f.OldPath, "old", func() ([]byte, error) {
				return readOldSide(repoRoot, oldRev, f.OldPath)
			})
			if ok {
				fileParsed = true
				oldDefinitions := map[[4]string]int{}
				for _, s := range oldSyms {
					oldDefinitions[changedDefinitionKey(s)]++
				}
				for _, s := range selected(oldSyms, f.OldLines, f.OldPath, "old") {
					gone := f.NewPath == "" || (newParsed && !newNames[s.Name])
					if gone {
						add(a.Deleted, &a.DeletedOrder, s.Name, f.OldPath)
						addSymbol(s, f.OldPath, true)
						continue
					}
					// Legacy attribution is name-scoped, even if the new side is
					// unknown. Rich seeds must instead use known new definitions,
					// never stale old coordinates masquerading as live symbols.
					add(a.Changed, &a.ChangedOrder, s.Name, f.NewPath)
					if !newParsed {
						limitation("missing_live_seed", "Old-side changed definition cannot be verified in the new blob.", f.OldPath, f.NewPath)
						a.Limitations[len(a.Limitations)-1].Seeds = []index.AffectedSymbol{affectedChangedSymbol(s, f.OldPath, false)}
						continue
					}
					key := changedDefinitionKey(s)
					matches := newDefinitions[key]
					if len(matches) != 1 || oldDefinitions[key] != 1 {
						// Do not guess which same-name definition survived a
						// deletion or move. New-side changed lines still seed any
						// definitions whose identities we can establish directly.
						limitation("ambiguous_definitions", "Old-side changed definition has no unique live identity.", f.OldPath, f.NewPath)
						a.Limitations[len(a.Limitations)-1].Seeds = []index.AffectedSymbol{affectedChangedSymbol(s, f.OldPath, false)}
						continue
					}
					addSymbol(matches[0], f.NewPath, false)
				}
			}
		}
		if !fileParsed {
			a.Skipped++
		}
	}
	sort.Strings(a.ChangedOrder)
	sort.Strings(a.DeletedOrder)
	for path, deleted := range files {
		a.Files = append(a.Files, index.AffectedFile{Path: path, Deleted: deleted})
	}
	sort.Slice(a.Files, func(i, j int) bool { return a.Files[i].Path < a.Files[j].Path })
	for s := range seeds {
		a.Symbols = append(a.Symbols, s)
	}
	sort.Slice(a.Symbols, func(i, j int) bool { return changedSymbolLess(a.Symbols[i], a.Symbols[j]) })
	return a
}

func changedDefinitionKey(s symbols.Symbol) [4]string {
	return [4]string{s.Name, s.Kind, s.Parent, s.Language}
}

func affectedChangedSymbol(s symbols.Symbol, path string, deleted bool) index.AffectedSymbol {
	return index.AffectedSymbol{
		Path: path, Name: s.Name, Kind: s.Kind, Language: s.Language,
		StartLine: s.StartLine, EndLine: s.EndLine, Deleted: deleted,
	}
}

func changedSymbolLess(a, b index.AffectedSymbol) bool {
	switch {
	case a.Path != b.Path:
		return a.Path < b.Path
	case a.StartLine != b.StartLine:
		return a.StartLine < b.StartLine
	case a.EndLine != b.EndLine:
		return a.EndLine < b.EndLine
	case a.Name != b.Name:
		return a.Name < b.Name
	case a.Kind != b.Kind:
		return a.Kind < b.Kind
	case a.Language != b.Language:
		return a.Language < b.Language
	default:
		return !a.Deleted && b.Deleted
	}
}

func changedParseFailure(src []byte, path string) string {
	switch {
	case len(src) > maxChangedParseBytes:
		return "oversized_input"
	case bytes.IndexByte(src, 0) >= 0:
		return "binary_input"
	}
	l := lang.Default.ForFile(path)
	if l == nil && filepath.Ext(path) == "" {
		l = lang.Default.ForShebang(src)
	}
	if l == nil || !l.Parseable() {
		return "unsupported_input"
	}
	return "parse_failure"
}

// innermostChangedDefinitions retains definition identity, not just name. The
// bool reports changed lines outside all navigable definitions. The legacy
// innermostChanged wrapper applies name deduplication to this same search.
func innermostChangedDefinitions(syms []symbols.Symbol, lines map[int]bool) ([]symbols.Symbol, bool) {
	sorted := make([]int, 0, len(lines))
	for line := range lines {
		sorted = append(sorted, line)
	}
	sort.Ints(sorted)
	best := make(map[int]*symbols.Symbol, len(sorted))
	for i := range syms {
		s := &syms[i]
		if !isChangedUnit(s.Kind, s.Depth) {
			continue
		}
		for j := sort.SearchInts(sorted, s.StartLine); j < len(sorted) && sorted[j] <= s.EndLine; j++ {
			line := sorted[j]
			if b := best[line]; b == nil || (s.EndLine-s.StartLine) < (b.EndLine-b.StartLine) {
				best[line] = s
			}
		}
	}
	seen := map[index.AffectedSymbol]bool{}
	var out []symbols.Symbol
	outside := false
	for _, line := range sorted {
		s := best[line]
		if s == nil {
			outside = true
			continue
		}
		key := affectedChangedSymbol(*s, s.File, false)
		if !seen[key] {
			seen[key] = true
			out = append(out, *s)
		}
	}
	return out, outside
}

// changedMetadataPath decodes extended-header and combined-diff paths. Unlike
// ---/+++ headers these have no a/ or b/ prefix and no tab separator.
func changedMetadataPath(path string) string {
	if strings.HasPrefix(path, `"`) {
		if decoded, err := strconv.Unquote(path); err == nil {
			return decoded
		}
	}
	return path
}

// changedMetadataPaths reads the two paths in a diff --git header without
// splitting filenames on spaces. Git quotes control bytes but leaves spaces
// unquoted. Equal-path blocks can be disambiguated by their repeated path;
// rename/copy extended headers later override unequal-path metadata.
func changedMetadataPaths(header string) (string, string) {
	if strings.HasPrefix(header, `"`) {
		for i := 1; i < len(header); i++ {
			if header[i] == '\\' {
				i++
				continue
			}
			if header[i] == '"' && i+1 < len(header) && header[i+1] == ' ' {
				return diffPathSide(header[:i+1]), diffPathSide(header[i+2:])
			}
		}
		return "", ""
	}
	// The second path can be quoted while the first is not.
	if i := strings.Index(header, ` "b/`); i >= 0 {
		return diffPathSide(header[:i]), diffPathSide(header[i+1:])
	}
	for start := 0; start < len(header); {
		i := strings.Index(header[start:], " b/")
		if i < 0 {
			break
		}
		i += start
		oldPath, newPath := diffPathSide(header[:i]), diffPathSide(header[i+1:])
		if oldPath == newPath {
			return oldPath, newPath
		}
		start = i + 1
	}
	if i := strings.LastIndex(header, " b/"); i >= 0 {
		return diffPathSide(header[:i]), diffPathSide(header[i+1:])
	}
	return "", ""
}

func changedConflictPaths(diff string) []string {
	var paths []string
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --cc "):
			paths = append(paths, changedMetadataPath(strings.TrimPrefix(line, "diff --cc ")))
		case strings.HasPrefix(line, "diff --combined "):
			paths = append(paths, changedMetadataPath(strings.TrimPrefix(line, "diff --combined ")))
		}
	}
	return paths
}
