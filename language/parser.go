package language

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// variableNamePattern constrains variable names to Expr identifiers. Names are
// referenced in expressions as variables.<name> (member access), so a name
// containing a dot or other non-identifier character is unreachable — reject it
// at validation rather than silently binding a dead variable.
const variableNamePattern = `^[A-Za-z_][A-Za-z0-9_]*$`

var validVariableName = regexp.MustCompile(variableNamePattern)

const (
	// DefaultMaxRules is the default cap on the number of rules a single mapping
	// file may declare. Override per-call with WithMaxRules.
	DefaultMaxRules = 100

	maxTupleFilters = 3
)

// validateConfig holds the resolved options for a Validate call.
type validateConfig struct {
	maxRules int
}

// ValidateOption configures a Validate call.
type ValidateOption func(*validateConfig)

// WithMaxRules overrides the default cap on the number of rules per mapping file.
// A non-positive value is ignored and the default is retained.
//
// Note: the identically-named mapper.WithMaxRules instead treats a non-positive
// value as a configuration error surfaced from Compile, rather than a no-op.
func WithMaxRules(n int) ValidateOption {
	return func(c *validateConfig) {
		if n > 0 {
			c.maxRules = n
		}
	}
}

// parseMapping parses a YAML mapping configuration into a MappingConfig.
// It acts as a version router: parses the AST once, extracts the version
// field, and delegates to the appropriate version-specific parser.
// Call Validate on the result to check structural constraints.
func parseMapping(data []byte) (*MappingConfig, error) {
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if file == nil || len(file.Docs) == 0 {
		return nil, fmt.Errorf("invalid YAML: empty YAML document")
	}
	if len(file.Docs) > 1 {
		return nil, fmt.Errorf("invalid YAML: multiple YAML documents are not supported (found %d); provide a single mapping document", len(file.Docs))
	}

	root := file.Docs[0].Body
	if root == nil {
		return nil, fmt.Errorf("invalid YAML: empty YAML document")
	}

	versionNode := resolveNodePath(root, "version")
	version, ok := extractVersion(versionNode)

	if versionNode != nil && !ok {
		return nil, &ValidationError{
			Field:    "version",
			Message:  fmt.Sprintf("must be a string, got %s", versionNode.GetToken().Value),
			Position: nodePosition(versionNode),
		}
	}

	switch version {
	case versionV1:
		return parseV1(root)
	case "":
		return nil, &ValidationError{
			Field:    "version",
			Message:  "is required",
			Position: nodePosition(versionNode),
		}
	default:
		return nil, &ValidationError{
			Field:    "version",
			Message:  fmt.Sprintf("unsupported version %q (supported: %q)", version, versionV1),
			Position: nodePosition(versionNode),
		}
	}
}

// extractVersion reads the version from a YAML AST node.
// Returns the version string and true if the node is a string or integer,
// or ("", false) if the node is nil or an unsupported type.
func extractVersion(node ast.Node) (string, bool) {
	if node == nil {
		return "", false
	}
	if strNode, ok := node.(*ast.StringNode); ok {
		return strNode.Value, true
	}
	if intNode, ok := node.(*ast.IntegerNode); ok {
		return intNode.Token.Value, true
	}
	return "", false
}

// toValidationError converts a goccy/go-yaml error into a *ValidationError,
// extracting position from the token when available.
func toValidationError(err error) error {
	if unknownErr, ok := errors.AsType[*yaml.UnknownFieldError](err); ok {
		pos := Position{}
		field := "unknown"
		if tk := unknownErr.Token; tk != nil && tk.Position != nil {
			field = tk.Value
			pos = Position{
				StartLine:   tk.Position.Line,
				StartColumn: tk.Position.Column,
				EndLine:     tk.Position.Line,
				EndColumn:   tk.Position.Column + len(tk.Value),
			}
		}
		return &ValidationError{
			Field:    field,
			Message:  unknownErr.Message,
			Position: pos,
		}
	}
	return fmt.Errorf("invalid YAML: %w", err)
}

