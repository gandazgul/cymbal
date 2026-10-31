# Cymbal Domain Language

Cymbal provides code navigation for people and AI agents. It identifies code symbols and their relationships in repositories and Git worktrees.

## Language

### Code and relationships

**Symbol**: A named code element, such as a function, method, type, or variable. A symbol has a kind, a source location, and a language. It can have a parent symbol and members.

**Reference**: A site where code refers to a name. References are matched by name, not by full semantic analysis. A matching reference is a lead, not proof that it refers to one specific definition. _Avoid_: Verified dependency.

**Call**: A reference that invokes a function, method, or constructor.

**Use**: A reference that is not a call or an implements relationship. Type mentions are uses.

**Implements relationship**: A declared inheritance, conformance, extension, or embedding relationship between a type and another named type. It does not prove implicit structural conformance. _Avoid_: Complete interface implementation detection.

**Import**: A source statement that names a module, package, or file used by the source file. Static JS/TS re-exports with a source also create import records; this does not resolve aliases or dynamic imports.

**Caller**: A symbol that contains a call to another name.

**Callee**: The name invoked by a call in a caller.

**Member**: A symbol nested in, or associated with, a parent type.

**Body hash**: A value that identifies the source text within a symbol's line range. Moving the symbol without changing that text does not change its body hash.

### Languages and resolution

**Recognized language**: A language that Cymbal can identify from a file extension, a special filename, or a supported shebang. Recognition alone does not mean that Cymbal can extract symbols from the file.

**Supported language**: A recognized language for which Cymbal can parse source and extract symbols and relationships. _Avoid_: Recognized language when parse support is required.

**Language family**: A group of languages that can share name resolution. The families are JavaScript/TypeScript/TSX, Java/Kotlin/Scala, and C/C++.

**Resolution scope**: The set of languages allowed when matching names. `same` allows the source language, `family` also allows its language family, and `all` allows all languages. The default is `family`.

**Unresolved name**: A referenced name with no matching indexed definition in the query's resolution scope. This can be an external name or a name excluded by the scope. _Avoid_: Missing code as a definite conclusion.

### Repositories and indexes

**Repository**: The source tree that Cymbal indexes and queries.

**Index**: Cymbal's local record of files, symbols, imports, and references for a source tree.

**Incremental refresh**: An update that reparses changed files and removes stale entries without reparsing unchanged files.

**Scoped indexing**: An index update limited to a subtree. It does not remove or reparse files outside that subtree.

**Worktree**: A Git checkout with its own working files and Cymbal index. Sibling worktrees share a Git common directory.

**Federation**: A lookup across indexed sibling worktrees of the same repository. Results keep their worktree identity. Relationship traversal stays in the worktree that owns the seed symbol. _Avoid_: Merged cross-branch call graph.

### Navigation queries

**Investigate**: A query shaped by the symbol's kind. Functions and methods receive source, references, and impact. Types receive source, members, and references.

**Trace**: A downward traversal from a symbol to its callees. By default, it follows call references only. _Avoid_: Impact for downward traversal.

**Impact**: An upward traversal from a symbol to its callers and their callers. Results distinguish production callers from test callers. _Avoid_: Trace for upward traversal, guaranteed breakage.

**Importer**: A file that imports the file or package associated with a query target. Importer queries can follow more than one import step.

**Changed symbol**: A symbol touched by the selected Git diff. Cymbal uses source from both sides of the diff to identify it. Deleted symbols can be listed, but have no current impact result.

**Graph**: A relationship view with nodes and edges. It can show calls, imports, or implements relationships. It is distinct from a list of reference sites.

**Outline**: The symbols defined in a source file, with their locations and nesting.

**Context**: A query bundle with a symbol's source, referenced types, callers, and file imports.

### Affected-test selection

