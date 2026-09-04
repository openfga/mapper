package mapper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper/language"
)

func TestRecordRuleErrorStampsRuleName(t *testing.T) {
	t.Parallel()
	mapping := &Mapping{trace: false}
	result := &Result{}
	start := time.Now()

	t.Run("direct EvalError", func(t *testing.T) {
		err := &EvalError{Expression: "x", Err: errors.New("fail")}
		mapping.recordRuleError(result, "my rule", err, start)
		assert.Equal(t, "my rule", err.RuleName)
	})

	t.Run("wrapped EvalError", func(t *testing.T) {
		inner := &EvalError{Expression: "x", Err: errors.New("fail")}
		wrapped := fmt.Errorf("variables: %w", inner)
		mapping.recordRuleError(result, "my rule", wrapped, start)
		assert.Equal(t, "my rule", inner.RuleName)
	})

	t.Run("no EvalError is a no-op", func(t *testing.T) {
		err := errors.New("plain error")
		require.NotPanics(t, func() { mapping.recordRuleError(result, "my rule", err, start) })
	})
}

func TestRecordRuleErrorRecordsTrace(t *testing.T) {
	t.Parallel()
	mapping := &Mapping{trace: true}
	result := &Result{Trace: &Trace{Rules: make([]RuleTrace, 0)}}
	start := time.Now()

	err := &EvalError{Expression: "x", Err: errors.New("fail")}
	mapping.recordRuleError(result, "my rule", err, start)

	require.Len(t, result.Trace.Rules, 1)
	assert.Equal(t, "my rule", result.Trace.Rules[0].Name)
	assert.Equal(t, RuleErrored, result.Trace.Rules[0].Status)
	assert.Equal(t, err, result.Trace.Rules[0].Error)
	assert.NotZero(t, result.Trace.Duration)
}

func TestNewCompilerDefaults(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	assert.Equal(t, DefaultTimeout, compiler.timeout)
	assert.Equal(t, DefaultMaxTuples, compiler.maxTuples)
	assert.False(t, compiler.trace)
}

func TestNewCompilerWithOptions(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(
		WithTimeout(50*time.Millisecond),
		WithMaxTuples(100),
		WithTrace(true),
	)
	assert.Equal(t, 50*time.Millisecond, compiler.timeout)
	assert.Equal(t, 100, compiler.maxTuples)
	assert.True(t, compiler.trace)
}

func TestNewCompilerOptionValidation(t *testing.T) {
	t.Parallel()
	minimalYAML := []byte("version: \"1\"\nrules: []\n")

	tests := []struct {
		name  string
		build func() *Compiler
	}{
		{name: "zero timeout", build: func() *Compiler { return NewCompiler(WithTimeout(0)) }},
		{name: "negative timeout", build: func() *Compiler { return NewCompiler(WithTimeout(-1 * time.Millisecond)) }},
		{name: "zero max tuples", build: func() *Compiler { return NewCompiler(WithMaxTuples(0)) }},
		{name: "negative max tuples", build: func() *Compiler { return NewCompiler(WithMaxTuples(-1)) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.build().Compile(minimalYAML)
			assert.Error(t, err)
		})
	}
}

func TestCompileFacade(t *testing.T) {
	t.Parallel()
	validYAML := []byte(`version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "user:{{ input.id }}"
        relation: "member"
        object: "org:acme"
`)

	t.Run("compiles with options", func(t *testing.T) {
		t.Parallel()
		m, err := Compile(validYAML, WithTimeout(50*time.Millisecond))
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, 50*time.Millisecond, m.timeout)
	})

	t.Run("surfaces option validation error", func(t *testing.T) {
		t.Parallel()
		_, err := Compile(validYAML, WithMaxTuples(0))
		assert.Error(t, err)
	})
}

func TestCompile(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	assert.Equal(t, DefaultTimeout, mapping.timeout)
	assert.Equal(t, DefaultMaxTuples, mapping.maxTuples)
}

func TestCompileInvalidYAML(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - tuples:
    - this is: [malformed
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid YAML")
}

func TestCompileFile(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	// Test with nonexistent file
	_, err := compiler.CompileFile("/nonexistent/path/mapping.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading mapping file")
}

func TestEvaluateReturnsResult(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:x", result.Tuples[0].User)
	assert.Equal(t, "r", result.Tuples[0].Relation)
	assert.Equal(t, "o:x", result.Tuples[0].Object)
	assert.Equal(t, language.ActionWrite, result.Tuples[0].Action)
}

func TestEvaluatePipelineWhenGuardEmpty(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "no when guard rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:x", result.Tuples[0].User)
	require.NotNil(t, result.Trace)
	assert.Equal(t, 1, len(result.Trace.Rules))
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
}

func TestEvaluatePipelineWhenGuardTrue(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "always match"
    when: "true"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:x", result.Tuples[0].User)
	require.NotNil(t, result.Trace)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
}

func TestEvaluatePipelineWhenGuardFalse(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "never match"
    when: "false"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(result.Tuples))
	require.NotNil(t, result.Trace)
	assert.Equal(t, RuleSkipped, result.Trace.Rules[0].Status)
}

func TestEvaluatePipelineWhenGuardWithInput(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "user created rule"
    when: "input.type == \"user.created\""
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Should match
	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"type": "user.created",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)

	// Should not match
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"type": "user.updated",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleSkipped, result.Trace.Rules[0].Status)
}

func TestEvaluatePipelineWhenGuardWithVariables(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "domain check"
    variables:
      domain: "json_path(input.email_data, \"domain\")"
    when: "input.email_data != nil"
    tuples:
      - user: "u:{{ variables.domain }}"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Should match
	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"email_data": map[string]any{
			"domain": "example.com",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)

	// Should not match
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"other_data": map[string]any{
			"domain": "other.com",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, RuleSkipped, result.Trace.Rules[0].Status)
}

func TestEvaluatePipelineWhenGuardComplex(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "complex when guard"
    when: "input.active && (input.type == \"admin\" || input.type == \"moderator\")"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Should match: active and admin
	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"active": true,
		"type":   "admin",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)

	// Should match: active and moderator
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"active": true,
		"type":   "moderator",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)

	// Should not match: active but wrong type
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"active": true,
		"type":   "user",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleSkipped, result.Trace.Rules[0].Status)

	// Should not match: not active
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"active": false,
		"type":   "admin",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleSkipped, result.Trace.Rules[0].Status)
}

func TestCompileInvalidWhenGuardFails(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "bad when guard"
    when: "invalid syntax here ]["
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
}

func TestCompileWhenGuardErrorCarriesContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "my rule"
    when: "invalid syntax ]["
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr))
	assert.Equal(t, "my rule", evalErr.RuleName)
	assert.Equal(t, "when", evalErr.Field)
	assert.Greater(t, evalErr.Position.StartLine, 0, "when error should have a source position")
}

func TestCompileVariableErrorCarriesContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "my rule"
    variables:
      bad_var: "invalid syntax ]["
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr))
	assert.Equal(t, "my rule", evalErr.RuleName)
	assert.Contains(t, evalErr.Field, "bad_var")
}

func TestCompileForwardRefErrorCarriesContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "my rule"
    variables:
      a: "variables.b"
      b: "\"value\""
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr), "expected *EvalError for forward reference")
	assert.Equal(t, "my rule", evalErr.RuleName)
	assert.Contains(t, evalErr.Field, "variables.a", "Field should identify the offending variable")
	assert.Greater(t, evalErr.Position.StartLine, 0, "forward-ref error should carry a source position")
}

