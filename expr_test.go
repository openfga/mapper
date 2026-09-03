package mapper

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper/language"
)

// compileVarsT pre-compiles a Variables slice into []compiledVariable for testing.
func compileVarsT(t *testing.T, vars language.Variables) []compiledVariable {
	t.Helper()
	result := make([]compiledVariable, len(vars))
	for i, v := range vars {
		prog, err := compileExpr(v.Expression)
		require.NoError(t, err, "failed to compile variable %q: %s", v.Name, v.Expression)
		result[i] = compiledVariable{name: v.Name, code: v.Expression, program: prog}
	}
	return result
}

func TestJsonPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		obj      any
		path     string
		expected any
	}{
		{
			name:     "simple_map",
			obj:      map[string]any{"name": "Alice"},
			path:     "name",
			expected: "Alice",
		},
		{
			name:     "nested_map",
			obj:      map[string]any{"user": map[string]any{"email": "alice@example.com"}},
			path:     "user.email",
			expected: "alice@example.com",
		},
		{
			name:     "deep_nesting",
			obj:      map[string]any{"data": map[string]any{"user": map[string]any{"email": "test@example.com"}}},
			path:     "data.user.email",
			expected: "test@example.com",
		},
		{
			name:     "missing_key",
			obj:      map[string]any{"user": map[string]any{"name": "Alice"}},
			path:     "user.email",
			expected: nil,
		},
		{
			name:     "missing_top_level",
			obj:      map[string]any{"user": map[string]any{"name": "Alice"}},
			path:     "missing.key",
			expected: nil,
		},
		{
			name:     "nil_input",
			obj:      nil,
			path:     "any.path",
			expected: nil,
		},
		{
			name:     "empty_path",
			obj:      map[string]any{"key": "value"},
			path:     "",
			expected: nil,
		},
		{
			name:     "array_access",
			obj:      map[string]any{"items": []any{"a", "b", "c"}},
			path:     "items.1",
			expected: "b",
		},
		{
			name:     "array_out_of_bounds",
			obj:      map[string]any{"items": []any{"a", "b"}},
			path:     "items.5",
			expected: nil,
		},
		{
			name:     "array_negative_index",
			obj:      map[string]any{"items": []any{"a", "b", "c"}},
			path:     "items.-1",
			expected: nil,
		},
		{
			name:     "empty_segment",
			obj:      map[string]any{"a": map[string]any{"b": "value"}},
			path:     "a..b",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := jsonPath(tt.obj, tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJsonPathEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		params      []any
		errContains string
	}{
		{
			name:        "wrong_argument_count_0",
			params:      []any{},
			errContains: "requires exactly 2 arguments",
		},
		{
			name:        "wrong_argument_count_1",
			params:      []any{"obj"},
			errContains: "requires exactly 2 arguments",
		},
		{
			name:        "wrong_argument_count_3",
			params:      []any{"obj", "path", "extra"},
			errContains: "requires exactly 2 arguments",
		},
		{
			name:        "non_string_path",
			params:      []any{map[string]any{"key": "value"}, 123},
			errContains: "path must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := jsonPath(tt.params...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestJsonPathMaxDepth(t *testing.T) {
	t.Parallel()
	obj := map[string]any{"data": map[string]any{"value": "test"}}

	// Create a path with more than maxJsonPathSegment segments
	var pathParts []string
	for i := 0; i <= maxJsonPathSegment; i++ {
		pathParts = append(pathParts, "a")
	}
	deepPath := strings.Join(pathParts, ".")

	_, err := jsonPath(obj, deepPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum of")
}

func TestEvaluateVariablesTruncationUTF8(t *testing.T) {
	t.Parallel()
	// Create a string with multi-byte UTF-8 characters (emojis) that's > 4KB
	emoji := "🎉"                           // 4 bytes in UTF-8
	numEmojis := (maxVariableSize / 4) + 1 // Create string longer than 4KB in bytes
	longString := strings.Repeat(emoji, numEmojis)

	ctx := context.Background()
	input := map[string]any{"data": longString}

	vars := compileVarsT(t, language.Variables{
		{Name: "emoji_var", Expression: `input.data`},
	})

	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)

	truncated, ok := result["emoji_var"].(string)
	require.True(t, ok, "expected string result")

	// Truncated at byte boundary, so len <= maxVariableSize
	assert.LessOrEqual(t, len(truncated), maxVariableSize)
	// Must be shorter than the original
	assert.Less(t, len(truncated), len(longString))
	// Must still be valid UTF-8 (no split runes)
	assert.True(t, utf8.ValidString(truncated), "truncated string must be valid UTF-8")
	// Each emoji is 4 bytes, so the truncated size should be 4096 - (4096 % 4) = 4096
	assert.Equal(t, maxVariableSize, len(truncated), "should truncate to exact byte boundary when aligned")
}

func TestCompileExprMaxSize(t *testing.T) {
	t.Parallel()
	// Create an expression larger than maxExpressionSize
	largeExpr := strings.Repeat("x", maxExpressionSize+1)

	program, err := compileExpr(largeExpr)
	require.Error(t, err)
	assert.Nil(t, program)

	evalErr, ok := err.(*EvalError)
	require.True(t, ok)
	assert.Contains(t, evalErr.Error(), "exceeds max size")
	// Expression in error should be truncated, not the full 64KB+
	assert.LessOrEqual(t, len(evalErr.Expression), maxExprInError+3)
}

func TestCompileExprSyntaxError(t *testing.T) {
	t.Parallel()
	program, err := compileExpr("invalid syntax here ][")
	require.Error(t, err)
	assert.Nil(t, program)

	evalErr, ok := err.(*EvalError)
	require.True(t, ok)
	// Short expressions are not truncated
	assert.Equal(t, "invalid syntax here ][", evalErr.Expression)
}

func TestTruncateExpr(t *testing.T) {
	t.Parallel()
	short := "x + 1"
	assert.Equal(t, short, truncateExpr(short))

	long := strings.Repeat("a", maxExprInError+50)
	result := truncateExpr(long)
	assert.True(t, strings.HasSuffix(result, "..."))
	assert.LessOrEqual(t, len(result), maxExprInError+3) // +3 for "..."

	// Multi-byte rune safety: create a string of 4-byte emojis near the boundary
	emoji := strings.Repeat("🎉", maxExprInError/4+1)
	truncated := truncateExpr(emoji)
	assert.True(t, utf8.ValidString(truncated), "truncated expression must be valid UTF-8")
	assert.True(t, strings.HasSuffix(truncated, "..."))
}

func TestJsonPathRegistered(t *testing.T) {
	t.Parallel()
	// Verify json_path is callable through the compiled expression path.
	program, err := compileExpr(`json_path({"a": 1}, "a")`)
	require.NoError(t, err)
	result, err := runExpr(program, map[string]any{}, `json_path({"a": 1}, "a")`)
	require.NoError(t, err)
	assert.Equal(t, 1, result)
}

func TestEvaluateVariablesEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"key": "value"}

	result, err := evaluateVariables(ctx, []compiledVariable{}, input)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestEvaluateVariablesSimple(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"email": "test@example.com"}

	vars := compileVarsT(t, language.Variables{
		{Name: "user_email", Expression: `input.email`},
	})

	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", result["user_email"])
}

func TestEvaluateVariablesSequential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"value": "hello"}

	vars := compileVarsT(t, language.Variables{
		{Name: "a", Expression: `input.value`},
		{Name: "b", Expression: `variables.a + " world"`},
	})

	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)
	assert.Equal(t, "hello", result["a"])
	assert.Equal(t, "hello world", result["b"])
}

func TestEvaluateVariablesChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"value": "test"}

	vars := compileVarsT(t, language.Variables{
		{Name: "a", Expression: `input.value`},
		{Name: "b", Expression: `variables.a + "1"`},
		{Name: "c", Expression: `variables.b + "2"`},
	})

	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)
	assert.Equal(t, "test", result["a"])
	assert.Equal(t, "test1", result["b"])
	assert.Equal(t, "test12", result["c"])
}

func TestCheckForwardRefsSyntaxErrorSkipped(t *testing.T) {
	t.Parallel()
	// A variable with a syntax error should not cause checkForwardRefs to fail.
	// The syntax error will be caught later during compilation.
	vars := language.Variables{
		{Name: "a", Expression: `invalid syntax ][`},
		{Name: "b", Expression: `"value"`},
	}

	err := checkForwardRefs(vars)
	require.NoError(t, err)
}

func TestCheckForwardRefsOversizedExpression(t *testing.T) {
	t.Parallel()
	// A variable with an expression exceeding maxExpressionSize should be rejected
	// before parsing to prevent DoS via pathologically large expressions.
	largeExpr := strings.Repeat("x", maxExpressionSize+1)
	vars := language.Variables{
		{Name: "a", Expression: largeExpr},
		{Name: "b", Expression: `"value"`},
	}

	err := checkForwardRefs(vars)
	require.Error(t, err)

	// The error should contain information about the size limit
	assert.Contains(t, err.Error(), "exceeds max size")
}

func TestForwardRefDetectedAtCompileTime(t *testing.T) {
	t.Parallel()
	// Variable "a" references "variables.c" which is defined later in the list.
	// This is caught at compile time in compileRule via checkForwardRefs.
	vars := language.Variables{
		{Name: "a", Expression: `variables.c`},
		{Name: "b", Expression: `"value"`},
		{Name: "c", Expression: `"later"`},
	}

	err := checkForwardRefs(vars)
	require.Error(t, err)

	var evalErr *EvalError
	require.True(t, errors.As(err, &evalErr), "error should contain *EvalError")
	assert.Contains(t, evalErr.Error(), "references \"c\" which is defined later")
}

