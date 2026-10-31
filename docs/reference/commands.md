# Commands

All commands support `--json` for structured output. Each repo gets its own database in the OS cache directory, auto-resolved from your working directory (`~/.cache/cymbal/repos/<hash>/index.db` on Linux, `~/Library/Caches/cymbal/repos/<hash>/index.db` on macOS, `%LOCALAPPDATA%\cymbal\repos\<hash>\index.db` on Windows).

Path/language heuristics recognize some special filenames such as `Dockerfile`, `Makefile`, `Jenkinsfile`, and `CMakeLists.txt` even though they are not parseable/indexable languages.

## Global Flags

| Flag | Description |
|------|-------------|
| `-d, --db <path>` | Override path to cymbal database (default: auto-resolved per repo) |
| `--json` | Output as JSON instead of frontmatter+content |

Passive update notices are suppressed automatically for `--json` output and
`affected-tests --quiet`. Set `CYMBAL_NO_UPDATE_NOTIFIER=1` to disable passive update notices entirely.

Query commands refresh the index before reading it and exit nonzero if that refresh fails. For a name the index knows, `impact`, `trace`, `refs`, `impls` and `investigate` exit 0 even when the answer is empty, with their usual output and a zero count, such as `total_callers: 0` or an empty `results` list in JSON. A name the index has never seen exits nonzero with `symbol not found: <name>`, as in `show` and `context`. A name that is only referenced, like a call to an external `Println` or an interface such as `io.Reader` named in an `implements` clause or an embedding, is known to `impact`, `trace`, `refs` and `impls`; `investigate` needs a definition. Those four match bare names, so ask for `Reader`, not `io.Reader`. In a batch, all five print the names that resolved and exit nonzero for the rest (`impls --json` and `investigate --json` still list a missing name, with its error), and `outline` keeps the files that worked when another fails, so batch consumers should check the exit status as well as the payload. An empty outline is not an error, and `search` exits nonzero when it has no matches.

Symbol objects in JSON output (`search`, `show`, `outline`, `context`, `investigate`, `structure`) include `body_hash`: the first 16 hex characters of the SHA-256 of the symbol's source lines, `start_line` to `end_line`, with line endings and trailing whitespace removed. It changes when those lines change, and not when the symbol only moves or other code in the file changes, so a script can store `file:Name` with its `body_hash` and later check whether that symbol changed. Symbols with identical source share a hash. An index built by an earlier release reparses every file once on its next refresh to fill it in.

---

## Graph Output

Add `--graph` to `trace`, `impact`, `importers`, or `impls` when you want a
high-level relationship map rather than call-site detail.

- Default format is Mermaid on a TTY and JSON when piped.
- Use `--graph-format mermaid|dot|json` to force a specific format.
- Use `--graph-limit <n>` to cap dense graphs by degree.
- `impact --graph` defaults to depth `1` unless you explicitly pass `--depth`.
- `--include-unresolved` is useful on symbol graphs when external relationships matter.

Stay with the normal text or JSON output when you need exact source lines,
call sites, or detail you will edit against.

---

## `cymbal index`

Index a directory for symbol discovery.

```sh
cymbal index [path] [flags]
```

| Flag | Description |
|------|-------------|
| `--exclude <glob>` | Exclude files whose path matches this glob during indexing (repeatable) |
| `-f, --force` | Force re-index all files |
| `--include-generated` | Index generated files that cymbal skips by default |
| `--include-large-files` | Index large source files that cymbal skips by default |
| `-w, --workers <n>` | Number of parallel workers (0 = NumCPU) |

```sh
# Index current directory
cymbal index .

# Force re-index with 8 workers
cymbal index . --force --workers 8

# Skip an additional generated subtree
cymbal index . --exclude 'src/generated/**'
```

---

## `cymbal version`

Print build/version information and cached release status.

```sh
cymbal version [--json]
```

- Human output includes the current build information and, when available, a suggested update command.
- `--json` adds a structured `update` object with `checked_at`, `cache_stale`, `available`, `latest_version`, `install_type`, `command`, `release_url`, and `source`.
- `cymbal --version` stays terse and only prints the installed version.