The requirement source is [issue #77](https://github.com/1broseidon/cymbal/issues/77).
See the [command contract](reference/commands.md#cymbal-affected-tests) and
[library API](guide/library.md#select-affected-tests).

**Affected-test selection**: A ranked, explainable, advisory selection of indexed test paths from changed files or definitions. It uses only the current repository index, never sibling-worktree federation. It does not run tests, replace required validation, or prove unselected tests safe to skip. _Avoid_: Safe-to-skip list, Coverage guarantee.

**Test classification**: A file-path rule that labels code as production, test, or unknown. It is not runnable-test discovery. Selection stops at the first classified test file, including helpers such as `tests/helpers.ts`, `testdata/*`, or `conftest.py`; unknown-class helpers such as `testutil/*` remain traversable. A changed test is selected directly, not traversed to infer its consumers.

**Changed seed**: A changed definition identified by path, name, kind, language, line range, and deletion state, or a changed-file identity when no usable definition exists. A live definition must exactly match the current store; a name alone is not an identity. Hunk precision identifies changed definitions; file precision uses all indexed definitions in each input file and reports the precision gap.

**Evidence path**: An ordered, location-bearing explanation from a changed seed to a selected test path. Reference steps retain their actual kinds; import steps retain the raw import and matched target. Canonical shortest explanations do not claim fully resolved semantic dependencies. File-only selections retain a changed-file seed and no changed-symbol list.

**Evidence class**: The strength category used for ranking, in order: changed test, direct symbol reference, transitive symbol reference, direct import candidate, colocated naming match, reverse import fallback. Import matches use raw suffix/basename/package heuristics and stay low-confidence. Colocated matches use only existing same-directory Go, JS/TS, or Python naming counterparts, not universal test discovery.

**Independent support**: Distinct terminal reference/import sites or changed seeds supporting a selected path. Duplicate input or repeated discovery of the same evidence does not add support. More weak evidence does not outrank a stronger evidence class.

**Rank**: A 1-based position, not an unexplained numeric score. Ordering uses strongest evidence class, its shortest distance, ambiguity penalty, independent support within that class descending, then normalized path. Identical repository state, options, and implementation give deterministic ordered results; algorithm releases need not preserve rankings.

**Dependency distance**: The number of actual reference or import hops in an explanation. A selected path reports its shortest actual distance across evidence; a changed test has distance 0 and a naming-only match has null distance. This can differ from the distance used to rank its strongest evidence class.

**Confidence**: Evidence strength, not coverage probability. Changed tests and unambiguous hunk-precise direct symbol references are high; unambiguous transitive symbol evidence and whole-file direct references are medium; ambiguous, import-only, and naming-only evidence is low. Use the best retained evidence, not a count of weak matches. _Avoid_: Probability that skipping other tests is safe.

**Analysis status**: `complete` means the supported static scan finished without a detected gap, not full test coverage. An empty diff is complete. `partial` means some analysis was possible but input, precision, traversal, or inference gaps remain. `unavailable` means nonempty input had no analyzable seed or applicable evidence; no test match for an analyzed seed alone is not unavailable. Partial and unavailable results recommend the full suite. All three are advisory successes (CLI exit 0); operational failures exit nonzero. Static name matching cannot resolve all aliases, callbacks, reflection, or interface dispatch. Encountered indirect boundaries cause gaps; general static limits alone do not force every scan to partial.

**Affected-test schema version**: `schema_version: 1` versions the selection result independently of the unchanged CLI outer `version: "0.1"` envelope. Schema 1 allows additive fields; consumers must tolerate unknown fields. Breaking changes require a new schema version, not a promise of unchanged ranking algorithms.

### Agent integration

**Agent hook**: An integration that supplies Cymbal guidance to an AI agent runtime. It is not a Git hook.

**Nudge**: A non-blocking suggestion to use a Cymbal query instead of a recognized code-search command.

**Reminder**: A block of Cymbal usage guidance supplied at session start or on request.

## Open Language Questions

None identified.