func TestMultipleForwardRefsDetectedAtCompileTime(t *testing.T) {
	t.Parallel()
	// Both "a" and "b" have forward references. Both should be reported.
	vars := language.Variables{
		{Name: "a", Expression: `variables.c`},
		{Name: "b", Expression: `variables.c`},
		{Name: "c", Expression: `"value"`},
	}

	err := checkForwardRefs(vars)
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, `variable "a" references "c"`)
	assert.Contains(t, msg, `variable "b" references "c"`)
}

func TestEvaluateVariablesSelfRefAllowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{}

	// Self-reference: pos[a] == i, not > i, so it's not a forward ref.
	// At runtime, a isn't in the map yet, so it resolves to nil and coalesces.
	vars := compileVarsT(t, language.Variables{
		{Name: "a", Expression: `variables.a ?? "default"`},
	})
	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)
	assert.Equal(t, "default", result["a"])
}

func TestEvaluateVariablesInputAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{
		"data": map[string]any{
			"email": "user@example.com",
		},
	}

	vars := compileVarsT(t, language.Variables{
		{Name: "extracted_email", Expression: `input.data.email`},
	})

	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", result["extracted_email"])
}

func TestEvaluateVariablesTruncation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a string longer than 4KB
	longString := strings.Repeat("a", 5000)
	input := map[string]any{"data": longString}

	vars := compileVarsT(t, language.Variables{
		{Name: "long_var", Expression: `input.data`},
	})

	result, err := evaluateVariables(ctx, vars, input)
	require.NoError(t, err)

	truncated, ok := result["long_var"].(string)
	require.True(t, ok, "expected string result")
	assert.Equal(t, maxVariableSize, len(truncated))
	assert.Equal(t, strings.Repeat("a", maxVariableSize), truncated)
}

func TestEvaluateVariablesContextDeadline(t *testing.T) {
	t.Parallel()
	// Use a context whose deadline is already in the past
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Millisecond))
	defer cancel()

	input := map[string]any{}
	prog, err := compileExpr(`"value"`)
	require.NoError(t, err)
	vars := []compiledVariable{{name: "a", code: `"value"`, program: prog}}

	_, err = evaluateVariables(ctx, vars, input)
	require.Error(t, err)

	evalErr, ok := err.(*EvalError)
	assert.True(t, ok)
	assert.Equal(t, "a", evalErr.Expression)
}

func TestEvaluateWhenGuardEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	matched, err := evaluateWhenGuard(ctx, nil, "", map[string]any{}, map[string]any{})
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestEvaluateWhenGuardTrue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prog, err := compileExpr("true")
	require.NoError(t, err)
	matched, err := evaluateWhenGuard(ctx, prog, "true", map[string]any{}, map[string]any{})
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestEvaluateWhenGuardFalse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prog, err := compileExpr("false")
	require.NoError(t, err)
	matched, err := evaluateWhenGuard(ctx, prog, "false", map[string]any{}, map[string]any{})
	require.NoError(t, err)
	assert.False(t, matched)
}

func TestEvaluateWhenGuardWithInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"type": "user.created"}

	prog, err := compileExpr(`input.type == "user.created"`)
	require.NoError(t, err)
	matched, err := evaluateWhenGuard(ctx, prog, `input.type == "user.created"`, input, map[string]any{})
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestEvaluateWhenGuardWithVariables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"email": "test@example.com"}
	variables := map[string]any{"domain": "example.com"}

	prog, err := compileExpr(`variables.domain == "example.com"`)
	require.NoError(t, err)
	matched, err := evaluateWhenGuard(ctx, prog, `variables.domain == "example.com"`, input, variables)
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestEvaluateWhenGuardComplex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	input := map[string]any{"active": true, "type": "admin"}
	variables := map[string]any{}

	prog, err := compileExpr(`input.active && input.type == "admin"`)
	require.NoError(t, err)
	matched, err := evaluateWhenGuard(ctx, prog, `input.active && input.type == "admin"`, input, variables)
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestEvaluateWhenGuardContextDeadline(t *testing.T) {
	t.Parallel()
	// Use a context whose deadline is already in the past
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Millisecond))
	defer cancel()

	prog, err := compileExpr("true")
	require.NoError(t, err)
	_, err = evaluateWhenGuard(ctx, prog, "true", map[string]any{}, map[string]any{})
	require.Error(t, err)
}