---

## `cymbal ls`

Show file tree, repo list, or repo statistics.

```sh
cymbal ls [path|pattern] [flags]
```

| Flag | Description |
|------|-------------|
| `-D, --depth <n>` | Max tree depth (0 = unlimited) |
| `--repos` | List all indexed repositories |
| `--stats` | Show repo overview (languages, file/symbol counts) |
| `--names` | Flat sorted list of indexed file paths (one per line) |
| `--lang <language>` | With `--names`: only files of this language |
| `--null` | With `--names`: NUL-terminate entries (for `xargs -0`) |

```sh
# File tree
cymbal ls

# Top-level only
cymbal ls --depth 1

# Repo stats
cymbal ls --stats

# All indexed repos
cymbal ls --repos

# Indexed file inventory (skip rules applied under default index options)
cymbal ls --names

# Narrow by pattern (same semantics as --path/--exclude) or language
cymbal ls --names 'parser/**'
cymbal ls --names --lang typescript

# Feed batch commands (NUL-safe)
cymbal ls --names --null 'cmd/**' | xargs -0 -n1 cymbal outline
```

---

## `cymbal structure`

Show the structural shape of the indexed codebase. All data is derived from the
existing index. Reports entry points, most referenced symbols, most imported
files, and largest packages. Designed to answer "I've never seen this repo —
where do I start?"

```sh
cymbal structure [flags]
```

| Flag | Description |
|------|-------------|
| `-n, --limit <n>` | Max items per section (default: 10) |

```sh
cymbal structure
cymbal structure --limit 5
cymbal structure --json
```

---

## `cymbal outline`

Show symbols defined in a file.

```sh
cymbal outline <file> [file2 ...] [flags]
```

| Flag | Description |
|------|-------------|
| `-s, --signatures` | Show full parameter signatures |

```sh
cymbal outline internal/auth/handler.go
cymbal outline internal/auth/handler.go --signatures
cymbal outline internal/auth/handler.go internal/auth/service.go --names
```

---

## `cymbal search`

Search symbols by name, or use `--text` for full-text grep. Results are ranked: exact match > prefix > fuzzy.

```sh
cymbal search <query> [path ...] [flags]
```

Trailing path operands are accepted as `--path` filters, which matches common
`rg` usage: `cymbal search --text <pattern> cmd internal/foo.go`.
In symbol mode, multiple query arguments are searched independently:
`cymbal search Foo Bar Baz`.

Text mode accepts a line-oriented Go regular expression, for example `Alpha|Beta` or `(?i)todo`. It searches the indexed file inventory using the same matching rules with or without ripgrep. Generated, large, and explicitly excluded files remain absent unless enabled when indexing. `.gitignore` does not independently change text-search eligibility: an indexed file is eligible even if Git ignores it. Results are ordered by relative path and line, and language/path filters apply before the result limit. Invalid regex patterns and file-read failures produce nonzero exits.

| Flag | Description |
|------|-------------|
| `-t, --text` | Full-text grep across file contents |
| `-e, --exact` | Exact name match only |
| `-k, --kind <type>` | Filter by symbol kind (function, class, method, etc.) |
| `-l, --lang <name>` | Filter by language (go, python, typescript, etc.) |
| `-n, --limit <n>` | Max results (default: 20) |
| `--path <glob>` | Include only results whose path matches this glob or path fragment (repeatable) |
| `--exclude <glob>` | Exclude results whose path matches this glob or path fragment (repeatable) |
| `--stdin` | Read additional symbol queries from stdin |

```sh
# Symbol search
cymbal search handleAuth

# Batch symbol search
cymbal search PatchMulti MultiEdit EditTool PatchTool

# Full-text grep
cymbal search "TODO" --text

# Full-text grep scoped to files/directories, rg-style
cymbal search --text 'os\.WriteFile\(' tools/file.go tools/patch.go

# Only Go functions
cymbal search parse --kind function --lang go
```

---

## `cymbal show`

Read source code by symbol name or file path.

```sh
cymbal show <symbol|file[:L1-L2|:Symbol]> [flags]
```

