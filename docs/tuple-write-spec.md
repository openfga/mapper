# FGA Tuple Write Specification

Version: 1

This document specifies how a consumer of the mapping engine result turns it into FGA API calls — covering both non-filtered tuple writes and tuple filter operations (patch/delete). For the mapping language itself (syntax, validation, engine output), see [`language-spec.md`](language-spec.md).

## Overview

The mapping engine is stateless — it evaluates a mapping file against a JSON event and produces a result containing:

- **Tuples** — non-filtered tuples with explicit `write` or `delete` actions (from rules without `tuple_filters`)
- **Tuple filter operations** — one per rule that has `tuple_filters`, each grouping the rendered filters and the rule's desired-state tuples

The consumer is responsible for turning this result into FGA API calls. Both non-filtered tuples and tuple filter operations are combined and written together to reduce partial-failure risk and narrow the TOCTOU window. OpenFGA accepts a maximum of 40 write operations per API call; when the combined set exceeds this, the OpenFGA SDK splits it into multiple sequential calls. Each call is independently non-transactional, so this is best-effort, not atomic.

## Non-Filtered Tuples

Rules without `tuple_filters` produce tuples with explicit `write` or `delete` actions. The engine has already deduplicated them and checked for write/delete conflicts. The consumer includes them in the final combined write alongside any deltas from tuple filter operations.

## Tuple Filter Operations

Each tuple filter operation contains:
- **Filters** — rendered partial-key queries (from the rule's `tuple_filters` field), each with an `action` (`patch` or `delete`)
- **Tuples** — the desired-state tuples produced by that rule (used by `patch` filters; empty for `delete`-only rules)

Tuple filter operations require a live FGA connection because the consumer must read the current state to compute a diff. If no live connection is available (e.g., file-output mode), the consumer returns an error.

## Execution Order

For each event, the consumer processes the engine result in five phases:

1. **Validate** — For any tuple filter operation containing `patch` filters, verify that the engine produced non-empty desired-state tuples. If empty, fail fast before reading from FGA — an empty desired state on `patch` would delete all matching tuples, which is almost always a bug. (For `delete` filters, empty desired state is expected.)
2. **Read** — For each tuple filter operation, issue an FGA Read API call for each filter. Each call is paginated independently using continuation tokens until exhausted or the context deadline is reached. If pagination is incomplete due to timeout or error, the entire event fails (see [Error Handling](#error-handling)). Filters with identical rendered values across operations should be deduplicated so only one FGA Read call is made per unique filter.
3. **Diff** — For each tuple filter operation, compute the delta based on the filter action. Comparison includes condition and context — a tuple with the same (user, relation, object) but a different condition or context is treated as a different tuple, producing a delete of the old version and a write of the new one.
   - `patch` filters: `toDelete = existing - desired` (tuples in FGA but not in the desired state), `toWrite = desired - existing` (tuples in the desired state but not in FGA). Desired-state tuples are those produced by the rule.
   - `delete` filters: `toDelete = existing` (delete everything read), no `toWrite`. Desired-state tuples are not involved — `delete` filters operate independently even in rules that also have `patch` filters.
4. **Combine** — Merge all deltas (`toDelete` as deletes, `toWrite` as writes) with all non-filtered tuples into a single set.
5. **Write** — Execute the combined set via the FGA Write API. OpenFGA accepts a maximum of 40 write operations (writes + deletes) per API call. When the combined set exceeds this limit, the OpenFGA SDK automatically splits it into multiple sequential API calls. Each call is independently non-transactional — a failure in a later call leaves earlier calls committed.

When no tuple filter operations are present, phases 1-3 are skipped and the non-filtered tuples are written directly.

This combined approach reduces Write API calls, minimises the time-of-check/time-of-use (TOCTOU) window, and reduces the risk of partial-failure states by submitting all operations together (though FGA Write is not transactional — the SDK may split large sets across multiple calls).

### Diff Example

Given a tuple filter operation with a `patch` filter matching `org:123` members:

- **FGA currently has:** `{user:alice, member, org:123}`, `{user:bob, member, org:123}`, `{user:charlie, member, org:123}`
- **Desired state:** `{user:alice, member, org:123}`, `{user:bob, member, org:123}`, `{user:dave, member, org:123}`

The diff produces:
- **Delete:** `{user:charlie, member, org:123}` (in FGA, not in desired state)
- **Write:** `{user:dave, member, org:123}` (in desired state, not in FGA)
- **No-op:** `{user:alice, ...}` and `{user:bob, ...}` (already correct)

No-op tuples (already correct in FGA) are excluded from the write. The remaining deltas are combined with any non-filtered tuples and written together. In this example, the write contains one delete and one write operation.

The filter-level `action` field controls how empty desired state is handled:
- **`action: delete`** — deletes everything matching the filter from FGA. No desired-state tuples are involved; the diff simply produces all read tuples as deletes.
- **`action: patch`** — requires a non-empty desired state. If the rule produces zero tuples (e.g., an iterator produces zero items), the consumer returns an error before reading from FGA. This prevents accidental full revocation — use `action: delete` when intentional revocation is needed.

## Condition and Context Handling

Tuples may carry an FGA condition name and associated context (see [`language-spec.md`](language-spec.md) for the YAML syntax). The consumer handles conditions as follows:

- **Writes:** When a tuple has a `condition`, the consumer populates the OpenFGA SDK's `RelationshipCondition` field (name and context) on the write request. Tuples without a condition are written without it.
- **Reads:** The consumer preserves condition and context from FGA Read API responses. Empty context maps from FGA are normalised to nil to ensure consistent diff behaviour.
- **Diff:** Condition and context are included in the diff comparison. A tuple with the same (user, relation, object) but a different condition or context is treated as changed, producing a delete of the old version and a write of the new one. This is necessary because FGA returns a 409 Conflict if you write a tuple with a different condition than what is stored — you must delete first, then write.
- **Deletes:** The FGA Delete API does not accept a condition field (`TupleKeyWithoutCondition`), so condition and context are stripped from delete operations. This means a delete targets the tuple by its (user, relation, object) identity regardless of what condition it carries.

## Error Handling

- **Read failure is a hard error.** Proceeding with an incomplete read produces a wrong diff — missing tuples would be incorrectly treated as absent, potentially causing valid tuples to be deleted. The consumer aborts the entire event on any read failure.
- **Write failures** may be fatal or non-fatal depending on configuration. When configured to continue on write errors, the consumer logs the error and continues processing subsequent events. When not (the default), a write failure is fatal.

## Limitations

- **TOCTOU gap:** A time-of-check/time-of-use window exists between the Read and Write phases. Other processes writing to FGA during this window may cause the diff to be stale. Combining all operations into the write phase minimises the window but does not eliminate it.
- **Write batching:** OpenFGA limits each Write API call to 40 operations. The OpenFGA SDK handles splitting larger sets into multiple sequential calls, but a failure in a later batch leaves earlier batches committed — there is no cross-batch rollback.
- **Filter narrowness:** A filter that is too narrow will leave stale tuples in FGA. The language spec's "all three fields set" validation catches the worst case (single-tuple filter); the `assert_writes_covered_by_filter` test assertion helps catch intermediate narrowness at test time.
- **API call overhead:** Each event with tuple filters triggers one FGA Read call per unique filter (paginated). High-volume streams produce proportionally more API calls; monitor in production.
