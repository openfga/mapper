package mapper

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper/language"
)

// allValidationErrors extracts all *language.ValidationError instances from a (possibly joined) error.
func allValidationErrors(err error) []*language.ValidationError {
	var result []*language.ValidationError
	for _, e := range unwrapAll(err) {
		if ve, ok := errors.AsType[*language.ValidationError](e); ok {
			result = append(result, ve)
		}
	}
	return result
}

// allEvalErrors extracts all *EvalError instances from a (possibly joined) error.
func allEvalErrors(err error) []*EvalError {
	var result []*EvalError
	for _, e := range unwrapAll(err) {
		if ee, ok := errors.AsType[*EvalError](e); ok {
			result = append(result, ee)
		}
	}
	return result
}

// unwrapAll returns the leaf errors from an errors.Join tree.
// It handles only the multi-error Unwrap() []error interface (as produced by errors.Join).
// It does not recurse into single-wrap Unwrap() error chains; a wrapped error returned
// as a leaf is still reachable via errors.As but its inner cause is not further unwrapped here.
func unwrapAll(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result []error
		for _, e := range joined.Unwrap() {
			result = append(result, unwrapAll(e)...)
		}
		return result
	}
	return []error{err}
}

func TestCompileMultipleInterpErrors(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "bad interp"
    tuples:
      - user: "user:{{ input.id }"
        relation: "member"
        object: "org:{{ input.org }"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	evalErrs := allEvalErrors(err)
	assert.Len(t, evalErrs, 2, "expected 2 eval errors (user + object)")
}

func TestCompileValidationAndInterpErrorsCombined(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	// Rule 1 has a validation error (missing name), rule 2 has an interpolation error
	yaml := []byte(`
version: "1"
rules:
  - tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
  - name: "bad interp"
    tuples:
      - user: "user:{{ input.id }"
        relation: "member"
        object: "org:test"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	// Both ValidationError and EvalError should be reachable
	var ve *language.ValidationError
	require.True(t, errors.As(err, &ve), "expected ValidationError in combined error")

	var ee *EvalError
	require.True(t, errors.As(err, &ee), "expected EvalError in combined error")
}

func TestCompileDuplicateInvalidInterpProducesOneError(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	// The same invalid interpolation string appears as the user field in two separate rules.
	// compilationContext deduplicates failures and emits exactly one EvalError.
	yaml := []byte(`
version: "1"
rules:
  - name: "rule A"
    tuples:
      - user: "user:{{ input.id }"
        relation: "member"
        object: "org:1"
  - name: "rule B"
    tuples:
      - user: "user:{{ input.id }"
        relation: "member"
        object: "org:2"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	evalErrs := allEvalErrors(err)
	assert.Len(t, evalErrs, 1, "duplicate invalid interpolation string should produce exactly one EvalError")
}

func TestCompileTupleFiltersEmptyListYAML(t *testing.T) {
	t.Parallel()
	// Explicitly writing `tuple_filters: []` in YAML should produce a validation error,
	// not silently be treated as "no tuple_filters".
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "test"
    tuple_filters: []
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:123"
`)
	_, err := compiler.Compile(yaml)
	require.Error(t, err)
	errs := allValidationErrors(err)
	require.NotEmpty(t, errs)
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "must contain at least one filter") {
			found = true
		}
	}
	assert.True(t, found, "expected empty tuple_filters list to be rejected")
}

func TestCompilePatchFilterNoIteratorNoTuples(t *testing.T) {
	t.Parallel()
	// A rule with patch tuple_filters, no iterator, and no tuples can never produce
	// desired-state tuples. This should be caught at validation time rather than
	// deferring to the eval-time empty-state check.
	compiler := NewCompiler()

	t.Run("patch filter without tuples rejected", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "bad patch"
    tuple_filters:
      - relation: member
        object: "org:123"
`)
		_, err := compiler.Compile(yaml)
		require.Error(t, err)
		errs := allValidationErrors(err)
		require.NotEmpty(t, errs)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "must contain at least one tuple") {
				found = true
			}
		}
		assert.True(t, found, "expected validation error requiring tuples for patch filter")
	})

	t.Run("delete-only filter without tuples allowed", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "delete all"
    tuple_filters:
      - action: delete
        object: "org:123"
`)
		_, err := compiler.Compile(yaml)
		require.NoError(t, err)
	})

	t.Run("patch filter with iterator allowed", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - relation: member
        object: "org:123"
    iterator:
      source: input.members
      as: member
      tuples:
        - user: 'user:{{ member }}'
          relation: member
          object: "org:123"
`)
		_, err := compiler.Compile(yaml)
		require.NoError(t, err)
	})
}