func TestCompileForwardRefAndSyntaxErrorsSurfacedTogether(t *testing.T) {
	t.Parallel()
	// A forward-ref error and a syntax error in the same variables block must both
	// be reported in a single Compile() call (no short-circuit).
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "my rule"
    variables:
      a: "variables.b"
      b: "invalid syntax ]["
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	diags := DiagnosticsFrom(err)
	assert.GreaterOrEqual(t, len(diags), 2, "both forward-ref and syntax errors must be reported")

	var hasForwardRef, hasSyntax bool
	for _, d := range diags {
		if strings.Contains(d.Message, "forward") || strings.Contains(d.Message, "defined later") {
			hasForwardRef = true
		}
		if strings.Contains(d.Message, "syntax") || strings.Contains(d.Message, "unexpected") {
			hasSyntax = true
		}
	}
	assert.True(t, hasForwardRef, "forward-ref diagnostic expected")
	assert.True(t, hasSyntax, "syntax error diagnostic expected")
}

func TestCompileIteratorSourceErrorCarriesContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "my rule"
    iterator:
      source: "invalid syntax ]["
      as: item
      tuples:
        - user: "u:x"
          relation: "r"
          object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr))
	assert.Equal(t, "my rule", evalErr.RuleName)
	assert.Equal(t, "iterator.source", evalErr.Field)
	assert.Greater(t, evalErr.Position.StartLine, 0, "iterator.source error should have a source position")
}

func TestCompileTupleWhenGuardErrorCarriesContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "my rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
        when: "invalid syntax ]["
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)

	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr))
	assert.Equal(t, "my rule", evalErr.RuleName)
	assert.Contains(t, evalErr.Field, "when")
	assert.Greater(t, evalErr.Position.StartLine, 0, "tuple when error should have a source position")
}

func TestEvaluateVariablesInPipeline(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "variable test"
    variables:
      email_lower: "lower(input.email)"
      domain: "trim(variables.email_lower)" # This won't extract domain but verifies variable chaining
    when: "input.email != \"\""
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"email": "USER@EXAMPLE.COM",
	})
	require.NoError(t, err)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
}

func TestEvaluateWhenGuardBeforeVariables(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "user rule"
    when: "input.type == \"user.created\""
    variables:
      user_id: "input.data.user.id"
    tuples:
      - user: "user:{{ variables.user_id }}"
        relation: "member"
        object: "org:default"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Input does not match condition and is missing data.user.id.
	// Because condition is evaluated first, the rule should be skipped
	// without ever evaluating the variables block.
	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"type": "org.created",
	})
	require.NoError(t, err, "non-matching rule should not error even when variable paths are missing")
	assert.Empty(t, result.Tuples)
	assert.Equal(t, RuleSkipped, result.Trace.Rules[0].Status)
}

func TestCompileRejectsVariablesInRuleWhenGuard(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "bad when guard"
    variables:
      domain: "input.domain"
    when: "variables.domain == \"example.com\""
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)
	var valErr *language.ValidationError
	require.True(t, errors.As(err, &valErr), "expected ValidationError, got %T: %v", err, err)
	assert.Contains(t, valErr.Error(), "variables")
	assert.Contains(t, valErr.Error(), "rule-level when guards")
}

func TestEvaluateMultipleRules(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    when: "input.type == \"a\""
    tuples:
      - user: "u:1"
        relation: "r"
        object: "o:1"
  - name: "rule2"
    when: "input.type == \"b\""
    tuples:
      - user: "u:2"
        relation: "r"
        object: "o:2"
  - name: "rule3"
    when: "true"
    tuples:
      - user: "u:3"
        relation: "r"
        object: "o:3"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"type": "a",
	})
	require.NoError(t, err)

	// rule1 and rule3 match, rule2 skipped → 2 tuples
	require.Equal(t, 2, len(result.Tuples))
	assert.Equal(t, "u:1", result.Tuples[0].User)
	assert.Equal(t, "u:3", result.Tuples[1].User)

	assert.Equal(t, 3, len(result.Trace.Rules))
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status) // rule1 matches
	assert.Equal(t, RuleSkipped, result.Trace.Rules[1].Status) // rule2 skipped
	assert.Equal(t, RuleMatched, result.Trace.Rules[2].Status) // rule3 matches
}

func TestEvaluateTimeout(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTimeout(1 * time.Millisecond))

	yaml := []byte(`
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to simulate timeout

	_, err = mapping.Evaluate(ctx, map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestEvaluateTraceEnabled(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "test rule"
    when: "true"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)

	require.Equal(t, 1, len(result.Tuples))
	require.NotNil(t, result.Trace)
	assert.Equal(t, 1, len(result.Trace.Rules))
	assert.Equal(t, "test rule", result.Trace.Rules[0].Name)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.GreaterOrEqual(t, result.Trace.Duration, time.Duration(0))
}

func TestEvaluateTraceDisabled(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(false))

	yaml := []byte(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)

	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:x", result.Tuples[0].User)
	assert.Nil(t, result.Trace)
}

func TestEvaluateNilEvent(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "test rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	_, err = mapping.Evaluate(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event must not be nil")
}

func TestCompileInvalidVariableSyntaxFails(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "rule with bad variable"
    variables:
      bad: "invalid syntax ]["
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`)

	_, err := compiler.Compile(yaml)
	require.Error(t, err)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
}

// Interpolation rendering tests

func TestCompileInterpValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		yaml        string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid literal fields",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:alice"
        relation: "r"
        object: "o:x"
`,
		},
		{
			name: "valid expr interpolation",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:{{ input.id }}"
        relation: "r"
        object: "o:x"
`,
		},
		{
			name: "unclosed interpolation in user",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:{{ bad"
        relation: "r"
        object: "o:x"
`,
			wantErr:     true,
			errContains: "unclosed",
		},
		{
			name: "unclosed interpolation in relation",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:ok"
        relation: "{{ bad"
        object: "o:x"
`,
			wantErr:     true,
			errContains: "relation",
		},
		{
			name: "unclosed interpolation in object",
			yaml: `
version: "1"
rules:
  - name: "test"
    tuples:
      - user: "u:ok"
        relation: "r"
        object: "{{ bad"
`,
			wantErr:     true,
			errContains: "object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiler := NewCompiler()

			_, err := compiler.Compile([]byte(tt.yaml))
			if tt.wantErr {
				require.Error(t, err)
				var ee *EvalError
				assert.True(t, errors.As(err, &ee))
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRenderTupleLiterals(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "literal rule"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:alice", result.Tuples[0].User)
	assert.Equal(t, "member", result.Tuples[0].Relation)
	assert.Equal(t, "o:team1", result.Tuples[0].Object)
	assert.Equal(t, language.ActionWrite, result.Tuples[0].Action)
}

func TestRenderTupleWithVariableInterpolation(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "template rule"
    variables:
      user_id: "input.id"
    tuples:
      - user: "u:{{variables.user_id }}"
        relation: "member"
        object: "o:{{input.team }}"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"id":   "alice",
		"team": "team1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:alice", result.Tuples[0].User)
	assert.Equal(t, "member", result.Tuples[0].Relation)
	assert.Equal(t, "o:team1", result.Tuples[0].Object)
}

func TestRenderTupleActionExplicitDelete(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "delete rule"
    tuples:
      - user: "u:bob"
        relation: "member"
        object: "o:team2"
        action: "delete"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, language.ActionDelete, result.Tuples[0].Action)
}

func TestRenderTupleActionDefault(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "default action rule"
    tuples:
      - user: "u:charlie"
        relation: "member"
        object: "o:team3"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, language.ActionWrite, result.Tuples[0].Action)
}

func TestRenderTupleWhenGuardTrue(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "tuple when guard true"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        when: "input.active"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"active": true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
}

func TestRenderTupleWhenGuardFalse(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "tuple when guard false"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        when: "input.active"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"active": false,
	})
	require.NoError(t, err)
	require.Equal(t, 0, len(result.Tuples))
}

