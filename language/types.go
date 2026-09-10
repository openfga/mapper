package language

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// Position represents a source location range in a YAML file.
// All fields are 1-based. A value of 0 means the position is unknown.
// Parent structs use json:"omitzero" to omit zero-valued positions from JSON.
type Position struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

// TupleAction represents the action to perform on a tuple.
type TupleAction string

const (
	ActionWrite  TupleAction = "write"
	ActionDelete TupleAction = "delete"
)

// TupleFilterAction represents the action for a tuple filter.
// Semantically distinct from TupleAction: patch/delete operate on FGA read results,
// not individual tuples.
type TupleFilterAction string

const (
	FilterActionPatch  TupleFilterAction = "patch"
	FilterActionDelete TupleFilterAction = "delete"
)

// ParsedTupleFilter is a tuple filter parsed from YAML. Object is a required
// interpolated string; User and Relation are optional interpolated strings.
// Empty rendered fields act as wildcards.
//
// The *Pos fields carry the source position of each interpolated field, stamped
// at parse time so the mapper can attach diagnostics without reaching back into
// the YAML AST.
type ParsedTupleFilter struct {
	User     string            `yaml:"user,omitempty"`
	Relation string            `yaml:"relation,omitempty"`
	Object   string            `yaml:"object,omitempty"`
	Action   TupleFilterAction `yaml:"action,omitempty"`

	UserPos     Position `yaml:"-"`
	RelationPos Position `yaml:"-"`
	ObjectPos   Position `yaml:"-"`
}

// TupleFilter is a rendered tuple filter produced by the mapper. Empty fields act as
// wildcards that match any value in the corresponding FGA Read API field.
type TupleFilter struct {
	User     string            `json:"user,omitempty"`
	Relation string            `json:"relation,omitempty"`
	Object   string            `json:"object,omitempty"`
	Action   TupleFilterAction `json:"action"`
}

// MappingConfig is the top-level structure parsed from a mapping YAML file.
// It contains the schema version, an ordered list of rules, and optional embedded test cases.
type MappingConfig struct {
	Version string     `yaml:"version"`
	Rules   []Rule     `yaml:"rules"`
	Tests   []TestCase `yaml:"tests,omitempty"`
	root    ast.Node   // AST node tree for source position lookup during validation
}

// Rule defines a mapping from an input event to one or more tuples.
// Each rule optionally filters via a When guard (Expr), computes Variables (Expr),
// iterates over a collection, and renders Tuples via interpolated strings ({{ expr }}).
type Rule struct {
	Name         string              `yaml:"name"`
	When         string              `yaml:"when,omitempty"`
	Action       TupleAction         `yaml:"action,omitempty"`
	RawVariables any                 `yaml:"variables,omitempty"` // sink for strict YAML; unused -- variables extracted from AST
	Variables    Variables           `yaml:"-"`
	TupleFilters []ParsedTupleFilter `yaml:"tuple_filters,omitempty"`
	Iterator     *Iterator           `yaml:"iterator,omitempty"`
	Tuples       []ParsedTuple       `yaml:"tuples"`

	WhenPos Position `yaml:"-"` // source position of the when guard, stamped at parse time
}

// Variable represents a named variable with an Expr expression to evaluate.
type Variable struct {
	Name       string
	Expression string
	Position   Position // source position of the variable entry, stamped at parse time
}

// Variables is an ordered list of Variable, extracted from the YAML AST
// while preserving definition order for sequential evaluation.
type Variables []Variable

// Iterator represents a fan-out configuration that iterates over a collection.
type Iterator struct {
	Source string        `yaml:"source"`
	As     string        `yaml:"as"`
	Tuples []ParsedTuple `yaml:"tuples"`

	SourcePos Position `yaml:"-"` // source position of the iterator source expression, stamped at parse time
}

// ParsedTuple is a tuple parsed from YAML whose User, Relation, and Object fields
// are interpolated strings rendered against the evaluation context (input, variables, iterator value).
type ParsedTuple struct {
	When      string            `yaml:"when,omitempty"`
	Action    TupleAction       `yaml:"action,omitempty"`
	User      string            `yaml:"user"`
	Relation  string            `yaml:"relation"`
	Object    string            `yaml:"object"`
	Condition string            `yaml:"condition,omitempty"` // FGA condition name
	Context   map[string]string `yaml:"context,omitempty"`   // FGA context (values are interpolation templates)

	// Source positions stamped at parse time so the mapper can attach diagnostics
	// to compiled interpolations without reaching back into the YAML AST.
	WhenPos     Position            `yaml:"-"`
	UserPos     Position            `yaml:"-"`
	RelationPos Position            `yaml:"-"`
	ObjectPos   Position            `yaml:"-"`
	ContextPos  map[string]Position `yaml:"-"`
}

// Tuple is a resolved FGA relationship tuple. The mapper produces values of this
// type; the language owns the type because embedded test cases (TestCase) declare
// expected tuples, keeping the data model in a single leaf package.
type Tuple struct {
	User      string         `yaml:"user"      json:"user"`
	Relation  string         `yaml:"relation"  json:"relation"`
	Object    string         `yaml:"object"    json:"object"`
	Action    TupleAction    `yaml:"action,omitempty"    json:"action,omitempty"`
	Condition string         `yaml:"condition,omitempty" json:"condition,omitempty"` // FGA condition name
	Context   map[string]any `yaml:"context,omitempty"   json:"context,omitempty"`   // rendered FGA context
}

// Key returns a comparable string key for use as a map key.
// Includes all fields: user, relation, object, action, condition, and context.
// Uses length-prefixed encoding to avoid separator collision issues.
// Context is serialized as JSON (json.Marshal sorts map keys deterministically).
func (t Tuple) Key() string {
	var b strings.Builder
	writeField := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	writeField(t.User)
	writeField(t.Relation)
	writeField(t.Object)
	writeField(string(t.Action))
	writeField(t.Condition)
	if len(t.Context) > 0 {
		ctx, err := json.Marshal(t.Context)
		if err != nil {
			// Context values come from renderInterp (strings) or FGA reads (JSON-safe types).
			// Marshal failure here indicates a programming error, not user input.
			panic(fmt.Sprintf("TupleKey: failed to marshal context: %v", err))
		}
		writeField(string(ctx))
	}
	return b.String()
}

// TestCase represents an embedded test case defined in the mapping file.
type TestCase struct {
	Name                        string         `yaml:"name"`
	Input                       map[string]any `yaml:"input"`
	ExpectTuples                []Tuple        `yaml:"expect_tuples"`
	ExpectTupleFilters          []TupleFilter  `yaml:"expect_tuple_filters,omitempty"`
	AssertWritesCoveredByFilter bool           `yaml:"assert_writes_covered_by_filter,omitempty"`
}

// ValidationError represents a validation error in the mapping configuration.
type ValidationError struct {
	Field    string
	Message  string
	Position Position
}

func (e *ValidationError) Error() string {
	if e.Position.StartLine > 0 {
		return fmt.Sprintf("validation error: %s (line %d, col %d): %s",
			e.Field, e.Position.StartLine, e.Position.StartColumn, e.Message)
	}
	return fmt.Sprintf("validation error: %s: %s", e.Field, e.Message)
}