func TestValidateTupleFiltersIntegration(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	t.Run("valid tuple_filters compiles successfully", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{ input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{ input.user_id }}'
        relation: member
        object: 'org:{{ input.org_id }}'
`)
		mapping, err := compiler.Compile(yaml)
		require.NoError(t, err)
		assert.NotNil(t, mapping)
	})

	t.Run("delete-only filter with no tuples compiles", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "delete all"
    tuple_filters:
      - object: 'org:{{ input.org_id }}'
        action: delete
`)
		mapping, err := compiler.Compile(yaml)
		require.NoError(t, err)
		assert.NotNil(t, mapping)
	})

	t.Run("mixed patch and delete filters", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "mixed"
    tuple_filters:
      - object: 'org:{{ input.org_id }}'
        relation: member
        action: patch
      - object: 'org:{{ input.org_id }}'
        relation: viewer
        action: delete
    tuples:
      - user: 'user:{{ input.user_id }}'
        relation: member
        object: 'org:{{ input.org_id }}'
`)
		mapping, err := compiler.Compile(yaml)
		require.NoError(t, err)
		assert.NotNil(t, mapping)
	})

	t.Run("invalid filter interp fails", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "bad interp"
    tuple_filters:
      - object: '{{ input.org_id'
    tuples:
      - user: 'user:x'
        relation: member
        object: 'org:y'
`)
		_, err := compiler.Compile(yaml)
		require.Error(t, err)
		_, ok := errors.AsType[*EvalError](err)
		assert.True(t, ok)
	})
}

func TestRuleLevelActionValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		yaml        string
		wantErr     bool
		errContains string // substring expected in ValidationError.Message
		errField    string // substring expected in ValidationError.Field
	}{
		{
			name: "rule action omitted -- tuples can have individual actions",
			yaml: `
version: "1"
rules:
  - name: "no rule action"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"
        action: "delete"`,
			wantErr: false,
		},
		{
			name: "rule action write -- valid",
			yaml: `
version: "1"
rules:
  - name: "write rule"
    action: "write"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"`,
			wantErr: false,
		},
		{
			name: "rule action delete -- valid",
			yaml: `
version: "1"
rules:
  - name: "delete rule"
    action: "delete"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"`,
			wantErr: false,
		},
		{
			name: "rule action invalid",
			yaml: `
version: "1"
rules:
  - name: "bad action"
    action: "bogus"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"`,
			wantErr:     true,
			errContains: `invalid action "bogus"`,
			errField:    `rules["bad action"].action`,
		},
		{
			name: "rule action + tuple action conflict",
			yaml: `
version: "1"
rules:
  - name: "conflict"
    action: "delete"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"
        action: "delete"`,
			wantErr:     true,
			errContains: "tuple-level action is not allowed when rule has action",
			errField:    `rules["conflict"].tuples[0].action`,
		},
		{
			name: "rule action + iterator tuple action conflict",
			yaml: `
version: "1"
rules:
  - name: "iter conflict"
    action: "write"
    iterator:
      source: "input.items"
      as: "item"
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:y"
          action: "write"`,
			wantErr:     true,
			errContains: "tuple-level action is not allowed when rule has action",
			errField:    `rules["iter conflict"].iterator.tuples[0].action`,
		},
		{
			name: "rule action delete propagates to tuples",
			yaml: `
version: "1"
rules:
  - name: "delete all"
    action: "delete"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"
      - user: "u:z"
        relation: "r"
        object: "o:w"`,
			wantErr: false,
		},
		{
			name: "rule action delete + patch tuple_filters",
			yaml: `
version: "1"
rules:
  - name: "bad combo"
    action: "delete"
    tuple_filters:
      - object: "org:123"
        relation: "member"
    tuples:
      - user: "u:x"
        relation: "member"
        object: "org:123"`,
			wantErr:     true,
			errContains: `rule-level "delete" action is not allowed when rule has patch tuple_filters`,
			errField:    `rules["bad combo"].action`,
		},
		{
			name: "rule action write + patch tuple_filters -- valid",
			yaml: `
version: "1"
rules:
  - name: "write patch"
    action: "write"
    tuple_filters:
      - object: "org:123"
        relation: "member"
    tuples:
      - user: "u:x"
        relation: "member"
        object: "org:123"`,
			wantErr: false,
		},
		{
			name: "rule action delete + delete-only tuple_filters -- valid",
			yaml: `
version: "1"
rules:
  - name: "delete delete"
    action: "delete"
    tuple_filters:
      - object: "org:123"
        action: "delete"`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiler := NewCompiler()
			_, err := compiler.Compile([]byte(tt.yaml))
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			valErrs := allValidationErrors(err)
			require.Len(t, valErrs, 1, "expected exactly 1 ValidationError, got: %v", valErrs)

			ve := valErrs[0]
			assert.Contains(t, ve.Message, tt.errContains)
			if tt.errField != "" {
				assert.Equal(t, tt.errField, ve.Field)
			}
		})
	}
}

func TestTupleFiltersRuleWithoutTuplesAllowed(t *testing.T) {
	t.Parallel()
	// A rule with only delete tuple_filters and no tuples should be valid
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "delete only"
    tuple_filters:
      - object: 'org:123'
        action: delete
`)
	_, err := compiler.Compile(yaml)
	require.NoError(t, err)
}