func TestRenderTupleWhenGuardWithVariables(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "tuple when guard with variables"
    variables:
      is_admin: "input.role == \"admin\""
    tuples:
      - user: "u:{{input.id }}"
        relation: "admin"
        object: "o:org"
        when: "variables.is_admin"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Should emit tuple when admin
	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"id":   "alice",
		"role": "admin",
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "u:alice", result.Tuples[0].User)

	// Should not emit tuple when not admin
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"id":   "bob",
		"role": "user",
	})
	require.NoError(t, err)
	require.Equal(t, 0, len(result.Tuples))
}

func TestRenderMultipleTuplesPerRule(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "multi tuple rule"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:alice"
        relation: "owner"
        object: "o:project1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 2, len(result.Tuples))
	assert.Equal(t, "u:alice", result.Tuples[0].User)
	assert.Equal(t, "member", result.Tuples[0].Relation)
	assert.Equal(t, "u:alice", result.Tuples[1].User)
	assert.Equal(t, "owner", result.Tuples[1].Relation)
}

func TestRenderMultipleRulesTuples(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    when: "input.type == \"user.created\""
    tuples:
      - user: "u:{{input.id }}"
        relation: "member"
        object: "o:default"
  - name: "rule2"
    when: "input.type == \"admin.promoted\""
    tuples:
      - user: "u:{{input.id }}"
        relation: "admin"
        object: "o:system"
      - user: "u:{{input.id }}"
        relation: "moderator"
        object: "o:forums"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Trigger rule1
	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"type": "user.created",
		"id":   "alice",
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Tuples))

	// Trigger rule2
	result, err = mapping.Evaluate(context.Background(), map[string]any{
		"type": "admin.promoted",
		"id":   "bob",
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(result.Tuples))
	assert.Equal(t, "u:bob", result.Tuples[0].User)
	assert.Equal(t, "admin", result.Tuples[0].Relation)
}

func TestRenderTupleNilFieldError(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "nil field rule"
    tuples:
      - user: "u:{{ input.missing_field }}"
        relation: "member"
        object: "o:team"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	_, err = mapping.Evaluate(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evaluated to nil")
}

func TestRenderInterpRunExprErrorNotDoubleWrapped(t *testing.T) {
	t.Parallel()
	// When a segment expression fails at runtime, the error from runExpr is
	// already an *EvalError. renderInterp must NOT re-wrap it in another *EvalError
	// (which would produce a nested "evaluation error ... evaluation error ..." message).
	// Instead it wraps with fmt.Errorf so the inner *EvalError is reachable via errors.As.
	//
	// lower() expects a string; passing an integer triggers a runtime type error.
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "bad expr"
    tuples:
      - user: "u:{{ lower(input.count) }}"
        relation: "member"
        object: "o:team"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	_, err = mapping.Evaluate(t.Context(), map[string]any{"count": 42})
	require.Error(t, err)

	// The inner *EvalError must be reachable.
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr), "inner *EvalError must be reachable via errors.As")

	// The error message should contain "user: " field context added by renderInterp,
	// and the inner expression (the segment code), not a re-wrapped "evaluation error in
	// expression \"u:{{ lower(input.count) }}\"" using the full ci.raw.
	msg := err.Error()
	assert.True(t, strings.Contains(msg, "user: "), "field context must appear in error message")
	assert.False(t, strings.Contains(msg, `"u:{{ lower(input.count) }}"`), "ci.raw must not appear as outer expression")
}

func TestTraceEmittedN(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    when: "true"
    tuples:
      - user: "u:a"
        relation: "r1"
        object: "o:1"
      - user: "u:b"
        relation: "r2"
        object: "o:2"
  - name: "rule2"
    when: "false"
    tuples:
      - user: "u:c"
        relation: "r3"
        object: "o:3"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, result.Trace)
	require.Equal(t, 2, len(result.Trace.Rules))

	// Rule1 matched and emitted 2 tuples
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.Equal(t, 2, result.Trace.Rules[0].EmittedN)

	// Rule2 skipped
	assert.Equal(t, RuleSkipped, result.Trace.Rules[1].Status)
	assert.Equal(t, 0, result.Trace.Rules[1].EmittedN)
}

func TestTraceEmittedNWithTupleWhenGuards(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "mixed when guards"
    when: "true"
    tuples:
      - user: "u:a"
        relation: "r1"
        object: "o:1"
        when: "true"
      - user: "u:b"
        relation: "r2"
        object: "o:2"
        when: "false"
      - user: "u:c"
        relation: "r3"
        object: "o:3"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, result.Trace)
	require.Equal(t, 1, len(result.Trace.Rules))

	// Rule matched, but only 2 tuples emitted (first and third; second's when guard was false)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.Equal(t, 2, result.Trace.Rules[0].EmittedN)
	assert.Equal(t, 2, len(result.Tuples))
}

func TestMaxTuplesEnforcement(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithMaxTuples(3))

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    tuples:
      - user: "u:1"
        relation: "r"
        object: "o:1"
      - user: "u:2"
        relation: "r"
        object: "o:2"
  - name: "rule2"
    tuples:
      - user: "u:3"
        relation: "r"
        object: "o:3"
      - user: "u:4"
        relation: "r"
        object: "o:4"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	_, err = mapping.Evaluate(context.Background(), map[string]any{})
	require.Error(t, err)
	var ee *EvalError
	require.True(t, errors.As(err, &ee))
	assert.Equal(t, "maxTuples", ee.Expression)
	assert.Contains(t, err.Error(), "exceeding limit")
}

// Post-processing integration tests

func TestEvaluateDedupIdenticalTuples(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	// Two rules that both emit the same tuple
	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
  - name: "rule2"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Tuples))
	assert.Equal(t, "user:alice", result.Tuples[0].User)
}

func TestEvaluateConflictWriteDelete(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	// Rule 1 writes a tuple; Rule 2 deletes the same (user, relation, object)
	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
        action: "write"
  - name: "rule2"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
        action: "delete"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.Error(t, err)
	var ce *ConflictError
	require.True(t, errors.As(err, &ce), "expected *ConflictError, got %T: %v", err, err)
	assert.Equal(t, "user:alice", ce.User)
	assert.Equal(t, "member", ce.Relation)
	assert.Equal(t, "org:1", ce.Object)
	assert.NotNil(t, result)
}

func TestEvaluateDedupBelowMaxTuples(t *testing.T) {
	t.Parallel()
	// maxTuples=2; two rules each emit the same tuple → dedup yields 1 tuple → within limit
	compiler := NewCompiler(WithMaxTuples(2))

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
      - user: "user:alice"
        relation: "member"
        object: "org:1"
      - user: "user:alice"
        relation: "member"
        object: "org:1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Without dedup: 3 tuples > limit of 2 → would fail.
	// With dedup: 1 unique tuple ≤ limit of 2 → succeeds.
	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Tuples))
}

