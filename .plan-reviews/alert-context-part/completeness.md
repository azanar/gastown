# Plan Completeness

## Verdict
PASS WITH NOTES — Plan covers the core requirement thoroughly but has gaps in error handling, observability, and rollback planning.

## Must Fix (blocks implementation)

### 1. `slack_context` precedent does not exist in codebase
- **Issue**: Plan references "parallel to Henry's `slack_context` (PR #3273)" as architectural precedent, but `slack_context` does not exist anywhere in the current codebase. The only mention is in this plan doc itself.
- **Why it matters**: If `alert_context` is supposed to follow an established pattern, that pattern doesn't exist yet. The implementation has no reference to follow, and the two content parts may need to be designed together to avoid divergent approaches. If PR #3273 lands first and differs from what this plan assumes, rework is needed.
- **Suggested resolution**: Clarify whether `slack_context` is merged, in-flight, or planned. If in-flight, coordinate sequencing — either `alert_context` waits for `slack_context` to land (establishing the pattern), or this plan explicitly states it's establishing the pattern that `slack_context` will follow. Remove the "parallel to" framing if there's no existing precedent to follow.

## Should Fix (important but not blocking)

### 1. No rollback plan
- The plan describes a 3-phase migration but doesn't address rollback. If Phase 1 reveals bugs (e.g., hydration causes thread load latency spikes), what's the procedure? Feature flag off is implied but not explicit. What about threads that already have `alert_context` parts written — do they gracefully degrade when the flag is turned off?

### 2. No observability/monitoring plan
- Hydration adds a database query on every thread load. No mention of metrics (hydration latency, cache hit rates, error rates) or alerting on hydration failures. The `fetchAlertsByIds` batch query could be slow for threads with many alerts.

### 3. Missing error handling for `findMessageWithAlertContext`
- The `updateAlertContextInThread` function (line 225) calls `findMessageWithAlertContext(alertId)` but this function isn't defined anywhere in the plan. What's the query strategy? Full-text JSONB search across all messages in all threads? That could be expensive. Needs an index strategy or at minimum a description of the lookup approach.

### 4. No test plan in checklist
- The implementation checklist (line 332-341) has no test items. Unit tests for hydration logic, integration tests for mutation → thread update flow, and E2E tests for the card rendering from content part are all missing from the checklist.

## Observations
- The plan correctly identifies that `alert_context` should NOT be an artifact (artifacts are excluded from agent input). This is a well-reasoned architectural decision.
- Thread compaction handling (Open Question 4) is acknowledged as a tradeoff but adequately addressed — re-invocation matches existing patterns.
- The dual-emit migration strategy (Option A) is pragmatic and low-risk.
