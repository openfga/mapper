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

func TestCompact(t *testing.T) {
	t.Parallel()

	t.Run("nil input — returns nil, no error", func(t *testing.T) {
		collapsed, err := Compact(nil)
		require.NoError(t, err)
		assert.Nil(t, collapsed)
	})

	t.Run("empty input — returns empty, no error", func(t *testing.T) {
		collapsed, err := Compact([]language.Tuple{})
		require.NoError(t, err)
		assert.Empty(t, collapsed)
	})

	t.Run("single tuple — returned unchanged", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		}
		collapsed, err := Compact(input)
		require.NoError(t, err)
		assert.Equal(t, input, collapsed)
	})

	t.Run("distinct tuples — all preserved in order", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			{User: "user:bob", Relation: "viewer", Object: "doc:1", Action: language.ActionDelete},
		}
		collapsed, err := Compact(input)
		require.NoError(t, err)
		assert.Equal(t, input, collapsed)
	})

	t.Run("exact-identity duplicates — first occurrence wins, order preserved", func(t *testing.T) {
		t1 := language.Tuple{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite}
		t2 := language.Tuple{User: "user:bob", Relation: "viewer", Object: "org:1", Action: language.ActionWrite}
		input := []language.Tuple{t1, t2, t1} // t1 appears twice, t2 in between
		collapsed, err := Compact(input)
		require.NoError(t, err)
		assert.Equal(t, []language.Tuple{t1, t2}, collapsed)
	})

	t.Run("order preservation with mixed duplicates", func(t *testing.T) {
		t1 := language.Tuple{User: "user:a", Relation: "r", Object: "o:1", Action: language.ActionWrite}
		t2 := language.Tuple{User: "user:b", Relation: "r", Object: "o:1", Action: language.ActionWrite}
		t3 := language.Tuple{User: "user:c", Relation: "r", Object: "o:1", Action: language.ActionWrite}
		input := []language.Tuple{t1, t2, t1, t3, t2}
		collapsed, err := Compact(input)
		require.NoError(t, err)
		assert.Equal(t, []language.Tuple{t1, t2, t3}, collapsed)
	})

	t.Run("same URO differing condition — two writes are a ConflictCompetingWrites", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite, Condition: "cond_a"},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite, Condition: "cond_b"},
		}
		_, err := Compact(input)
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictCompetingWrites, ce.Kind)
		assert.Equal(t, "user:alice", ce.User)
		assert.Equal(t, "member", ce.Relation)
		assert.Equal(t, "org:1", ce.Object)
	})

	t.Run("same URO differing context — two writes are a ConflictCompetingWrites", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite, Condition: "c", Context: map[string]any{"k": "v1"}},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite, Condition: "c", Context: map[string]any{"k": "v2"}},
		}
		_, err := Compact(input)
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictCompetingWrites, ce.Kind)
	})

	t.Run("write then delete on same URO — ConflictWriteDelete, condition from write", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite, Condition: "cond_a"},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
		}
		_, err := Compact(input)
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictWriteDelete, ce.Kind)
		assert.Equal(t, "cond_a", ce.Condition)
	})

	t.Run("delete then write on same URO — ConflictWriteDelete", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		}
		_, err := Compact(input)
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ConflictWriteDelete, ce.Kind)
	})

	t.Run("repeated identical deletes — collapse to one, no error", func(t *testing.T) {
		del := language.Tuple{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete}
		input := []language.Tuple{del, del, del}
		collapsed, err := Compact(input)
		require.NoError(t, err)
		assert.Equal(t, []language.Tuple{del}, collapsed)
	})

	t.Run("repeated deletes on same URO with differing conditions — collapse to first, no error", func(t *testing.T) {
		// URO-level dedup: delete targets (user,relation,object) regardless of condition.
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete, Condition: "cond_a"},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete, Condition: "cond_b"},
		}
		collapsed, err := Compact(input)
		require.NoError(t, err)
		require.Len(t, collapsed, 1)
		assert.Equal(t, "cond_a", collapsed[0].Condition)
	})

	t.Run("unknown action — returns ValidationError", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: "bogus"},
		}
		_, err := Compact(input)
		require.Error(t, err)
		var ve *language.ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Contains(t, ve.Message, `unknown tuple action`)
		assert.Contains(t, ve.Message, `"bogus"`)
	})

	t.Run("fast path — only first conflict returned", func(t *testing.T) {
		input := []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			{User: "user:bob", Relation: "viewer", Object: "org:2", Action: language.ActionWrite},
			{User: "user:bob", Relation: "viewer", Object: "org:2", Action: language.ActionDelete},
		}
		_, err := Compact(input)
		require.Error(t, err)
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, "user:alice", ce.User) // first conflict, not the second
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
