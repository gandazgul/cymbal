# Library Guide

cymbal exposes five Go packages for embedding code indexing and navigation into your own tools.

## Install

```sh
CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go get github.com/1broseidon/cymbal@latest
```

> **CGO required.** cymbal uses tree-sitter (C) for parsing and SQLite (C) for storage. The `SQLITE_ENABLE_FTS5` flag enables full-text search.

## Packages

| Package | Import | Purpose |
|---------|--------|---------|
| `index` | `github.com/1broseidon/cymbal/index` | Indexing engine, SQLite store, and all query APIs |
| `lang` | `github.com/1broseidon/cymbal/lang` | Unified language registry for names, extensions, special filenames, `#!` interpreters, and parser availability |
| `parser` | `github.com/1broseidon/cymbal/parser` | Tree-sitter parsing for 22 languages |
| `symbols` | `github.com/1broseidon/cymbal/symbols` | Core data types: `Symbol`, `Import`, `Ref`, `ParseResult` |
| `walker` | `github.com/1broseidon/cymbal/walker` | Concurrent file discovery with language detection |

Most consumers only need `index`. The other packages are useful if you want to parse files without indexing, walk directories with custom filters, or work with raw symbol data.

---

## Language registry

The `lang` package is the canonical source of truth for language support in cymbal. It models both:

- **known / recognized languages** — files cymbal can classify by extension, special filename, or (for extensionless files) `#!` interpreter
- **supported / parseable languages** — files with a tree-sitter grammar that can be parsed and indexed

```go
import "github.com/1broseidon/cymbal/lang"

fmt.Println(lang.Default.Supported("go"))            // true
fmt.Println(lang.Default.Known("dockerfile"))       // true
fmt.Println(lang.Default.LangForFile("Dockerfile")) // "dockerfile"

l := lang.Default.ForFile("notes.toml")
fmt.Println(l.Name)        // "toml"
fmt.Println(l.Parseable()) // false

// Classify an extensionless script by its first bytes (up to lang.ShebangMaxBytes).
fmt.Println(lang.Default.ForShebang([]byte("#!/usr/bin/env python3\n")).Name) // "python"
```

Use `lang.Default.Supported` when you need the parseable subset for indexing or parsing. Use `Known` / `LangForFile` when classification alone is enough.

---

## Indexing

### Index a repository

```go
import "github.com/1broseidon/cymbal/index"

stats, err := index.Index("/path/to/repo", "", index.Options{})
// stats.FilesIndexed, stats.SymbolsFound, stats.StaleRemoved, etc.
```

**Parameters:**
- `root` — absolute path to the repo root
- `dbPath` — path to the SQLite database. Pass `""` to auto-compute from the repo root (stored under the OS cache directory)
- `opts.Workers` — number of parallel parse workers. `0` defaults to `runtime.NumCPU()`
- `opts.Force` — if `true`, re-index all files regardless of mtime
- `opts.Exclude` — repeatable repo-relative path patterns to skip while indexing
- `opts.IncludeGenerated` — if `true`, disables default generated-file skips
- `opts.IncludeLargeFiles` — if `true`, disables default large-source-file skips

**What it does:**
1. Walks the directory tree, skipping dot-dirs, `node_modules`, `vendor`, generated/large files, and any configured excludes
2. Compares each file's mtime+size against the stored index — skips unchanged files
3. Parses changed files with tree-sitter, extracting symbols, imports, and references
4. Writes results to SQLite in batched transactions
5. Prunes stale entries for deleted/renamed files

### Resolve the database path

```go
dbPath, err := index.RepoDBPath("/path/to/repo")
// e.g. ~/.cache/cymbal/repos/a1b2c3d4e5f6g7h8/index.db
```

Each repo gets its own database, keyed by a SHA-256 hash of the repo root path.

### Keep the index fresh

```go
refreshed, err := index.EnsureFreshWithError(dbPath)
if err != nil {
    return err
}
```

