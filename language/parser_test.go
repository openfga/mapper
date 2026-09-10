package language

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allValidationErrors extracts all *ValidationError instances from a (possibly joined) error.
func allValidationErrors(err error) []*ValidationError {
	var result []*ValidationError
	for _, e := range unwrapAll(err) {
		if ve, ok := errors.AsType[*ValidationError](e); ok {
			result = append(result, ve)
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

// ---------------------------------------------------------------------------
// Happy-path parsing tests
// ---------------------------------------------------------------------------

func TestParseMappingFullConfig(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "Initialize User Permissions"
    when: >
      input.type == "user.created" &&
      input.data.email != ""
    variables:
      user_email: lower(trim(input.data.email))
      org_id: input.data.org_id ?? "default"
    tuples:
      - user: "user:{{ .variables.user_email }}"
        relation: "member"
        object: "org:{{ .variables.org_id }}"
      - when: input.data.is_admin == true
        user: "user:{{ .variables.user_email }}"
        relation: "admin"
        object: "org:{{ .variables.org_id }}"

  - name: "Revoke Old Tier"
    when: input.type == "subscription.upgraded"
    variables:
      old_tier: input.data.old_tier ?? ""
    tuples:
      - when: variables.old_tier != ""
        action: delete
        user: "user:{{ .input.data.user_id }}"
        relation: "subscriber"
        object: "tier:{{ .variables.old_tier }}"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	assert.Equal(t, "1", config.Version)
	require.Len(t, config.Rules, 2)

	// First rule: Initialize User Permissions
	rule0 := config.Rules[0]
	assert.Equal(t, "Initialize User Permissions", rule0.Name)
	assert.Contains(t, rule0.When, `input.type == "user.created"`)
	assert.Contains(t, rule0.When, `input.data.email != ""`)
	assert.Nil(t, rule0.Iterator)

	// Variables preserve order
	require.Len(t, rule0.Variables, 2)
	assert.Equal(t, "user_email", rule0.Variables[0].Name)
	assert.Equal(t, "lower(trim(input.data.email))", rule0.Variables[0].Expression)
	assert.Equal(t, "org_id", rule0.Variables[1].Name)
	assert.Equal(t, `input.data.org_id ?? "default"`, rule0.Variables[1].Expression)

	// Tuples
	require.Len(t, rule0.Tuples, 2)
	assert.Equal(t, ActionWrite, rule0.Tuples[0].Action)
	assert.Empty(t, rule0.Tuples[0].When)
	assert.Equal(t, `user:{{ .variables.user_email }}`, rule0.Tuples[0].User)

	assert.Equal(t, "input.data.is_admin == true", rule0.Tuples[1].When)
	assert.Equal(t, ActionWrite, rule0.Tuples[1].Action)

	// Second rule: Revoke Old Tier
	rule1 := config.Rules[1]
	assert.Equal(t, "Revoke Old Tier", rule1.Name)
	require.Len(t, rule1.Tuples, 1)
	assert.Equal(t, ActionDelete, rule1.Tuples[0].Action)
	assert.Equal(t, `variables.old_tier != ""`, rule1.Tuples[0].When)
}

func TestParseMappingIterator(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "Sync User Roles"
    when: len(input.data.roles) > 0
    iterator:
      source: input.data.roles
      as: role_name
      tuples:
        - user: "user:{{ .input.data.user_id }}"
          relation: "member"
          object: "role:{{ .role_name }}"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	rule := config.Rules[0]
	assert.Equal(t, "Sync User Roles", rule.Name)
	assert.Equal(t, "len(input.data.roles) > 0", rule.When)

	require.NotNil(t, rule.Iterator)
	assert.Equal(t, "input.data.roles", rule.Iterator.Source)
	assert.Equal(t, "role_name", rule.Iterator.As)

	require.Len(t, rule.Iterator.Tuples, 1)
	assert.Equal(t, `role:{{ .role_name }}`, rule.Iterator.Tuples[0].Object)
}

func TestParseMappingEmbeddedTests(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "Map Admin Role"
    when: input.type == "project.member.added"
    tuples:
      - user: "user:{{ .input.data.email }}"
        relation: "member"
        object: "project:{{ .input.data.project_id }}"
tests:
  - name: "Ensure Admin Role Maps Correctly"
    input:
      type: "project.member.added"
      data:
        urn: "urn:example:project:101"
        email: "alice@example.com"
        role: "admin"
    expect_tuples:
      - user: "user:alice@example.com"
        relation: "member"
        object: "project:101"
      - user: "group:auditors"
        relation: "monitor"
        object: "project:101"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	require.Len(t, config.Tests, 1)
	tc := config.Tests[0]
	assert.Equal(t, "Ensure Admin Role Maps Correctly", tc.Name)

	// Verify input is parsed as nested map
	assert.Equal(t, "project.member.added", tc.Input["type"])
	data, ok := tc.Input["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alice@example.com", data["email"])

	// Verify expected tuples
	require.Len(t, tc.ExpectTuples, 2)
	assert.Equal(t, "user:alice@example.com", tc.ExpectTuples[0].User)
	assert.Equal(t, "member", tc.ExpectTuples[0].Relation)
	assert.Equal(t, "project:101", tc.ExpectTuples[0].Object)
	assert.Equal(t, ActionWrite, tc.ExpectTuples[0].Action) // default

	assert.Equal(t, "group:auditors", tc.ExpectTuples[1].User)
	assert.Equal(t, "monitor", tc.ExpectTuples[1].Relation)
}

func TestParseMappingVariableOrdering(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    variables:
      first: "input.a"
      second: "first + input.b"
      third: "second + input.c"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	vars := config.Rules[0].Variables
	require.Len(t, vars, 3)
	assert.Equal(t, "first", vars[0].Name)
	assert.Equal(t, "input.a", vars[0].Expression)
	assert.Equal(t, "second", vars[1].Name)
	assert.Equal(t, "first + input.b", vars[1].Expression)
	assert.Equal(t, "third", vars[2].Name)
	assert.Equal(t, "second + input.c", vars[2].Expression)
}

func TestParseMappingRuleWithoutWhenGuard(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())
	assert.Empty(t, config.Rules[0].When)
	assert.Empty(t, config.Rules[0].Variables)
	assert.Nil(t, config.Rules[0].Iterator)
}

func TestParseMappingNoTestsBlock(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())
	assert.Empty(t, config.Tests)
}

func TestParseMappingExactly100Rules(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("version: \"1\"\nrules:\n")
	for i := range 100 {
		fmt.Fprintf(&b, "  - name: \"rule %d\"\n    tuples:\n      - user: \"u:x\"\n        relation: \"r\"\n        object: \"o:x\"\n", i)
	}

	config, err := parseMapping([]byte(b.String()))
	require.NoError(t, err)
	require.NoError(t, config.Validate())
	assert.Len(t, config.Rules, 100)
}

func TestWithMaxRulesNonPositiveIsNoop(t *testing.T) {
	t.Parallel()
	// A non-positive value must be ignored and DefaultMaxRules retained.
	// Use a two-rule mapping so that WithMaxRules(1) would fail but
	// WithMaxRules(0) (no-op) succeeds.
	var b strings.Builder
	b.WriteString("version: \"1\"\nrules:\n")
	for i := range 2 {
		fmt.Fprintf(&b, "  - name: \"r%d\"\n    tuples:\n      - user: \"u:x\"\n        relation: \"r\"\n        object: \"o:x\"\n", i)
	}
	yaml := b.String()
	cfg, err := parseMapping([]byte(yaml))
	require.NoError(t, err)
	require.NoError(t, cfg.Validate(WithMaxRules(0)), "non-positive WithMaxRules must be a no-op")
	require.NoError(t, cfg.Validate(WithMaxRules(-1)), "negative WithMaxRules must be a no-op")
}

func TestParseMappingMalformedYAML(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - tuples:
    - this is: [malformed`)

	_, err := parseMapping(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid YAML")
}

// Test for Issue 6: Non-string key should error with "must be a string".
func TestParseVariablesNonStringKey(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    variables:
      123: "some expression"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := parseMapping(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a string")
}

// Test for Issue 6: Empty string key should NOT error during parseVariablesFromNode
// It should pass parsing and fail validation.
func TestParseVariablesEmptyStringKey(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    variables:
      "": "some expression"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping(input)
	require.NoError(t, err, "parsing should succeed for empty string key")

	// Validation should catch the empty name requirement
	err = config.Validate()
	require.Error(t, err)
	errors := allValidationErrors(err)
	require.Len(t, errors, 1)
	assert.Contains(t, errors[0].Message, "is required")
	assert.Contains(t, errors[0].Field, "name")
}

// Test for Issue 7: Non-string value should error with "must be a string".
func TestParseVariablesNonStringValue(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    variables:
      var_name:
        nested: "mapping"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := parseMapping(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a string")
}

// Test for Issue 7: Empty string value should NOT error during parseVariablesFromNode
// It should pass parsing and fail validation.
func TestParseVariablesEmptyStringValue(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    variables:
      var_name: ""
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping(input)
	require.NoError(t, err, "parsing should succeed for empty string value")

	// Validation should catch the empty expression requirement
	err = config.Validate()
	require.Error(t, err)
	errors := allValidationErrors(err)
	require.Len(t, errors, 1)
	assert.Contains(t, errors[0].Message, "is required")
	assert.Contains(t, errors[0].Field, "expression")
}

// Test for Issues 8 & 9: Position tracking for empty variable names
// Empty names should fail validation with proper position tracking.
func TestValidateEmptyVariableNamePosition(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "test rule"
    variables:
      "": "input.data.test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)

	err = config.Validate()
	require.Error(t, err)
	errors := allValidationErrors(err)

	// Should contain a name is required error with proper position
	var foundNameError bool
	for _, ve := range errors {
		if strings.Contains(ve.Field, "name") && strings.Contains(ve.Message, "is required") {
			foundNameError = true
			// Position should be set (even if it's the variables mapping itself)
			assert.True(t, ve.Position.StartLine > 0, "position should have line info")
			break
		}
	}
	assert.True(t, foundNameError, "should have a name is required error")
}

// ---------------------------------------------------------------------------
// Validation error tests (table-driven, themed)
// ---------------------------------------------------------------------------

func TestParseMappingTopLevelValidation(t *testing.T) {
	t.Parallel()
	var tooManyRulesYAML strings.Builder
	tooManyRulesYAML.WriteString("version: \"1\"\nrules:\n")
	for i := range 101 {
		fmt.Fprintf(&tooManyRulesYAML, "  - name: \"rule %d\"\n    tuples:\n      - user: \"u:x\"\n        relation: \"r\"\n        object: \"o:x\"\n", i)
	}

	tests := []struct {
		name            string
		yaml            string
		fieldContains   []string
		messageContains []string
	}{
		{
			name: "empty rules",
			yaml: `
version: "1"
rules: []`,
			fieldContains: []string{"rules"},
		},
		{
			name:            "too many rules",
			yaml:            tooManyRulesYAML.String(),
			fieldContains:   []string{"rules"},
			messageContains: []string{"100"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			var valErr *ValidationError
			require.True(t, errors.As(err, &valErr))
			for _, s := range tt.fieldContains {
				assert.Contains(t, valErr.Field, s)
			}
			for _, s := range tt.messageContains {
				assert.Contains(t, valErr.Message, s)
			}
		})
	}
}

func TestParseMappingNameValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		yaml            string
		fieldContains   []string
		messageContains []string
	}{
		{
			name: "missing rule name",
			yaml: `
version: "1"
rules:
  - tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			fieldContains:   []string{"rules[0].name"},
			messageContains: []string{"required"},
		},
		{
			name: "missing test case name",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - input:
      type: "test"
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			fieldContains:   []string{"tests[0].name"},
			messageContains: []string{"required"},
		},
		{
			name: "error path includes rule name",
			yaml: `
version: "1"
rules:
  - name: "Grant Access"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
        action: invalid`,
			fieldContains: []string{"Grant Access"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			var valErr *ValidationError
			require.True(t, errors.As(err, &valErr))
			for _, s := range tt.fieldContains {
				assert.Contains(t, valErr.Field, s)
			}
			for _, s := range tt.messageContains {
				assert.Contains(t, valErr.Message, s)
			}
		})
	}
}

func TestParseMappingTupleValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		yaml          string
		fieldContains []string
	}{
		{
			name: "empty tuples list",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    when: "true"
    tuples: []`,
			fieldContains: []string{"tuples"},
		},
		{
			name: "missing user",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - relation: "member"
        object: "org:test"`,
			fieldContains: []string{"user"},
		},
		{
			name: "missing relation",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "user:test"
        object: "org:test"`,
			fieldContains: []string{"relation"},
		},
		{
			name: "missing object",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "user:test"
        relation: "member"`,
			fieldContains: []string{"object"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			var valErr *ValidationError
			require.True(t, errors.As(err, &valErr))
			for _, s := range tt.fieldContains {
				assert.Contains(t, valErr.Field, s)
			}
		})
	}
}

func TestParseMappingVariableValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		yaml            string
		fieldContains   []string
		messageContains []string
		expectParseErr  bool
	}{
		{
			name: "empty expression",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      empty_var: ""
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			fieldContains:  []string{"expression"},
			expectParseErr: false, // now allowed through parsing; caught by Validate()
		},
		{
			// goccy's parser catches duplicate keys during YAML parsing, not in our custom logic
			name: "duplicate names",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      x: "input.a"
      x: "input.b"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			messageContains: []string{"x"},
			expectParseErr:  true, // goccy catches this during parsing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			if tt.expectParseErr {
				require.Error(t, err)
				for _, s := range tt.messageContains {
					assert.Contains(t, err.Error(), s)
				}
				return
			}

			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			var valErr *ValidationError
			require.True(t, errors.As(err, &valErr))
			for _, s := range tt.fieldContains {
				assert.Contains(t, valErr.Field, s)
			}
			for _, s := range tt.messageContains {
				assert.Contains(t, valErr.Message, s)
			}
		})
	}
}

func TestParseMappingVariablesEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		yaml        string
		shouldError bool
		expectLen   int
	}{
		{
			name: "null variables (explicit null)",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables: null
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			shouldError: false,
			expectLen:   0,
		},
		{
			name: "null variables (tilde)",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables: ~
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			shouldError: false,
			expectLen:   0,
		},
		{
			name: "empty variables mapping",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables: {}
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			shouldError: false,
			expectLen:   0,
		},
		{
			name: "omitted variables",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			shouldError: false,
			expectLen:   0,
		},
		{
			name: "single variable",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      x: "input.a"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			shouldError: false,
			expectLen:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()

			if tt.shouldError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, config.Rules[0].Variables, tt.expectLen)
			}
		})
	}
}

func TestParseMappingVariablesLargeSet(t *testing.T) {
	t.Parallel()
	// Test that order is preserved with many variables
	var yaml strings.Builder
	yaml.WriteString(`version: "1"
rules:
  - name: "test rule"
    variables:
`)
	for i := range 50 {
		fmt.Fprintf(&yaml, "      var_%d: \"expr_%d\"\n", i, i)
	}
	yaml.WriteString(`    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	config, err := parseMapping([]byte(yaml.String()))
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	vars := config.Rules[0].Variables
	require.Len(t, vars, 50)

	// Verify order is preserved
	for i := range 50 {
		assert.Equal(t, fmt.Sprintf("var_%d", i), vars[i].Name)
		assert.Equal(t, fmt.Sprintf("expr_%d", i), vars[i].Expression)
	}
}

func TestParseMappingVariablesSpecialCharacters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		yaml     string
		expectOK bool
	}{
		{
			// Names must be Expr identifiers; a space makes the name
			// unreferenceable via variables.<name>, so validation rejects it.
			name: "quoted key with spaces",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      "user email": "input.email"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			expectOK: false,
		},
		{
			// Dots and hyphens are not valid identifier characters.
			name: "quoted key with special chars",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      "user.email": "input.email"
      "org-id": "input.org"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			expectOK: false,
		},
		{
			name: "bare key with underscores and numbers",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      user_email_v2: "input.email"
      _private_var: "input.secret"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			expectOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			if tt.expectOK {
				require.NoError(t, err)
				assert.Greater(t, len(config.Rules[0].Variables), 0)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestParseVariablesFromNodeEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		yaml        string
		expectLen   int
		expectError bool
	}{
		{
			name: "null variables node",
			yaml: `version: "1"
rules:
  - name: "test rule"
    variables: null
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			expectLen:   0,
			expectError: false,
		},
		{
			name: "nil node returns empty",
			yaml: `version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			expectLen:   0,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, config.Rules[0].Variables, tt.expectLen)
			}
		})
	}
}

func TestExtractVariablesWithMissingVariablesNode(t *testing.T) {
	t.Parallel()
	// Test that extractVariables gracefully handles rules without a variables key
	yaml := []byte(`version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`)

	config, err := parseMapping(yaml)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	// Rule should have empty variables slice
	assert.Empty(t, config.Rules[0].Variables)
}

func TestExtractVariablesDefensiveCases(t *testing.T) {
	t.Parallel()
	// Test defensive code paths in extractVariables that shouldn't happen in practice
	// but need to be covered for completeness

	// Case 1: ruleIndex out of bounds
	// This would happen if extractVariablesForRules is called with wrong index
	root, err := parseMappingNodes([]byte(`version: "1"
rules:
  - name: "rule1"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`))
	require.NoError(t, err)

	// Extract with valid index should work
	vars, err := extractVariables(root, 0)
	assert.NoError(t, err)
	assert.Empty(t, vars)

	// Extract with out-of-bounds index returns nil (defensive case)
	vars, err = extractVariables(root, 999)
	assert.NoError(t, err)
	assert.Nil(t, vars)

	// Case 2: nil root (shouldn't happen but defensive)
	vars, err = extractVariables(nil, 0)
	assert.NoError(t, err)
	assert.Nil(t, vars)
}

func TestParseVariablesFromNodeNil(t *testing.T) {
	t.Parallel()
	// Test parseVariablesFromNode with nil node (defensive code path)
	vars, err := parseVariablesFromNode(nil)
	assert.NoError(t, err)
	assert.Nil(t, vars)
}

func TestParseMappingVariablesInvalidType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		yaml     string
		errMatch string
		expectAt string // "parse" or "validate"
	}{
		{
			name: "variables as array (sequence)",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      - "item1"
      - "item2"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			errMatch: "variables must be a mapping",
			expectAt: "parse",
		},
		{
			name: "variables as scalar string",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables: "not a mapping"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			errMatch: "variables must be a mapping",
			expectAt: "parse",
		},
		{
			name: "variable value is not a scalar (array)",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      var_name:
        - "array"
        - "value"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			errMatch: "variable expression must be a string",
			expectAt: "parse",
		},
		{
			name: "variable value is not a scalar (mapping)",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    variables:
      var_name:
        nested: value
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			errMatch: "variable expression must be a string",
			expectAt: "parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))

			if tt.expectAt == "parse" {
				// Error occurs during parseMapping, not validation
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMatch)
			} else {
				require.NoError(t, err)
				err = config.Validate()
				require.Error(t, err)
				var valErr *ValidationError
				require.True(t, errors.As(err, &valErr), "expected ValidationError, got %v", err)
				assert.Contains(t, valErr.Message, tt.errMatch)
			}
		})
	}
}

func TestParseMappingIteratorValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		yaml            string
		fieldContains   []string
		messageContains []string
	}{
		{
			name: "missing source",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    iterator:
      as: item
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"`,
			fieldContains: []string{"source"},
		},
		{
			name: "missing as",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    iterator:
      source: input.data.items
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"`,
			fieldContains: []string{"as"},
		},
		{
			name: "input reserved as iterator name",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    iterator:
      source: input.data.items
      as: input
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"`,
			messageContains: []string{`iterator "as" name "input" shadows the built-in input scope`},
		},
		{
			name: "variables reserved as iterator name",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    iterator:
      source: input.data.items
      as: variables
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"`,
			messageContains: []string{`iterator "as" name "variables" shadows the built-in variables scope`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			var valErr *ValidationError
			require.True(t, errors.As(err, &valErr))
			for _, s := range tt.fieldContains {
				assert.Contains(t, valErr.Field, s)
			}
			for _, s := range tt.messageContains {
				assert.Contains(t, valErr.Message, s)
			}
		})
	}
}

