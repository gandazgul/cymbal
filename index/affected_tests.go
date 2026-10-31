package index

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// AffectedSymbol identifies a changed definition, not just a name.
type AffectedSymbol struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Language  string `json:"language"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Deleted   bool   `json:"deleted"`
}

type AffectedFile struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted"`
}

type AffectedLimitation struct {
	Code    string           `json:"code"`
	Message string           `json:"message"`
	Paths   []string         `json:"paths"`
	Seeds   []AffectedSymbol `json:"seeds"`
}

// AffectedInput contains repository-relative slash paths. Precision is hunk or file.
// File precision expands each file to all its indexed definitions.
type AffectedInput struct {
	Files       []AffectedFile       `json:"files"`
	Symbols     []AffectedSymbol     `json:"symbols"`
	Precision   string               `json:"precision"`
	Limitations []AffectedLimitation `json:"limitations"`
}

type AffectedOptions struct {
	Depth       int          `json:"depth"`
	Limit       int          `json:"limit"`
	MaxSymbols  int          `json:"max_symbols"`
	MaxEvidence int          `json:"max_evidence"`
	TestPaths   []string     `json:"test_paths"`
	Scope       ResolveScope `json:"resolve_scope"`
}

// DefaultAffectedOptions supplies the CLI defaults. Zero budgets mean unlimited.
func DefaultAffectedOptions() AffectedOptions {
	return AffectedOptions{Depth: 2, Limit: 50, MaxSymbols: 40, MaxEvidence: 500, TestPaths: []string{}, Scope: ResolveScopeFamily}
}

type AffectedStep struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line,omitempty"`
	RawImport string `json:"raw_import,omitempty"`
	Target    string `json:"target,omitempty"`
}

type AffectedEvidence struct {
	Kind       string         `json:"kind"`
	Seed       AffectedSymbol `json:"seed"`
	Distance   *int           `json:"distance"`
	Ambiguous  bool           `json:"ambiguous"`
	Confidence string         `json:"confidence"`
	Steps      []AffectedStep `json:"steps"`
}

type AffectedTest struct {
	Path           string             `json:"path"`
	Rank           int                `json:"rank"`
	Confidence     string             `json:"confidence"`
	Distance       *int               `json:"distance"`
	ChangedSymbols []AffectedSymbol   `json:"changed_symbols"`
	Evidence       []AffectedEvidence `json:"evidence"`
}

type AffectedFallback struct {
	Recommended bool   `json:"recommended"`
	Reason      string `json:"reason"`
}

type AffectedResult struct {
	SchemaVersion       int                  `json:"schema_version"`
	Status              string               `json:"status"`
	InputPrecision      string               `json:"input_precision"`
	Bounds              AffectedOptions      `json:"bounds"`
	Tests               []AffectedTest       `json:"tests"`
	Limitations         []AffectedLimitation `json:"limitations"`
	Fallback            AffectedFallback     `json:"fallback"`
	Advisory            string               `json:"advisory"`
	StaticAnalysisLimit string               `json:"static_analysis_limit"`
}

