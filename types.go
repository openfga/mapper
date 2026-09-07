package mapper

import (
	"fmt"
	"time"

	"github.com/openfga/mapper/language"
)

// Tuple is an OpenFGA relationship tuple produced by evaluation. It is an alias
// for language.Tuple so SDK callers can name it without a second import; the two
// are the same type.
type Tuple = language.Tuple

// TupleFilter is a rendered tuple filter produced by evaluation. It is an alias
// for language.TupleFilter so SDK callers can name it without a second import; the
// two are the same type.
type TupleFilter = language.TupleFilter

// Position is a source location within a mapping file. It is an alias for
// language.Position so SDK callers handling the mapper error path (EvalError,
// Diagnostic) can name it without importing the language package; the two are the
// same type.
type Position = language.Position

// ValidationError is a structural validation error carrying a source Position. It
// is an alias for language.ValidationError so SDK callers can branch on the mapper
// error surface (alongside EvalError and ConflictError) without a second import;
// the two are the same type.
type ValidationError = language.ValidationError

// TupleFilterOperation groups a rule's rendered filters with its desired-state tuples.
// One per rule that has tuple_filters. The consumer uses these to drive read-diff-write
// against FGA.
type TupleFilterOperation struct {
	Filters []TupleFilter `json:"filters"`
	Tuples  []Tuple       `json:"tuples"`
}

// EvalError represents an Expr expression evaluation error.
type EvalError struct {
	Expression string
	RuleName   string            // populated during evaluation before wrapping; empty if unknown
	Field      string            // tuple field name ("user", "relation", "object"); set for compile-time interpolation errors
	Position   language.Position // source location; set for compile-time interpolation errors; zero = unknown
	Err        error
}

func (e *EvalError) Error() string {
	if e.Position.StartLine > 0 {
		return fmt.Sprintf("evaluation error at %d:%d in expression %q: %v",
			e.Position.StartLine, e.Position.StartColumn, e.Expression, e.Err)
	}
	return fmt.Sprintf("evaluation error in expression %q: %v", e.Expression, e.Err)
}

func (e *EvalError) Unwrap() error {
	return e.Err
}

// Conflict describes an unsatisfiable set of desired states on a single
// (User, Relation, Object) key.
type Conflict struct {
	User     string
	Relation string
	Object   string
}

// ConflictKind classifies why a set of desired states on one URO cannot be
// satisfied in a single OpenFGA Write batch.
type ConflictKind int

const (
	// ConflictWriteDelete is a write and a delete targeting the same URO.
	ConflictWriteDelete ConflictKind = iota
	// ConflictCompetingWrites is two writes on the same URO whose condition or
	// context differ, i.e. two incompatible desired states for one relationship.
	ConflictCompetingWrites
)

// ConflictError represents a runtime conflict on a single relationship (URO).
// OpenFGA identifies a relationship by (user, relation, object) only, so any
// URO carrying more than one incompatible desired state produces an invalid
// Write batch and is rejected here instead.
type ConflictError struct {
	Conflict
	Condition string // write/delete conflicts: the write tuple's condition, if any
	Kind      ConflictKind
}

func (e *ConflictError) Error() string {
	if e.Kind == ConflictCompetingWrites {
		return fmt.Sprintf("conflict: tuple (%s, %s, %s) has competing write actions with differing condition or context",
			e.User, e.Relation, e.Object)
	}
	if e.Condition != "" {
		return fmt.Sprintf("conflict: tuple (%s, %s, %s) with condition %q has both a write and a delete action",
			e.User, e.Relation, e.Object, e.Condition)
	}
	return fmt.Sprintf("conflict: tuple (%s, %s, %s) has both a write and a delete action",
		e.User, e.Relation, e.Object)
}

// Result is the output of Mapping.Evaluate.
type Result struct {
	Tuples                []Tuple                `json:"tuples"`
	TupleFilterOperations []TupleFilterOperation `json:"tuple_filter_operations,omitempty"`
	Trace                 *Trace                 `json:"trace,omitempty"` // nil unless tracing is enabled on the Compiler
}

// PostProcessResult holds metadata from tuple post-processing (dedup + conflict detection).
// Populated on the Trace only when tracing is enabled.
type PostProcessResult struct {
	RemovedTuples []Tuple    // the duplicate tuples that were removed
	Conflicts     []Conflict // all write/delete conflicts detected (empty if none)
}

// uroKey returns a key identifying a relationship by (user, relation, object)
// only — OpenFGA's notion of relationship identity. Condition and context are
// payload, not identity.
func uroKey(t language.Tuple) string {
	k := language.Tuple{User: t.User, Relation: t.Relation, Object: t.Object}
	return k.Key()
}

