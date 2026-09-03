package mapper

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper/language"
)

func TestDiagnosticsFromNilError(t *testing.T) {
	assert.Nil(t, DiagnosticsFrom(nil))
}

func TestDiagnosticsFromSingleError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantDiag Diagnostic
	}{
		{
			name: "ValidationError",
			err: &language.ValidationError{
				Field:    "rules[0].name",
				Message:  "is required",
				Position: language.Position{StartLine: 5, StartColumn: 3, EndLine: 5, EndColumn: 7},
			},
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "validation",
				Field:    "rules[0].name",
				Message:  "is required",
				Position: language.Position{StartLine: 5, StartColumn: 3, EndLine: 5, EndColumn: 7},
			},
		},
		{
			name: "EvalError",
			err:  &EvalError{Expression: "input.data.id", Err: fmt.Errorf("undefined: id")},
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "eval",
				Field:    "",
				Message:  `evaluation error in expression "input.data.id": undefined: id`,
			},
		},
		{
			name: "EvalError with rule name",
			err:  &EvalError{Expression: "input.data.id", RuleName: "My Rule", Err: fmt.Errorf("undefined: id")},
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "eval",
				Field:    "My Rule",
				Message:  `evaluation error in expression "input.data.id": undefined: id`,
			},
		},
		{
			name: "EvalError with field and position (compile-time interpolation)",
			err: &EvalError{
				Expression: `user:{{ input.id }`,
				Field:      "user",
				Position:   language.Position{StartLine: 5, StartColumn: 12, EndLine: 5, EndColumn: 30},
				Err:        fmt.Errorf("unclosed {{ in user field"),
			},
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "eval",
				Field:    "user",
				Message:  `evaluation error at 5:12 in expression "user:{{ input.id }": unclosed {{ in user field`,
				Position: language.Position{StartLine: 5, StartColumn: 12, EndLine: 5, EndColumn: 30},
			},
		},
		{
			name: "EvalError field takes priority over RuleName",
			err: &EvalError{
				Expression: `user:{{ input.id }`,
				RuleName:   "My Rule",
				Field:      "user",
				Position:   language.Position{StartLine: 3, StartColumn: 7},
				Err:        fmt.Errorf("unclosed {{"),
			},
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "eval",
				Field:    "user",
				Message:  `evaluation error at 3:7 in expression "user:{{ input.id }": unclosed {{`,
				Position: language.Position{StartLine: 3, StartColumn: 7},
			},
		},
		{
			name: "ConflictError",
			err:  &ConflictError{Conflict: Conflict{User: "u:1", Relation: "r", Object: "o:1"}},
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "conflict",
				Field:    "",
				Message:  "conflict: tuple (u:1, r, o:1) has both a write and a delete action",
			},
		},
		{
			name: "unknown error",
			err:  fmt.Errorf("some random error"),
			wantDiag: Diagnostic{
				Severity: SeverityError,
				Category: "unknown",
				Message:  "some random error",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diags := DiagnosticsFrom(tc.err)
			require.Len(t, diags, 1)
			assert.Equal(t, tc.wantDiag.Severity, diags[0].Severity)
			assert.Equal(t, tc.wantDiag.Category, diags[0].Category)
			assert.Equal(t, tc.wantDiag.Field, diags[0].Field)
			assert.Equal(t, tc.wantDiag.Message, diags[0].Message)
			assert.Equal(t, tc.wantDiag.Position, diags[0].Position)
		})
	}
}

func TestDiagnosticsFromJoinedErrors(t *testing.T) {
	t.Parallel()

	joined := errors.Join(
		&language.ValidationError{Field: "version", Message: "is required"},
		&language.ValidationError{Field: "rules[0].name", Message: "is required"},
	)

	diags := DiagnosticsFrom(joined)
	require.Len(t, diags, 2)
	assert.Equal(t, CategoryValidation, diags[0].Category)
	assert.Equal(t, CategoryValidation, diags[1].Category)
}

func TestDiagnosticsFromWrappedError(t *testing.T) {
	t.Parallel()

	inner := &EvalError{Expression: "x", Err: fmt.Errorf("fail")}
	wrapped := fmt.Errorf("rule %q variables: %w", "test rule", inner)

	diags := DiagnosticsFrom(wrapped)
	require.Len(t, diags, 1)
	assert.Equal(t, CategoryEval, diags[0].Category)
}

func TestDiagnosticsStringEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", Diagnostics(nil).String())
	assert.Equal(t, "", Diagnostics{}.String())
}

