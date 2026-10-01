# Apply Package

> **Pipeline specification:** See [`docs/tuple-write-spec.md`](../docs/tuple-write-spec.md). This README covers internal architecture and design decisions.

## Overview

The `apply` package implements the tuple-write pipeline from `docs/tuple-write-spec.md`. It takes a `mapper.Result` — the output of evaluating a mapping against a JSON event — and applies the write/delete delta to a store through a narrow `TupleClient` interface.

The package has no dependency on the OpenFGA Go SDK. `TupleClient` is the sole boundary; callers supply an implementation that adapts the SDK (or any other transport) to the two-method interface.

## Pipeline

`Reconciler.Execute` runs five phases on a `mapper.Result`:

1. **Validate** — checks that patch filter operations have non-empty desired-state tuples. This is a quick structural check before any I/O.
2. **Read** — issues `TupleClient.ReadTuples` calls for each filter in each filter operation, deduplicating by `(user, relation, object)` so the same URO is never read twice.
3. **Diff** — for each filter, computes the write/delete delta against the desired-state tuples (`patch`: desired−existing = write, existing−desired = delete; `delete`: all existing = delete). Comparison is by full tuple identity (URO + condition + context), so a condition change on the same URO produces `delete(old) + write(new)`, not a no-op.
4. **Combine** — merges non-filtered tuples (from `result.Tuples`) with the filter deltas, deduplicates by full tuple identity, and detects conflicts.
5. **Write** — passes the combined set to `TupleClient.WriteTuples`.

Phases 2–3 are skipped when `result.TupleFilterOperations` is empty, but phase 4 still runs to validate direct tuples.

## Conflict Detection

Two conflict kinds are detected in phase 4, both returning `*mapper.ConflictError`:

**Competing writes** (`ConflictCompetingWrites`): two write operations targeting the same URO with different conditions. The final store state would be indeterminate — one write silently overwrites the other within the batch.

**Write/delete on the same tuple identity** (`ConflictWriteDelete`): a write and a delete on the same (URO + condition + context). This is distinct from a condition change: `write(URO, condA) + delete(URO, condB)` is intentional (condition transition) and passes through; `write(URO, condA) + delete(URO, condA)` is a contradiction.

Conflict detection iterates `combined` in slice order, so the first conflict reported is deterministic (nonFiltered before filter-delta writes before deletes).

After conflict detection, delete tuples are deduplicated by URO. The FGA delete API ignores condition and context — a direct delete `{URO}` and a filter-derived delete `{URO, condX}` map to the same delete key, and a batch with duplicate keys is rejected by the server. The first occurrence per URO is kept.

## TupleClient Contract

`ReadTuples` receives a `language.TupleFilter` — any empty field is a wildcard and should be omitted from the underlying API request. The return value must reflect the current committed store state at the moment of the call; stale reads produce incorrect diffs.

`WriteTuples` receives the full combined set including both writes and deletes, distinguished by `tuple.Action`. Callers must handle sequencing: **deletes must reach the store before writes**. The OpenFGA server rejects a write to a URO that already holds a tuple with a different condition (`TupleConditionConflictError`), so a condition change requires the old tuple to be deleted first. An implementation using the OpenFGA Go SDK with `Transaction.Disable` must issue deletes and writes as separate requests in that order.

## Validate Checks (Phase 1)

`validate` runs three checks before any I/O:

**1. Patch with empty desired state.** A patch filter that scopes to `user:anne` but whose operation's `Tuples` contains no anne-scoped tuples will receive an empty desired set from `scopeDesired`. `diffFilter` treats an empty desired set as "delete everything the filter reads" — correct patch semantics, but a mapping template error that drops the anne-scoped tuples silently becomes a mass delete. `validate` returns an error if a patch operation's desired-state list is empty.

**2. Desired tuple outside all filter scopes.** `scopeDesired` uses exact-match on each filter's non-empty fields. A desired tuple that matches no filter's scope would be silently dropped — never diffed, never written. `validate` errors if any desired tuple is uncovered, pointing to a mismatch between the filter fields and the desired-state tuples (e.g. a type-scoped filter like `object: "org:"` with specific desired objects like `object: "org:acme"`).

**3. Competing desired conditions on the same URO.** Two desired write tuples with the same (user, relation, object) but different conditions are ambiguous: the final store state is indeterminate. `diffFilter` may strip one as a no-op if it already exists in the store, bypassing the post-diff conflict check in `combineTuples`. `validate` catches this before any diffing.

## Errors

`*WriteError` wraps errors from `TupleClient.WriteTuples` (phase 5). All other errors — validation, read failures, and conflict errors — are returned unwrapped. Callers can use `errors.As(*WriteError)` to distinguish write failures (which may be recoverable per-record) from hard errors (which should always abort the event).

## Usage

```go
import (
    "github.com/openfga/mapper"
    "github.com/openfga/mapper/apply"
)

// Implement apply.TupleClient against your FGA transport.
type myClient struct { /* ... */ }
func (c *myClient) ReadTuples(ctx context.Context, filter language.TupleFilter) ([]language.Tuple, error) { /* ... */ }
func (c *myClient) WriteTuples(ctx context.Context, tuples []language.Tuple) error { /* ... */ }

rec := apply.New(&myClient{})

// compiled is a *mapper.Mapping from mapper.Compile.
result, err := compiled.Evaluate(ctx, event)
if err != nil {
    // evaluation error
}

if err := rec.Execute(ctx, result); err != nil {
    var writeErr *apply.WriteError
    if errors.As(err, &writeErr) {
        // write phase failed — may be retryable
    }
    // hard error — abort
}
```
