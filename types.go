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
	Filters []language.TupleFilter `json:"filters"`
	Tuples  []language.Tuple       `json:"tuples"`
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

// Conflict describes a write/delete conflict on a single (User, Relation, Object) key.
type Conflict struct {
	User     string
	Relation string
	Object   string
}

// ConflictError represents a runtime write/delete conflict returned as an error.
type ConflictError struct {
	Conflict
	Condition string // non-empty when the conflicting tuple has a condition
}

func (e *ConflictError) Error() string {
	if e.Condition != "" {
		return fmt.Sprintf("conflict: tuple (%s, %s, %s) with condition %q has both a write and a delete action",
			e.User, e.Relation, e.Object, e.Condition)
	}
	return fmt.Sprintf("conflict: tuple (%s, %s, %s) has both a write and a delete action",
		e.User, e.Relation, e.Object)
}

// Result is the output of Mapping.Evaluate.
type Result struct {
	Tuples                []language.Tuple       `json:"tuples"`
	TupleFilterOperations []TupleFilterOperation `json:"tuple_filter_operations,omitempty"`
	Trace                 *Trace                 `json:"trace,omitempty"` // nil unless tracing is enabled on the Compiler
}

// PostProcessResult holds metadata from tuple post-processing (dedup + conflict detection).
// Populated on the Trace only when tracing is enabled.
type PostProcessResult struct {
	RemovedTuples []language.Tuple // the duplicate tuples that were removed
	Conflicts     []Conflict       // all write/delete conflicts detected (empty if none)
}

// tupleConflictKey returns a string key for conflict detection and dedup: Key() with action stripped.
// Two tuples with the same user/relation/object/condition/context but different actions share a key,
// allowing conflict detection across write and delete.
func tupleConflictKey(t language.Tuple) string {
	t.Action = ""
	return t.Key()
}

// postProcess deduplicates result tuples in place and detects write/delete conflicts in a single pass.
// Uses a map keyed by full tuple identity (user, relation, object, condition, context) to track both
// dedup and conflict state. Tuples with different conditions on the same (user, relation, object)
// are treated as distinct and do not conflict.
// Dedup: first occurrence of each (key, action) pair retained, order preserved, backing array reused.
// Conflicts: all keys with both write and delete are collected.
// When tracing is enabled, all metadata is stored on Trace.PostProcess and all conflicts are collected.
// When tracing is disabled, returns immediately on the first conflict (fast path).
// Returns the first conflict as a *ConflictError, or nil.
func (r *Result) postProcess() error {
	tracing := r.Trace != nil

	type keyState struct {
		hasWrite  bool
		hasDelete bool
	}

	seen := make(map[string]keyState, len(r.Tuples))
	seenTuples := make(map[string]language.Tuple, len(r.Tuples))

	var removed []language.Tuple
	var conflictKeys []string
	var conflicts []Conflict

	w := 0
	for _, t := range r.Tuples {
		ck := tupleConflictKey(t)
		s := seen[ck]

		switch t.Action {
		case language.ActionWrite:
			if s.hasWrite {
				if tracing {
					removed = append(removed, t)
				}
				continue
			}
			s.hasWrite = true
			seenTuples[ck] = t
		case language.ActionDelete:
			if s.hasDelete {
				if tracing {
					removed = append(removed, t)
				}
				continue
			}
			s.hasDelete = true
			if _, ok := seenTuples[ck]; !ok {
				seenTuples[ck] = t
			}
		default:
			r.Tuples = r.Tuples[:w]
			return &language.ValidationError{
				Field:   fmt.Sprintf("(%s, %s, %s)", t.User, t.Relation, t.Object),
				Message: fmt.Sprintf("unknown tuple action %q", t.Action),
			}
		}

		seen[ck] = s
		r.Tuples[w] = t
		w++

		if s.hasWrite && s.hasDelete {
			ct := seenTuples[ck]
			if !tracing {
				r.Tuples = r.Tuples[:w]
				return &ConflictError{Conflict: Conflict{User: ct.User, Relation: ct.Relation, Object: ct.Object}, Condition: ct.Condition}
			}
			conflictKeys = append(conflictKeys, ck)
			conflicts = append(conflicts, Conflict{User: ct.User, Relation: ct.Relation, Object: ct.Object})
		}
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

	if len(conflicts) > 0 {
		ct := seenTuples[conflictKeys[0]]
		return &ConflictError{Conflict: conflicts[0], Condition: ct.Condition}
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