| Flag | Description |
|------|-------------|
| `-C, --context <n>` | Lines of context around the target |

`file.go:SymbolName` looks up a symbol with a file hint: results are narrowed to files whose path ends with (or contains) the hint, falling back to global results if nothing matches. Otherwise, an argument containing `/` or ending with a known extension is treated as a file path; anything else is a symbol name.

```sh
# Show a symbol's source
cymbal show handleAuth

# Narrow a common name by file hint
cymbal show store.go:SearchSymbols

# Show a file
cymbal show internal/auth/handler.go

# Show specific lines
cymbal show internal/auth/handler.go:80-120

# Show with surrounding context
cymbal show handleAuth -C 5
```

---

## `cymbal context`

Show bundled context for a symbol: source code, callers, and imports of the
defining file (plus referenced types in `--json` output). The single best
command for "I'm about to edit this symbol".

```sh
cymbal context <symbol> [flags]
```

| Flag | Description |
|------|-------------|
| `-n, --callers <n>` | Max callers to show (default: 20) |

```sh
cymbal context OpenStore
cymbal context ParseFile --callers 10
```

---

## `cymbal diff`

Show the git diff for a symbol's line range. Resolves the symbol to a file and
line range, then runs `git diff` filtered to only hunks that overlap the
symbol's definition.

```sh
cymbal diff <symbol> [base] [flags]
```

| Flag | Description |
|------|-------------|
| `--stat` | Show diffstat for the whole defining file (not symbol-filtered) |

```sh
# diff vs HEAD
cymbal diff ParseFile

# diff vs main branch
cymbal diff ParseFile main

# diff vs specific commit
cymbal diff ParseFile abc123

# show diffstat only
cymbal diff --stat ParseFile
```

---

## `cymbal importers`

Find files that import a given file or package.

```sh
cymbal importers <file|package> [flags]
```

| Flag | Description |
|------|-------------|
| `-D, --depth <n>` | Import chain depth (max 3, default: 1) |
| `-n, --limit <n>` | Max results (default: 50) |
| `--graph` | Render target's fan-in as a visual graph |
| `--graph-format <fmt>` | `mermaid`, `dot`, or `json` (implies `--graph`) |
| `--graph-limit <n>` | Cap the graph size by degree (0 for no cap) |

```sh
cymbal importers internal/auth
cymbal importers internal/auth --graph
```

---

## `cymbal impls`

Find types that implement an interface, or elements an explicit type implements.

```sh
cymbal impls <symbol> [flags]
```

| Flag | Description |
|------|-------------|
| `-n, --limit <n>` | Max results (default: 50) |
| `--of <type>` | Inverse: find interfaces that the given `<type>` implements |
| `--unresolved` | Only show external / unresolved targets |
| `--graph` | Render inheritance as a visual graph |
| `--graph-format <fmt>` | `mermaid`, `dot`, or `json` (implies `--graph`) |
| `--include-unresolved`| Graph unresolved external nodes as dashed `ext:` boxes |
| `--graph-limit <n>` | Cap the graph size by degree (0 for no cap) |

```sh
cymbal impls Reader
cymbal impls --of MyStruct --graph --include-unresolved
```

---

## `cymbal refs`

Find references to a symbol across indexed files.

```sh
cymbal refs <symbol> [flags]
```

| Flag | Description |
|------|-------------|
| `-n, --limit <n>` | Max results (default: 50) |
| `--importers` | Find files that import the defining file |
| `--impact` | Transitive impact analysis (`--importers --depth 2`) |
| `-D, --depth <n>` | Import chain depth for `--importers` (max 3, default: 1) |

References are best-effort based on AST name matching, not semantic analysis. Results are deduplicated — identical call sites in the same file are grouped.

```sh
# Direct references
cymbal refs handleAuth

# Who imports this package?
cymbal refs handleAuth --importers

# Transitive impact
cymbal refs handleAuth --impact
```

---

## `cymbal investigate`

Kind-adaptive investigation — returns the right shape of context for whatever a
symbol is, so you don't have to choose between `search`, `show`, `refs`, and
`impact`.