// FindAffectedTests selects advisory test paths from the current index only.
// It does not refresh the index, invoke Git, print, or run tests. Schema 1 allows
// additive fields; breaking field changes require a new schema version.
func FindAffectedTests(dbPath string, input AffectedInput, opts AffectedOptions) (*AffectedResult, error) {
	if opts.Depth < 1 || opts.Depth > 5 || opts.Limit < 1 || opts.MaxSymbols < 0 || opts.MaxEvidence < 0 {
		return nil, fmt.Errorf("invalid affected-test bounds")
	}
	if input.Precision != "hunk" && input.Precision != "file" {
		return nil, fmt.Errorf("input precision must be hunk or file")
	}
	if opts.Scope != "" && opts.Scope != ResolveScopeSame && opts.Scope != ResolveScopeFamily && opts.Scope != ResolveScopeAll {
		return nil, fmt.Errorf("invalid resolve scope %q", opts.Scope)
	}
	opts.Scope = NormalizeScope(opts.Scope)
	opts.TestPaths = append([]string{}, opts.TestPaths...)
	sort.Strings(opts.TestPaths)
	store, err := openCached(dbPath)
	if err != nil {
		return nil, err
	}
	symbols, files, members, err := store.affectedInventory()
	if err != nil {
		return nil, err
	}
	q := &affectedScan{store: store, opts: opts, classifier: NewClassifier(opts.TestPaths), symbols: symbols, files: files, members: members, candidates: map[string]map[string]AffectedEvidence{}, examined: map[string]bool{}, limitations: map[string]AffectedLimitation{}}
	result := &AffectedResult{SchemaVersion: 1, Status: "complete", InputPrecision: input.Precision, Bounds: opts, Tests: []AffectedTest{}, Limitations: []AffectedLimitation{}, Advisory: "Selection does not prove that unselected tests are safe to skip.", StaticAnalysisLimit: "Static name matching cannot resolve all aliases, callbacks, reflection, or interface dispatch."}
	q.precision = input.Precision
	for _, l := range input.Limitations {
		q.addLimitation(l)
	}
	unseeded := map[string]bool{}
	fileSet := map[string]AffectedFile{}
	for _, f := range input.Files {
		if old, ok := fileSet[f.Path]; !ok || old.Deleted {
			fileSet[f.Path] = f
		}
	}
	seeds := map[AffectedSymbol]bool{}
	for _, s := range input.Symbols {
		seeds[s] = true
		if _, ok := fileSet[s.Path]; !ok {
			fileSet[s.Path] = AffectedFile{Path: s.Path, Deleted: s.Deleted}
		}
	}
	// Attribution gaps are independent of successful live definitions in the
	// same file. Preserve file-level work alongside per-symbol evidence.
	for _, l := range input.Limitations {
		for _, p := range l.Paths {
			unseeded[p] = true
			if _, ok := fileSet[p]; !ok {
				fileSet[p] = AffectedFile{Path: p}
			}
		}
		for _, seed := range l.Seeds {
			unseeded[seed.Path] = true
			if _, ok := fileSet[seed.Path]; !ok {
				fileSet[seed.Path] = AffectedFile{Path: seed.Path}
			}
		}
	}
	if input.Precision == "file" && len(fileSet) > 0 {
		q.limit("file_level_precision", "Whole-file inputs do not identify changed lines.", "", nil)
		for _, s := range symbols {
			if f, ok := fileSet[s.Path]; ok && !f.Deleted {
				seeds[s] = true
			}
		}
	}
	paths := make([]string, 0, len(fileSet))
	for p := range fileSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	analyzable := false
	for _, p := range paths {
		f := fileSet[p]
		if !files[p] {
			code := "missing_live_seed"
			if f.Deleted {
				code = "missing_deleted_seed"
			}
			q.limit(code, "Changed file is absent from the current index.", p, nil)
		}
		if !f.Deleted && files[p] && q.classifier.Classify(p) == PathClassTest {
			q.admit(p, AffectedEvidence{Kind: "changed_test", Seed: AffectedSymbol{Path: p}, Distance: intPointer(0), Confidence: "high", Steps: []AffectedStep{{Path: p, Kind: "changed_file"}}})
			analyzable = true
		}
	}
	ordered := make([]AffectedSymbol, 0, len(seeds))
	for s := range seeds {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return stableJSON(ordered[i]) < stableJSON(ordered[j]) })
	symbolTest := map[AffectedSymbol]bool{}
	fileSeeds := map[string][]AffectedSymbol{}
	for i, s := range ordered {
		if opts.MaxSymbols > 0 && i >= opts.MaxSymbols {
			q.limit("seed_truncation", "Changed definition budget omitted this seed.", s.Path, &s)
			unseeded[s.Path] = true
			continue
		}
		if q.classifier.Classify(s.Path) == PathClassTest {
			continue
		}
		if s.Deleted {
			unseeded[s.Path] = true
			q.limit("missing_deleted_seed", "Deleted definition has no live graph identity; file evidence is heuristic.", s.Path, &s)
			continue
		}
		found := false
		for _, live := range symbols {
			if live == s {
				found = true
				break
			}
		}
		if !found {
			unseeded[s.Path] = true
			q.limit("missing_live_seed", "Changed definition does not match a current indexed definition.", s.Path, &s)
			continue
		}
		analyzable = true
		fileSeeds[s.Path] = append(fileSeeds[s.Path], s)
		reached, err := q.walkSymbols(s)
		if err != nil {
			return nil, err
		}
		if reached {
			symbolTest[s] = true
		}
	}
	for _, p := range paths {
		if q.classifier.Classify(p) == PathClassTest {
			continue
		}
		// Keep per-definition fallback and a changed-file seed for unseeded
		// work. A successful symbol must not suppress either kind of fallback.
		fallbackSeeds := fileSeeds[p]
		if len(fallbackSeeds) == 0 || unseeded[p] {
			fallbackSeeds = append(append([]AffectedSymbol{}, fallbackSeeds...), AffectedSymbol{Path: p, Deleted: fileSet[p].Deleted})
		}
		for _, seed := range fallbackSeeds {
			q.colocated(seed)
			applicable, err := q.walkImports(seed, !symbolTest[seed])
			if err != nil {
				return nil, err
			}
			if applicable {
				analyzable = true
			}
		}
	}
	for p, evidence := range q.candidates {
		t := AffectedTest{Path: p, ChangedSymbols: []AffectedSymbol{}, Evidence: []AffectedEvidence{}, Confidence: "low"}
		changed := map[AffectedSymbol]bool{}
		for _, e := range evidence {
			t.Evidence = append(t.Evidence, e)
			if e.Seed.Name != "" {
				changed[e.Seed] = true
			}
			if e.Distance != nil && (t.Distance == nil || *e.Distance < *t.Distance) {
				t.Distance = e.Distance
			}
			if confidenceOrder(e.Confidence) < confidenceOrder(t.Confidence) {
				t.Confidence = e.Confidence
			}
		}
		sort.Slice(t.Evidence, func(i, j int) bool { return evidenceLess(t.Evidence[i], t.Evidence[j]) })
		for s := range changed {
			t.ChangedSymbols = append(t.ChangedSymbols, s)
		}
		sort.Slice(t.ChangedSymbols, func(i, j int) bool { return stableJSON(t.ChangedSymbols[i]) < stableJSON(t.ChangedSymbols[j]) })
		result.Tests = append(result.Tests, t)
	}
	sort.Slice(result.Tests, func(i, j int) bool {
		a, b := result.Tests[i], result.Tests[j]
		ea, eb := a.Evidence[0], b.Evidence[0]
		if evidenceClass(ea.Kind) != evidenceClass(eb.Kind) {
			return evidenceClass(ea.Kind) < evidenceClass(eb.Kind)
		}
		if distanceValue(ea.Distance) != distanceValue(eb.Distance) {
			return distanceValue(ea.Distance) < distanceValue(eb.Distance)
		}
		if ea.Ambiguous != eb.Ambiguous {
			return !ea.Ambiguous
		}
		support := func(t AffectedTest) int {
			n := 0
			for _, e := range t.Evidence {
				if evidenceClass(e.Kind) == evidenceClass(t.Evidence[0].Kind) {
					n++
				}
			}
			return n
		}
		if support(a) != support(b) {
			return support(a) > support(b)
		}
		return a.Path < b.Path
	})
	if len(result.Tests) > opts.Limit {
		for _, t := range result.Tests[opts.Limit:] {
			q.limit("result_truncation", "Ranked test file omitted by result limit.", t.Path, nil)
		}
		result.Tests = result.Tests[:opts.Limit]
	}
	for i := range result.Tests {
		result.Tests[i].Rank = i + 1
	}
	if len(q.limitations) > 0 {
		result.Status = "partial"
	}
	if len(fileSet) > 0 && !analyzable && len(q.candidates) == 0 {
		result.Status = "unavailable"
		q.limit("no_analyzable_input", "No live definition or applicable file evidence was available.", "", nil)
	}
	for _, l := range q.limitations {
		result.Limitations = append(result.Limitations, l)
	}
	sort.Slice(result.Limitations, func(i, j int) bool { return stableJSON(result.Limitations[i]) < stableJSON(result.Limitations[j]) })
	result.Fallback = AffectedFallback{Recommended: result.Status != "complete", Reason: result.Advisory}
	if result.Fallback.Recommended {
		result.Fallback.Reason = "Run the full suite because analysis has gaps. " + result.Advisory
	}
	return result, nil
}