Call this before queries. It runs an incremental reindex, re-parsing only files that changed since the last index. It returns the number of files refreshed (0 if nothing changed) and reports failures separately. On a partial refresh, the count includes successful changes and the error describes the incomplete refresh.

Static JS/TS barrel re-exports (`export ... from` and `export * from`) are
stored as imports. Older indexes reparse unchanged files once on the next full
refresh to add these edges. A scoped or failed upgrade does not mark the full
index upgraded. This does not resolve aliases or dynamic imports.

If the database doesn't exist yet, it auto-indexes from the current working directory's git root. The older `index.EnsureFresh(dbPath) int` API remains available for callers that deliberately accept stale results; it returns 0 on failure.

---

## Querying

All query functions take a `dbPath` string and return typed results. They open/close the database internally — no connection management needed.

### Search symbols

```go
results, err := index.SearchSymbols(dbPath, index.SearchQuery{
    Text:     "handleAuth",
    Kind:     "function",  // optional: filter by kind
    Language: "go",        // optional: filter by language
    Exact:    false,       // true = exact name match only
    Limit:    50,
})

for _, r := range results {
    fmt.Printf("%s %s %s:%d\n", r.Kind, r.Name, r.RelPath, r.StartLine)
}
```

Search is ranked: exact match > prefix > fuzzy (via FTS5).

### Flexible search

```go
results, err := index.SearchSymbolsFlex(dbPath, "HandleAuth", 50)
```

Tries case-insensitive exact match first, then falls back to FTS prefix match. Useful when user input may not match the exact casing.

### Investigate a symbol

```go
inv, err := index.Investigate(dbPath, "handleAuth")
```

Returns a kind-adaptive result:
- **Functions/methods** → source + callers (refs) + transitive impact
- **Types/structs/classes** → source + members + references

```go
type InvestigateResult struct {
    Symbol  SymbolResult   `json:"symbol"`
    Source  string         `json:"source"`
    Kind    string         `json:"investigate_kind"` // "function" or "type"
    Refs    []RefResult    `json:"refs,omitempty"`
    Impact  []ImpactResult `json:"impact,omitempty"`
    Members []SymbolResult `json:"members,omitempty"`
}
```

Use `InvestigateOpts` to disambiguate when multiple symbols share a name:

```go
inv, err := index.Investigate(dbPath, "Config", index.InvestigateOpts{
    FileHint: "auth/config.go",
})
```

### Find references

```go
refs, err := index.FindReferences(dbPath, "handleAuth", 50)

for _, r := range refs {
    fmt.Printf("%s:%d\n", r.RelPath, r.Line)
}
```

Returns call sites and usages of a symbol across the indexed codebase. Based on AST name matching, not semantic analysis.

### Trace (downward call graph)

```go
trace, err := index.FindTrace(dbPath, "handleAuth", 3, 50)

for _, t := range trace {
    fmt.Printf("[%d] %s → %s  %s:%d\n", t.Depth, t.Caller, t.Callee, t.RelPath, t.Line)
}
```

Follows the call graph downward: what does this function call, what do those call, etc. `depth` controls how many hops to follow (max recommended: 3-4).

### Impact (upward call graph)

```go
impact, err := index.FindImpact(dbPath, "handleAuth", 2, 100)

for _, i := range impact {
    fmt.Printf("[%d] %s called by %s  %s:%d\n", i.Depth, i.Symbol, i.Caller, i.RelPath, i.Line)
}
```

Follows the call graph upward: what calls this function, what calls those callers. Answers "what breaks if I change this?"

### Select affected tests

`index.FindAffectedTests(dbPath, input, opts)` returns `(*index.AffectedResult,
error)`. It owns discovery, ranking, confidence, bounds, and status. It reads
**one current index only**: it does not refresh, invoke Git, federate across
worktrees, print, or run tests. The caller must select the correct repository
store, validate/normalize paths, refresh it, and supply changed input.