```sh
cymbal investigate <symbol> [symbol2 ...] [flags]
```

| Flag | Description |
|------|-------------|
| `--stdin` | Read additional symbol names (newline-separated) from stdin |
| `--resolve-scope <s>` | `same` \| `family` \| `all` (default: family) |

What you get back depends on the symbol's kind:

- function / method → source + callers + shallow impact
- class / struct / type / interface → source + members + references
- ambiguous name → auto-resolves to the best match and notes the alternatives

Disambiguate with a file or parent hint (`config.go:Config`, `auth.Middleware`),
and pass several names (or pipe newline-separated names via `--stdin`) to
investigate a batch.

```sh
cymbal investigate OpenStore
cymbal investigate config.go:Config     # file hint
cymbal investigate Foo Bar Baz          # batch
cymbal outline svc.go -s --names | cymbal investigate --stdin
```

---

## `cymbal trace`

Downward call trace — what does this symbol call? Complementary to `impact`,
which traces callers upward.

```sh
cymbal trace <symbol> [symbol2 ...] [flags]
```

| Flag | Description |
|------|-------------|
| `--depth <n>` | Max traversal depth (default: 3) |
| `-n, --limit <n>` | Max results per symbol (default: 50) |
| `--kinds <list>` | Comma-separated ref kinds to follow: `call`, `use`, `implements` (default: `call`) |
| `--stdin` | Read additional symbol names (newline-separated) from stdin |
| `--resolve-scope <s>` | `same` \| `family` \| `all` (default: family) |
| `--include-unresolved` | Keep callees that don't resolve to an indexed symbol (stdlib, third-party, builtins); dashed `ext:` nodes under `--graph` |
| `--graph` | Render the call tree as a visual graph |
| `--graph-format <fmt>` | `mermaid`, `dot`, or `json` (implies `--graph`) |
| `--graph-limit <n>` | Cap the graph size by degree (0 for no cap) |

By default `trace` follows only invocation edges (`call`) and drops callees that
don't resolve to an indexed symbol. `--include-unresolved` keeps those external
callees in the text/JSON output (and renders them as dashed `ext:` nodes under
`--graph`). Like `impact`, `trace` reports `truncated` when a per-symbol
`--limit` is hit. Pass several names (or pipe via `--stdin`) for the union of
callees, deduplicated with a `hit_symbols` attribution list.

```sh
cymbal trace handleRegister                      # call chain (depth 3)
cymbal trace handleRegister --depth 5            # deeper trace
cymbal trace Save Load Delete                    # union of callees
cymbal trace handleRegister --include-unresolved # keep stdlib/external calls
cymbal outline svc.go -s --names | cymbal trace --stdin
```

---

## `cymbal impact`

Transitive caller analysis — what is impacted if a symbol changes.

```sh
cymbal impact <symbol> [symbol2 ...] [flags]
```

| Flag | Description |
|------|-------------|
| `-D, --depth <n>` | Max call-chain depth (max 5, default: 2) |
| `-n, --limit <n>` | Max callers per symbol (default: 50) |
| `--no-tests` | Exclude callers in test files (keeps production + unknown) |
| `--test-path <pat>` | Classify matching paths as test code, in addition to the built-in conventions (substring, or glob with `**`; repeatable) |
| `--resolve-scope <s>` | `same` \| `family` \| `all` (default: family) |

Callers are classified by file path as **production**, **test**, or **unknown**,
so the header reports the real blast radius rather than a bare count:

```sh
$ cymbal impact Index --depth 1
total_callers: 29 (5 production, 24 test)
references: 39 (8 production, 31 test) in 12 (4 production, 8 test)
```

- `truncated: true` is shown (and emitted in `--json`) when a per-symbol
  `--limit` was hit, so a partial caller set is never presented as complete.
- The `references` line / JSON `metrics` block are exact, un-truncated counts of
  references to the symbol (reference sites and distinct files, split by class) —
  true breadth even when callers are capped. They are name-scoped: an ambiguous
  name (several definitions) is flagged via `definition_count` / `definitions`.