func TestParseMappingIteratorTuplesValidation(t *testing.T) {
	t.Parallel()
	t.Run("valid: iterator.tuples only, no rule-level tuples", func(t *testing.T) {
		yaml := `
version: "1"
rules:
  - name: "test rule"
    iterator:
      source: input.data.items
      as: item
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"`
		config, err := parseMapping([]byte(yaml))
		require.NoError(t, err)
		require.NoError(t, config.Validate())
	})

	tests := []struct {
		name           string
		yaml           string
		expectedErrors int
		fieldContains  string
	}{
		{
			name: "iterator present, iterator.tuples absent",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    iterator:
      source: input.data.items
      as: item
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			expectedErrors: 1,
			fieldContains:  "iterator.tuples",
		},
		{
			name: "iterator present, iterator.tuples empty",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    iterator:
      source: input.data.items
      as: item
    tuples: []`,
			expectedErrors: 1,
			fieldContains:  "iterator.tuples",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			valErrs := allValidationErrors(err)
			assert.Len(t, valErrs, tt.expectedErrors, "expected %d validation errors", tt.expectedErrors)

			found := false
			for _, ve := range valErrs {
				if strings.Contains(ve.Field, tt.fieldContains) {
					found = true
					break
				}
			}
			assert.True(t, found, "expected error field to contain %q", tt.fieldContains)
		})
	}
}

func TestParseMappingTestTupleMissingFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		yaml  string
		field string
	}{
		{
			name: "missing user",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "test 1"
    input:
      type: "test"
    expect_tuples:
      - relation: "r"
        object: "o:x"`,
			field: "user",
		},
		{
			name: "missing relation",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "test 1"
    input:
      type: "test"
    expect_tuples:
      - user: "u:x"
        object: "o:x"`,
			field: "relation",
		},
		{
			name: "missing object",
			yaml: `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "test 1"
    input:
      type: "test"
    expect_tuples:
      - user: "u:x"
        relation: "r"`,
			field: "object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseMapping([]byte(tt.yaml))
			require.NoError(t, err)
			err = config.Validate()
			require.Error(t, err)

			var valErr *ValidationError
			require.True(t, errors.As(err, &valErr))
			assert.Contains(t, valErr.Field, "expect_tuples")
			assert.Contains(t, valErr.Field, tt.field)
		})
	}
}

// ---------------------------------------------------------------------------
// Action validation tests (mixed success/error paths)
// ---------------------------------------------------------------------------

func TestParseMappingTupleAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		actionYAML     string
		expectError    bool
		expectedAction TupleAction
		errorMatch     string
	}{
		{
			name:           "default (omitted)",
			actionYAML:     "",
			expectedAction: ActionWrite,
		},
		{
			name:           "explicit write",
			actionYAML:     "\n        action: write",
			expectedAction: ActionWrite,
		},
		{
			name:           "delete",
			actionYAML:     "\n        action: delete",
			expectedAction: ActionDelete,
		},
		{
			name:        "invalid action",
			actionYAML:  "\n        action: upsert",
			expectError: true,
			errorMatch:  "upsert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := fmt.Sprintf(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "user:test"
        relation: "member"
        object: "org:test"%s
`, tt.actionYAML)

			config, err := parseMapping([]byte(yaml))
			require.NoError(t, err)
			err = config.Validate()
			if tt.expectError {
				require.Error(t, err)
				var valErr *ValidationError
				require.True(t, errors.As(err, &valErr))
				assert.Contains(t, valErr.Field, "action")
				assert.Contains(t, valErr.Message, tt.errorMatch)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedAction, config.Rules[0].Tuples[0].Action)
			}
		})
	}
}

func TestParseMappingTestBlockAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		actionYAML  string
		expectError bool
		expected    TupleAction
		errorMatch  string
	}{
		{
			name:       "default (omitted)",
			actionYAML: "",
			expected:   ActionWrite,
		},
		{
			name:       "explicit delete",
			actionYAML: "\n        action: delete",
			expected:   ActionDelete,
		},
		{
			name:        "invalid action",
			actionYAML:  "\n        action: upsert",
			expectError: true,
			errorMatch:  "upsert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := fmt.Sprintf(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "test 1"
    input:
      type: "test"
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"%s
`, tt.actionYAML)

			config, err := parseMapping([]byte(yaml))
			require.NoError(t, err)
			err = config.Validate()
			if tt.expectError {
				require.Error(t, err)
				var valErr *ValidationError
				require.True(t, errors.As(err, &valErr))
				assert.Contains(t, valErr.Message, tt.errorMatch)
			} else {
				require.NoError(t, err)
				require.Len(t, config.Tests[0].ExpectTuples, 1)
				assert.Equal(t, tt.expected, config.Tests[0].ExpectTuples[0].Action)
			}
		})
	}
}

func TestValidateRuleNamedRuleHasPosition(t *testing.T) {
	t.Parallel()
	// Regression test: validation errors for existing YAML nodes inside a named rule
	// must have a non-zero Position. Previously the named-rule display prefix
	// (rules["Grant Access"]) was passed to resolveNodePath, which returns nil for
	// quoted-key lookups on SequenceNodes, producing a zero position for all errors.
	// The fix uses numeric indices (rules[0]) for node path lookup.
	yaml := `version: "1"
rules:
  - name: "Grant Access"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
        action: invalid
`
	config, err := parseMapping([]byte(yaml))
	require.NoError(t, err)
	err = config.Validate()
	require.Error(t, err)

	valErrs := allValidationErrors(err)
	var actionErr *ValidationError
	for _, ve := range valErrs {
		if strings.Contains(ve.Field, "action") {
			actionErr = ve
			break
		}
	}
	require.NotNil(t, actionErr, "expected an invalid-action ValidationError")
	assert.Contains(t, actionErr.Field, "Grant Access", "Field must use named-rule prefix")
	assert.Greater(t, actionErr.Position.StartLine, 0, "Position must be non-zero: node exists in YAML")
}

// ---------------------------------------------------------------------------
// Multi-error tests
// ---------------------------------------------------------------------------

func TestParseMappingMultipleValidationErrors(t *testing.T) {
	t.Parallel()
	t.Run("two rules both missing names", func(t *testing.T) {
		yaml := `
version: "1"
rules:
  - tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
  - tuples:
      - user: "u:y"
        relation: "r"
        object: "o:y"
`
		config, err := parseMapping([]byte(yaml))
		require.NoError(t, err)
		err = config.Validate()
		require.Error(t, err)

		valErrs := allValidationErrors(err)
		require.Len(t, valErrs, 2)
		assert.Equal(t, "rules[0].name", valErrs[0].Field)
		assert.Equal(t, "rules[1].name", valErrs[1].Field)
	})

	t.Run("multiple missing fields in single tuple", func(t *testing.T) {
		yaml := `
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: ""
`
		config, err := parseMapping([]byte(yaml))
		require.NoError(t, err)
		err = config.Validate()
		require.Error(t, err)

		valErrs := allValidationErrors(err)
		// Expect: user required, relation required, object required
		require.Len(t, valErrs, 3)

		fields := make([]string, len(valErrs))
		for i, ve := range valErrs {
			fields[i] = ve.Field
		}
		assert.Contains(t, fields, `rules["test rule"].tuples[0].user`)
		assert.Contains(t, fields, `rules["test rule"].tuples[0].relation`)
		assert.Contains(t, fields, `rules["test rule"].tuples[0].object`)
	})

	t.Run("errors across rules and tuples", func(t *testing.T) {
		yaml := `
version: "1"
rules:
  - name: "rule A"
    tuples:
      - relation: "r"
        object: "o:x"
  - name: "rule B"
    tuples:
      - user: "u:x"
        object: "o:x"
`
		config, err := parseMapping([]byte(yaml))
		require.NoError(t, err)
		err = config.Validate()
		require.Error(t, err)

		valErrs := allValidationErrors(err)
		require.Len(t, valErrs, 2)
		assert.Contains(t, valErrs[0].Field, "rule A")
		assert.Contains(t, valErrs[0].Field, "user")
		assert.Contains(t, valErrs[1].Field, "rule B")
		assert.Contains(t, valErrs[1].Field, "relation")
	})
}

// ---------------------------------------------------------------------------
// Tuple filter validation tests
// ---------------------------------------------------------------------------

func TestValidateTupleFiltersEmptyList(t *testing.T) {
	t.Parallel()
	// tuple_filters key present but no entries should fail.
	rule := &Rule{
		Name:         "test",
		TupleFilters: []ParsedTupleFilter{},
	}
	errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "must contain at least one filter")
}

func TestValidateTupleFiltersMaxExceeded(t *testing.T) {
	t.Parallel()
	filters := make([]ParsedTupleFilter, 4)
	for i := range filters {
		filters[i] = ParsedTupleFilter{Object: "org:123"}
	}
	rule := &Rule{
		Name:         "test",
		TupleFilters: filters,
	}
	errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "exceeds maximum of 3 filters") {
			found = true
		}
	}
	assert.True(t, found, "expected max filter error")
}

func TestValidateTupleFiltersObjectRequired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		filter    ParsedTupleFilter
		wantError bool
	}{
		{name: "no fields set", filter: ParsedTupleFilter{}, wantError: true},
		{name: "user only", filter: ParsedTupleFilter{User: "user:{{ .input.id }}", Action: FilterActionDelete}, wantError: true},
		{name: "relation only", filter: ParsedTupleFilter{Relation: "member"}, wantError: true},
		{name: "object type prefix", filter: ParsedTupleFilter{Object: "org:"}, wantError: false},
		{name: "object with user", filter: ParsedTupleFilter{User: "user:alice", Object: "org:"}, wantError: false},
		{name: "templated object", filter: ParsedTupleFilter{Object: "org:{{ .variables.id }}"}, wantError: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &Rule{Name: "test", TupleFilters: []ParsedTupleFilter{tt.filter}}
			errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
			var found bool
			for _, e := range errs {
				if strings.Contains(e.Error(), "must be set to at least an object type prefix") {
					found = true
					assert.Contains(t, e.Error(), `rules["test"].tuple_filters[0].object`)
				}
			}
			assert.Equal(t, tt.wantError, found, "object-required error mismatch")
		})
	}
}

func TestValidateTupleFiltersActionValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		action    TupleFilterAction
		wantError bool
	}{
		{name: "empty defaults to patch", action: "", wantError: false},
		{name: "patch is valid", action: FilterActionPatch, wantError: false},
		{name: "delete is valid", action: FilterActionDelete, wantError: false},
		{name: "invalid action", action: "bogus", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &Rule{
				Name:         "test",
				TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: tt.action}},
			}
			errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
			if tt.wantError {
				var found bool
				for _, e := range errs {
					if strings.Contains(e.Error(), "invalid action") {
						found = true
					}
				}
				assert.True(t, found, "expected invalid action error")
			} else {
				// No action-related errors; the only possible error is the all-concrete check
				for _, e := range errs {
					assert.NotContains(t, e.Error(), "invalid action")
				}
			}
		})
	}
}

func TestValidateTupleFiltersDefaultsActionToPatch(t *testing.T) {
	t.Parallel()
	rule := &Rule{
		Name:         "test",
		TupleFilters: []ParsedTupleFilter{{Object: "org:123"}},
		Tuples:       []ParsedTuple{{User: "u:x", Relation: "r", Object: "o:y"}},
	}
	_ = validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
	assert.Equal(t, FilterActionPatch, rule.TupleFilters[0].Action)
}

func TestValidateTupleFiltersAllThreeConcreteRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		filter    ParsedTupleFilter
		wantError bool
	}{
		{
			name:      "all three concrete",
			filter:    ParsedTupleFilter{User: "user:alice", Relation: "member", Object: "org:123"},
			wantError: true,
		},
		{
			name:      "object type prefix is allowed",
			filter:    ParsedTupleFilter{User: "user:alice", Relation: "member", Object: "org:"},
			wantError: false,
		},
		{
			name:      "template in user is allowed",
			filter:    ParsedTupleFilter{User: "user:{{ .input.id }}", Relation: "member", Object: "org:123"},
			wantError: false,
		},
		{
			name:      "two concrete fields is allowed",
			filter:    ParsedTupleFilter{Relation: "member", Object: "org:123"},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := &Rule{
				Name:         "test",
				TupleFilters: []ParsedTupleFilter{tt.filter},
				Tuples:       []ParsedTuple{{User: "u:x", Relation: "r", Object: "o:y"}},
			}
			errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
			var found bool
			for _, e := range errs {
				if strings.Contains(e.Error(), "single tuple, not a range") {
					found = true
				}
			}
			if tt.wantError {
				assert.True(t, found, "expected all-concrete rejection error")
			} else {
				assert.False(t, found, "did not expect all-concrete rejection error")
			}
		})
	}
}

func TestValidateTupleFiltersDeleteOnlyWithTuples(t *testing.T) {
	t.Parallel()
	t.Run("rule-level tuples", func(t *testing.T) {
		rule := &Rule{
			Name:         "test",
			TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: FilterActionDelete}},
			Tuples:       []ParsedTuple{{User: "u:x", Relation: "r", Object: "o:y"}},
		}
		errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "delete filters remove everything matching") {
				found = true
			}
		}
		assert.True(t, found, "expected delete-only + tuples error")
	})

	t.Run("iterator tuples", func(t *testing.T) {
		rule := &Rule{
			Name:         "test",
			TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: FilterActionDelete}},
			Iterator:     &Iterator{Source: "input.items", As: "item", Tuples: []ParsedTuple{{User: "u:x", Relation: "r", Object: "o:y"}}},
		}
		errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "delete filters remove everything matching") {
				found = true
			}
		}
		assert.True(t, found, "expected delete-only + iterator tuples error")
	})

	t.Run("delete-only without tuples is valid", func(t *testing.T) {
		rule := &Rule{
			Name:         "test",
			TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: FilterActionDelete}},
		}
		errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
		for _, e := range errs {
			assert.NotContains(t, e.Error(), "delete filters remove everything matching")
		}
	})
}

func TestValidateTupleFiltersPatchWithTupleLevelDelete(t *testing.T) {
	t.Parallel()
	t.Run("rule-level tuple with delete action", func(t *testing.T) {
		rule := &Rule{
			Name:         "test",
			TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: FilterActionPatch}},
			Tuples: []ParsedTuple{
				{User: "u:x", Relation: "r", Object: "o:y", Action: ActionWrite},
				{User: "u:z", Relation: "r", Object: "o:y", Action: ActionDelete},
			},
		}
		errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "tuple-level \"delete\" action is not allowed") {
				found = true
			}
		}
		assert.True(t, found, "expected patch + tuple delete error")
	})

	t.Run("iterator tuple with delete action", func(t *testing.T) {
		rule := &Rule{
			Name:         "test",
			TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: FilterActionPatch}},
			Iterator: &Iterator{
				Source: "input.items",
				As:     "item",
				Tuples: []ParsedTuple{
					{User: "u:x", Relation: "r", Object: "o:y", Action: ActionDelete},
				},
			},
		}
		errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "tuple-level \"delete\" action is not allowed") {
				found = true
			}
		}
		assert.True(t, found, "expected patch + iterator tuple delete error")
	})

	t.Run("delete filter with tuple-level delete is allowed", func(t *testing.T) {
		rule := &Rule{
			Name:         "test",
			TupleFilters: []ParsedTupleFilter{{Object: "org:123", Action: FilterActionDelete}},
		}
		errs := validateTupleFilters(rule, `rules["test"]`, "rules[0]", nil)
		for _, e := range errs {
			assert.NotContains(t, e.Error(), "tuple-level \"delete\" action is not allowed")
		}
	})
}

func TestValidateTestCaseExpectTupleFilters(t *testing.T) {
	t.Parallel()
	t.Run("empty fields rejected", func(t *testing.T) {
		tc := &TestCase{
			Name:               "test",
			Input:              map[string]any{},
			ExpectTupleFilters: []TupleFilter{{}},
		}
		errs := validateTestCase(tc, 0, nil)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "must have at least one field set") {
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("invalid action rejected", func(t *testing.T) {
		tc := &TestCase{
			Name:               "test",
			Input:              map[string]any{},
			ExpectTupleFilters: []TupleFilter{{Object: "org:123", Action: "bogus"}},
		}
		errs := validateTestCase(tc, 0, nil)
		var found bool
		for _, e := range errs {
			if strings.Contains(e.Error(), "invalid action") {
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("defaults action to patch", func(t *testing.T) {
		tc := &TestCase{
			Name:               "test",
			Input:              map[string]any{},
			ExpectTupleFilters: []TupleFilter{{Object: "org:123"}},
		}
		_ = validateTestCase(tc, 0, nil)
		assert.Equal(t, FilterActionPatch, tc.ExpectTupleFilters[0].Action)
	})
}

// ---------------------------------------------------------------------------
// Version routing
// ---------------------------------------------------------------------------

func TestVersionRouting(t *testing.T) {
	t.Parallel()
	t.Run("routes to v1 parser", func(t *testing.T) {
		yamlStr := `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`
		config, err := parseMapping([]byte(yamlStr))
		require.NoError(t, err)
		assert.Equal(t, "1", config.Version)
	})

	t.Run("routes unquoted integer version to v1 parser", func(t *testing.T) {
		yamlStr := `
version: 1
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`
		config, err := parseMapping([]byte(yamlStr))
		require.NoError(t, err)
		assert.Equal(t, "1", config.Version)
	})

	t.Run("rejects unsupported version", func(t *testing.T) {
		yamlStr := `
version: "99"
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`
		_, err := parseMapping([]byte(yamlStr))
		require.Error(t, err)
		var valErr *ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "version", valErr.Field)
		assert.Contains(t, valErr.Message, "99")
	})

	t.Run("rejects non-string non-integer version", func(t *testing.T) {
		tests := []struct {
			name  string
			value string
		}{
			{name: "boolean", value: "true"},
			{name: "float", value: "1.0"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				yamlStr := fmt.Sprintf(`
version: %s
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`, tt.value)
				_, err := parseMapping([]byte(yamlStr))
				require.Error(t, err)
				var valErr *ValidationError
				require.ErrorAs(t, err, &valErr)
				assert.Equal(t, "version", valErr.Field)
				assert.Contains(t, valErr.Message, "must be a string")
				assert.Contains(t, valErr.Message, tt.value)
			})
		}
	})

	t.Run("rejects missing version", func(t *testing.T) {
		yamlStr := `
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`
		_, err := parseMapping([]byte(yamlStr))
		require.Error(t, err)
		var valErr *ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "version", valErr.Field)
		assert.Contains(t, valErr.Message, "required")
	})

	t.Run("rejects empty version string", func(t *testing.T) {
		yamlStr := `
version: ""
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`
		_, err := parseMapping([]byte(yamlStr))
		require.Error(t, err)
		var valErr *ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "version", valErr.Field)
	})
}

// ---------------------------------------------------------------------------
// Strict YAML parsing — reject unknown fields
// ---------------------------------------------------------------------------

func TestParsingRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		yaml         string
		unknownField string
	}{
		{
			name: "unknown top-level field",
			yaml: `
vversion: "1"
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			unknownField: "vversion",
		},
		{
			name: "unknown rule field",
			yaml: `
version: "1"
rules:
  - name: "test"
    conditon: "true"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			unknownField: "conditon",
		},
		{
			name: "unknown tuple field",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - usr: "u:x"
        user: "u:x"
        relation: "r"
        object: "o:x"`,
			unknownField: "usr",
		},
		{
			name: "unknown iterator field",
			yaml: `
version: "1"
rules:
  - name: "test"
    iterator:
      sorce: input.data.items
      source: input.data.items
      as: item
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"`,
			unknownField: "sorce",
		},
		{
			name: "unknown tuple_filter field",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuple_filters:
      - usr: "user:alice"
        relation: "member"
        object: "org:"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			unknownField: "usr",
		},
		{
			name: "unknown test case field",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - nme: "test 1"
    name: "test 1"
    input:
      type: "test"
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"`,
			unknownField: "nme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseMapping([]byte(tt.yaml))
			require.Error(t, err, "expected error for unknown field %q", tt.unknownField)

			var valErr *ValidationError
			require.ErrorAs(t, err, &valErr, "expected *ValidationError")
			assert.Contains(t, valErr.Message, tt.unknownField, "error message should mention the unknown field")
			assert.Equal(t, tt.unknownField, valErr.Field, "Field should be the unknown key name")
			assert.Greater(t, valErr.Position.StartLine, 0, "position should have non-zero line")
			assert.Equal(t, valErr.Position.StartColumn+len(tt.unknownField), valErr.Position.EndColumn, "EndColumn should span the key name")
		})
	}
}

