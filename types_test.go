package mapper

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper/language"
)

func TestErrorMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		message string
	}{
		{
			name:    "EvalError",
			err:     &EvalError{Expression: "1 + x", Err: fmt.Errorf("undefined: x")},
			message: `evaluation error in expression "1 + x": undefined: x`,
		},
		{
			name:    "ValidationError",
			err:     &language.ValidationError{Field: "version", Message: "is required"},
			message: "validation error: version: is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.EqualError(t, tt.err, tt.message)
		})
	}
}

func TestErrorTypesDistinguishableViaErrorsAs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		eval bool
		val  bool
	}{
		{
			name: "EvalError",
			err:  &EvalError{Expression: "x", Err: fmt.Errorf("fail")},
			eval: true,
		},
		{
			name: "ValidationError",
			err:  &language.ValidationError{Field: "f", Message: "m"},
			val:  true,
		},
		{
			name: "wrapped EvalError",
			err:  fmt.Errorf("wrapped: %w", &EvalError{Expression: "x", Err: fmt.Errorf("fail")}),
			eval: true,
		},
		{
			name: "wrapped ValidationError",
			err:  fmt.Errorf("wrapped: %w", &language.ValidationError{Field: "f", Message: "m"}),
			val:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var evalErr *EvalError
			var valErr *language.ValidationError

			assert.Equal(t, tt.eval, errors.As(tt.err, &evalErr), "EvalError match")
			assert.Equal(t, tt.val, errors.As(tt.err, &valErr), "ValidationError match")
		})
	}
}

func TestConflictErrorMessage(t *testing.T) {
	t.Parallel()
	t.Run("without condition", func(t *testing.T) {
		err := &ConflictError{Conflict: Conflict{User: "u:1", Relation: "viewer", Object: "doc:x"}}
		assert.EqualError(t, err, "conflict: tuple (u:1, viewer, doc:x) has both a write and a delete action")
	})
	t.Run("with condition", func(t *testing.T) {
		err := &ConflictError{Conflict: Conflict{User: "u:1", Relation: "viewer", Object: "doc:x"}, Condition: "my_cond"}
		assert.EqualError(t, err, `conflict: tuple (u:1, viewer, doc:x) with condition "my_cond" has both a write and a delete action`)
	})
}

func TestPostProcess_URODedup(t *testing.T) {
	t.Parallel()
	t.Run("duplicate writes with identical identity are deduplicated", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "cond_a"},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "cond_a"},
			},
		}
		err := r.postProcess()
		require.NoError(t, err)
		assert.Len(t, r.Tuples, 1)
	})

	t.Run("duplicate deletes on the same URO are deduplicated", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionDelete},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionDelete},
			},
		}
		err := r.postProcess()
		require.NoError(t, err)
		assert.Len(t, r.Tuples, 1)
	})
}

func TestPostProcess_UROConflict(t *testing.T) {
	t.Parallel()
	// OpenFGA identifies a relationship by (user, relation, object); condition and
	// context are payload, not identity. Two competing writes on one URO, or a
	// write and a delete on one URO, cannot both be sent in a single Write batch.

	t.Run("two writes on same URO with different conditions conflict", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "cond_a"},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "cond_b"},
			},
		}
		err := r.postProcess()
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictCompetingWrites, ce.Kind)
	})

	t.Run("two writes on same URO with different context conflict", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "c", Context: map[string]any{"region": "us"}},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "c", Context: map[string]any{"region": "eu"}},
			},
		}
		err := r.postProcess()
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictCompetingWrites, ce.Kind)
	})

	t.Run("write and delete on same URO with different conditions conflict", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "cond_new"},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionDelete, Condition: "cond_old"},
			},
		}
		err := r.postProcess()
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictWriteDelete, ce.Kind)
	})

	t.Run("write and delete with same condition is a conflict", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite, Condition: "cond_a"},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionDelete, Condition: "cond_a"},
			},
		}
		err := r.postProcess()
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictWriteDelete, ce.Kind)
		assert.Equal(t, "cond_a", ce.Condition)
	})

	t.Run("write and delete without condition is a conflict", func(t *testing.T) {
		r := &Result{
			Tuples: []language.Tuple{
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionWrite},
				{User: "u:1", Relation: "viewer", Object: "doc:x", Action: language.ActionDelete},
			},
		}
		err := r.postProcess()
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictWriteDelete, ce.Kind)
		assert.Empty(t, ce.Condition)
	})
}

func TestErrorUnwrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "EvalError",
			err:  &EvalError{Expression: "x", Err: fmt.Errorf("inner")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unwrapper, ok := tt.err.(interface{ Unwrap() error })
			assert.True(t, ok)
			assert.NotNil(t, unwrapper.Unwrap())
		})
	}
}