func TestEvaluateTraceEmittedNUnaffectedByDedup(t *testing.T) {
	t.Parallel()
	// Each rule's EmittedN reflects pre-dedup per-rule count.
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    when: "true"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
      - user: "user:alice"
        relation: "member"
        object: "org:1"
  - name: "rule2"
    when: "true"
    tuples:
      - user: "user:alice"
        relation: "member"
        object: "org:1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)

	// Post-dedup global result: 1 unique tuple
	assert.Equal(t, 1, len(result.Tuples))

	// Per-rule trace counts reflect what each rule produced before global dedup
	require.Equal(t, 2, len(result.Trace.Rules))
	assert.Equal(t, 2, result.Trace.Rules[0].EmittedN) // rule1 emitted 2
	assert.Equal(t, 1, result.Trace.Rules[1].EmittedN) // rule2 emitted 1
}

func TestMaxTuplesEnforcementAtLimit(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithMaxTuples(2))

	yaml := []byte(`
version: "1"
rules:
  - name: "rule1"
    tuples:
      - user: "u:1"
        relation: "r"
        object: "o:1"
      - user: "u:2"
        relation: "r"
        object: "o:2"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, 2, len(result.Tuples))
}

// Iterator tests

func TestEvaluateIteratorSimple(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "roles rule"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"roles": []any{"alice", "bob", "charlie"},
	})
	require.NoError(t, err)
	require.Equal(t, 3, len(result.Tuples))

	assert.Equal(t, "alice", result.Tuples[0].User)
	assert.Equal(t, "bob", result.Tuples[1].User)
	assert.Equal(t, "charlie", result.Tuples[2].User)

	require.NotNil(t, result.Trace)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.Equal(t, 3, result.Trace.Rules[0].EmittedN) // 3 tuples total across iterations
}

func TestEvaluateIteratorEmpty(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "empty roles rule"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"roles": []any{},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, len(result.Tuples))

	require.NotNil(t, result.Trace)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.Equal(t, 0, result.Trace.Rules[0].EmittedN)
}

func TestEvaluateIteratorWithMissingField(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "missing field rule"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Missing roles field → treated as empty array
	result, err := mapping.Evaluate(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(result.Tuples))

	require.NotNil(t, result.Trace)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.Equal(t, 0, result.Trace.Rules[0].EmittedN)
}

func TestEvaluateIteratorWithTupleWhenGuard(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "filter roles"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - when: "role != \"guest\""
          user: "{{role }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"roles": []any{"alice", "guest", "bob"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(result.Tuples)) // guest is filtered out

	assert.Equal(t, "alice", result.Tuples[0].User)
	assert.Equal(t, "bob", result.Tuples[1].User)

	require.NotNil(t, result.Trace)
	assert.Equal(t, 2, result.Trace.Rules[0].EmittedN)
}

func TestEvaluateIteratorNonArraySource(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "non-array source"
    iterator:
      source: "input.name"
      as: "name"
      tuples:
        - user: "{{name }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	_, err = mapping.Evaluate(context.Background(), map[string]any{
		"name": "john",
	})
	require.Error(t, err)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
	assert.Contains(t, err.Error(), "iterator source must be an array")
}

func TestEvaluateIteratorWithVariables(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "roles with org"
    variables:
      org_id: "input.org"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "admin"
          object: "org:{{variables.org_id }}"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"org":   "acme",
		"roles": []any{"alice", "bob"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(result.Tuples))

	assert.Equal(t, "org:acme", result.Tuples[0].Object)
	assert.Equal(t, "org:acme", result.Tuples[1].Object)
}

func TestEvaluateMultipleRulesWithIterators(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "admin roles"
    iterator:
      source: "input.admin_roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "admin"
          object: "org:acme"
  - name: "viewer roles"
    iterator:
      source: "input.viewer_roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "viewer"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"admin_roles":  []any{"alice"},
		"viewer_roles": []any{"bob", "charlie"},
	})
	require.NoError(t, err)
	require.Equal(t, 3, len(result.Tuples)) // 1 admin + 2 viewers

	// Check admin tuple
	assert.Equal(t, "alice", result.Tuples[0].User)
	assert.Equal(t, "admin", result.Tuples[0].Relation)

	// Check viewer tuples
	assert.Equal(t, "bob", result.Tuples[1].User)
	assert.Equal(t, "viewer", result.Tuples[1].Relation)
	assert.Equal(t, "charlie", result.Tuples[2].User)
	assert.Equal(t, "viewer", result.Tuples[2].Relation)

	require.NotNil(t, result.Trace)
	assert.Equal(t, 1, result.Trace.Rules[0].EmittedN)
	assert.Equal(t, 2, result.Trace.Rules[1].EmittedN)
}

func TestEvaluateIteratorMaxTuples(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithMaxTuples(2))

	yaml := []byte(`
version: "1"
rules:
  - name: "roles"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "{{role }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"roles": []any{"alice", "bob", "charlie"}, // 3 items > maxTuples of 2
	})
	require.Error(t, err)
	require.NotNil(t, result)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
	assert.Contains(t, err.Error(), "exceeding limit of 2")
}