// Validate checks structural constraints on a parsed MappingConfig.
// Source positions are attached to errors using the YAML node tree
// stored during parsing.
// Returns a joined error tree containing one or more *ValidationError values,
// or nil if the configuration is valid.
//
// Validate does not consume the AST node tree; it may be called repeatedly and
// still attach source positions. Callers done with position data should call
// ClearRoot to release the tree for garbage collection.
func (m *MappingConfig) Validate(opts ...ValidateOption) error {
	cfg := validateConfig{maxRules: DefaultMaxRules}
	for _, opt := range opts {
		opt(&cfg)
	}

	var errs []error

	if len(m.Rules) == 0 {
		errs = append(errs, &ValidationError{
			Field:    "rules",
			Message:  "must contain at least one rule",
			Position: nodePosition(resolveNodePath(m.root, "rules")),
		})
	}
	if len(m.Rules) > cfg.maxRules {
		errs = append(errs, &ValidationError{
			Field:    "rules",
			Message:  fmt.Sprintf("exceeds maximum of %d rules", cfg.maxRules),
			Position: nodePosition(resolveNodePath(m.root, "rules")),
		})
	}

	for i := range m.Rules {
		errs = append(errs, validateRule(&m.Rules[i], i, m.root)...)
	}

	for i := range m.Tests {
		errs = append(errs, validateTestCase(&m.Tests[i], i, m.root)...)
	}

	return errors.Join(errs...)
}

// ClearRoot releases the retained YAML AST node tree so it can be garbage
// collected. Source positions are stamped onto the parsed structs at parse time,
// so they survive; only Validate's ability to look up positions for structural
// errors depends on the tree. Call this once, after the final Validate, when a
// long-lived MappingConfig no longer needs position lookup — the compiled mapping
// reads only the flat Position fields, never the tree.
func (m *MappingConfig) ClearRoot() {
	m.root = nil
}