// postProcess deduplicates result tuples in place and detects conflicts in a single pass.
// A relationship is identified by its (user, relation, object) triple, matching how
// OpenFGA stores tuples and validates a Write batch. Two desired states on the same URO
// that cannot both be satisfied in one batch are conflicts:
//   - a write and a delete on the same URO (ConflictWriteDelete);
//   - two writes on the same URO whose condition or context differ (ConflictCompetingWrites).
//
// Dedup: exact-identity duplicates (including condition and context) collapse to the first,
// as do repeated deletes on the same URO; order is preserved and the backing array reused.
// When tracing is enabled, all metadata is stored on Trace.PostProcess and all conflicts are
// collected. When tracing is disabled, returns immediately on the first conflict (fast path).
// Returns the first conflict as a *ConflictError, or nil.
func (r *Result) postProcess() error {
	tracing := r.Trace != nil

	type uroState struct {
		hasWrite   bool
		writeTuple language.Tuple
		hasDelete  bool
	}

	seenFull := make(map[string]struct{}, len(r.Tuples))
	states := make(map[string]*uroState, len(r.Tuples))

	var removed []language.Tuple
	var conflicts []Conflict
	var firstConflictErr *ConflictError

	recordConflict := func(ce *ConflictError) {
		if firstConflictErr == nil {
			firstConflictErr = ce
		}
		conflicts = append(conflicts, ce.Conflict)
	}

	w := 0
	for _, t := range r.Tuples {
		if t.Action != language.ActionWrite && t.Action != language.ActionDelete {
			r.Tuples = r.Tuples[:w]
			return &language.ValidationError{
				Field:   fmt.Sprintf("(%s, %s, %s)", t.User, t.Relation, t.Object),
				Message: fmt.Sprintf("unknown tuple action %q", t.Action),
			}
		}

		fullKey := t.Key()
		if _, dup := seenFull[fullKey]; dup {
			if tracing {
				removed = append(removed, t)
			}
			continue
		}

		uk := uroKey(t)
		st := states[uk]
		if st == nil {
			st = &uroState{}
			states[uk] = st
		}

		conflict := Conflict{User: t.User, Relation: t.Relation, Object: t.Object}
		var ce *ConflictError
		switch t.Action {
		case language.ActionWrite:
			switch {
			case st.hasDelete:
				ce = &ConflictError{Conflict: conflict, Condition: t.Condition, Kind: ConflictWriteDelete}
			case st.hasWrite:
				// Distinct from the stored write (an identical one is caught by seenFull),
				// so this is a second, incompatible desired state for the same relationship.
				ce = &ConflictError{Conflict: conflict, Kind: ConflictCompetingWrites}
			}
		case language.ActionDelete:
			switch {
			case st.hasWrite:
				ce = &ConflictError{Conflict: conflict, Condition: st.writeTuple.Condition, Kind: ConflictWriteDelete}
			case st.hasDelete:
				// A delete targets a relationship by URO regardless of condition, so a
				// second delete on the same URO is a duplicate.
				if tracing {
					removed = append(removed, t)
				}
				continue
			}
		}

		if ce != nil {
			if !tracing {
				r.Tuples = r.Tuples[:w]
				return ce
			}
			recordConflict(ce)
		}

		switch t.Action {
		case language.ActionWrite:
			if !st.hasWrite {
				st.hasWrite = true
				st.writeTuple = t
			}
		case language.ActionDelete:
			st.hasDelete = true
		}

		seenFull[fullKey] = struct{}{}
		r.Tuples[w] = t
		w++
	}
	r.Tuples = r.Tuples[:w]

	// Dedup each TupleFilterOperation's desired-state tuples independently.
	// Desired state is a set per rule; duplicates from iterators are wasteful.
	for i := range r.TupleFilterOperations {
		r.TupleFilterOperations[i].Tuples = dedupTuples(r.TupleFilterOperations[i].Tuples)
	}

	if tracing && (len(removed) > 0 || len(conflicts) > 0) {
		r.Trace.PostProcess = &PostProcessResult{
			RemovedTuples: removed,
			Conflicts:     conflicts,
		}
	}

	if firstConflictErr != nil {
		return firstConflictErr
	}
	return nil
}

// dedupTuples removes duplicate tuples by their full identity (including condition and context),
// preserving insertion order. First occurrence wins.
func dedupTuples(tuples []language.Tuple) []language.Tuple {
	if len(tuples) <= 1 {
		return tuples
	}
	seen := make(map[string]struct{}, len(tuples))
	w := 0
	for _, t := range tuples {
		k := t.Key()
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		tuples[w] = t
		w++
	}
	return tuples[:w]
}

// Trace holds execution trace data for debugging rule evaluation.
// Populated only when WithTrace(true) is set on the Compiler.
type Trace struct {
	Rules       []RuleTrace
	Duration    time.Duration
	PostProcess *PostProcessResult // nil unless dedup or conflicts occurred
}

// RuleStatus represents the outcome of evaluating a single rule.
type RuleStatus string

const (
	// RuleMatched indicates the rule's when guard evaluated to true and the rule produced tuples.
	RuleMatched RuleStatus = "matched"
	// RuleSkipped indicates the rule's when guard evaluated to false and the rule was skipped.
	RuleSkipped RuleStatus = "skipped"
	// RuleErrored indicates an error occurred while evaluating the rule.
	RuleErrored RuleStatus = "error"
)

// RuleTrace records evaluation details for a single rule.
type RuleTrace struct {
	Name     string
	Status   RuleStatus
	Error    error // populated only when Status == RuleErrored
	EmittedN int
	FilterN  int // number of rendered tuple filters (0 if rule has no tuple_filters)
}
