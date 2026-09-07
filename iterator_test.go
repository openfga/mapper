package mapper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateIteratorSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		source    string
		input     map[string]any
		variables map[string]any
		want      []any
		wantErr   bool
		errType   error
	}{
		{
			name:    "valid []any source",
			source:  "input.roles",
			input:   map[string]any{"roles": []any{"admin", "user"}},
			want:    []any{"admin", "user"},
			wantErr: false,
		},
		{
			name:    "empty []any source",
			source:  "input.roles",
			input:   map[string]any{"roles": []any{}},
			want:    []any{},
			wantErr: false,
		},
		{
			name:    "nil result (missing field)",
			source:  "input.missing",
			input:   map[string]any{"roles": []any{"admin"}},
			want:    []any{},
			wantErr: false,
		},
		{
			name:    "non-array result (string)",
			source:  "input.name",
			input:   map[string]any{"name": "john"},
			wantErr: true,
			errType: &EvalError{},
		},
		{
			name:    "non-array result (number)",
			source:  "input.count",
			input:   map[string]any{"count": 42},
			wantErr: true,
			errType: &EvalError{},
		},
		{
			name:      "source referencing variables",
			source:    "variables.items",
			input:     map[string]any{},
			variables: map[string]any{"items": []any{1, 2, 3}},
			want:      []any{1, 2, 3},
			wantErr:   false,
		},
		{
			name:    "[]string converted to []any",
			source:  "input.tags",
			input:   map[string]any{"tags": []string{"tag1", "tag2"}},
			want:    []any{"tag1", "tag2"},
			wantErr: false,
		},
		{
			name:    "[]int converted to []any",
			source:  "input.numbers",
			input:   map[string]any{"numbers": []int{1, 2, 3}},
			want:    []any{1, 2, 3},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			prog, err := compileExpr(tt.source)
			require.NoError(t, err, "unexpected compile error for source %q", tt.source)

			result, err := evaluateIteratorSource(ctx, prog, tt.source, tt.input, tt.variables, DefaultMaxIteratorItems)

			if tt.wantErr {
				require.Error(t, err)
				assert.IsType(t, tt.errType, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, result)
			}
		})
	}
}

func TestEvaluateIteratorSourceInvalidSyntax(t *testing.T) {
	t.Parallel()
	// Invalid expression syntax is caught at compile time (before evaluateIteratorSource is called).
	_, err := compileExpr("input.[invalid")
	require.Error(t, err)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
}

func TestEvaluateIteratorSourceMaxItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("exactly DefaultMaxIteratorItems []any succeeds", func(t *testing.T) {
		items := make([]any, DefaultMaxIteratorItems)
		for i := range items {
			items[i] = i
		}
		prog, err := compileExpr("input.items")
		require.NoError(t, err)
		result, err := evaluateIteratorSource(ctx, prog, "input.items", map[string]any{"items": items}, nil, DefaultMaxIteratorItems)
		require.NoError(t, err)
		assert.Len(t, result, DefaultMaxIteratorItems)
	})

	t.Run("[]any over limit returns EvalError", func(t *testing.T) {
		items := make([]any, DefaultMaxIteratorItems+1)
		prog, err := compileExpr("input.items")
		require.NoError(t, err)
		result, err := evaluateIteratorSource(ctx, prog, "input.items", map[string]any{"items": items}, nil, DefaultMaxIteratorItems)
		require.Error(t, err)
		var evalErr *EvalError
		assert.True(t, errors.As(err, &evalErr))
		assert.Contains(t, err.Error(), "exceeding maximum")
		assert.Nil(t, result)
	})

	t.Run("typed slice over limit returns EvalError", func(t *testing.T) {
		// Exercises the reflect path specifically.
		items := make([]string, DefaultMaxIteratorItems+1)
		prog, err := compileExpr("input.items")
		require.NoError(t, err)
		result, err := evaluateIteratorSource(ctx, prog, "input.items", map[string]any{"items": items}, nil, DefaultMaxIteratorItems)
		require.Error(t, err)
		var evalErr *EvalError
		assert.True(t, errors.As(err, &evalErr))
		assert.Contains(t, err.Error(), "exceeding maximum")
		assert.Nil(t, result)
	})
}

func TestEvaluateIteratorSourceContextDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Give the context time to expire
	time.Sleep(10 * time.Millisecond)

	prog, err := compileExpr("input.roles")
	require.NoError(t, err)
	result, err := evaluateIteratorSource(ctx, prog, "input.roles", map[string]any{"roles": []any{}}, map[string]any{}, DefaultMaxIteratorItems)
	assert.Error(t, err)
	var evalErr *EvalError
	assert.True(t, errors.As(err, &evalErr))
	assert.Nil(t, result)
}