func TestIsConcreteValue(t *testing.T) {
	t.Parallel()
	assert.True(t, isConcreteValue("user:alice"))
	assert.True(t, isConcreteValue("member"))
	assert.False(t, isConcreteValue(""))
	assert.False(t, isConcreteValue("user:{{ .input.id }}"))
	assert.False(t, isConcreteValue("{{ .input.user }}"))
}

func TestIsObjectTypePrefix(t *testing.T) {
	t.Parallel()
	assert.True(t, isObjectTypePrefix("org:"))
	assert.True(t, isObjectTypePrefix("x:"))
	assert.False(t, isObjectTypePrefix(""))
	assert.False(t, isObjectTypePrefix("org:123"))
	assert.False(t, isObjectTypePrefix("org"))
	assert.False(t, isObjectTypePrefix(":"))
}

func TestAllFiltersDelete(t *testing.T) {
	t.Parallel()
	assert.False(t, allFiltersDelete(nil))
	assert.False(t, allFiltersDelete([]ParsedTupleFilter{}))
	assert.True(t, allFiltersDelete([]ParsedTupleFilter{{Object: "o", Action: FilterActionDelete}}))
	assert.False(t, allFiltersDelete([]ParsedTupleFilter{{Object: "o", Action: FilterActionPatch}}))
	assert.False(t, allFiltersDelete([]ParsedTupleFilter{
		{Object: "o", Action: FilterActionDelete},
		{Object: "o", Action: FilterActionPatch},
	}))
}