type affectedScan struct {
	store       *Store
	opts        AffectedOptions
	precision   string
	classifier  *Classifier
	symbols     []AffectedSymbol
	members     map[AffectedSymbol]bool
	files       map[string]bool
	candidates  map[string]map[string]AffectedEvidence
	limitations map[string]AffectedLimitation
	examined    map[string]bool
}

func stableJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func intPointer(n int) *int   { return &n }
func distanceValue(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}
func confidenceOrder(c string) int {
	switch c {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}
func evidenceClass(k string) int {
	switch k {
	case "changed_test":
		return 0
	case "direct_symbol":
		return 1
	case "transitive_symbol":
		return 2
	case "direct_import":
		return 3
	case "colocated":
		return 4
	default:
		return 5
	}
}
func evidenceLess(a, b AffectedEvidence) bool {
	if evidenceClass(a.Kind) != evidenceClass(b.Kind) {
		return evidenceClass(a.Kind) < evidenceClass(b.Kind)
	}
	if distanceValue(a.Distance) != distanceValue(b.Distance) {
		return distanceValue(a.Distance) < distanceValue(b.Distance)
	}
	if a.Ambiguous != b.Ambiguous {
		return !a.Ambiguous
	}
	if stepsLess(a.Steps, b.Steps) {
		return true
	}
	if stepsLess(b.Steps, a.Steps) {
		return false
	}
	return stableJSON(a) < stableJSON(b)
}

// Compare location-bearing steps by path and numeric source position. JSON
// strings would incorrectly put line 10 before line 2.
func stepsLess(a, b []AffectedStep) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		if x.Line != y.Line {
			return x.Line < y.Line
		}
		if x.EndLine != y.EndLine {
			return x.EndLine < y.EndLine
		}
		if stableJSON(x) != stableJSON(y) {
			return stableJSON(x) < stableJSON(y)
		}
	}
	return len(a) < len(b)
}
func (q *affectedScan) addLimitation(l AffectedLimitation) {
	l.Paths = append([]string{}, l.Paths...)
	l.Seeds = append([]AffectedSymbol{}, l.Seeds...)
	sort.Strings(l.Paths)
	sort.Slice(l.Seeds, func(i, j int) bool { return stableJSON(l.Seeds[i]) < stableJSON(l.Seeds[j]) })
	q.limitations[stableJSON(l)] = l
}
func (q *affectedScan) limit(code, message, p string, s *AffectedSymbol) {
	l := AffectedLimitation{Code: code, Message: message, Paths: []string{}, Seeds: []AffectedSymbol{}}
	if p != "" {
		l.Paths = append(l.Paths, p)
	}
	if s != nil {
		l.Seeds = append(l.Seeds, *s)
	}
	q.addLimitation(l)
}
func (q *affectedScan) admit(p string, e AffectedEvidence) {
	if q.candidates[p] == nil {
		q.candidates[p] = map[string]AffectedEvidence{}
	}
	terminal := e.Steps[len(e.Steps)-1]
	// Target describes the route, not the stored terminal import identity.
	// One raw import reached through two basename matches is one support site.
	if terminal.Kind == "import" {
		terminal.Target = ""
	}
	key := stableJSON([]any{e.Seed, e.Kind, terminal, p})
	if old, ok := q.candidates[p][key]; !ok || evidenceLess(e, old) {
		q.candidates[p][key] = e
	}
}
func (q *affectedScan) examine(key, p string, seed AffectedSymbol) bool {
	if q.examined[key] {
		return true
	}
	if q.opts.MaxEvidence > 0 && len(q.examined) >= q.opts.MaxEvidence {
		q.limit("evidence_truncation", "Dependency evidence budget omitted records.", p, &seed)
		return false
	}
	q.examined[key] = true
	return true
}
func (q *affectedScan) definitions(name, language string) []AffectedSymbol {
	out := []AffectedSymbol{}
	for _, s := range q.symbols {
		if s.Name == name && inLangs(s.Language, scopeLanguages(language, q.opts.Scope)) {
			out = append(out, s)
		}
	}
	return out
}
func (q *affectedScan) ambiguous(s AffectedSymbol, seed AffectedSymbol) bool {
	if count := len(q.definitions(s.Name, s.Language)); count > 1 {
		q.limit("ambiguous_definitions", fmt.Sprintf("Name-based references match %d scoped definitions of %q.", count, s.Name), s.Path, &seed)
		return true
	}
	return false
}
func symbolStep(s AffectedSymbol) AffectedStep {
	return AffectedStep{Path: s.Path, Name: s.Name, Kind: s.Kind, Line: s.StartLine, EndLine: s.EndLine}
}
func appendStep(steps []AffectedStep, s AffectedStep) []AffectedStep {
	out := append([]AffectedStep{}, steps...)
	return append(out, s)
}