func TestEvaluateIteratorMaxTuplesStopsEarly(t *testing.T) {
	t.Parallel()
	// With maxTuples=2 and an iterator source of 5 distinct items, enforcement
	// must stop as soon as the distinct count exceeds the limit, reporting
	// maxTuples+1 (3) rather than rendering the entire source and reporting 5.
	compiler := NewCompiler(WithMaxTuples(2), WithMaxIteratorItems(100))

	yaml := []byte(`
version: "1"
rules:
  - name: "roles"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "user:{{role }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"roles": []any{"a", "b", "c", "d", "e"},
	})
	require.Error(t, err)
	require.NotNil(t, result)
	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr))
	assert.Equal(t, "maxTuples", evalErr.Expression)
	assert.Contains(t, err.Error(), "produced 3 tuples")
	assert.Contains(t, err.Error(), "exceeding limit of 2")
}

func TestEvaluateIteratorWithVariablesFromArray(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "items from variables"
    variables:
      items: "input.all_items"
    iterator:
      source: "variables.items"
      as: "item"
      tuples:
        - user: "{{item }}"
          relation: "owner"
          object: "resource:1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"all_items": []any{"user1", "user2"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(result.Tuples))

	assert.Equal(t, "user1", result.Tuples[0].User)
	assert.Equal(t, "user2", result.Tuples[1].User)
}

func TestEvaluateIteratorWithTypedArrays(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))

	yaml := []byte(`
version: "1"
rules:
  - name: "numeric ids"
    iterator:
      source: "input.ids"
      as: "id"
      tuples:
        - user: "user:{{id }}"
          relation: "admin"
          object: "org:acme"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(context.Background(), map[string]any{
		"ids": []int{1, 2, 3}, // typed array, should be converted to []any
	})
	require.NoError(t, err)
	require.Equal(t, 3, len(result.Tuples))

	assert.Equal(t, "user:1", result.Tuples[0].User)
	assert.Equal(t, "user:2", result.Tuples[1].User)
	assert.Equal(t, "user:3", result.Tuples[2].User)
}

// RunTests tests

func TestRunTests(t *testing.T) {
	t.Parallel()
	type wantResult struct {
		passed  bool
		wantErr bool
		check   func(*testing.T, TestResult)
	}

	tests := []struct {
		name string
		yaml string
		want []wantResult
	}{
		{
			name: "single passing test",
			yaml: `
version: "1"
rules:
  - name: "grant member"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
tests:
  - name: "basic test"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`,
			want: []wantResult{{
				passed: true,
				check: func(t *testing.T, r TestResult) {
					t.Helper()
					assert.Equal(t, "basic test", r.Name)
					assert.Equal(t, []language.Tuple{{User: "u:alice", Relation: "member", Object: "o:team1", Action: language.ActionWrite}}, r.Expected)
					assert.Equal(t, r.Expected, r.Actual)
				},
			}},
		},
		{
			name: "multiple passing tests",
			yaml: `
version: "1"
rules:
  - name: "type router"
    when: "input.type == \"a\""
    tuples:
      - user: "u:1"
        relation: "r"
        object: "o:1"
  - name: "always"
    tuples:
      - user: "u:2"
        relation: "r"
        object: "o:2"
tests:
  - name: "type a"
    input: { "type": "a" }
    expect_tuples:
      - user: "u:1"
        relation: "r"
        object: "o:1"
      - user: "u:2"
        relation: "r"
        object: "o:2"
  - name: "type b"
    input: { "type": "b" }
    expect_tuples:
      - user: "u:2"
        relation: "r"
        object: "o:2"
`,
			want: []wantResult{{passed: true}, {passed: true}},
		},
		{
			name: "mismatch wrong tuple value",
			yaml: `
version: "1"
rules:
  - name: "grant"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
tests:
  - name: "wrong user"
    input: {}
    expect_tuples:
      - user: "u:bob"
        relation: "member"
        object: "o:team1"
`,
			want: []wantResult{{
				check: func(t *testing.T, r TestResult) {
					t.Helper()
					assert.Equal(t, []language.Tuple{{User: "u:alice", Relation: "member", Object: "o:team1", Action: language.ActionWrite}}, r.Actual)
				},
			}},
		},
		{
			name: "mismatch extra actual tuple",
			yaml: `
version: "1"
rules:
  - name: "grant"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:bob"
        relation: "viewer"
        object: "o:team1"
tests:
  - name: "expect only alice"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`,
			want: []wantResult{{}},
		},
		{
			name: "mismatch missing actual tuple",
			yaml: `
version: "1"
rules:
  - name: "grant"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
tests:
  - name: "expect extra"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:bob"
        relation: "viewer"
        object: "o:team1"
`,
			want: []wantResult{{}},
		},
		{
			name: "order independence",
			yaml: `
version: "1"
rules:
  - name: "grant"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:bob"
        relation: "viewer"
        object: "o:team1"
tests:
  - name: "reversed order"
    input: {}
    expect_tuples:
      - user: "u:bob"
        relation: "viewer"
        object: "o:team1"
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`,
			want: []wantResult{{passed: true}},
		},
		{
			name: "empty expect and no tuples produced",
			yaml: `
version: "1"
rules:
  - name: "conditional"
    when: "false"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "nothing expected"
    input: {}
    expect_tuples: []
`,
			want: []wantResult{{
				passed: true,
				check: func(t *testing.T, r TestResult) {
					t.Helper()
					assert.Empty(t, r.Expected)
					assert.Empty(t, r.Actual)
				},
			}},
		},
		{
			name: "evaluate error marks failed",
			yaml: `
version: "1"
rules:
  - name: "bad rule"
    tuples:
      - user: "u:{{ input.required_field }}"
        relation: "r"
        object: "o:x"
tests:
  - name: "triggers eval error"
    input: {}
    expect_tuples: []
`,
			want: []wantResult{{wantErr: true}},
		},
		{
			name: "conflict marks failed with actual populated",
			yaml: `
version: "1"
rules:
  - name: "write"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        action: "write"
  - name: "delete"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        action: "delete"
tests:
  - name: "conflict case"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`,
			want: []wantResult{{
				wantErr: true,
				check: func(t *testing.T, r TestResult) {
					t.Helper()
					assert.NotEmpty(t, r.Actual, "conflict should still populate Actual for diagnostics")
					var ce *ConflictError
					assert.True(t, errors.As(r.Error, &ce), "expected *ConflictError, got %T", r.Error)
				},
			}},
		},
		{
			name: "no test cases returns empty slice",
			yaml: `
version: "1"
rules:
  - name: "rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`,
		},
		{
			name: "action matching write vs delete",
			yaml: `
version: "1"
rules:
  - name: "delete rule"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        action: "delete"
tests:
  - name: "write does not match delete"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        action: "write"
  - name: "delete matches delete"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
        action: "delete"
`,
			want: []wantResult{{}, {passed: true}},
		},
		{
			name: "duplicate expected tuple does not match distinct actual",
			yaml: `
version: "1"
rules:
  - name: "two distinct"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:bob"
        relation: "viewer"
        object: "o:team1"
tests:
  - name: "expects duplicate alice"
    input: {}
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`,
			want: []wantResult{{}},
		},
		{
			name: "runtime eval error marks failed with eval error",
			yaml: `
version: "1"
rules:
  - name: "rule"
    iterator:
      source: "input.name"
      as: "name"
      tuples:
        - user: "{{ name }}"
          relation: "admin"
          object: "org:acme"
tests:
  - name: "non-array iterator source"
    input:
      name: "john"
    expect_tuples: []
`,
			want: []wantResult{{
				wantErr: true,
				check: func(t *testing.T, r TestResult) {
					t.Helper()
					var evalErr *EvalError
					assert.True(t, errors.As(r.Error, &evalErr), "expected *EvalError, got %T", r.Error)
				},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapping, err := NewCompiler().Compile([]byte(tt.yaml))
			require.NoError(t, err)

			results := mapping.RunTests(t.Context())
			require.Len(t, results, len(tt.want))

			for i, w := range tt.want {
				r := results[i]
				assert.Equal(t, w.passed, r.Passed, "result %d %q", i, r.Name)
				if w.wantErr {
					assert.Error(t, r.Error, "result %d %q", i, r.Name)
				} else {
					assert.NoError(t, r.Error, "result %d %q", i, r.Name)
				}
				if w.check != nil {
					w.check(t, r)
				}
			}
		})
	}
}

// RunTestsFiltered tests

func TestRunTestsFiltered(t *testing.T) {
	t.Parallel()
	const yaml = `
version: "1"
rules:
  - name: "rule_a"
    when: 'input.type == "a"'
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:1"
  - name: "rule_b"
    when: 'input.type == "b"'
    tuples:
      - user: "u:bob"
        relation: "member"
        object: "o:2"
tests:
  - name: "test alpha"
    input: { "type": "a" }
    expect_tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:1"
  - name: "test beta"
    input: { "type": "b" }
    expect_tuples:
      - user: "u:bob"
        relation: "member"
        object: "o:2"
  - name: "test gamma wrong"
    input: { "type": "a" }
    expect_tuples:
      - user: "u:wrong"
        relation: "member"
        object: "o:1"
`
	mapping, err := NewCompiler().Compile([]byte(yaml))
	require.NoError(t, err)

	t.Run("no filter no fail-fast returns all results", func(t *testing.T) {
		run := mapping.RunTestsFiltered(t.Context(), "", false)
		assert.Len(t, run.Results, 3)
		assert.Equal(t, 0, run.Filtered)
		assert.Equal(t, 0, run.Skipped)
		assert.False(t, run.Stopped())
		assert.True(t, run.Results[0].Passed)
		assert.True(t, run.Results[1].Passed)
		assert.False(t, run.Results[2].Passed)
	})

	t.Run("filter by substring runs only matching tests", func(t *testing.T) {
		run := mapping.RunTestsFiltered(t.Context(), "alpha", false)
		assert.Len(t, run.Results, 1)
		assert.Equal(t, "test alpha", run.Results[0].Name)
		assert.Equal(t, 2, run.Filtered)
		assert.Equal(t, 0, run.Skipped)
		assert.False(t, run.Stopped())
	})

	t.Run("filter matching zero tests", func(t *testing.T) {
		run := mapping.RunTestsFiltered(t.Context(), "nonexistent", false)
		assert.Empty(t, run.Results)
		assert.Equal(t, 3, run.Filtered)
		assert.Equal(t, 0, run.Skipped)
		assert.False(t, run.Stopped())
	})

	t.Run("fail-fast on last test does not report stopped", func(t *testing.T) {
		// "test gamma wrong" is the 3rd and last test; it fails with fail-fast
		// enabled. Since no matching tests were skipped, Stopped() is false.
		run := mapping.RunTestsFiltered(t.Context(), "", true)
		assert.Len(t, run.Results, 3)
		assert.Equal(t, 0, run.Filtered)
		assert.Equal(t, 0, run.Skipped)
		assert.False(t, run.Stopped())
	})

	t.Run("fail-fast stops before remaining tests", func(t *testing.T) {
		const yaml2 = `
version: "1"
rules:
  - name: "always"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "fail first"
    input: {}
    expect_tuples:
      - user: "u:wrong"
        relation: "r"
        object: "o:x"
  - name: "pass second"
    input: {}
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
  - name: "pass third"
    input: {}
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`
		e2, err := NewCompiler().Compile([]byte(yaml2))
		require.NoError(t, err)

		run := e2.RunTestsFiltered(t.Context(), "", true)
		assert.Len(t, run.Results, 1, "only the first (failing) test should have run")
		assert.False(t, run.Results[0].Passed)
		assert.Equal(t, 0, run.Filtered)
		assert.Equal(t, 2, run.Skipped, "two tests skipped due to fail-fast")
		assert.True(t, run.Stopped())
	})

	t.Run("fail-fast with filter separates filtered from skipped", func(t *testing.T) {
		// 5 tests: alpha(pass), beta(pass), gamma(fail).
		// Filter "t" matches all 3. Fail-fast triggers on gamma.
		// But gamma is the last matching test, so Skipped=0.
		run := mapping.RunTestsFiltered(t.Context(), "test", true)
		assert.Len(t, run.Results, 3, "all 3 match the filter")
		assert.Equal(t, 0, run.Filtered)
		assert.Equal(t, 0, run.Skipped, "fail-fast on last matched test skips nothing")
		assert.False(t, run.Stopped())
	})

	t.Run("results include duration", func(t *testing.T) {
		run := mapping.RunTestsFiltered(t.Context(), "alpha", false)
		require.Len(t, run.Results, 1)
		assert.Greater(t, run.Results[0].Duration, time.Duration(0))
	})

	t.Run("results include input", func(t *testing.T) {
		run := mapping.RunTestsFiltered(t.Context(), "alpha", false)
		require.Len(t, run.Results, 1)
		assert.Equal(t, map[string]any{"type": "a"}, run.Results[0].Input)
	})

	t.Run("RunTests delegates to RunTestsFiltered", func(t *testing.T) {
		allResults := mapping.RunTests(t.Context())
		run := mapping.RunTestsFiltered(t.Context(), "", false)
		require.Equal(t, len(allResults), len(run.Results))
		assert.Equal(t, 0, run.Filtered)
		assert.Equal(t, 0, run.Skipped)
		assert.False(t, run.Stopped())
		for i := range allResults {
			assert.Equal(t, allResults[i].Name, run.Results[i].Name)
			assert.Equal(t, allResults[i].Passed, run.Results[i].Passed)
		}
	})
}

// Iterator tuples scoping tests

func TestEvaluateIteratorTuplesOnly(t *testing.T) {
	t.Parallel()
	yaml := `
version: "1"
rules:
  - name: "roles"
    iterator:
      source: input.roles
      as: role
      tuples:
        - user: "user:{{input.user_id }}"
          relation: "{{role }}"
          object: "org:{{input.org_id }}"
`
	mapping, err := NewCompiler().Compile([]byte(yaml))
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"user_id": "alice",
		"org_id":  "acme",
		"roles":   []string{"admin", "editor"},
	})
	require.NoError(t, err)

	assert.Len(t, result.Tuples, 2)
	assert.Equal(t, "user:alice", result.Tuples[0].User)
	assert.Equal(t, "admin", result.Tuples[0].Relation)
	assert.Equal(t, "user:alice", result.Tuples[1].User)
	assert.Equal(t, "editor", result.Tuples[1].Relation)
}

