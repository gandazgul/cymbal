---
planId: "5f1a86f9-4cce-41d4-9df0-985f1291b451"
classification: "PLANNED_CHANGE"
workKind: "FEATURE"
complexity: "HIGH"
affectedPaths:
  - "cmd/changed.go"
  - "cmd/affected_tests.go"
  - "index/affected_tests.go"
  - "index/store.go"
  - "index/index.go"
  - "parser/parser.go"
  - "docs/reference/commands.md"
  - "docs/guide/library.md"
  - "docs/domain-language.md"
tickets:
  - url: "https://github.com/1broseidon/cymbal/issues/77"
planDeviations:
  - id: "call_02bc84356e7947d58d4dea49ffa9e007|fc_09eb52924199b4f5016ae61eae15c08190b77989b4a443f5fc"
    supersededRequirement: "8. **The upstream PR is reviewable.** After implementation and full verification, push the feature branch to the user's `origin` fork and open a PR against the upstream default branch, using `.github/PULL_REQUEST_TEMPLATE.md`. Include the issue-closing reference, feature scope, tests actually run, and remaining limitations. The issue is already closed as stale; do not report that this work newly closed it or that the PR merged. If push/PR permissions fail, report the exact failure and preserve the local branch and prepared PR text."
    replacementRequirement: "8. **Prepare the upstream PR and defer delivery.** Complete implementation and focused verification, and prepare PR text using `.github/PULL_REQUEST_TEMPLATE.md`, with the issue-closing reference, feature scope, checks actually run, remaining limitations, and full `make ci` validation marked pending. Hand the implementation to RunWield Workflow Validation without committing, pushing, or opening the PR. Defer the push to the user's `origin` fork and the PR against the upstream default branch until after Workflow Validation passes; then use the actual full-validation results for delivery. Do not claim that the already-closed issue was newly closed or that the PR merged."
    reason: "The user approved deferring PR delivery until after Workflow Validation, so RunWield can run make ci after implementation handoff."
    approvedAt: "2026-10-31T17:12:16.238Z"
executionAgent: "engineer"
collaborationRecommendation: "autonomous"
createdAt: "2026-10-30"
origin: "internal"
userVerifiedAt: null
targetBranch: "main"
deliveryBranch: "plan/affected-test-selection"
routingIntent: "PLANNED_CHANGE"
sessionName: "Affected Tests Command"
status: "implemented"
---

# Ranked, Explainable Affected-Test Selection

## Context