// validateRule validates a single rule, using index for YAML node path lookups
// and rule.Name (when set) for the human-readable Field path in errors.
func validateRule(rule *Rule, index int, root ast.Node) []error {
	var errs []error

	// lookupPrefix uses the numeric index for resolveNodePath traversal (rules is a
	// SequenceNode, so only numeric indices work). displayPrefix uses the quoted name
	// for human-readable Field paths in ValidationErrors.
	lookupPrefix := fmt.Sprintf("rules[%d]", index)
	var displayPrefix string
	if rule.Name == "" {
		displayPrefix = lookupPrefix
		field := lookupPrefix + ".name"
		errs = append(errs, &ValidationError{
			Field:    field,
			Message:  "is required",
			Position: nodePosition(resolveNodePath(root, field)),
		})
	} else {
		displayPrefix = fmt.Sprintf("rules[%q]", rule.Name)
	}

	// Validate rule-level action
	switch rule.Action {
	case "":
		// omitted -- no rule-level action
	case ActionWrite, ActionDelete:
		// valid -- propagate to tuples below after conflict checks
	default:
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".action",
			Message:  fmt.Sprintf("invalid action %q (must be %q or %q)", rule.Action, ActionWrite, ActionDelete),
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".action")),
		})
	}

	// Forbid tuple-level action when rule-level action is set.
	// Only check for valid actions to avoid cascading errors when the action itself is invalid.
	if rule.Action == ActionWrite || rule.Action == ActionDelete {
		for j := range rule.Tuples {
			lookupPath := fmt.Sprintf("%s.tuples[%d].action", lookupPrefix, j)
			if node := resolveNodePath(root, lookupPath); node != nil {
				errs = append(errs, &ValidationError{
					Field:    fmt.Sprintf("%s.tuples[%d].action", displayPrefix, j),
					Message:  fmt.Sprintf("tuple-level action is not allowed when rule has action %q; remove the tuple-level action", rule.Action),
					Position: nodePosition(node),
				})
			}
		}
		if rule.Iterator != nil {
			for j := range rule.Iterator.Tuples {
				lookupPath := fmt.Sprintf("%s.iterator.tuples[%d].action", lookupPrefix, j)
				if node := resolveNodePath(root, lookupPath); node != nil {
					errs = append(errs, &ValidationError{
						Field:    fmt.Sprintf("%s.iterator.tuples[%d].action", displayPrefix, j),
						Message:  fmt.Sprintf("tuple-level action is not allowed when rule has action %q; remove the tuple-level action", rule.Action),
						Position: nodePosition(node),
					})
				}
			}
		}

		// Propagate rule-level action to tuples that don't have an explicit action.
		// This runs before validateTupleTemplate so the default ActionWrite is not applied
		// over the rule-level action.
		for j := range rule.Tuples {
			if rule.Tuples[j].Action == "" {
				rule.Tuples[j].Action = rule.Action
			}
		}
		if rule.Iterator != nil {
			for j := range rule.Iterator.Tuples {
				if rule.Iterator.Tuples[j].Action == "" {
					rule.Iterator.Tuples[j].Action = rule.Action
				}
			}
		}
	}

	// Check AST presence (not slice length) so that `tuple_filters: []` is validated
	// and rejected rather than silently treated as "no tuple_filters".
	hasTupleFilters := len(rule.TupleFilters) > 0 ||
		resolveNodePath(root, lookupPrefix+".tuple_filters") != nil

	switch {
	case rule.Iterator != nil:
		// Iterator present: iterator.tuples required (unless all tuple_filters are delete-only),
		// rule.tuples optional
		if len(rule.Iterator.Tuples) == 0 && !allFiltersDelete(rule.TupleFilters) {
			errs = append(errs, &ValidationError{
				Field:    displayPrefix + ".iterator.tuples",
				Message:  "must contain at least one tuple",
				Position: nodePosition(resolveNodePath(root, lookupPrefix+".iterator.tuples")),
			})
		}
		for j := range rule.Iterator.Tuples {
			errs = append(errs, validateTupleTemplate(&rule.Iterator.Tuples[j], displayPrefix+".iterator", lookupPrefix+".iterator", j, root)...)
		}
	case len(rule.Tuples) == 0 && !hasTupleFilters:
		// No iterator and no tuple_filters: rule.tuples required
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".tuples",
			Message:  "must contain at least one tuple",
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".tuples")),
		})
	case len(rule.Tuples) == 0 && hasTupleFilters && anyFilterPatch(rule.TupleFilters):
		// No iterator, no tuples, but patch filters present: this rule can never produce
		// desired-state tuples, so it would always fail the eval-time empty-state check.
		// Catch it early with a clearer validation error.
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".tuples",
			Message:  "must contain at least one tuple when tuple_filters includes a patch filter",
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".tuples")),
		})
	}

	if hasTupleFilters {
		errs = append(errs, validateTupleFilters(rule, displayPrefix, lookupPrefix, root)...)
	}

	// Validate each rule-level tuple template regardless of iterator presence.
	for j := range rule.Tuples {
		errs = append(errs, validateTupleTemplate(&rule.Tuples[j], displayPrefix, lookupPrefix, j, root)...)
	}

	seenVars := make(map[string]int, len(rule.Variables))
	for j, v := range rule.Variables {
		// Build node path for position tracking
		var varNodePath string
		if v.Name != "" {
			varNodePath = fmt.Sprintf("rules[%d].variables[\"%s\"]", index, v.Name)
		} else {
			// For empty names, use the variables mapping as fallback position
			varNodePath = fmt.Sprintf("rules[%d].variables", index)
		}

		if v.Name == "" {
			errs = append(errs, &ValidationError{
				Field:    fmt.Sprintf("%s.variables[%d].name", displayPrefix, j),
				Message:  "is required",
				Position: nodePosition(resolveNodePath(root, varNodePath)),
			})
		}
		if v.Expression == "" {
			errs = append(errs, &ValidationError{
				Field:    fmt.Sprintf("%s.variables[%d].expression", displayPrefix, j),
				Message:  "is required",
				Position: nodePosition(resolveNodePath(root, varNodePath)),
			})
		}
		if v.Name != "" && !validVariableName.MatchString(v.Name) {
			errs = append(errs, &ValidationError{
				Field:    fmt.Sprintf("%s.variables[%d].name", displayPrefix, j),
				Message:  fmt.Sprintf("invalid variable name %q (must match %s)", v.Name, variableNamePattern),
				Position: nodePosition(resolveNodePath(root, varNodePath)),
			})
		}
		if v.Name != "" {
			if prev, exists := seenVars[v.Name]; exists {
				errs = append(errs, &ValidationError{
					Field:    fmt.Sprintf("%s.variables[%d]", displayPrefix, j),
					Message:  fmt.Sprintf("duplicate variable name %q (first defined at index %d)", v.Name, prev),
					Position: nodePosition(resolveNodePath(root, varNodePath)),
				})
			}
			seenVars[v.Name] = j
		}
	}

	if rule.Iterator != nil {
		if rule.Iterator.Source == "" {
			errs = append(errs, &ValidationError{
				Field:    displayPrefix + ".iterator.source",
				Message:  "is required",
				Position: nodePosition(resolveNodePath(root, lookupPrefix+".iterator.source")),
			})
		}
		if rule.Iterator.As == "" {
			errs = append(errs, &ValidationError{
				Field:    displayPrefix + ".iterator.as",
				Message:  "is required",
				Position: nodePosition(resolveNodePath(root, lookupPrefix+".iterator.as")),
			})
		}

		switch rule.Iterator.As {
		case "input", "variables":
			errs = append(errs, &ValidationError{
				Field: displayPrefix + ".iterator.as",
				Message: fmt.Sprintf(
					`iterator "as" name %q shadows the built-in %s scope; choose a different name`,
					rule.Iterator.As, rule.Iterator.As,
				),
				Position: nodePosition(resolveNodePath(root, lookupPrefix+".iterator.as")),
			})
		}
	}

	return errs
}