func TestEvaluateMixedIteratorAndStaticTuples(t *testing.T) {
	t.Parallel()
	yaml := `
version: "1"
rules:
  - name: "mixed"
    iterator:
      source: input.roles
      as: role
      tuples:
        - user: "user:{{input.user_id }}"
          relation: "{{role }}"
          object: "org:{{input.org_id }}"
    tuples:
      - user: "user:{{input.user_id }}"
        relation: "member"
        object: "org:{{input.org_id }}"
`
	mapping, err := NewCompiler().Compile([]byte(yaml))
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"user_id": "bob",
		"org_id":  "acme",
		"roles":   []string{"admin"},
	})
	require.NoError(t, err)

	assert.Len(t, result.Tuples, 2)
	// Iterator tuple first (relation is the role value)
	assert.Equal(t, "admin", result.Tuples[0].Relation)
	// Static tuple second
	assert.Equal(t, "member", result.Tuples[1].Relation)
}

func TestEvaluateIteratorVariableNotAccessibleInStaticTuples(t *testing.T) {
	t.Parallel()
	yaml := `
version: "1"
rules:
  - name: "scoped"
    iterator:
      source: input.roles
      as: role
      tuples:
        - user: "user:{{input.user_id }}"
          relation: "{{role }}"
          object: "org:{{input.org_id }}"
    tuples:
      - user: "user:{{input.user_id }}"
        relation: "{{role }}"
        object: "org:{{input.org_id }}"
`
	mapping, err := NewCompiler().Compile([]byte(yaml))
	require.NoError(t, err)

	_, err = mapping.Evaluate(t.Context(), map[string]any{
		"user_id": "carol",
		"org_id":  "acme",
		"roles":   []string{"admin"},
	})

	// Should fail on static tuple trying to access role (not in scope for static tuples)
	require.Error(t, err)
	var ee *EvalError
	assert.True(t, errors.As(err, &ee))
	assert.Contains(t, err.Error(), "evaluated to nil")
}

// ---------------------------------------------------------------------------
// Tuple filter mapping tests
// ---------------------------------------------------------------------------

func TestEvaluateTupleFilterRouting(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
  - name: "static write"
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: viewer
        object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"org_id":  "acme",
		"user_id": "alice",
	})
	require.NoError(t, err)

	// Static write goes to result.Tuples
	require.Len(t, result.Tuples, 1)
	assert.Equal(t, language.Tuple{User: "user:alice", Relation: "viewer", Object: "org:acme", Action: language.ActionWrite}, result.Tuples[0])

	// Tuple filter rule goes to TupleFilterOperations
	require.Len(t, result.TupleFilterOperations, 1)
	op := result.TupleFilterOperations[0]

	require.Len(t, op.Filters, 1)
	assert.Equal(t, language.TupleFilter{Object: "org:acme", Relation: "member", Action: language.FilterActionPatch}, op.Filters[0])

	require.Len(t, op.Tuples, 1)
	assert.Equal(t, language.Tuple{User: "user:alice", Relation: "member", Object: "org:acme", Action: language.ActionWrite}, op.Tuples[0])
}

