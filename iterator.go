package mapper

import (
	"context"
	"fmt"
	"reflect"

	"github.com/expr-lang/expr/vm"
)

// evaluateIteratorSource evaluates the pre-compiled source program for an iterator rule.
// Returns the resolved collection as []any. Returns *EvalError if the expression fails
// to evaluate or the result is not a slice/array.
// A nil or missing result is treated as an empty collection (not an error).
func evaluateIteratorSource(
	ctx context.Context,
	program *vm.Program,
	sourceCode string,
	input map[string]any,
	variables map[string]any,
	maxItems int,
) ([]any, error) {
	// Check context deadline
	if err := ctx.Err(); err != nil {
		return nil, &EvalError{Expression: sourceCode, Err: err}
	}

	// A nil program means the source was empty (validation would have caught this).
	if program == nil {
		return []any{}, nil
	}

	// Build environment with input and variables
	env := map[string]any{
		"input":     input,
		"variables": variables,
	}

	// Run the pre-compiled program.
	result, err := runExpr(program, env, sourceCode)
	if err != nil {
		return nil, err
	}

	// nil or missing field → return empty collection (not an error)
	if result == nil {
		return []any{}, nil
	}

	// Handle []any directly
	if arr, ok := result.([]any); ok {
		if len(arr) > maxItems {
			return nil, &EvalError{
				Expression: sourceCode,
				Err:        fmt.Errorf("iterator source has %d items, exceeding maximum of %d", len(arr), maxItems),
			}
		}
		return arr, nil
	}

	// Handle other slice types via reflection
	val := reflect.ValueOf(result)
	if val.Kind() == reflect.Slice {
		length := val.Len()
		if length > maxItems {
			return nil, &EvalError{
				Expression: sourceCode,
				Err:        fmt.Errorf("iterator source has %d items, exceeding maximum of %d", length, maxItems),
			}
		}
		arr := make([]any, length)
		for i := 0; i < length; i++ {
			arr[i] = val.Index(i).Interface()
		}
		return arr, nil
	}

	// Non-slice non-nil result is an error
	return nil, &EvalError{
		Expression: sourceCode,
		Err:        fmt.Errorf("iterator source must be an array, got %T", result),
	}
}