// validateTupleTemplate validates a single tuple template. displayRulePrefix is used
// in ValidationError.Field paths (human-readable); lookupRulePrefix uses numeric indices
// for resolveNodePath traversal.
func validateTupleTemplate(tuple *ParsedTuple, displayRulePrefix, lookupRulePrefix string, tupleIndex int, root ast.Node) []error {
	displayPrefix := fmt.Sprintf("%s.tuples[%d]", displayRulePrefix, tupleIndex)
	lookupPrefix := fmt.Sprintf("%s.tuples[%d]", lookupRulePrefix, tupleIndex)

	var errs []error

	if tuple.User == "" {
		errs = append(errs, &ValidationError{Field: displayPrefix + ".user", Message: "is required", Position: nodePosition(resolveNodePath(root, lookupPrefix+".user"))})
	}
	if tuple.Relation == "" {
		errs = append(errs, &ValidationError{Field: displayPrefix + ".relation", Message: "is required", Position: nodePosition(resolveNodePath(root, lookupPrefix+".relation"))})
	}
	if tuple.Object == "" {
		errs = append(errs, &ValidationError{Field: displayPrefix + ".object", Message: "is required", Position: nodePosition(resolveNodePath(root, lookupPrefix+".object"))})
	}

	switch tuple.Action {
	case "":
		tuple.Action = ActionWrite
	case ActionWrite, ActionDelete:
		// valid
	default:
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".action",
			Message:  fmt.Sprintf("invalid action %q (must be %q or %q)", tuple.Action, ActionWrite, ActionDelete),
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".action")),
		})
	}

	// context requires a condition name
	if len(tuple.Context) > 0 && tuple.Condition == "" {
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".context",
			Message:  "context requires a condition name",
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".context")),
		})
	}

	// condition is not allowed on delete tuples
	if tuple.Condition != "" && tuple.Action == ActionDelete {
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".condition",
			Message:  "condition is not allowed on delete tuples",
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".condition")),
		})
	}

	return errs
}

// allFiltersDelete reports whether every filter in the slice has action: delete.
// Returns false for empty slices.
func allFiltersDelete(filters []ParsedTupleFilter) bool {
	if len(filters) == 0 {
		return false
	}
	for _, f := range filters {
		if f.Action != FilterActionDelete {
			return false
		}
	}
	return true
}

// anyFilterPatch reports whether any filter in the slice has action: patch.
func anyFilterPatch(filters []ParsedTupleFilter) bool {
	for _, f := range filters {
		if f.Action == FilterActionPatch || f.Action == "" {
			return true
		}
	}
	return false
}