// ---------------------------------------------------------------------------
// Rule-level action validation
// ---------------------------------------------------------------------------

func TestRuleLevelActionPropagation(t *testing.T) {
	t.Parallel()
	t.Run("rule action delete propagates to all tuples", func(t *testing.T) {
		yaml := []byte(`
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
        object: "o:w"`)
		config, err := parseMapping(yaml)
		require.NoError(t, err)
		err = config.Validate()
		require.NoError(t, err)
		assert.Equal(t, ActionDelete, config.Rules[0].Tuples[0].Action)
		assert.Equal(t, ActionDelete, config.Rules[0].Tuples[1].Action)
	})

	t.Run("rule action write propagates to all tuples", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "write all"
    action: "write"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:y"`)
		config, err := parseMapping(yaml)
		require.NoError(t, err)
		err = config.Validate()
		require.NoError(t, err)
		assert.Equal(t, ActionWrite, config.Rules[0].Tuples[0].Action)
	})

	t.Run("rule action delete propagates to iterator tuples", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "delete iter"
    action: "delete"
    iterator:
      source: "input.items"
      as: "item"
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:y"`)
		config, err := parseMapping(yaml)
		require.NoError(t, err)
		err = config.Validate()
		require.NoError(t, err)
		assert.Equal(t, ActionDelete, config.Rules[0].Iterator.Tuples[0].Action)
	})
}