func (q *affectedScan) indirectBoundary(s, seed AffectedSymbol) {
	if q.members[s] || s.Kind == "method" || s.Kind == "interface" || s.Kind == "trait" || s.Kind == "protocol" || s.Kind == "impl" {
		q.limit("indirect_dispatch", "Member or interface dispatch, including declared implements relationships, is not resolved by name matching.", s.Path, &seed)
	}
}

func (q *affectedScan) walkSymbols(seed AffectedSymbol) (bool, error) {
	type node struct {
		s         AffectedSymbol
		steps     []AffectedStep
		depth     int
		ambiguous bool
	}
	firstAmb := q.ambiguous(seed, seed)
	q.indirectBoundary(seed, seed)
	queue := []node{{seed, []AffectedStep{symbolStep(seed)}, 0, firstAmb}}
	seen := map[AffectedSymbol]bool{seed: true}
	reached := false
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		refs, capped, err := q.store.affectedRefs(n.s.Name, scopeLanguages(n.s.Language, q.opts.Scope), q.opts.MaxEvidence)
		if err != nil {
			return false, err
		}
		if capped {
			q.limit("evidence_truncation", "Dependency query exceeded the evidence budget; more records may be omitted.", n.s.Path, &seed)
		}
		next := []node{}
		for _, r := range refs {
			// Probing the depth boundary also examines dependency records. Count
			// them before filtering visited cycles, terminal paths, or owners.
			if !q.examine("ref:"+stableJSON(r), r.Path, seed) {
				continue
			}
			// At the boundary only new relationships prove a cutoff; cycles to already
			// visited production definitions do not imply omitted traversal.
			owners := q.enclosing(r.Path, r.Line)
			terminal := q.classifier.Classify(r.Path) == PathClassTest
			if n.depth >= q.opts.Depth {
				unvisited := len(owners) == 0
				for _, owner := range owners {
					unvisited = unvisited || !seen[owner]
				}
				if terminal || unvisited {
					q.limit("depth_truncation", "Additional symbol relationships remain beyond the depth bound.", r.Path, &seed)
				}
				continue
			}
			indirect := r.Kind != "call" && !terminal
			if indirect {
				q.limit("indirect_dispatch", "Production non-call or implements relationship does not resolve runtime dispatch.", r.Path, &seed)
			}
			amb := n.ambiguous
			if !terminal && len(owners) > 1 {
				q.limit("ambiguous_enclosing_symbol", "Reference line has multiple minimum-span owners; reference columns are not indexed.", r.Path, &seed)
				amb = true
			}
			steps := appendStep(n.steps, AffectedStep{Path: r.Path, Name: r.Name, Kind: r.Kind, Line: r.Line})
			if terminal {
				kind, confidence := "transitive_symbol", "medium"
				if n.depth == 0 {
					kind = "direct_symbol"
					if q.precision == "hunk" {
						confidence = "high"
					}
				}
				if amb {
					confidence = "low"
				}
				q.admit(r.Path, AffectedEvidence{Kind: kind, Seed: seed, Distance: intPointer(n.depth + 1), Ambiguous: amb, Confidence: confidence, Steps: steps})
				reached = true
				continue
			}
			if len(owners) == 0 {
				q.limit("unresolved_reference_boundary", "Production reference has no enclosing indexed definition; symbol traversal cannot continue through this site.", r.Path, &seed)
			}
			for _, owner := range owners {
				q.indirectBoundary(owner, seed)
				ownerAmb := q.ambiguous(owner, seed) || amb
				if !seen[owner] {
					next = append(next, node{owner, appendStep(steps, symbolStep(owner)), n.depth + 1, ownerAmb})
				}
			}
		}
		sort.Slice(next, func(i, j int) bool { return stepsLess(next[i].steps, next[j].steps) })
		for _, v := range next {
			if !seen[v.s] {
				seen[v.s] = true
				queue = append(queue, v)
			}
		}
	}
	return reached, nil
}