func TestEvaluateTupleFilterWithIterator(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    iterator:
      source: input.members
      as: member
      tuples:
        - user: 'user:{{member.id }}'
          relation: member
          object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"org_id":  "acme",
		"members": []any{map[string]any{"id": "alice"}, map[string]any{"id": "bob"}},
	})
	require.NoError(t, err)

	assert.Empty(t, result.Tuples, "filter rules should not produce result.Tuples")
	require.Len(t, result.TupleFilterOperations, 1)
	op := result.TupleFilterOperations[0]

	require.Len(t, op.Filters, 1)
	assert.Equal(t, "org:acme", op.Filters[0].Object)
	assert.Equal(t, "member", op.Filters[0].Relation)
	assert.Empty(t, op.Filters[0].User)

	require.Len(t, op.Tuples, 2)
	assert.Equal(t, "user:alice", op.Tuples[0].User)
	assert.Equal(t, "user:bob", op.Tuples[1].User)
}

func TestEvaluateTupleFilterDeleteOnly(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "delete all"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        action: delete
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"org_id": "acme",
	})
	require.NoError(t, err)

	assert.Empty(t, result.Tuples)
	require.Len(t, result.TupleFilterOperations, 1)
	op := result.TupleFilterOperations[0]
	require.Len(t, op.Filters, 1)
	assert.Equal(t, language.FilterActionDelete, op.Filters[0].Action)
	assert.Equal(t, "org:acme", op.Filters[0].Object)
	assert.Empty(t, op.Tuples)
}

func TestEvaluateTupleFilterEmptyStatePatchError(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "empty state"
    when: "true"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
        action: patch
    iterator:
      source: input.members
      as: member
      tuples:
        - user: 'user:{{member.id }}'
          relation: member
          object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// Empty members list means zero tuples with a patch filter
	_, err = mapping.Evaluate(t.Context(), map[string]any{
		"org_id":  "acme",
		"members": []any{},
	})
	require.Error(t, err)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
	assert.Contains(t, err.Error(), "produced no tuples")
}

func TestEvaluateTupleFilterDeleteOnlyEmptyTuplesOK(t *testing.T) {
	t.Parallel()
	// Delete-only filters with no tuples should not trigger the empty-state error
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "delete all"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        action: delete
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{"org_id": "acme"})
	require.NoError(t, err)
	require.Len(t, result.TupleFilterOperations, 1)
	assert.Empty(t, result.TupleFilterOperations[0].Tuples)
}

func TestEvaluateTupleFilterMixedPatchDelete(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "mixed"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
        action: patch
      - object: 'org:{{input.org_id }}'
        relation: viewer
        action: delete
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"org_id":  "acme",
		"user_id": "alice",
	})
	require.NoError(t, err)

	require.Len(t, result.TupleFilterOperations, 1)
	op := result.TupleFilterOperations[0]
	require.Len(t, op.Filters, 2)
	assert.Equal(t, language.FilterActionPatch, op.Filters[0].Action)
	assert.Equal(t, language.FilterActionDelete, op.Filters[1].Action)
	require.Len(t, op.Tuples, 1)
}

func TestEvaluateTupleFilterWhenGuardSkip(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "conditional"
    when: 'input.type == "sync"'
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// When guard is false: no tuple filter operations should be produced
	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"type":    "other",
		"org_id":  "acme",
		"user_id": "alice",
	})
	require.NoError(t, err)
	assert.Empty(t, result.TupleFilterOperations)
	assert.Empty(t, result.Tuples)
}

func TestEvaluateTupleFilterTrace(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(WithTrace(true))
	yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"org_id":  "acme",
		"user_id": "alice",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Trace)
	require.Len(t, result.Trace.Rules, 1)
	assert.Equal(t, RuleMatched, result.Trace.Rules[0].Status)
	assert.Equal(t, 1, result.Trace.Rules[0].EmittedN)
	assert.Equal(t, 1, result.Trace.Rules[0].FilterN)
}

func TestEvaluateTupleFilterWildcardFields(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "object only"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"org_id":  "acme",
		"user_id": "alice",
	})
	require.NoError(t, err)

	require.Len(t, result.TupleFilterOperations, 1)
	filter := result.TupleFilterOperations[0].Filters[0]
	assert.Equal(t, "org:acme", filter.Object)
	assert.Empty(t, filter.User, "user should be wildcard (empty)")
	assert.Empty(t, filter.Relation, "relation should be wildcard (empty)")
}

// ---------------------------------------------------------------------------
// Tuple filter test framework tests
// ---------------------------------------------------------------------------

func TestRunTestsFilteredTupleFilters(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
tests:
  - name: "tuple filter test"
    input:
      org_id: acme
      user_id: alice
    expect_tuples:
      - user: "user:alice"
        relation: member
        object: "org:acme"
    expect_tuple_filters:
      - object: "org:acme"
        relation: member
        action: patch
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	results := mapping.RunTests(t.Context())
	require.Len(t, results, 1)
	assert.True(t, results[0].Passed, "test should pass: %v", results[0].Error)
}

func TestRunTestsFilteredTupleFiltersMismatch(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
tests:
  - name: "wrong filter"
    input:
      org_id: acme
      user_id: alice
    expect_tuples:
      - user: "user:alice"
        relation: member
        object: "org:acme"
    expect_tuple_filters:
      - object: "org:wrong"
        relation: member
        action: patch
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	results := mapping.RunTests(t.Context())
	require.Len(t, results, 1)
	assert.False(t, results[0].Passed, "test should fail due to filter mismatch")
}

func TestRunTestsAssertWritesCoveredByFilter(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	t.Run("covered", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
tests:
  - name: "covered test"
    input:
      org_id: acme
      user_id: alice
    expect_tuples:
      - user: "user:alice"
        relation: member
        object: "org:acme"
    assert_writes_covered_by_filter: true
`)
		mapping, err := compiler.Compile(yaml)
		require.NoError(t, err)
		results := mapping.RunTests(t.Context())
		require.Len(t, results, 1)
		assert.True(t, results[0].Passed)
	})

	t.Run("not covered", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "sync members"
    tuple_filters:
      - object: 'org:{{input.org_id }}'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
  - name: "extra write"
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: viewer
        object: 'org:{{input.org_id }}'
tests:
  - name: "not covered test"
    input:
      org_id: acme
      user_id: alice
    expect_tuples:
      - user: "user:alice"
        relation: member
        object: "org:acme"
      - user: "user:alice"
        relation: viewer
        object: "org:acme"
    assert_writes_covered_by_filter: true
`)
		mapping, err := compiler.Compile(yaml)
		require.NoError(t, err)
		results := mapping.RunTests(t.Context())
		require.Len(t, results, 1)
		assert.False(t, results[0].Passed, "viewer tuple not covered by member filter")
	})

	t.Run("covered by type prefix", func(t *testing.T) {
		yaml := []byte(`
version: "1"
rules:
  - name: "sync by type prefix"
    tuple_filters:
      - object: 'org:'
        relation: member
    tuples:
      - user: 'user:{{input.user_id }}'
        relation: member
        object: 'org:{{input.org_id }}'
tests:
  - name: "type prefix coverage"
    input:
      org_id: acme
      user_id: alice
    expect_tuples:
      - user: "user:alice"
        relation: member
        object: "org:acme"
    assert_writes_covered_by_filter: true
`)
		mapping, err := compiler.Compile(yaml)
		require.NoError(t, err)
		results := mapping.RunTests(t.Context())
		require.Len(t, results, 1)
		assert.True(t, results[0].Passed, "type prefix org: should cover org:acme")
	})

	t.Run("type prefix does not cover different type", func(t *testing.T) {
		// Directly test isTupleCoveredByFilter for prefix semantics
		f := language.TupleFilter{Object: "org:", Relation: "member", Action: language.FilterActionPatch}
		assert.True(t, isTupleCoveredByFilter(
			language.Tuple{User: "user:alice", Relation: "member", Object: "org:acme", Action: language.ActionWrite}, f,
		))
		assert.False(t, isTupleCoveredByFilter(
			language.Tuple{User: "user:alice", Relation: "member", Object: "team:acme", Action: language.ActionWrite}, f,
		))
	})
}