// isConcreteValue reports whether a template string is a concrete (non-template) value.
func isConcreteValue(s string) bool {
	return s != "" && !strings.Contains(s, "{{")
}

// isObjectTypePrefix reports whether a string is an object type prefix (e.g., "org:").
// The type portion must be non-empty, so ":" alone is not a valid prefix.
func isObjectTypePrefix(s string) bool {
	if len(s) < 2 {
		return false
	}
	idx := strings.IndexByte(s, ':')
	return idx > 0 && idx == len(s)-1
}

// validateTupleFilters validates the tuple_filters field on a rule.
func validateTupleFilters(rule *Rule, displayPrefix, lookupPrefix string, root ast.Node) []error {
	var errs []error
	filters := rule.TupleFilters

	// Empty list check (tuple_filters key present but empty sequence)
	if len(filters) == 0 {
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".tuple_filters",
			Message:  "must contain at least one filter",
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".tuple_filters")),
		})
		return errs
	}

	// Max filters check
	if len(filters) > maxTupleFilters {
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".tuple_filters",
			Message:  fmt.Sprintf("exceeds maximum of %d filters", maxTupleFilters),
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".tuple_filters")),
		})
	}

	for j := range filters {
		f := &filters[j]
		filterDisplay := fmt.Sprintf("%s.tuple_filters[%d]", displayPrefix, j)
		filterLookup := fmt.Sprintf("%s.tuple_filters[%d]", lookupPrefix, j)

		// At least one field set
		if f.User == "" && f.Relation == "" && f.Object == "" {
			errs = append(errs, &ValidationError{
				Field:    filterDisplay,
				Message:  "must have at least one field set (user, relation, or object)",
				Position: nodePosition(resolveNodePath(root, filterLookup)),
			})
		}

		// Action validation and defaulting
		switch f.Action {
		case "":
			f.Action = FilterActionPatch
		case FilterActionPatch, FilterActionDelete:
			// valid
		default:
			errs = append(errs, &ValidationError{
				Field:    filterDisplay + ".action",
				Message:  fmt.Sprintf("invalid action %q (must be %q or %q)", f.Action, FilterActionPatch, FilterActionDelete),
				Position: nodePosition(resolveNodePath(root, filterLookup+".action")),
			})
		}

		// All-three-concrete rejection: if user, relation, AND object are all concrete
		// (non-empty, no template syntax) and object is not a type prefix, reject it.
		if isConcreteValue(f.User) && isConcreteValue(f.Relation) && isConcreteValue(f.Object) && !isObjectTypePrefix(f.Object) {
			errs = append(errs, &ValidationError{
				Field:    filterDisplay,
				Message:  "filter with all three fields set to concrete values describes a single tuple, not a range; use tuple-level action instead",
				Position: nodePosition(resolveNodePath(root, filterLookup)),
			})
		}
	}

	// Delete-only + tuples check: if ALL filters are delete, rule must not define tuples
	if allFiltersDelete(filters) {
		hasTuples := len(rule.Tuples) > 0 || (rule.Iterator != nil && len(rule.Iterator.Tuples) > 0)
		if hasTuples {
			errs = append(errs, &ValidationError{
				Field:    displayPrefix + ".tuple_filters",
				Message:  "all filters have action \"delete\" but rule defines tuples; delete filters remove everything matching, so desired-state tuples are contradictory",
				Position: nodePosition(resolveNodePath(root, lookupPrefix+".tuple_filters")),
			})
		}
	}

	// Rule-level action: delete + patch filters check
	if rule.Action == ActionDelete && anyFilterPatch(filters) {
		errs = append(errs, &ValidationError{
			Field:    displayPrefix + ".action",
			Message:  `rule-level "delete" action is not allowed when rule has patch tuple_filters; tuples define the desired state for patch operations`,
			Position: nodePosition(resolveNodePath(root, lookupPrefix+".action")),
		})
	}

	// Patch + tuple-level deletes check — skip when rule-level action caused the propagation
	if rule.Action != ActionDelete && anyFilterPatch(filters) {
		for j, pt := range rule.Tuples {
			if pt.Action == ActionDelete {
				errs = append(errs, &ValidationError{
					Field:    fmt.Sprintf("%s.tuples[%d].action", displayPrefix, j),
					Message:  "tuple-level \"delete\" action is not allowed when rule has patch tuple_filters; tuples define the desired state for patch operations",
					Position: nodePosition(resolveNodePath(root, fmt.Sprintf("%s.tuples[%d].action", lookupPrefix, j))),
				})
			}
		}
		if rule.Iterator != nil {
			for j, pt := range rule.Iterator.Tuples {
				if pt.Action == ActionDelete {
					errs = append(errs, &ValidationError{
						Field:    fmt.Sprintf("%s.iterator.tuples[%d].action", displayPrefix, j),
						Message:  "tuple-level \"delete\" action is not allowed when rule has patch tuple_filters; tuples define the desired state for patch operations",
						Position: nodePosition(resolveNodePath(root, fmt.Sprintf("%s.iterator.tuples[%d].action", lookupPrefix, j))),
					})
				}
			}
		}
	}

	return errs
}

