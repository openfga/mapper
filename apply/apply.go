package apply

import (
	"context"
	"fmt"

	"github.com/openfga/mapper"
	"github.com/openfga/mapper/language"
)

// TupleClient reads and writes relationship tuples in an OpenFGA store.
type TupleClient interface {
	ReadTuples(ctx context.Context, filter language.TupleFilter) ([]language.Tuple, error)
	WriteTuples(ctx context.Context, tuples []language.Tuple) error
}

// WriteError wraps an error that occurred during the write phase (phase 5) of the pipeline.
// Consumers can use errors.As to distinguish write failures (which may be continuable)
// from read/validation failures (which are always hard errors per the spec).
type WriteError struct {
	Err error
}

func (e *WriteError) Error() string { return e.Err.Error() }
func (e *WriteError) Unwrap() error { return e.Err }

// Executor runs the reconciliation pipeline against a mapper.Result.
type Executor interface {
	Execute(ctx context.Context, result *mapper.Result) error
}

var _ Executor = (*Reconciler)(nil)

// Reconciler orchestrates the 5-phase tuple write pipeline: validate, read, diff, combine, write.
type Reconciler struct {
	client TupleClient
}

// New creates a Reconciler backed by the given TupleClient.
func New(client TupleClient) *Reconciler {
	return &Reconciler{client: client}
}

// Execute runs the 5-phase pipeline on a mapper.Result, turning engine output into FGA API calls.
// Errors from the write phase are wrapped in *WriteError; all other errors (validation, read)
// are hard errors that should always abort the event.
func (r *Reconciler) Execute(ctx context.Context, result *mapper.Result) error {
	var toWrite, toDelete []language.Tuple

	if len(result.TupleFilterOperations) > 0 {
		// Phase 1: Validate
		if err := r.validate(result.TupleFilterOperations); err != nil {
			return err
		}

		// Phase 2: Read
		// Sequential reads per filter; a future optimisation could issue concurrent reads
		// with bounded parallelism.
		cache, err := r.readAll(ctx, result.TupleFilterOperations)
		if err != nil {
			return err
		}

		// Phase 3: Diff
		for _, op := range result.TupleFilterOperations {
			for _, f := range op.Filters {
				key := mapper.Conflict{User: f.User, Relation: f.Relation, Object: f.Object}
				existing := cache[key]
				scoped := scopeDesired(f, op.Tuples)
				fw, fd := diffFilter(f.Action, existing, scoped)
				toWrite = append(toWrite, fw...)
				toDelete = append(toDelete, fd...)
			}
		}
	}

	// Phase 4: Combine, deduplicate, and conflict-check (always — catches conflicts in
	// direct-tuple-only results too).
	combined, err := combineTuples(result.Tuples, toWrite, toDelete)
	if err != nil {
		return err
	}

	// Phase 5: Write
	return r.writeTuples(ctx, combined)
}

// validate checks patch operations for structural errors before any I/O.
//
// Two checks are applied:
//  1. Patch filters with empty desired-state tuples would delete everything in scope — fail fast.
//  2. Competing write conditions on the same URO in a single operation's desired state are
//     ambiguous. diffFilter strips one as a no-op when the store already holds it, so the
//     conflict would not be caught by combineTuples. Detect it here before diffing.
//
// Note: emptiness is checked per operation, not per filter. A patch filter whose
// scope (user/relation/object) matches none of op.Tuples will receive an empty
// desired set from scopeDesired, which diffFilter treats as "delete everything in scope".
// This is correct patch semantics but can delete all matching tuples if desired tuples
// are missing due to a mapping error.
func (r *Reconciler) validate(ops []mapper.TupleFilterOperation) error {
	for _, op := range ops {
		hasPatch := false
		for _, f := range op.Filters {
			if f.Action == language.FilterActionPatch {
				hasPatch = true
				break
			}
		}
		if hasPatch && len(op.Tuples) == 0 {
			return fmt.Errorf("tuple filter operation has patch filters but empty desired-state tuples")
		}

		// Check for competing write conditions on the same URO in desired state.
		writesByURO := make(map[string]string) // uroKey → diffKey of first write seen
		for _, t := range op.Tuples {
			if t.Action == language.ActionDelete {
				continue
			}
			uk := uroKey(t)
			dk := diffKey(t)
			if prev, seen := writesByURO[uk]; seen && prev != dk {
				return &mapper.ConflictError{
					Conflict: mapper.Conflict{User: t.User, Relation: t.Relation, Object: t.Object},
					Kind:     mapper.ConflictCompetingWrites,
				}
			}
			writesByURO[uk] = dk
		}

		// Check that every desired tuple is covered by at least one filter's scope.
		// scopeDesired uses exact-match on non-empty filter fields. A desired tuple
		// that matches no filter scope would be silently dropped — fail fast instead.
		for _, t := range op.Tuples {
			covered := false
			for _, f := range op.Filters {
				if len(scopeDesired(f, []language.Tuple{t})) > 0 {
					covered = true
					break
				}
			}
			if !covered {
				return fmt.Errorf("desired tuple %s %s %s is not covered by any filter scope in this operation",
					t.User, t.Relation, t.Object)
			}
		}
	}
	return nil
}