// All minimum line-span owners are possible: references have no columns.
// This also covers same-line methods and their enclosing class.
func (q *affectedScan) enclosing(p string, line int) []AffectedSymbol {
	best := []AffectedSymbol{}
	span := int(^uint(0) >> 1)
	for _, s := range q.symbols {
		if s.Path != p || s.StartLine > line || s.EndLine < line {
			continue
		}
		size := s.EndLine - s.StartLine
		if size < span {
			best = best[:0]
			span = size
		}
		if size == span {
			best = append(best, s)
		}
	}
	sort.Slice(best, func(i, j int) bool {
		return stepsLess([]AffectedStep{symbolStep(best[i])}, []AffectedStep{symbolStep(best[j])})
	})
	return best
}

func (q *affectedScan) walkImports(seed AffectedSymbol, fallback bool) (bool, error) {
	type node struct {
		p     string
		steps []AffectedStep
		depth int
	}
	queue := []node{{seed.Path, []AffectedStep{{Path: seed.Path, Kind: "changed_file"}}, 0}}
	seen := map[string]bool{seed.Path: true}
	applicable := false
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		imports, capped, err := q.store.affectedImporters(n.p, q.opts.MaxEvidence)
		if err != nil {
			return false, err
		}
		if capped {
			q.limit("evidence_truncation", "Dependency query exceeded the evidence budget; more records may be omitted.", n.p, &seed)
		}
		for _, r := range imports {
			if !q.examine("import:"+stableJSON(r), r.Path, seed) {
				continue
			}
			terminal := q.classifier.Classify(r.Path) == PathClassTest
			if seen[r.Path] {
				continue
			}
			if n.depth >= q.opts.Depth {
				q.limit("depth_truncation", "Additional reverse imports remain beyond the depth bound.", r.Path, &seed)
				continue
			}
			applicable = true
			q.limit("heuristic_import_fallback", "Raw import matching is heuristic, including basename and package matches.", r.Path, &seed)
			steps := appendStep(n.steps, AffectedStep{Path: r.Path, Kind: "import", RawImport: r.Raw, Target: n.p})
			if terminal {
				kind := "reverse_import"
				if n.depth == 0 {
					kind = "direct_import"
				}
				q.admit(r.Path, AffectedEvidence{Kind: kind, Seed: seed, Distance: intPointer(n.depth + 1), Confidence: "low", Steps: steps})
				continue
			}
			if fallback {
				seen[r.Path] = true
				queue = append(queue, node{r.Path, steps, n.depth + 1})
			}
		}
	}
	return applicable, nil
}
func (q *affectedScan) colocated(seed AffectedSymbol) {
	p := seed.Path
	dir := path.Dir(p)
	ext := path.Ext(p)
	stem := strings.TrimSuffix(path.Base(p), ext)
	for candidate := range q.files {
		if path.Dir(candidate) != dir || q.classifier.Classify(candidate) != PathClassTest {
			continue
		}
		base := path.Base(candidate)
		match := false
		switch ext {
		case ".go":
			match = base == stem+"_test.go"
		case ".py":
			match = base == "test_"+stem+".py" || base == stem+"_test.py"
		case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts":
			for _, testExt := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts"} {
				if base == stem+".test"+testExt || base == stem+".spec"+testExt {
					match = true
				}
			}
		}
		if match {
			q.admit(candidate, AffectedEvidence{Kind: "colocated", Seed: seed, Confidence: "low", Steps: []AffectedStep{{Path: p, Kind: "changed_file"}, {Path: candidate, Kind: "colocated"}}})
			q.limit("colocated_inference", "Source-to-test naming is heuristic, not a dependency edge.", candidate, &seed)
		}
	}
}