[Issue #77](https://github.com/1broseidon/cymbal/issues/77) asks Cymbal to select relevant test files from changed code. The result must explain its choices, rank them deterministically, and report gaps. The user requests an upstream PR. The maintainer approved the feature and later closed the issue as stale, explicitly inviting a PR.

There is no owning PRD or ADR in this checkout. The issue owns the requested capability and acceptance criteria. Existing behavior is documented in [Changed](../reference/commands.md#cymbal-changed), [Impact](../reference/commands.md#cymbal-impact), [the library guide](../guide/library.md), and [the glossary](../domain-language.md).

The current implementation supplies most inputs, but not the selection contract:

- `cmd/changed.go` attributes changed lines using old and new Git blobs. Its attribution loop is inside the command handler.
- `index/store.go` traverses name-matched references and heuristic import matches. Existing traversals lose some evidence, do not stop at tests, and do not report all depth or import limits.
- `index/classify.go` classifies file paths, including repository-specific `--test-path` patterns. It does not identify runnable test cases.
- `parser/parser.go:extractImportJS` does not record barrel re-exports as imports.

Preserve `changed`, `impact`, `refs`, and `importers` contracts. This change adds a selector, not a replacement for those commands or a full semantic resolver.

## Objective

Deliver `cymbal affected-tests` and an embeddable index API that select repository-relative test paths with ranks, confidence, changed-symbol attribution, dependency distances, and evidence paths.

Support unstaged diff, staged diff, base-ref diff, explicit files, and newline-separated stdin. Provide normal text, JSON, and path-only output. Report uncertainty and recommend the full suite when analysis has gaps. Open a PR against `1broseidon/cymbal` with `Fixes https://github.com/1broseidon/cymbal/issues/77` and the actual verification results.

Do not run selected tests, alter repository-required validation, add an LLM ranker, implement a RunWield tool, or reopen/merge the issue or PR automatically.

## Approach

### Ownership and reuse

Keep Git input handling in `cmd` and selection policy in `index`. Consumers must not reconstruct ranking or status from several query results.

```text
cmd/affected_tests.go
  diff modes -> shared blob attribution used by changed
  file modes -> normalized file inputs
  index.FindAffectedTests(dbPath, input, options)
    reference evidence + import evidence + colocated matches
    test classification + bounded traversal
    deduplication + ranking + analysis status
  text | existing JSON envelope | quiet paths
```

Extract the attribution loop into a package-private `cmd/changed_analysis.go`. Its richer result retains file paths, symbol locations, deletion information, and reasons for failed or skipped analysis. `changed` projects this result into its existing output and counters. Do not make `affected-tests` consume `changed` JSON.

Add a focused selector in `index/affected_tests.go`, with private query code in a companion file if needed. Reuse classification, language scopes, enclosing-symbol logic, index storage, and existing import-matching rules. Do not wrap the existing capped `FindImpactWithOptions` or `BuildGraph` results: they cannot prove complete evidence or stable selection. A small dedicated traversal costs more than concatenating results, but keeps ordering, evidence, and uncertainty under one owner.

No new database tables or graph framework are needed. Record JS/TS re-exports with a source as imports through `extractImportJS`; bump `indexFormat` so existing unchanged files receive these edges on refresh. Do not add alias, receiver, callback, or dynamic-import resolution.

### Inputs and command contract

The following is the proposed contract for review:

| Input or flag | Behavior |
|---|---|
| No files and no `--stdin` | Unstaged working tree versus index, as `changed` does; untracked files are not included |
| `--staged` | Index versus HEAD; attribute using staged blobs even if disk differs |
| `--base REF` | Working tree versus one ref; reject option-like refs and ranges through `changedDiffArgs` |
| Files and/or `--stdin` | Union of file inputs; use every indexed symbol in each file, with file-level precision |
| `--depth/-D` | Default 2, maximum 5; applies to symbol and reverse-import walks |
| `--limit/-n` | Default 50 selected test files, applied after ranking; positive values only |
| `--max-symbols` | Default 40 seed definitions, 0 unlimited; skipped seeds are reported |
| `--max-evidence` | Default 500 examined dependency records across the query, 0 unlimited; exhaustion is reported |
| `--test-path` | Repeatable existing test classification patterns |
| `--resolve-scope` | Existing `same`, `family`, `all`; default `family` |
| `--quiet` | Only selected paths on stdout, one per line; gap/full-suite advice on stderr |
| `--json` | Existing `{ "version": "0.1", "results": ... }` envelope |

Reject staged/base combinations, explicit-file modes combined with diff flags, and `--json --quiet`. Validate flags before refresh or Git work. Empty stdin is an explicit empty input, not a request to analyze the working diff. Arguments and stdin may be combined.

Use a path reader, not `collectSymbols`: preserve spaces and leading `#`, accept CRLF, skip empty lines, propagate read errors, and document that newline input cannot represent filenames containing newlines. Resolve relative inputs from cwd, then normalize to repository-relative slash paths; accept Windows separators consistently. Reject paths outside the selected repository. Missing in-repository files remain visible as limitations rather than disappearing.

Use only the current repository index, not sibling-worktree federation. Honor `--db`/`CYMBAL_DB`, but reject a database whose stored repository root differs from the input repository. Refresh errors and Git/database errors return nonzero without a successful-looking result. An advisory `partial` or `unavailable` result returns zero, with structured status and fallback advice.

### Evidence and ranking

`FindAffectedTests` accepts typed changed files/symbols, input precision, upstream limitations, and selection options. It returns a typed result without printing or invoking Git. Seed identity includes path, name, kind, and location where available; names alone cannot distinguish changed definitions.

Collect candidates in this strength order:

1. **Changed test file:** include the existing changed test itself, distance 0.
2. **Direct symbol reference:** a classified test contains a call or use of the seed name, including top-level test expressions without an enclosing symbol.
3. **Transitive symbol reference:** reverse reference traversal reaches a test through production or unknown-class code. Retain real reference kinds; do not label every edge a call.
4. **Direct import candidate:** a classified test directly imports a path matched to a changed file/package.
5. **Colocated match:** a known test path matches the source stem in the same directory.
6. **Reverse import fallback:** a test is reached through multiple file imports when a seed has no symbol-backed test candidate, or changed content has no usable symbol seed.

Stop each traversal branch at the first file classified as test. Files such as `tests/helpers.ts`, `testdata/*`, and `conftest.py` can be terminal candidates. They are not guaranteed runnable tests. Unknown-class helpers such as `testutil/*` remain traversable. A changed classified test is included directly; do not traverse through it to infer its consumers.

Keep file-import matching explicitly heuristic. Reuse the existing raw-path suffix/basename convention, include nonempty package-directory candidates for file inputs, and treat SQL wildcard characters in paths literally. Store the matched raw import and target in evidence. Basename collisions and broad package matches must not become high-confidence edges. Do not silently drop high-fan-out branches; preserve admitted evidence and report every budget cutoff.

Colocated matching uses existing indexed files, not invented paths. Start with same-directory `<stem>_test.go`, `<stem>.test.*`/`<stem>.spec.*` in the JS/TS family, and `test_<stem>.py`/`<stem>_test.py`. Match supported counterpart extensions within the JS/TS family. Other classifier conventions still work through references, imports, and changed-test inclusion; no claim of universal source-to-test naming inference.

Retain a canonical shortest explanation for each distinct `(seed, evidence kind, terminal reference site or import, test path)` tuple. Duplicate input and repeated discovery of the same evidence do not increase support. Distinct terminal sites or changed seeds count as independent support; do not enumerate every possible graph path. Preserve alternate predecessors needed to construct these explanations without re-querying nodes.

Sort by strongest evidence class, that class's shortest distance, ambiguity penalty, independent support count descending, then normalized path. Sort evidence and changed-symbol lists as well. Resolve equal shortest paths lexically by their location-bearing steps. Apply the test result limit last. Return a 1-based rank rather than an unexplained numeric score.

Confidence is evidence strength, not coverage probability:

- `high`: changed test itself, or an unambiguous direct symbol reference with hunk-precise input.
- `medium`: unambiguous transitive symbol evidence; direct symbol evidence from whole-file inputs.
- `low`: ambiguous symbol paths, import-only paths, and colocated-only matches. Whole-file precision never upgrades weak evidence.

Any ambiguity on an explanation lowers that explanation's confidence. Choose confidence from the best retained evidence, not the number of weak matches. Imports never outrank symbol evidence through fan-out or support count.

### Status and JSON contract

Each result contains `schema_version`, `status`, `input_precision`, effective bounds, `tests`, `limitations`, and `fallback`. Each test contains `path`, `rank`, `confidence`, shortest actual dependency `distance` (null for a naming-only match), `changed_symbols`, and `evidence`. Each evidence item contains its kind, seed identity, distance, and ordered location-bearing symbol or file steps. File-only selections have an empty changed-symbol list and a changed-file seed in their evidence.

Use stable reason codes plus explanatory messages and affected paths/seed identities. Required cases include depth/evidence/seed/result truncation, ambiguous definitions, missing live/deleted seeds, renames, binary/unsupported/conflicted/unreadable inputs, content outside symbols, file-level precision, heuristic import fallback, and unmodelled indirect dispatch. Return empty collections as `[]`, not null.

Approved status meanings:

- `complete`: the supported static analysis finished without a detected gap; an empty diff is complete with no tests.
- `partial`: some analysis was possible, but an input, traversal, precision, or inference gap remains.
- `unavailable`: nonempty input provided no analyzable seed or applicable evidence; explain why. Finding no test for an otherwise analyzed seed alone does not mean unavailable.

Probe a non-test frontier at the depth boundary for additional unvisited relationships. Reaching the numeric bound exactly is not enough to claim truncation. Detect all internal query caps with an extra row or equivalent count. Check ambiguity on intermediate symbols as well as seeds. Propagate query errors instead of swallowing them.

Cymbal cannot detect every unresolved callback or interface dispatch from its current reference schema. Always expose the static-analysis limitation in the contract. Production non-call uses, method/interface seeds, and observed implements relationships must retain uncertainty rather than claim resolved dispatch. These encountered indirect boundaries cause partial status; this is conservative reporting, not new semantic resolution.

**Owner decision:** `complete` describes a completed supported static scan, never full test coverage. Keep the warning that unselected tests are not proven safe to skip. Do not force every nonempty analysis to `partial` solely because static analysis has general limits.

For `partial` and `unavailable`, `fallback.recommended` is true and the reason explicitly says to run the full suite. All statuses carry advisory wording: selection does not prove that unselected tests are safe to skip. Quiet mode preserves its stdout contract and places warnings on stderr; suppress passive update notices for quiet mode.

**Owner decision:** Ship a versioned result contract with `schema_version: 1`. Preserve existing field meanings within that schema version, allow additive fields, and increment the schema version for breaking changes. Consumers must tolerate unknown additive fields.

This versioning applies only to the new command's result schema. Do not change the shared outer envelope or promise that rankings remain identical across algorithm releases. Determinism means identical repository state, options, and implementation produce identical ordered results.

## Expected Change Surface

The boundaries this change is expected to touch. This list is guidance, not an allowlist: verify the real footprint during implementation and change whatever the Implementation Steps need, including files not named here. Stop and report only when discovery changes approved intent — the change reaches another subsystem, public behavior or architecture shifts, migration or compatibility risk grows, or the Verification Plan no longer proves the objective.

- `cmd/changed.go`, new `cmd/changed_analysis.go`, and changed tests — share blob attribution while preserving existing command behavior; retain file-level diagnostics for the new caller.
- New `cmd/affected_tests.go` and CLI tests; `cmd/update.go` — input modes, output formats, validation, current-worktree isolation, and quiet notice suppression.
- New `index/affected_tests.go` and selector query/tests; `index/store.go`, `index/enclosing.go`, `index/scope.go`, `index/classify.go` as needed — authoritative selection, stable evidence, honest bounds, and reuse of existing rules. Existing query APIs remain compatible.
- `parser/parser.go` and parser tests; `index/index.go` and upgrade tests — include static JS/TS re-export imports and refresh old indexes once through `indexFormat`.
- `docs/reference/commands.md`, `docs/guide/library.md`, `README.md`, `MANUAL.md` — command/API examples, ranking and status definitions, JSON compatibility, stdin limits, and advisory behavior.
- `docs/domain-language.md` — add implemented definitions for Affected-test selection, Evidence path, Confidence, and Analysis status. Avoid aliases such as Safe-to-skip list or Coverage guarantee.

No frontend, agent hook, test runner integration, new dependency, or cross-worktree graph is planned.

## Reuse Opportunities

- `changedDiffArgs`, `parseChangedFiles`, `diffPathSide`, `readNewSide`, `readOldSide`, `parseBlobSymbols`, `innermostChanged`, and `isChangedUnit` — preserve current two-sided diff attribution. Enrich the shared result, not a second attribution algorithm.
- `getDBPath`, `ensureFresh`, `gitRepoRoot`, and `canonicalExistingPath` — existing index and repository lifecycle.
- `NewClassifier`, `scopeLanguages`/`scopeLanguagesUnion`, `FileOutline`, file enumeration, and enclosing-symbol intervals — existing model and naming policies. Do not rely on capped `SymbolsByName` to prove absence of further definitions.
- `writeJSON`, `frontmatter`, scope-flag helpers, and Cobra registration — existing command conventions.
- `gitChanged`, `writeFile`, `captureProcessOutput`, `withWorkingDir`, and existing index fixtures — real repository/database tests. Tests that change cwd or process streams remain serial.

## Implementation Steps

1. **Shared attribution retains required evidence.** Both commands use one blob-attribution implementation. Its result retains definition identities, changed/deleted paths, both-side failures, and file-level limitations. Pure renames, binary blocks, and conflicts retain their paths through robust Git metadata parsing; do not split quoted paths on spaces. `changed` keeps its legacy name deduplication, counters, pure-rename treatment, output fields, cap semantics, and deletion rules.
2. **Barrel dependencies exist in fresh and upgraded indexes.** Static `export ... from` and `export * from` statements in supported JS/TS variants yield import records. Existing `import_statement` behavior remains intact. `indexFormat` forces one successful full refresh for old indexes without marking scoped or failed upgrades complete.
3. **The index selector owns discovery and uncertainty.** `FindAffectedTests` and typed input/options/result declarations expose the contract above. Its real Store queries preserve reference kinds and locations, include top-level test references, count scoped definitions without hidden limits, stop at classified tests, follow unknown helpers, and emit heuristic import and naming evidence. Deterministic ordering precedes every budget, and errors do not become empty successful results.
4. **Ranking, explanations, and status agree.** The selector deduplicates tests and support tuples, retains reproducible shortest explanations, applies the stated ranking/confidence rules, and reports all skipped work. Result, seed, evidence, and depth bounds each have an observable limitation. No-result, no-change, whole-file, deleted, and indirect-dispatch cases follow the reviewed status policy. No CLI code duplicates these decisions.
5. **The CLI supports all five input modes and three outputs.** Its registered command applies the stated validation, current-root checks, normalized path handling, classification, and scope. Text shows ranked explanations and advice; JSON uses the existing envelope; quiet writes paths only to stdout and gap advice to stderr. Input/Git/refresh/database errors remain nonzero and do not emit success JSON. Passive update checks respect quiet mode.
6. **Tests prove selection, not just output shape.** Index, parser, upgrade, shared-attribution, and executable CLI fixtures satisfy the verification matrix below through the real parser and SQLite index. Preserve existing attribution and query regression tests; use direct Store fixtures only for relationships/error cases the parser cannot construct reliably.
7. **Documentation matches delivered behavior.** Update the command and library guides, command discovery tables, manual, and glossary together. Define the reviewed schema/status policy and the limits of path classification, name matching, dynamic dispatch, file precision, and naming conventions. Link to issue #77 as the requirement source; do not claim unsupported semantic resolution or universal test discovery.
8. **The upstream PR is reviewable.** After implementation and full verification, push the feature branch to the user's `origin` fork and open a PR against the upstream default branch, using `.github/PULL_REQUEST_TEMPLATE.md`. Include the issue-closing reference, feature scope, tests actually run, and remaining limitations. The issue is already closed as stale; do not report that this work newly closed it or that the PR merged. If push/PR permissions fail, report the exact failure and preserve the local branch and prepared PR text.

## Approval Confirmation

No completed Work Records are proposed for supersession. The owner confirmed bounded static-scan completeness and a versioned JSON result contract. Other proposed defaults in Approach remain reviewable assumptions.

## Verification Plan

### Focused automated checks

- `CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go test ./index -run 'TestAffectedTests|Test.*Format.*|Test.*Upgrade.*' -count=1`
- `CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go test ./cmd -run 'TestAffectedTests|TestChanged|TestParseChanged|TestDiffPathSide|TestInnermostChanged|TestIsChangedUnit|TestParseBlobSymbols|Test.*UpdateNotice.*' -count=1`
- `CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go test ./parser -run 'Test.*Import|Test.*ReExport|Test.*Barrel' -count=1`
- Repeat the deterministic selection tests with `-count=20`. New test names must match these focused commands; adjust commands to include any retained regression tests whose current names differ.

The saved full-project verification command is `make ci`; RunWield runs it after implementation. Focused tests are not a substitute for it.

### Behavioral matrix

| Fixture/action | Required observable result |
|---|---|
| Unique changed function, one direct test, one unrelated test | Select only the direct test with its real file/line reference, distance 1, and expected confidence. A return-all-tests or empty selector fails. |
| Changed symbol through two production callers to a test | Correct distance and complete ordered evidence chain; unrelated callers do not appear in the explanation. |
| Top-level `foo()` in a JS test without enclosing symbol | Direct candidate is retained, unlike the current impact-only traversal. |
| Shared unknown helper; classified test helper with a caller beyond it | Traverse the unknown helper; select and stop at the classified test helper. Do not claim it is runnable or select its downstream consumer through that branch. |
| Two changed symbols and repeated reference sites selecting one test | One test row; both seed identities; deduplicated evidence; independent support affects ties but repeated discovery does not. |
| Diamond/cycle graph and reversed insertion/input order | Finite traversal, canonical shortest evidence, and byte-identical JSON payload/order for equivalent inputs. IDs and insertion order cannot break ties. |
| Direct symbol test plus many barrel/import-only candidates | Symbol test ranks first even with lower support; actual parser-produced re-export edges reach import-only candidates. |
| Same-basename files and literal `%`/`_` import paths | Heuristic candidates are low-confidence and explained; SQL wildcards do not expand the query unintentionally. |
| Colocated Go, JS/TS, and Python test with no graph evidence | Select the existing counterpart with naming evidence, low confidence, and null dependency distance; no nonexistent path is emitted. |
| Callback use and declared interface/implementation fixture | Report actual reference/implements evidence and indirect-dispatch limitations; do not invent a concrete runtime call chain or return a false complete result. |
| Ambiguous seed and ambiguous intermediate symbol, including cross-language collision | Mark affected explanations low-confidence and partial; `same`/`family`/`all` produce the expected scoped candidates and definition counts. |
| Deleted symbol/file, new-side parse failure, rename, unsupported, binary, conflict | Preserve affected paths and reasons; no false live seed, false deletion, or false complete result. |
| Changed test, fixture/config file, supported file change outside symbols | Include an existing changed test itself; preserve symbol-free inputs and attempt file evidence where possible; expose gaps and full-suite advice. |
| Each cap: seeds, examined evidence, selected tests, depth | Test one below, exactly at, and beyond the bound. Beyond-bound work produces a specific reason and full-suite advice; a fully exhausted graph at the bound is not falsely truncated. |
| Default diff, staged with divergent unstaged content, base-ref diff | Exact expected seed from the correct old/new blobs; evidence uses current-worktree index with any missing live seed made explicit. |
| Positional paths, stdin, combined duplicates, empty stdin, CRLF, spaces, `#` filenames, subdirectory cwd | Expected normalized file set and file precision; stdin content is not treated as symbols/comments or silently replaced by the working diff. |
| Windows and Unix separators, Unicode/spaced paths, outside-root path, mismatched DB root | Stable slash-form output; preserve valid names; invalid repository scope is rejected. |
| Empty diff, precise unambiguous scan with all branches exhausted, analyzed seed with no test, wholly unsupported input | Empty diff and gap-free bounded scans are `complete`; lack of candidates alone does not make an analyzed seed unavailable; wholly unsupported input without evidence is `unavailable`. Empty arrays and advisory wording remain correct; no claim that no tests are required. |
| JSON, quiet, text, invalid flag combinations, refresh/Git/query failure | Parseable existing envelope with `results.schema_version: 1`, contract fields and types, exact path-only quiet stdout, explicit advice, documented status/exit behavior, and no success output after operational failure. |
| Indexed sibling worktree has the only matching test | No cross-worktree candidate leaks into selection. |
| Old index with unchanged barrel source | Normal refresh adds the re-export edge once; subsequent refresh does not repeat the format upgrade. |

Keep existing tests for staged attribution, deletion naming, innermost enclosing definitions, quoted paths, header-like hunk text, no false deletions on failed parsing, `changed` caps, hidden-test traversal in `impact`, language scope, and importer/query compatibility. None of these existing behaviors is being retired.

### Manual and semantic review

- In a temporary Git fixture, change a function with a direct and a transitive test. Run each documented mode against the built binary. Compare text explanations and JSON paths with actual source, not snapshots alone.
- Use `--depth 1`, `--max-evidence 1`, and `--limit 1` on fixtures that exceed each bound. Check the reason and full-suite recommendation. Pipe quiet output to a file and confirm it contains only selected paths.
- Inspect every evidence step against a stored reference, import match, changed-file fact, or documented naming rule. A plausible explanation assembled from disconnected same-name nodes must carry ambiguity, never asserted semantic certainty.
- Confirm one index-level entry point owns ranking/status, both diff commands share attribution, and no existing output contract changed. Confirm the documentation/glossary uses the implemented meanings and leaves unresolved static-analysis capability explicitly unsupported.
- Check the final PR URL, upstream repository/base, issue-closing reference, and reported commands. Do not claim delivery from a local commit alone.

## Edge Cases & Considerations

- **Staged attribution versus current graph:** preserve `changed` semantics. If the staged symbol has no matching live definition, keep the limitation; never substitute another same-name definition as a proven target.
- **File classification is not test discovery:** stopping at test helpers follows the issue's rule, but may omit runnable consumers. Document this and never equate selection with executable test inventory.
- **Static blind spots:** reference names cannot prove callback, alias, reflection, or interface dispatch. Report encountered limitations and retain the general advisory even for a completed bounded scan.
- **High fan-out and cycles:** sorted, budgeted traversal prevents unbounded path enumeration; every cutoff remains visible. Users can raise explicit budgets when needed.
- **Parser upgrade:** reuse the existing index format mechanism rather than adding a schema migration or assuming unchanged files reparse automatically.
- **Path inputs:** newline-separated input cannot encode newlines in filenames. Separator normalization follows the documented cross-platform path policy; no glob expansion, directory expansion, or shell evaluation is added.
- **Scope assumption:** file modes operate within the current Git repository, as diff modes do. Support for a standalone non-Git directory is not part of this PR.
- **Delivery:** open the upstream PR after checks; no automatic merge or separate issue-state change is requested by this Plan.