func TestDiagnosticsStringOutput(t *testing.T) {
	t.Parallel()

	diags := Diagnostics{
		{Severity: SeverityError, Category: "validation", Field: "version", Message: "is required"},
		{Severity: SeverityError, Category: "eval", Field: "user", Message: "unclosed action", Position: language.Position{StartLine: 8, StartColumn: 5, EndLine: 8, EndColumn: 20}},
		{Severity: SeverityError, Category: "eval", Message: "undefined: id"},
	}

	want := "3 errors found:\n" +
		"\nvalidation (1):\n" +
		"  version: is required\n" +
		"\neval (2):\n" +
		"  8:5-8:20: user: unclosed action\n" +
		"  undefined: id\n"

	assert.Equal(t, want, diags.String())
}

func TestDiagnosticsStringUnknownCategory(t *testing.T) {
	t.Parallel()

	diags := Diagnostics{
		{Severity: SeverityError, Category: "validation", Message: "bad field"},
		{Severity: SeverityError, Category: "custom", Message: "a custom error"},
	}

	got := diags.String()
	assert.Contains(t, got, "2 errors found:")
	assert.Contains(t, got, "\nvalidation (1):\n")
	assert.Contains(t, got, "\ncustom (1):\n")
	assert.Contains(t, got, "a custom error")
}

func TestSeverityJSONRoundTrip(t *testing.T) {
	t.Parallel()

	diag := Diagnostic{
		Severity: SeverityError,
		Category: "validation",
		Message:  "is required",
	}

	data, err := json.Marshal(diag)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"severity":"error"`)

	var got Diagnostic
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, diag.Severity, got.Severity)
}

func TestParseYAMLErrorPosition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msg     string
		wantPos language.Position
		wantOK  bool
	}{
		{
			name:    "YAML parse error with line and column",
			msg:     `[1:6] could not find end character of double-quoted text`,
			wantPos: language.Position{StartLine: 1, StartColumn: 6},
			wantOK:  true,
		},
		{
			name:    "YAML parse error multiline position",
			msg:     `[3:12] found unexpected ':'`,
			wantPos: language.Position{StartLine: 3, StartColumn: 12},
			wantOK:  true,
		},
		{
			name:   "non-yaml error",
			msg:    "some random error",
			wantOK: false,
		},
		{
			name:   "empty string",
			msg:    "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pos, ok := parseYAMLErrorPosition(tc.msg)
			assert.Equal(t, tc.wantOK, ok)
			if ok {
				assert.Equal(t, tc.wantPos, pos)
			}
		})
	}
}

func TestDiagnosticsFromYAMLParseError(t *testing.T) {
	t.Parallel()

	// Malformed YAML that causes a parse error — should produce a validation
	// diagnostic with a position extracted from the YAML error message.
	yamlData := []byte("key: \"unterminated\nother: val")

	compiler := NewCompiler()
	_, err := compiler.Compile(yamlData)
	require.Error(t, err)

	diags := DiagnosticsFrom(err)
	require.NotEmpty(t, diags)
	assert.Equal(t, CategoryValidation, diags[0].Category)
	assert.Greater(t, diags[0].Position.StartLine, 0, "should have a non-zero start line")
}

func TestDiagnosticsEndToEndCompile(t *testing.T) {
	t.Parallel()

	yaml := []byte(`
version: "1"
rules:
  - tuples:
      - user: "user:{{ input.id }"
        relation: "member"
        object: "org:test"
`)

	compiler := NewCompiler()
	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	diags := DiagnosticsFrom(err)
	require.NotEmpty(t, diags)

	var hasValidation, hasEval bool
	for _, d := range diags {
		switch d.Category {
		case CategoryValidation:
			hasValidation = true
		case CategoryEval:
			hasEval = true
		}
	}
	assert.True(t, hasValidation, "expected at least one validation diagnostic")
	assert.True(t, hasEval, "expected at least one eval diagnostic")
}

func TestDiagnosticsInterpErrorCarriesFieldAndPosition(t *testing.T) {
	t.Parallel()

	// Compile a mapping with an unclosed interpolation on a known YAML line.
	// The resulting EvalError should have Field="user" and a non-zero Position,
	// and DiagnosticsFrom should surface both on the Diagnostic.
	yaml := []byte(`
version: "1"
rules:
  - name: "bad interp"
    tuples:
      - user: "user:{{ input.id }"
        relation: "member"
        object: "org:test"
`)

	compiler := NewCompiler()
	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	// The raw error must be an *EvalError with Field and Position set.
	var ee *EvalError
	require.True(t, errors.As(err, &ee), "expected *EvalError")
	assert.Equal(t, "user", ee.Field)
	assert.Greater(t, ee.Position.StartLine, 0, "Position.StartLine must be non-zero")

	// DiagnosticsFrom must surface Field and Position on the Diagnostic.
	diags := DiagnosticsFrom(err)
	require.Len(t, diags, 1)
	assert.Equal(t, CategoryEval, diags[0].Category)
	assert.Equal(t, "user", diags[0].Field)
	assert.Greater(t, diags[0].Position.StartLine, 0, "diagnostic Position.StartLine must be non-zero")
}
