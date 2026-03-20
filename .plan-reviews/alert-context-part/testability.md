# Testability and Verifiability

## Verdict
FAIL — No test plan exists. The implementation checklist contains zero test items, and the plan doesn't define acceptance criteria for any step.

## Must Fix (blocks implementation)

### 1. No test plan whatsoever
- **Issue**: The 10-item implementation checklist (lines 332-341) contains zero test items. There is no mention of unit tests, integration tests, or E2E tests anywhere in the plan.
- **Why it matters**: This is a cross-cutting change touching the thread model, agent input, UI rendering, and mutation flow. Without tests, regressions will be caught only by the feature flag (turn it off and hope). Key behaviors that MUST be tested:
  - Hydration correctly patches `alert_context` parts with live state
  - Hydration handles missing/deleted alerts gracefully (returns stale snapshot)
  - `toUiMessagePart` converts `alert_context` to correct text format
  - `updateAlertContextInThread` correctly patches JSONB content
  - Alert card renders from `AlertContextPart` props
  - Feature flag correctly gates all new behavior (flag off = old behavior)
  - Dual-emit produces both artifact and content part
- **Suggested resolution**: Add test items to the checklist:
  - Unit test: `hydrateAlertContext` with mock data (0 alerts, 1 alert, multiple alerts, deleted alert)
  - Unit test: `toUiMessagePart` for `alert_context` type
  - Unit test: `updateAlertContextInThread` (happy path, missing message, concurrent update)
  - Integration test: `trigger_alert` → thread contains `alert_context` part
  - Integration test: resolve mutation → `alert_context` updated in thread
  - Component test: AlertCard renders from `AlertContextPart` props

## Should Fix (important but not blocking)

### 1. No acceptance criteria per checklist item
- Each implementation item should have a brief "done when" statement. For example: "Thread history loader hydration — done when threads with `alert_context` parts show current alert status to the agent."

### 2. No production verification plan
- After Phase 1 rollout to early adopters, how do we verify it's working? Suggested: check that agent responses reference correct alert status, check that alert cards render without polling, check hydration latency metrics.

## Observations
- The feature flag provides a built-in escape hatch, which partially compensates for the lack of a test plan — problems can be disabled quickly.
- The plan's code samples are detailed enough to derive test cases from, which is good. But the tests themselves need to be in the plan.
- The dual-emit migration (Option A) is inherently testable — you can compare old and new card behavior side-by-side during the migration period.