This complete Go example initializes or refreshes a repository index and selects
from a whole file. `root` must be an absolute repository root; `relPath` must be
an in-repository slash-separated file path relative to that root, not cwd.

```go
package example

import (
    "fmt"

    "github.com/1broseidon/cymbal/index"
)

func SelectTests(root, relPath string) (*index.AffectedResult, error) {
    dbPath, err := index.RepoDBPath(root)
    if err != nil {
        return nil, err
    }
    stats, err := index.Index(root, dbPath, index.Options{})
    if err != nil {
        return nil, err
    }
    if stats.Errors > 0 {
        return nil, fmt.Errorf("incomplete index: %d errors", stats.Errors)
    }

    input := index.AffectedInput{
        Precision: "file",
        Files: []index.AffectedFile{
            {Path: relPath},
        },
    }
    opts := index.DefaultAffectedOptions()
    return index.FindAffectedTests(dbPath, input, opts)
}
```

For an existing store, call `index.EnsureFreshWithError(dbPath)` before building
input and querying, and propagate refresh errors. The example uses `Index` to
make initialization independent of cwd. Do not treat an incomplete refresh as a
successful empty selection.

#### Input identities and options

`AffectedInput` has `Files []AffectedFile`, `Symbols []AffectedSymbol`,
`Precision string` (`"hunk"` or `"file"`), and
`Limitations []AffectedLimitation`. File precision expands files to all their
indexed definitions and reports `file_level_precision`; it is not hunk precision.
Each `AffectedFile` has `Path` and `Deleted`.

For hunk input, supply definitions actually touched by the change, not bare
names. A live `AffectedSymbol` must **exactly match the current store** across
`Path`, `Name`, `Kind`, `Language`, `StartLine`, `EndLine`, and `Deleted` (false
for live definitions). Lines are the stored 1-based definition bounds, not hunk
bounds. Obtain current locations and identity fields from the indexed outline
or symbol results after refresh. Paths are repository-relative slash paths.
Include affected files too, so symbol-free changes retain file evidence.