- `--no-tests` drops test-file callers; classification happens during traversal,
  so test callers never consume the `--limit` budget ahead of production ones.
  In `--graph` mode, hidden test nodes are contracted: a production caller
  reachable only through a test helper stays connected to the seed via a dashed
  edge marked `"indirect": true` in the JSON.
- `--test-path` extends the built-in test classification with repo-specific
  patterns (e.g. `--test-path qa/ --test-path '**/*_it.go'`) for layouts the
  conventions don't cover. It affects the caller split, `--no-tests`, and the
  `metrics` block on both `impact` and `changed`.

```sh
# only the production callers worth inspecting
cymbal impact Index --no-tests

# treat the qa/ tree as test code too
cymbal impact Index --no-tests --test-path qa/
```

`trace` likewise reports `truncated` when its `--limit` is hit.

---

## `cymbal changed`

Diff-scoped impact — map a git diff to the symbols it touches and report each
one's references and transitive impact in a single call.

```sh
cymbal changed [flags]
```

| Flag | Description |
|------|-------------|
| `--staged` | Diff the staged changes (index vs HEAD) instead of the working tree |
| `--base <ref>` | Diff the working tree against another single ref (e.g. `main`) |
| `-D, --depth <n>` | Max call-chain depth for impact (max 5, default: 2) |
| `-n, --limit <n>` | Max callers per changed symbol (default: 50) |
| `--max-symbols <n>` | Max changed symbols to analyze (default: 40, 0 = unlimited) |
| `--max-impact <n>` | Soft cap on total caller rows across symbols (default: 500) |
| `--no-tests` | Exclude callers in test files from impact |
| `--test-path <pat>` | Classify matching paths as test code, in addition to the built-in conventions (substring, or glob with `**`; repeatable) |
| `--resolve-scope <s>` | `same` \| `family` \| `all` (default: family) |

Defaults to **unstaged** working-tree changes (`git diff`). Changed symbols are
attributed by parsing the actual diffed blobs on both sides: added/modified lines
map to symbols in the new version, deleted lines to symbols in the old version.
So whole-symbol deletions are **named** (listed under `deleted`), `--staged`
attribution matches the staged content even with unstaged edits present, and
each changed line maps to its enclosing navigable definition.

```sh
# what does my uncommitted edit affect?
$ cymbal changed
changed_symbols: 1
base: working tree
---
# ClassifyPath  (index/classify.go)
  references: 6 (4 production, 2 test) in 6 (4 production, 2 test)
  impact: 14 (12 production, 2 test) callers
```

```sh
cymbal changed --staged        # staged changes (index vs HEAD)
cymbal changed --base main     # working tree vs your branch point
cymbal changed --json          # full structured payload for agents
```

Operates only on the current worktree. Counts are name-scoped (cymbal resolves
references by name); arbitrary commit ranges (`a..b`) and deleted-symbol impact
are out of scope. Deleted/binary/unsupported files are reported, not silently
dropped.

---

## `cymbal affected-tests`

