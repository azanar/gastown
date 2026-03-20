# Plan Review: Alert Context Content Part

## Overall Verdict
**GO WITH FIXES** — The plan is architecturally sound and well-reasoned. The core design (structured content part replacing artifact + polling) is correct and follows the right patterns. However, the complete absence of a test plan is a blocking gap, and the `slack_context` precedent the plan cites doesn't exist in the codebase, which needs clarification before implementation begins. The remaining issues (concurrency, performance, observability) are important but addressable during implementation.

## Leg Verdicts
| Dimension | Verdict | Key Finding |
|-----------|---------|-------------|
| Completeness | PASS WITH NOTES | `slack_context` precedent doesn't exist; missing error handling, observability, rollback, and test items |
| Sequencing | PASS WITH NOTES | Feature flag creation should be step 1, not last; `findMessageWithAlertContext` needs index before mutations ship |
| Risk | PASS WITH NOTES | JSONB read-modify-write race condition in mutations; hydration query needs perf analysis |
| Scope Discipline | PASS | Well-scoped; Phase 3 could be a follow-up bead |
| Testability | FAIL | Zero test items in implementation checklist; no acceptance criteria; no production verification plan |

## Must Fix Before Creating Beads

### 1. Add test plan to implementation checklist
- **Found by**: Testability, Completeness
- **Problem**: The 10-item implementation checklist has zero test items. A cross-cutting change to the thread data model, agent input pipeline, UI rendering, and mutation flow cannot ship without tests.
- **Required fix**: Add test items to the checklist covering:
  - Unit tests for `hydrateAlertContext` (0/1/many alerts, deleted alert)
  - Unit tests for `toUiMessagePart` alert_context conversion
  - Unit tests for `updateAlertContextInThread` (happy path, missing message)
  - Integration test for trigger_alert → thread contains alert_context
  - Integration test for resolve mutation → alert_context updated
  - Component test for AlertCard rendering from AlertContextPart

### 2. Clarify `slack_context` precedent
- **Found by**: Completeness
- **Problem**: Plan states `alert_context` is "parallel to Henry's `slack_context` (PR #3273)" but `slack_context` does not exist anywhere in the codebase. If it's an unmerged PR, the plan should state whether `alert_context` depends on it or establishes the pattern independently.
- **Required fix**: Either (a) remove the `slack_context` reference and state this plan establishes the content-part-as-structured-context pattern, or (b) add an explicit dependency on PR #3273 landing first and specify what to reuse from it.

## Should Fix
- **Feature flag sequencing**: Move "Create feature flag in PostHog" from last checklist item to first. All gated code depends on the flag existing.
- **Concurrency strategy for JSONB mutations**: `updateAlertContextInThread` does read-modify-write on JSONB content without specifying locking. Add a note about using a transaction with `SELECT ... FOR UPDATE` or equivalent.
- **`findMessageWithAlertContext` query strategy**: This function is called but undefined. Specify the lookup approach (JSONB containment query, dedicated join table, or index strategy) to avoid an expensive full scan.
- **Rollback procedure**: Explicitly state what happens when the feature flag is turned off for an org that already has `alert_context` parts in threads. Confirm graceful degradation.
- **Observability**: Add metrics for hydration latency, hydration error rate, and mutation latency to catch performance issues during rollout.
- **Phase 3 as follow-up**: Move Phase 3 (deprecate artifact) out of this plan and into a separate follow-up bead.

## Observations
- The architectural decision to use a content part (not artifact) is correct — artifacts are excluded from agent input by design.
- The hydrate-on-read vs write-on-change decision is well-reasoned and appropriate for v1.
- The dual-emit migration strategy (Option A) is pragmatic and low-risk.
- Open questions 1-4 are all correctly deferred or adequately addressed.
- The code samples are detailed enough to implement from, which is a strength of this plan.
- The plan correctly builds on existing patterns (content part union, feature flags, Inngest events).

## Next Steps
- [ ] Address must-fix items (test plan, slack_context clarification)
- [ ] Address should-fix items (flag sequencing, concurrency, query strategy)
- [ ] Re-review if significant changes are made
- [ ] Pour beads per updated checklist if GO after fixes