For a removed definition, preserve its old identity with `Deleted: true`;
mark a removed file likewise. Deleted definitions have no live graph identity.
A stale/staged definition with different current lines or any other identity
field becomes `missing_live_seed`, not a proven match to another same-name
symbol. Deleted seeds remain limitations and can use heuristic file evidence.
Callers must supply upstream gaps through `AffectedLimitation{Code, Message,
Paths, Seeds}`, including failed attribution, unsupported input, or changes
outside definitions. The API does not discover a Git diff or validate repository
containment for the caller. The [CLI](../reference/commands.md#cymbal-affected-tests)
adds cwd resolution, cross-platform separator handling, and repository-root checks.

Start with `DefaultAffectedOptions()`, not a zero-valued `AffectedOptions`:

| Field | Default | Constraint |
|-------|---------|------------|
| `Depth` | 2 | 1–5; symbol and reverse-import depth |
| `Limit` | 50 | Positive; selected files after ranking |
| `MaxSymbols` | 40 | Nonnegative; 0 unlimited seed definitions |
| `MaxEvidence` | 500 | Nonnegative; 0 unlimited examined dependency records |
| `TestPaths` | empty | Additional test classification patterns, substring or glob with `**` |
| `Scope` | `ResolveScopeFamily` | `ResolveScopeSame`, `ResolveScopeFamily`, or `ResolveScopeAll`; empty normalizes to family |

#### Results and interpretation

`AffectedResult` includes `SchemaVersion`, `Status`, `InputPrecision`, effective
`Bounds`, `Tests`, `Limitations`, `Fallback`, `Advisory`, and
`StaticAnalysisLimit`. Each `AffectedTest` includes `Path`, 1-based `Rank`,
`Confidence`, `Distance *int`, `ChangedSymbols`, and `Evidence`. Evidence retains
`Kind`, `Seed`, `Distance`, `Ambiguous`, `Confidence`, and ordered
location-bearing `Steps`. Import steps retain raw import and target. File-only
selections have no changed symbols and retain a changed-file seed in evidence.
Empty result collections serialize as `[]`, not `null`.

Evidence classes, strongest first, are `changed_test`, `direct_symbol`,
`transitive_symbol`, `direct_import`, `colocated`, and `reverse_import`.
Ranking compares strongest class, its shortest distance, ambiguity penalty,
independent support within that class descending, then normalized path. Distinct terminal
sites or seeds add support; duplicate discovery does not. Canonical shortest
explanations and sorted evidence/identities make results deterministic for the
same repository state, options, and implementation. Rankings can change across
algorithm releases. `Distance` is the shortest actual dependency distance across
evidence, not a score; it is nil for naming-only matches.

Confidence is evidence strength, not coverage probability: changed tests and
unambiguous hunk-precise direct references are high; unambiguous transitive
references and whole-file direct references are medium; ambiguous, import-only,
and colocated-only explanations are low. The best retained evidence determines
confidence; many weak matches do not upgrade it.

`Status` is `complete` for a supported scan without detected gaps (including
empty input), `partial` when some analysis has input/precision/traversal/inference
gaps, or `unavailable` when nonempty input has no analyzable seed or applicable
evidence. No matching test for an otherwise analyzed seed alone is not
unavailable. Whole-file precision, heuristic inference, missing live/deleted
seeds, encountered indirect dispatch, and budget cutoffs remain limitations.
General static limits alone do not force every scan to partial. For partial or
unavailable, `Fallback.Recommended` is true and `Fallback.Reason` says to run the
full suite. A nil error can return any advisory status; operational query
errors return an error instead. **No status proves unselected tests safe to skip.**

Classification uses file paths, not runnable-test discovery. Traversal stops at
the first classified test, including helpers such as `tests/helpers.ts`,
`testdata/*`, and `conftest.py`; unknown-class helpers such as `testutil/*` remain
traversable. References match names, not resolved runtime dispatch. Aliases,
callbacks, reflection, and interface dispatch are not fully resolved. Imports
use low-confidence raw suffix/basename/package heuristics. Colocated matches
check only existing same-directory Go `<stem>_test.go`, JS/TS
`<stem>.test.*`/`<stem>.spec.*` (js/jsx/ts/tsx/mjs/cjs/mts/cts counterparts),
and Python `test_<stem>.py`/`<stem>_test.py` for `.py` sources; they do not infer
all test naming conventions.

JSON tags use `schema_version: 1`. Preserve existing meanings within that
version, tolerate unknown additive fields, and require a new schema version for
breaking changes. The CLI alone wraps this result in the unchanged
`{"version":"0.1","results":...}` envelope; the API returns the typed result,
not that envelope. See the [command contract](../reference/commands.md#cymbal-affected-tests)
for detailed evidence/status rules. Requirement source:
[issue #77](https://github.com/1broseidon/cymbal/issues/77).

### Find importers

```go
// By symbol name — finds files that import the file containing a symbol
importers, err := index.FindImporters(dbPath, "handleAuth", 2, 50)

// By file/package path directly
importers, err := index.FindImportersByPath(dbPath, "internal/auth", 2, 50)
```

`depth` controls transitive import analysis (1 = direct importers only, 2 = importers of importers).

### Full context bundle

```go
ctx, err := index.SymbolContext(dbPath, "handleAuth", 20)
// ctx.Symbol   — the resolved symbol
// ctx.Source   — full source code
// ctx.TypeRefs — type symbols referenced in this function
// ctx.Callers  — who calls this
// ctx.FileImports — imports in the same file
```

### Structural overview

```go
structure, err := index.Structure(dbPath, 10)
// structure.EntryPoints    — main/init/handler functions
// structure.TopByRefs      — most-referenced symbols
// structure.TopByImportFan — most-imported files
// structure.TopPackages    — largest packages
```

### File outline

```go
syms, err := index.FileOutline(dbPath, "/absolute/path/to/file.go")

for _, s := range syms {
    indent := strings.Repeat("  ", s.Depth)
    fmt.Printf("%s%s %s (L%d-%d)\n", indent, s.Kind, s.Name, s.StartLine, s.EndLine)
}
```

### Text search (grep)

```go
results, err := index.TextSearch(dbPath, "TODO", "go", 50)
// Pass "" for lang to search all languages
```

`TextSearch` matches a literal substring. For the CLI's line-oriented Go regex behavior and path constraints:

```go
results, err := index.TextSearchWithOptions(dbPath, `TODO|FIXME`, "go", 50,
    index.TextSearchOptions{
        Regexp: true,
        Paths: index.PathFilter{Include: []string{"src/**"}, Exclude: []string{"**/*_test.go"}},
    })
```

Both variants search indexed files in path/line order. Filters apply before the result limit. Symbol search accepts the same filter in `index.SearchQuery.Paths`; reference lookup accepts it through `index.FindReferencesWithPaths`.

### List indexed repos

```go
repos, err := index.ListRepos()
for _, r := range repos {
    fmt.Printf("%s — %d files, %d symbols\n", r.Path, r.FileCount, r.SymbolCount)
}
```

---

## Lower-level: Store

For advanced use cases, open the store directly:

```go
store, err := index.OpenStore(dbPath)
defer store.Close()

// Direct store methods
results, err := store.SearchSymbols("handleAuth", "function", "go", true, 50)
refs, err := store.FindReferences("handleAuth", 50)
members, err := store.ChildSymbols("UserService", 50, "/path/to/file.go")
trace, err := store.FindTrace("handleAuth", 3, 50)
impact, err := store.FindImpact("handleAuth", 2, 100)
imports, err := store.FileImports("/path/to/file.go")
stats, err := store.RepoStats()

// Metadata
root, err := store.GetMeta("repo_root")
err = store.SetMeta("key", "value")
```

This avoids repeated open/close overhead when running multiple queries in sequence. The store holds a `*sql.DB` connection with WAL mode and busy timeout configured.

---

## Parsing without indexing

Use the `parser` package to extract symbols from a single file without touching SQLite:

```go
import (
    "github.com/1broseidon/cymbal/parser"
    "github.com/1broseidon/cymbal/symbols"
)

// From a file path
result, err := parser.ParseFile("/path/to/handler.go", "go")

// From bytes (avoids re-reading the file)
src, _ := os.ReadFile("/path/to/handler.go")
result, err := parser.ParseBytes(src, "/path/to/handler.go", "go")

// result.Symbols — []symbols.Symbol
// result.Imports — []symbols.Import
// result.Refs    — []symbols.Ref

for _, sym := range result.Symbols {
    fmt.Printf("%s %s L%d-%d\n", sym.Kind, sym.Name, sym.StartLine, sym.EndLine)
}
```

Check language support:

```go
import "github.com/1broseidon/cymbal/lang"

if parser.SupportedLanguage("go") {
    // ...
}

if lang.Default.Known("dockerfile") {
    // recognized for classification, but not parseable/indexable
}
```

`parser.SupportedLanguage` delegates to `lang.Default.Supported`.

### Supported languages

Parseable/indexed languages:

- Go
- Python (`.py`, `.pyw`)
- JavaScript (`.js`, `.jsx`, `.mjs`, `.cjs`)
- TypeScript (`.ts`, `.tsx`, `.mts`, `.cts`)
- Rust
- C / C++
- C#
- Java
- Ruby (`.rb`, `.rake`, `.gemspec`)
- Swift
- Kotlin (`.kt`, `.kts`)
- Scala (`.scala`, `.sc`)
- PHP
- Lua
- Bash / shell
- YAML
- Elixir
- HCL / Terraform (`.tf`, `.hcl`, `.tfvars`)
- Protobuf
- Dart

Recognized but not parseable/indexable examples: `Dockerfile`, `Makefile`, `Jenkinsfile`, `CMakeLists.txt`, Apex, JSON, TOML, Markdown, SQL, Vue, Svelte, Zig, Erlang, Haskell, OCaml, R, and Perl.

---

## File discovery

Use the `walker` package to find source files with concurrent directory traversal:

```go
import (
    "github.com/1broseidon/cymbal/lang"
    "github.com/1broseidon/cymbal/walker"
)

// Walk only parseable/indexable files
files, err := walker.Walk("/path/to/repo", 0, lang.Default.Supported)

for _, f := range files {
    fmt.Printf("%s (%s, %d bytes)\n", f.RelPath, f.Language, f.Size)
}
```

`Walk` skips dot-directories, `node_modules`, `vendor`, `__pycache__`, build output, etc. Pass `nil` for the language filter to include all recognized file types, including non-parseable ones such as `Dockerfile` and `Makefile`. An extensionless file that the name alone cannot classify (`bin/deploy`) is classified by its `#!` line, so scripts are indexed too; only regular files are read, and never ones matched by `WalkOptions.Exclude`.

Detect a file's language from its path (this does not read the file, so an extensionless script returns `""` here even though `Walk` would classify it):

```go
lang := walker.LangForFile("handler.go") // "go"
lang := walker.LangForFile("Dockerfile") // "dockerfile"
lang := walker.LangForFile("styles.css") // "" (unrecognized)
```

Build a directory tree (for `cymbal ls`-style output):

```go
tree, err := walker.BuildTree("/path/to/repo", 3) // maxDepth 3, 0 = unlimited
walker.PrintTree(os.Stdout, tree, "")
```

---

## Result types

All result types have JSON struct tags and serialize cleanly.

### SymbolResult

```go
type SymbolResult struct {
    Name      string `json:"name"`
    Kind      string `json:"kind"`       // function, method, struct, class, etc.
    File      string `json:"file"`       // absolute path
    RelPath   string `json:"rel_path"`   // relative to repo root
    StartLine int    `json:"start_line"`
    EndLine   int    `json:"end_line"`
    Parent    string `json:"parent,omitempty"`    // enclosing type/class
    Depth     int    `json:"depth"`               // nesting depth (0 = top-level)
    Signature string `json:"signature,omitempty"` // parameter list
    Language  string `json:"language"`
    BodyHash  string `json:"body_hash,omitempty"` // changes only when the symbol's own lines change
}
```

### RefResult

```go
type RefResult struct {
    File    string `json:"file"`
    RelPath string `json:"rel_path"`
    Line    int    `json:"line"`
    Name    string `json:"name"`
}
```

### TraceResult

```go
type TraceResult struct {
    Caller  string `json:"caller"`    // the function making the call
    Callee  string `json:"callee"`    // the function being called
    File    string `json:"file"`
    RelPath string `json:"rel_path"`
    Line    int    `json:"line"`
    Depth   int    `json:"depth"`     // hop distance from root
}
```

### ImpactResult

```go
type ImpactResult struct {
    Symbol  string `json:"symbol"`    // the callee
    Caller  string `json:"caller"`    // the calling function
    File    string `json:"file"`
    RelPath string `json:"rel_path"`
    Line    int    `json:"line"`
    Depth   int    `json:"depth"`     // hop distance from original
}
```

### ImporterResult

```go
type ImporterResult struct {
    File    string `json:"file"`
    RelPath string `json:"rel_path"`
    Import  string `json:"import"`
    Depth   int    `json:"depth"`
}
```

---

## CGO and build notes

cymbal requires CGO for tree-sitter and SQLite:

```sh
# Build
CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go build ./...

# Test
CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go test ./...

# Install
CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go install github.com/1broseidon/cymbal@latest
```

Without the `SQLITE_ENABLE_FTS5` flag, the database will fail to create the FTS5 virtual table and all queries will error on first use.

Cross-compilation requires a C cross-compiler for the target platform.