Select ranked, explainable **test paths**, not runnable test cases. This is an
advisory selector, not a test runner or coverage guarantee. Requirement source:
[issue #77](https://github.com/1broseidon/cymbal/issues/77).

```sh
cymbal affected-tests [files...] [flags]
```

### Input modes and scope

| Input | Comparison / precision |
|-------|------------------------|
| No files or `--stdin` | Unstaged working tree versus index; hunk precision; excludes untracked files |
| `--staged` | Index versus HEAD; hunk precision from staged blobs, even if disk differs |
| `--base <ref>` | Working tree versus one ref; hunk precision, not a commit range |
| Positional files | All indexed definitions in those files; file precision |
| `--stdin` | Newline-separated files; file precision; may combine with positional files |

Diff attribution parses both old and new blobs. The dependency graph always
comes from the refreshed **current working tree**, including in staged/base
modes. Deleted definitions and staged definitions that do not match a live
indexed identity remain limitations; a same-name definition is not substituted
as a proven target.

All modes require the current Git repository. The command refreshes its index
and never federates across sibling worktrees; `--no-federate` is not needed.
`-d, --db <path>` and `CYMBAL_DB` can select the database, but its stored
repository root must match the current repository. A mismatched root is an
error, not a way to query another checkout.

### Flags and conflicts

| Flag | Default | Meaning |
|------|---------|---------|
| `--staged` | `false` | Use staged diff mode |
| `--base <ref>` | unset | Use one Git ref; reject refs starting with `-` and ranges such as `a..b` |
| `--stdin` | `false` | Read file paths, not symbol names or comments |
| `-D, --depth <n>` | `2` | Maximum symbol and reverse-import dependency depth; 1–5 |
| `-n, --limit <n>` | `50` | Maximum selected test files, applied after ranking; positive only |
| `--max-symbols <n>` | `40` | Maximum seed definitions; 0 = unlimited, negative invalid |
| `--max-evidence <n>` | `500` | Maximum examined dependency records across the query; 0 = unlimited, negative invalid |
| `--test-path <pat>` | built-ins only | Add test classification patterns; substring or glob with `**`; repeatable |
| `--resolve-scope <s>` | `family` | Name matching in `same` language, language `family`, or `all` languages |
| `--quiet` | `false` | Only selected paths on stdout, one per line; warnings/advice on stderr |
| `--json` | `false` | Structured result in the shared JSON envelope |

`--staged` and `--base` conflict. File inputs (including `--stdin`) cannot be
combined with either diff flag. `--json` and `--quiet` conflict. Invalid options
are rejected before Git analysis or index refresh. There is no graph output
mode for this command.

```sh
cymbal affected-tests
cymbal affected-tests --staged --depth 3
cymbal affected-tests --base main --json
cymbal affected-tests 'src/auth handler.ts' --test-path 'qa/**'
printf '%s\n' 'src/auth.ts' '#helper.ts' | cymbal affected-tests src/api.ts --stdin --quiet
```

### Path input rules

- Arguments and stdin form a deduplicated union. Empty stdin with `--stdin`
  means empty explicit input, **not** the default working diff.
- Stdin accepts CRLF and skips empty lines. Spaces and leading `#` are
  preserved; lines are not trimmed or interpreted as comments.
- Relative paths resolve from the current working directory, including when
  it is a subdirectory. Output paths are repository-relative with `/` separators.
- Both `/` and `\` are separators on all platforms. A literal backslash in a
  Unix filename cannot be distinguished from a separator. Windows drive paths
  are not treated as relative Unix filenames.
- Paths outside the current repository, including symlink escapes, are rejected.
  Missing in-repository files are reported as limitations. Symbolic-link inputs
  are not indexed.
- Supply files, not directories. Cymbal adds no glob expansion, directory
  expansion, or shell evaluation. Quote paths to prevent unwanted shell expansion.
- Newline-separated input/output cannot represent filenames containing newlines;
  this command has no NUL-delimited mode.

### Evidence, ranking, and confidence

Candidates use this strength order:

| Order | Evidence kind | Meaning |
|-------|---------------|---------|
| 1 | `changed_test` | The existing changed file itself is classified as test; distance 0 |
| 2 | `direct_symbol` | A test directly calls or uses a changed name, including top-level expressions |
| 3 | `transitive_symbol` | Reverse references reach a test through production or unknown-class symbols |
| 4 | `direct_import` | A test directly imports a path heuristically matched to the changed file/package |
| 5 | `colocated` | An existing same-directory test matches the source stem; no dependency distance |
| 6 | `reverse_import` | Multiple reverse imports reach a test when the seed lacks symbol-backed test evidence, or usable symbols are absent |

Sort by strongest evidence class, its shortest distance, ambiguity penalty,
independent support count within that class descending, then normalized path. Distinct terminal
reference/import sites or changed seeds give independent support; repeated
input or repeated discovery of the same evidence does not. Many weak matches
do not outrank a stronger class. Retain canonical shortest explanations, with
lexical location-bearing steps breaking equal-path ties; sort evidence and
changed-symbol lists too. Apply the result limit last and assign 1-based ranks,
not numeric scores. Identical repository state, options, and implementation
produce identical ordering; algorithm releases need not keep the same ranking.

Confidence measures evidence strength, **not coverage probability**:

- `high`: a changed test itself, or unambiguous direct symbol evidence from
  hunk-precise input.
- `medium`: unambiguous transitive symbol evidence, or direct symbol evidence
  from whole-file input.
- `low`: ambiguous symbol paths, import-only evidence, or naming-only evidence.

Any ambiguity in an explanation lowers its confidence. A selected path takes
its confidence from its best retained evidence, not a count of weak matches.
Its `distance` is the shortest actual dependency distance across its evidence;
that can differ from the distance used for ranking its strongest class. A
naming-only match has `distance: null`.

### Static analysis and test classification limits

References are name matches, not full semantic resolution. Actual `call`, `use`,
and `implements` kinds are retained. Aliases, callbacks, reflection, and
interface dispatch are not fully resolved. Encountered indirect-dispatch
boundaries and ambiguous definitions cause gap reports; an otherwise completed
supported scan is not forced to `partial` merely because static analysis has
general limits.

Import matching uses raw-path suffix, basename/stem, and nonempty package-directory
heuristics, not a module resolver. Basename collisions and broad package matches
stay low-confidence; evidence keeps the raw import and matched target. Static
JS/TS `export ... from` and `export * from` barrel re-exports are indexed as
imports. Existing indexes reparse unchanged files once on the next full refresh
to acquire these edges; a failed or scoped upgrade does not complete that upgrade.
This does not add alias or dynamic-import resolution.

Traversal stops at the first file classified as test. For example,
`tests/helpers.ts`, `testdata/*`, and `conftest.py` can be terminal selections,
not runnable tests. Unknown-class helpers such as `testutil/*` remain traversable.
A changed classified test is selected directly, not traversed to infer consumers.
`--test-path` changes these boundaries, not just display labels.

Colocated inference uses **existing indexed files** in the same directory:

- Go: `<stem>_test.go`.
- JS/TS: `<stem>.test.<ext>` or `<stem>.spec.<ext>`, across counterpart extensions
  `js`, `jsx`, `ts`, `tsx`, `mjs`, `cjs`, `mts`, `cts`.
- Python: `test_<stem>.py` or `<stem>_test.py` for a `.py` source.

Other classification conventions can select tests through references, imports,
or changed-test inclusion; they do not imply universal source-to-test naming
inference. Generated/excluded/unindexed files are not a complete test inventory.

### Result contract, status, and exit behavior

`--json` uses the unchanged outer envelope
`{ "version": "0.1", "results": ... }`. Inside `results`, `schema_version: 1`
versions the affected-test contract separately. Existing field meanings are
preserved within schema 1; additive fields are allowed and consumers must
tolerate unknown fields. Breaking changes require a new schema version. This
is not a promise of identical rankings across releases.

The result contains `schema_version`, `status`, `input_precision` (`hunk` or
`file`), effective `bounds`, `tests`, `limitations`, `fallback`, `advisory`, and
`static_analysis_limit`. Each test has `path`, `rank`, `confidence`, `distance`,
`changed_symbols`, and `evidence`. Evidence contains `kind`, `seed`, `distance`,
`ambiguous`, `confidence`, and ordered location-bearing `steps`; import steps
also retain `raw_import` and `target`. Seeds identify path, name, kind, language,
line range, and deletion state. File-only selections have an empty
`changed_symbols` list and a changed-file seed in evidence. Empty collections
serialize as `[]`, not `null`.

| Status | Meaning | Fallback |
|--------|---------|----------|
| `complete` | Supported static scan finished without a detected gap; an empty diff is complete with no tests | No automatic full-suite recommendation; still advisory |
| `partial` | Some analysis was possible, but input, precision, traversal, or inference gaps remain | `fallback.recommended: true`; run the full suite |
| `unavailable` | Nonempty input has no analyzable seed or applicable evidence | `fallback.recommended: true`; run the full suite |

Finding no tests for an analyzed seed alone does not mean `unavailable`.
Whole-file precision is a limitation. Limitations have stable `code`, explanatory
`message`, affected `paths`, and `seeds`. They report seed/evidence/result/depth
cutoffs, ambiguous or missing live/deleted definitions, renames, binary,
unsupported, conflicted or unreadable input, content outside symbols, heuristic
inference, and encountered indirect dispatch. Reaching a depth bound alone
is not truncation unless further relationships remain.

All successful analyses, including `partial` and `unavailable`, exit **0**.
Validation, Git, refresh, and database/query errors exit **nonzero**, without a
successful-looking result. Read the status and fallback, not just the exit code.
Normal text shows ranks, explanations, limitations, and advice. Quiet output
keeps stdout path-only and sends advice to stderr; passive update notices are
suppressed. No mode runs tests or changes required validation.

**Selection does not prove that unselected tests are safe to skip**, including
when status is `complete` or no paths are selected.

---

## `cymbal hook`

Agent-integration helpers for session reminders, shell nudges, and supported
agent installers.

```sh
cymbal hook <subcommand> [flags]
```

### `cymbal hook remind`

Print the short persistent guidance block agents should treat as system
context.

```sh
cymbal hook remind [flags]
```

| Flag | Description |
|------|-------------|
| `--format <fmt>` | `text` (default), `json`, or `claude-code` |
| `--update <mode>` | `cache` (default) or `if-stale` |

- `--update=cache` uses cached update status only.
- `--update=if-stale` performs a bounded live update check only when cache is stale or missing.
- Reminder output can surface update guidance, but cymbal still never self-updates by default.

### `cymbal hook notify`

Emit a structured update notification payload for agent plugins that want to
surface update notices outside hidden system context.

```sh
cymbal hook notify [flags]
```

| Flag | Description |
|------|-------------|
| `--format <fmt>` | `json` (default) or `text` |
| `--update <mode>` | `cache` (default) or `if-stale` |

- Returns `{"notify": true, ...}` with version and command when an update is available and the notification throttle allows it.
- Returns `{"notify": false}` when no update is available or the user was already notified recently.
- `text` format prints a plain notice; empty output when no notice is due.
- Respects `CYMBAL_NO_UPDATE_NOTIFIER`.
- Uses cymbal's per-version notification throttle (24h TTL).

### `cymbal hook nudge`

Inspect a would-be shell command and, if it looks like code navigation through
`rg`, `grep`, `find`, or `fd`, emit a cymbal suggestion.

```sh
cymbal hook nudge [--format <fmt>] [-- <command> [args...]]
```

| Flag | Description |
|------|-------------|
| `--format <fmt>` | `claude-code` (default), `text`, or `json` |

- Exit code is always `0`.
- `text` writes the suggestion to stderr.
- `json` emits a generic `suggest` / `why` payload for non-Claude integrations.

### `cymbal hook install`

Install cymbal-managed integration for a supported agent.

```sh
cymbal hook install <agent> [flags]
```

| Flag | Description |
|------|-------------|
| `--scope <scope>` | `user` (default) or `project` |
| `--dry-run` | Print the target path and intended managed content without writing |

Supported agents:

- `opencode`
- `claude-code`

Examples:

```sh
cymbal hook install opencode
cymbal hook install opencode --scope project
cymbal hook install opencode --scope project --dry-run
cymbal hook install claude-code
```

For `opencode`, re-running install upgrades the existing cymbal-managed plugin
file in place. The managed plugin is written with OpenCode's single `CymbalPlugin`
export shape and nudges Bash, Grep, and Glob tool calls toward cymbal-first code
navigation. If a non-cymbal file already exists at cymbal's target path, install
refuses to overwrite it.

Only one cymbal-managed OpenCode scope is supported at a time. If a managed
plugin already exists in the other scope, install refuses until that scope is
uninstalled.

### `cymbal hook uninstall`

Remove cymbal-managed integration for a supported agent.

```sh
cymbal hook uninstall <agent> [flags]
```

The same `--scope` and `--dry-run` flags apply.

For `opencode`, uninstall removes only the cymbal-managed plugin file and
leaves unrelated user-owned files untouched.
