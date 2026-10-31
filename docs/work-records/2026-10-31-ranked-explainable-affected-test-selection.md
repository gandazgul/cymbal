---
kind: "work_record"
recordId: "def7e004-266a-42d9-888e-7d50c0cd9f25"
status: "approved"
scope: "planned_change"
workKind: "FEATURE"
origin: "internal"
completionMode: "verified"
createdAt: "2026-10-31T17:24:53.047Z"
tickets:
    - url: "https://github.com/1broseidon/cymbal/issues/77"
provenance:
    sourcePlans:
        - "5f1a86f9-4cce-41d4-9df0-985f1291b451"
---
# Ranked, explainable affected-test selection

## Summary

Cymbal provides `affected-tests` and `index.FindAffectedTests` to select repository-relative test paths with deterministic ranks, confidence, changed-symbol attribution, and evidence paths. The selector supports unstaged, staged, base-ref, explicit-file, and stdin inputs, with text, versioned JSON, and path-only output. Results report analysis gaps and bounds, recommend the full suite for partial or unavailable analysis, and remain advisory even when analysis is complete. Selection policy belongs to the index API, while diff attribution is shared with `changed` without changing existing command contracts. JS/TS barrel re-exports supply import evidence, and existing indexes acquire these edges through a successful full format refresh.

## Deviations from Plan

1. Superseded requirement: 8. **The upstream PR is reviewable.** After implementation and full verification, push the feature branch to the user's `origin` fork and open a PR against the upstream default branch, using `.github/PULL_REQUEST_TEMPLATE.md`. Include the issue-closing reference, feature scope, tests actually run, and remaining limitations. The issue is already closed as stale; do not report that this work newly closed it or that the PR merged. If push/PR permissions fail, report the exact failure and preserve the local branch and prepared PR text.
   Replacement requirement: 8. **Prepare the upstream PR and defer delivery.** Complete implementation and focused verification, and prepare PR text using `.github/PULL_REQUEST_TEMPLATE.md`, with the issue-closing reference, feature scope, checks actually run, remaining limitations, and full `make ci` validation marked pending. Hand the implementation to RunWield Workflow Validation without committing, pushing, or opening the PR. Defer the push to the user's `origin` fork and the PR against the upstream default branch until after Workflow Validation passes; then use the actual full-validation results for delivery. Do not claim that the already-closed issue was newly closed or that the PR merged.
   Reason: The user approved deferring PR delivery until after Workflow Validation, so RunWield can run make ci after implementation handoff.

## Out of Scope

This work does not include running selected tests, changing required repository validation, cross-worktree selection, full semantic resolution, LLM ranking, or test-runner integration.

## Future Planning Notes

Treat `complete` as completion of the supported static scan, not proof of test coverage or permission to skip unselected tests. Confidence measures evidence strength, not coverage probability; encountered indirect-dispatch boundaries, ambiguity, and cutoffs must remain visible. Classified test files are terminal candidates and can be helpers rather than runnable tests. Preserve existing field meanings within result schema version 1, tolerate additive fields, and increment the schema version for breaking changes without changing the shared JSON envelope. Determinism applies to identical repository state, options, and implementation, not rankings across releases. Index-format upgrades must not be marked complete after a failed or scoped refresh.