func TestEvaluateTupleFilterOperationDedup(t *testing.T) {
	t.Parallel()
	// An iterator that produces duplicate tuples within a tuple_filters rule
	// should have duplicates removed in the operation's Tuples.
	compiler := NewCompiler()
	yaml := []byte(`
version: "1"
rules:
  - name: "dedup in operation"
    tuple_filters:
      - relation: member
        object: "org:acme"
    iterator:
      source: input.members
      as: member
      tuples:
        - user: 'user:{{member }}'
          relation: member
          object: "org:acme"
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"members": []any{"alice", "alice", "bob"}, // alice duplicated
	})
	require.NoError(t, err)
	require.Len(t, result.TupleFilterOperations, 1)
	// Should be deduped to 2 tuples (alice + bob), not 3
	assert.Len(t, result.TupleFilterOperations[0].Tuples, 2)
}

func TestEvaluateMaxTuplesCountsOperationTuples(t *testing.T) {
	t.Parallel()
	// maxTuples limit should apply across result.Tuples + all TupleFilterOperation tuples.
	compiler := NewCompiler(WithMaxTuples(3))
	yaml := []byte(`
version: "1"
rules:
  - name: "direct write"
    tuples:
      - user: "user:alice"
        relation: "viewer"
        object: "org:acme"
  - name: "sync members"
    tuple_filters:
      - relation: member
        object: "org:acme"
    iterator:
      source: input.members
      as: member
      tuples:
        - user: 'user:{{member }}'
          relation: member
          object: "org:acme"
`)
	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	// 1 direct tuple + 3 operation tuples = 4 total > maxTuples of 3
	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"members": []any{"bob", "charlie", "dave"},
	})
	require.Error(t, err)
	require.NotNil(t, result)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
	assert.Contains(t, err.Error(), "exceeding limit of 3")
}

// ---------------------------------------------------------------------------
// Rule-level action evaluation
// ---------------------------------------------------------------------------

func TestRuleLevelActionDelete(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "delete all"
    action: "delete"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
      - user: "u:bob"
        relation: "member"
        object: "o:team2"
      - user: "u:charlie"
        relation: "viewer"
        object: "o:team1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 3)

	for _, tuple := range result.Tuples {
		assert.Equal(t, language.ActionDelete, tuple.Action, "expected all tuples to have delete action, got %q for %v", tuple.Action, tuple)
	}
}

func TestRuleLevelActionWrite(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "write all"
    action: "write"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 1)
	assert.Equal(t, language.ActionWrite, result.Tuples[0].Action)
}

func TestRuleLevelActionDeleteWithIterator(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "delete iter"
    action: "delete"
    iterator:
      source: "input.roles"
      as: "role"
      tuples:
        - user: "user:{{ input.user_id }}"
          relation: "has_role"
          object: "role:{{ role }}"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	input := map[string]any{
		"user_id": "alice",
		"roles":   []any{"admin", "editor"},
	}
	result, err := mapping.Evaluate(t.Context(), input)
	require.NoError(t, err)
	require.Len(t, result.Tuples, 2)

	for _, tuple := range result.Tuples {
		assert.Equal(t, language.ActionDelete, tuple.Action, "expected all iterator tuples to have delete action")
	}
}

func TestRuleLevelActionMixedRules(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "delete rule"
    action: "delete"
    when: "true"
    tuples:
      - user: "u:alice"
        relation: "member"
        object: "o:team1"

  - name: "default rule"
    when: "true"
    tuples:
      - user: "u:bob"
        relation: "viewer"
        object: "o:team2"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 2)

	assert.Equal(t, language.ActionDelete, result.Tuples[0].Action, "first rule should produce delete tuples")
	assert.Equal(t, language.ActionWrite, result.Tuples[1].Action, "second rule should produce write tuples (default)")
}

// ---------------------------------------------------------------------------
// Tuple condition and context
// ---------------------------------------------------------------------------

func TestEvaluateTupleConditionPassthrough(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "with condition"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        condition: in_allowed_ip_range
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"user_id": "alice",
		"doc_id":  "d1",
	})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 1)
	assert.Equal(t, "in_allowed_ip_range", result.Tuples[0].Condition)
	assert.Empty(t, result.Tuples[0].Context)
}

func TestEvaluateTupleConditionWithContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "with condition and context"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:{{ input.doc_id }}"
        condition: in_allowed_ip_range
        context:
          allowed_range: "{{ input.ip_address }}"
          device_type: "laptop"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"user_id":    "alice",
		"doc_id":     "d1",
		"ip_address": "192.168.1.0/24",
	})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 1)

	tuple := result.Tuples[0]
	assert.Equal(t, "in_allowed_ip_range", tuple.Condition)
	require.Len(t, tuple.Context, 2)
	assert.Equal(t, "192.168.1.0/24", tuple.Context["allowed_range"])
	assert.Equal(t, "laptop", tuple.Context["device_type"])
}

func TestEvaluateTupleConditionWithContextVariables(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "context using variables"
    variables:
      clean_ip: trim(input.ip)
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "viewer"
        object: "doc:d1"
        condition: ip_check
        context:
          ip: "{{ variables.clean_ip }}"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"user_id": "alice",
		"ip":      "  10.0.0.1  ",
	})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 1)
	assert.Equal(t, "10.0.0.1", result.Tuples[0].Context["ip"])
}

func TestEvaluateTupleNoConditionNoContext(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "no condition"
    tuples:
      - user: "user:alice"
        relation: "viewer"
        object: "doc:d1"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 1)
	assert.Empty(t, result.Tuples[0].Condition)
	assert.Empty(t, result.Tuples[0].Context)
}

func TestEvaluateTupleConditionWithIterator(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()

	yaml := []byte(`
version: "1"
rules:
  - name: "iterator with condition"
    iterator:
      source: input.roles
      as: role
      tuples:
        - user: "user:{{ input.user_id }}"
          relation: "{{ role.name }}"
          object: "org:{{ input.org_id }}"
          condition: role_active
          context:
            role_id: "{{ role.id }}"
`)

	mapping, err := compiler.Compile(yaml)
	require.NoError(t, err)

	result, err := mapping.Evaluate(t.Context(), map[string]any{
		"user_id": "alice",
		"org_id":  "org1",
		"roles": []any{
			map[string]any{"name": "admin", "id": "r1"},
			map[string]any{"name": "viewer", "id": "r2"},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Tuples, 2)

	assert.Equal(t, "role_active", result.Tuples[0].Condition)
	assert.Equal(t, "r1", result.Tuples[0].Context["role_id"])
	assert.Equal(t, "role_active", result.Tuples[1].Condition)
	assert.Equal(t, "r2", result.Tuples[1].Context["role_id"])
}