// readAll issues deduplicated FGA Read calls for all filters across all operations.
func (r *Reconciler) readAll(ctx context.Context, ops []mapper.TupleFilterOperation) (map[mapper.Conflict][]language.Tuple, error) {
	cache := make(map[mapper.Conflict][]language.Tuple)

	for _, op := range ops {
		for _, f := range op.Filters {
			key := mapper.Conflict{User: f.User, Relation: f.Relation, Object: f.Object}
			if _, done := cache[key]; done {
				continue
			}

			tuples, err := r.client.ReadTuples(ctx, f)
			if err != nil {
				return nil, fmt.Errorf("failed to read tuples for filter: %w", err)
			}
			cache[key] = tuples
		}
	}

	return cache, nil
}

// scopeDesired returns only the desired tuples that match a filter's concrete fields.
// Empty filter fields are wildcards and match any tuple value.
func scopeDesired(filter language.TupleFilter, desired []language.Tuple) []language.Tuple {
	var scoped []language.Tuple
	for _, t := range desired {
		if filter.User != "" && t.User != filter.User {
			continue
		}
		if filter.Relation != "" && t.Relation != filter.Relation {
			continue
		}
		if filter.Object != "" && t.Object != filter.Object {
			continue
		}
		scoped = append(scoped, t)
	}
	return scoped
}

// combineTuples merges non-filtered tuples with filter deltas, deduplicating by full tuple identity.
// Returns a ConflictError if the same tuple (including condition and context) appears with both a
// write and delete action. Tuples that share (user, relation, object) but differ in condition or
// context are distinct and do not conflict. Delete tuples are further deduplicated by URO after
// conflict detection, because the FGA delete API ignores condition and context — two deletes of the
// same URO with different conditions map to identical API keys and cause the server to reject the batch.
func combineTuples(nonFiltered, toWrite, toDelete []language.Tuple) ([]language.Tuple, error) {
	seen := make(map[string]struct{}, len(nonFiltered)+len(toWrite)+len(toDelete))
	combined := make([]language.Tuple, 0, len(nonFiltered)+len(toWrite)+len(toDelete))

	add := func(tuples []language.Tuple) {
		for _, t := range tuples {
			k := t.Key()
			if _, exists := seen[k]; exists {
				continue
			}
			seen[k] = struct{}{}
			combined = append(combined, t)
		}
	}

	add(nonFiltered)
	add(toWrite)
	add(toDelete)

	// Detect conflicts. Two distinct cases:
	//
	// 1. Write+delete on the same full tuple identity (URO+condition): keyed by diffKey.
	//    A condition *change* on the same URO produces delete(old)+write(new) which is
	//    intentional; only an exact match is a conflict.
	//
	// 2. Two competing writes on the same URO (different conditions): indeterminate store
	//    state — one would silently overwrite the other, so we surface this as a conflict.
	writesByKey := make(map[string]language.Tuple)
	writesByURO := make(map[string]language.Tuple)
	deletes := make(map[string]struct{})
	for _, t := range combined {
		key := diffKey(t)
		if t.Action == language.ActionDelete {
			deletes[key] = struct{}{}
		} else {
			writesByKey[key] = t
			uk := uroKey(t)
			if prev, conflict := writesByURO[uk]; conflict && diffKey(prev) != key {
				return nil, &mapper.ConflictError{
					Conflict: mapper.Conflict{User: t.User, Relation: t.Relation, Object: t.Object},
					Kind:     mapper.ConflictCompetingWrites,
				}
			}
			writesByURO[uk] = t
		}
	}
	// Iterate combined (slice order) rather than writesByKey (map) so the first
	// conflict reported is deterministic.
	for _, t := range combined {
		if t.Action == language.ActionDelete {
			continue
		}
		if _, ok := deletes[diffKey(t)]; ok {
			return nil, &mapper.ConflictError{
				Conflict:  mapper.Conflict{User: t.User, Relation: t.Relation, Object: t.Object},
				Condition: t.Condition,
			}
		}
	}

	// Dedup deletes by URO. The FGA delete API ignores condition and context — two deletes
	// of the same (user, relation, object) with different conditions map to identical delete
	// keys and cause the server to reject the batch as a duplicate. Keep the first occurrence.
	seenDeleteURO := make(map[string]struct{})
	out := combined[:0]
	for _, t := range combined {
		if t.Action != language.ActionDelete {
			out = append(out, t)
			continue
		}
		uk := uroKey(t)
		if _, seen := seenDeleteURO[uk]; seen {
			continue
		}
		seenDeleteURO[uk] = struct{}{}
		out = append(out, t)
	}

	return out, nil
}

// writeTuples writes the given tuples to the store, skipping if empty.
// Write-phase errors are wrapped in *WriteError.
func (r *Reconciler) writeTuples(ctx context.Context, tuples []language.Tuple) error {
	if len(tuples) == 0 {
		return nil
	}
	if err := r.client.WriteTuples(ctx, tuples); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}