func TestFgaEscape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no_forbidden_chars",
			input:    "hello-world_123",
			expected: "hello-world_123",
		},
		{
			name:     "hash",
			input:    "user#admin",
			expected: "user%23admin",
		},
		{
			name:     "colon",
			input:    "idp:abc123",
			expected: "idp%3Aabc123",
		},
		{
			name:     "at_sign",
			input:    "user@example.com",
			expected: "user%40example.com",
		},
		{
			name:     "asterisk",
			input:    "wild*card",
			expected: "wild%2Acard",
		},
		{
			name:     "percent_encoding_safety",
			input:    "already%23encoded",
			expected: "already%2523encoded",
		},
		{
			name:     "space",
			input:    "hello world",
			expected: "hello%20world",
		},
		{
			name:     "tab_control_char",
			input:    "hello\tworld",
			expected: "hello%09world",
		},
		{
			name:     "newline_control_char",
			input:    "hello\nworld",
			expected: "hello%0Aworld",
		},
		{
			name:     "null_byte",
			input:    "hello\x00world",
			expected: "hello%00world",
		},
		{
			name:     "multiple_forbidden_chars",
			input:    "user#1:admin@org",
			expected: "user%231%3Aadmin%40org",
		},
		{
			name:     "empty_string",
			input:    "",
			expected: "",
		},
		{
			name:     "pipe_id",
			input:    "idp|abc123",
			expected: "idp|abc123",
		},
		{
			name:     "google_oauth_id",
			input:    "google-oauth2|12345",
			expected: "google-oauth2|12345",
		},
		{
			name:     "unicode_safe_chars",
			input:    "user-日本語",
			expected: "user-日本語",
		},
		{
			name:     "all_forbidden_in_one",
			input:    "#:@* %",
			expected: "%23%3A%40%2A%20%25",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := fgaEscape(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFgaEscapeEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		params      []any
		errContains string
	}{
		{
			name:        "wrong_argument_count_0",
			params:      []any{},
			errContains: "requires exactly 1 argument",
		},
		{
			name:        "wrong_argument_count_2",
			params:      []any{"a", "b"},
			errContains: "requires exactly 1 argument",
		},
		{
			name:        "non_string_argument",
			params:      []any{123},
			errContains: "argument must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fgaEscape(tt.params...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestFgaEscapeRegistered(t *testing.T) {
	t.Parallel()
	// Verify fga_escape is callable through the compiled expression path.
	program, err := compileExpr(`fga_escape("user#admin")`)
	require.NoError(t, err)
	result, err := runExpr(program, map[string]any{}, `fga_escape("user#admin")`)
	require.NoError(t, err)
	assert.Equal(t, "user%23admin", result)
}

func TestFgaEscapeInInterpolation(t *testing.T) {
	t.Parallel()
	// Integration test: fga_escape works inside a mapping interpolation.
	yaml := `
version: "1"
rules:
  - name: "test escape"
    tuples:
      - user: "user:{{ fga_escape(input.user_id) }}"
        relation: "member"
        object: "org:{{ input.org_id }}"
`
	compiler := NewCompiler()
	mapping, err := compiler.Compile([]byte(yaml))
	require.NoError(t, err)

	event := map[string]any{
		"user_id": "idp#special:user",
		"org_id":  "org123",
	}
	result, err := mapping.Evaluate(t.Context(), event)
	require.NoError(t, err)
	require.Len(t, result.Tuples, 1)
	assert.Equal(t, "user:idp%23special%3Auser", result.Tuples[0].User)
	assert.Equal(t, "member", result.Tuples[0].Relation)
	assert.Equal(t, "org:org123", result.Tuples[0].Object)
}

func TestToBool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    any
		expected bool
	}{
		{name: "nil", value: nil, expected: false},
		{name: "bool_true", value: true, expected: true},
		{name: "bool_false", value: false, expected: false},
		{name: "int_zero", value: 0, expected: false},
		{name: "int_nonzero", value: 42, expected: true},
		{name: "int_negative", value: -1, expected: true},
		{name: "int64_zero", value: int64(0), expected: false},
		{name: "int64_nonzero", value: int64(42), expected: true},
		{name: "float64_zero", value: float64(0), expected: false},
		{name: "float64_nonzero", value: float64(3.14), expected: true},
		{name: "string_empty", value: "", expected: false},
		{name: "string_nonempty", value: "hello", expected: true},
		{name: "array_empty", value: []any{}, expected: false},
		{name: "array_nonempty", value: []any{1}, expected: true},
		{name: "map_empty", value: map[string]any{}, expected: false},
		{name: "map_nonempty", value: map[string]any{"key": "value"}, expected: true},
		{name: "unknown_type_truthy", value: struct{}{}, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toBool(tt.value)
			assert.Equal(t, tt.expected, result)
		})
	}
}