func validateTestCase(tc *TestCase, index int, root ast.Node) []error {
	var errs []error

	if tc.Name == "" {
		field := fmt.Sprintf("tests[%d].name", index)
		errs = append(errs, &ValidationError{
			Field:    field,
			Message:  "is required",
			Position: nodePosition(resolveNodePath(root, field)),
		})
	}

	// Input is required per the spec; an omitted or null value yields a nil map.
	// An empty mapping (input: {}) is a deliberate no-field event and is allowed.
	if tc.Input == nil {
		field := fmt.Sprintf("tests[%d].input", index)
		errs = append(errs, &ValidationError{
			Field:    field,
			Message:  "is required",
			Position: nodePosition(resolveNodePath(root, field)),
		})
	}

	for j := range tc.ExpectTuples {
		prefix := fmt.Sprintf("tests[%d].expect_tuples[%d]", index, j)

		if tc.ExpectTuples[j].User == "" {
			field := prefix + ".user"
			errs = append(errs, &ValidationError{Field: field, Message: "is required", Position: nodePosition(resolveNodePath(root, field))})
		}
		if tc.ExpectTuples[j].Relation == "" {
			field := prefix + ".relation"
			errs = append(errs, &ValidationError{Field: field, Message: "is required", Position: nodePosition(resolveNodePath(root, field))})
		}
		if tc.ExpectTuples[j].Object == "" {
			field := prefix + ".object"
			errs = append(errs, &ValidationError{Field: field, Message: "is required", Position: nodePosition(resolveNodePath(root, field))})
		}

		switch tc.ExpectTuples[j].Action {
		case "":
			tc.ExpectTuples[j].Action = ActionWrite
		case ActionWrite, ActionDelete:
			// valid
		default:
			field := prefix + ".action"
			errs = append(errs, &ValidationError{
				Field:    field,
				Message:  fmt.Sprintf("invalid action %q (must be %q or %q)", tc.ExpectTuples[j].Action, ActionWrite, ActionDelete),
				Position: nodePosition(resolveNodePath(root, field)),
			})
		}
	}

	for j := range tc.ExpectTupleFilters {
		prefix := fmt.Sprintf("tests[%d].expect_tuple_filters[%d]", index, j)

		if tc.ExpectTupleFilters[j].User == "" && tc.ExpectTupleFilters[j].Relation == "" && tc.ExpectTupleFilters[j].Object == "" {
			errs = append(errs, &ValidationError{
				Field:    prefix,
				Message:  "must have at least one field set (user, relation, or object)",
				Position: nodePosition(resolveNodePath(root, prefix)),
			})
		}

		switch tc.ExpectTupleFilters[j].Action {
		case "":
			tc.ExpectTupleFilters[j].Action = FilterActionPatch
		case FilterActionPatch, FilterActionDelete:
			// valid
		default:
			field := prefix + ".action"
			errs = append(errs, &ValidationError{
				Field:    field,
				Message:  fmt.Sprintf("invalid action %q (must be %q or %q)", tc.ExpectTupleFilters[j].Action, FilterActionPatch, FilterActionDelete),
				Position: nodePosition(resolveNodePath(root, field)),
			})
		}
	}

	return errs
}
