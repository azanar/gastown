# Sequencing and Dependencies

## Verdict
PASS WITH NOTES — Implementation checklist is roughly ordered correctly but lacks explicit dependency edges and misses a critical ordering constraint.

## Must Fix (blocks implementation)

(None)

## Should Fix (important but not blocking)

### 1. Feature flag must exist before any gated code ships
- The checklist lists "Create `alert-context-part` feature flag in PostHog" as the last item (line 341). But steps 4-9 all depend on the flag existing. The flag should be created first — or at minimum, the code should handle the flag not existing (defaulting to off). Reorder to make flag creation step 1.

### 2. Schema registration must precede all consumers
- Steps 1-2 (content-parts.ts, inngest events) must land before steps 3-8. This is implied by the checklist ordering but not explicitly stated. If these are separate PRs/beads, the dependency must be declared.

### 3. `findMessageWithAlertContext` query needs an index
- The mutation flow (step 8) calls `findMessageWithAlertContext(alertId)` which searches message content JSONB for a specific alertId. If this ships before an appropriate index exists, mutation latency could spike. The index creation should be sequenced before or alongside the mutation changes.

## Observations
- Steps 4-5 (thread history hydration + model conversion) can be implemented together in a single PR since they're in the same file.
- Steps 6-7 (tool emission + converter) have a tight dependency and should be implemented together.
- The three migration phases (dual-emit → default on → deprecate artifact) are well-sequenced with clear transition criteria.
- Parallelization opportunity: schema registration (steps 1-3) and UI component changes (step 9) could proceed in parallel once the type is defined.
