# Risk Assessment

## Verdict
PASS WITH NOTES — No critical risks, but the JSONB mutation pattern and hydration query introduce performance risks that need mitigation.

## Must Fix (blocks implementation)

(None)

## Should Fix (important but not blocking)

### 1. JSONB content mutation is a concurrency risk
- `updateAlertContextInThread` reads a message's content array, patches one element, and writes the whole array back. If two mutations happen simultaneously (e.g., user resolves alert while agent is writing a new message to the same thread), the read-modify-write cycle creates a race condition. The second write could overwrite the first.
- **Mitigation**: Use a database-level JSON patch operation (if available in Drizzle/Postgres), or wrap the read-modify-write in a transaction with row-level locking (`SELECT ... FOR UPDATE`). The plan should specify the concurrency strategy.

### 2. `findMessageWithAlertContext` performance at scale
- Searching JSONB content across all messages to find which message contains an `alert_context` with a given `alertId` is potentially expensive. A thread with hundreds of messages would require scanning each message's content array.
- **Mitigation**: Add a `message_id` column to the alerts table (or a join table) that records which message contains the `alert_context` for each alert. This avoids the JSONB search entirely.

### 3. Hydration adds latency to every thread load
- `hydrateAlertContext` runs on every `loadThreadMessages` call, even for threads with no alerts. The "collect alertIds" scan is O(messages × parts), and the batch fetch adds a database round-trip.
- **Mitigation**: The early return when `alertIds.size === 0` helps, but the scan still happens. Consider adding a thread-level flag or metadata that indicates "this thread has alert_context parts" to skip the scan entirely for non-alert threads. Alternatively, accept the overhead if benchmarks show it's negligible.

### 4. Breaking change risk in content part union
- Adding a new type to `contentPartSchema` could cause Zod validation failures if any consumer does strict parsing (`.parse()` on content arrays from the database). Older code that doesn't know about `alert_context` would reject messages containing it.
- **Mitigation**: Verify all consumers use `.safeParse()` or that the union is additive (unknown types pass through). The plan notes "No data migration needed — old messages are immutable" but doesn't address old code parsing new messages.

## Observations
- The feature flag approach effectively mitigates rollout risk — problems can be disabled per-org.
- The plan correctly chose hydrate-on-read over write-on-change for alert state, avoiding message churn. This is the right tradeoff for v1.
- Open Question 2 (proactive Inngest subscriber for external alert changes) is correctly deferred. The hydration pattern is sufficient for v1.
- The three-phase migration is conservative and appropriate for a change that touches the thread data model.