// ---------------------------------------------------------------------------
// Tuple condition and context
// ---------------------------------------------------------------------------

func TestParseMappingTupleConditionAndContext(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "conditional tuple"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        condition: in_allowed_ip_range
        context:
          allowed_range: "{{ input.ip_address }}"
          device_type: "laptop"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	require.Len(t, config.Rules[0].Tuples, 1)
	tuple := config.Rules[0].Tuples[0]
	assert.Equal(t, "in_allowed_ip_range", tuple.Condition)
	require.Len(t, tuple.Context, 2)
	assert.Equal(t, "{{ input.ip_address }}", tuple.Context["allowed_range"])
	assert.Equal(t, "laptop", tuple.Context["device_type"])
}

func TestParseMappingTupleConditionWithoutContext(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "condition only"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        condition: is_active
`)

	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	tuple := config.Rules[0].Tuples[0]
	assert.Equal(t, "is_active", tuple.Condition)
	assert.Empty(t, tuple.Context)
}

func TestValidateContextWithoutCondition(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "missing condition"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        context:
          key: "value"
`)

	config, err := parseMapping(input)
	require.NoError(t, err)

	err = config.Validate()
	require.Error(t, err)

	verrs := allValidationErrors(err)
	require.Len(t, verrs, 1)
	assert.Contains(t, verrs[0].Message, "context requires a condition")
}

func TestValidateConditionOnDeleteTuple(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "delete with condition"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        action: delete
        condition: in_allowed_ip_range
`)

	config, err := parseMapping(input)
	require.NoError(t, err)

	err = config.Validate()
	require.Error(t, err)

	verrs := allValidationErrors(err)
	require.Len(t, verrs, 1)
	assert.Contains(t, verrs[0].Message, "condition is not allowed on delete tuples")
}

func TestValidateConditionOnRuleLevelDeleteAction(t *testing.T) {
	t.Parallel()
	input := []byte(`
version: "1"
rules:
  - name: "rule delete with condition"
    action: delete
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        condition: in_allowed_ip_range
`)

	config, err := parseMapping(input)
	require.NoError(t, err)

	err = config.Validate()
	require.Error(t, err)

	verrs := allValidationErrors(err)
	found := false
	for _, v := range verrs {
		if strings.Contains(v.Message, "condition is not allowed on delete tuples") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected validation error about condition on delete tuples, got: %v", verrs)
}

// ---------------------------------------------------------------------------
// Regression tests for review findings (multi-doc, test input, variable names)
// ---------------------------------------------------------------------------

func TestParseMappingRejectsMultipleDocuments(t *testing.T) {
	t.Parallel()
	input := []byte(`version: "1"
rules:
  - name: "first"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
---
version: "1"
rules:
  - name: "second"
    tuples:
      - user: "u:y"
        relation: "r"
        object: "o:y"
`)
	_, err := parseMapping(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple YAML documents")
}

func TestValidateTestCaseInputRequired(t *testing.T) {
	t.Parallel()
	input := []byte(`version: "1"
rules:
  - name: "r"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "missing input"
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)
	config, err := parseMapping(input)
	require.NoError(t, err)

	err = config.Validate()
	require.Error(t, err)
	verrs := allValidationErrors(err)
	var found bool
	for _, ve := range verrs {
		if strings.Contains(ve.Field, "tests[0].input") && strings.Contains(ve.Message, "is required") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected tests[0].input is required error, got: %v", verrs)
}

func TestValidateTestCaseEmptyInputAllowed(t *testing.T) {
	t.Parallel()
	input := []byte(`version: "1"
rules:
  - name: "r"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "empty input"
    input: {}
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)
	config, err := parseMapping(input)
	require.NoError(t, err)
	require.NoError(t, config.Validate())
}

func TestValidateVariableNameMustBeIdentifier(t *testing.T) {
	t.Parallel()
	input := []byte(`version: "1"
rules:
  - name: "r"
    variables:
      "user.email": "input.data.email"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)
	config, err := parseMapping(input)
	require.NoError(t, err)

	err = config.Validate()
	require.Error(t, err)
	verrs := allValidationErrors(err)
	var found bool
	for _, ve := range verrs {
		if strings.Contains(ve.Field, "variables") && strings.Contains(ve.Message, "invalid variable name") {
			found = true
			assert.True(t, ve.Position.StartLine > 0, "invalid name error should carry a position")
			break
		}
	}
	assert.True(t, found, "expected invalid variable name error, got: %v", verrs)
}
