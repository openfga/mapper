package language

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPositionJSONWithOmitzero(t *testing.T) {
	t.Parallel()
	type container struct {
		Name     string   `json:"name"`
		Position Position `json:"position,omitzero"`
	}

	t.Run("zero position omitted", func(t *testing.T) {
		c := container{Name: "test"}
		got, err := json.Marshal(c)
		require.NoError(t, err)
		assert.Equal(t, `{"name":"test"}`, string(got))
	})

	t.Run("non-zero position included", func(t *testing.T) {
		c := container{Name: "test", Position: Position{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 5}}
		got, err := json.Marshal(c)
		require.NoError(t, err)
		assert.Contains(t, string(got), `"position"`)
		assert.Contains(t, string(got), `"startLine":1`)
	})
}

func TestValidationErrorMessage(t *testing.T) {
	t.Parallel()
	err := &ValidationError{Field: "version", Message: "is required"}
	assert.EqualError(t, err, "validation error: version: is required")
}

func TestTuple_Key(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		a, b   Tuple
		expect bool // true = same key
	}{
		{
			name:   "identical tuples same key",
			a:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite},
			b:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite},
			expect: true,
		},
		{
			name:   "different action different key",
			a:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite},
			b:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionDelete},
			expect: false,
		},
		{
			name:   "same tuple with condition same key",
			a:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "cond_a"},
			b:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "cond_a"},
			expect: true,
		},
		{
			name:   "different condition different key",
			a:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "cond_a"},
			b:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "cond_b"},
			expect: false,
		},
		{
			name:   "condition vs no condition different key",
			a:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "cond_a"},
			b:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite},
			expect: false,
		},
		{
			name: "same condition and context same key",
			a: Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "c",
				Context: map[string]any{"k": "v"}},
			b: Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "c",
				Context: map[string]any{"k": "v"}},
			expect: true,
		},
		{
			name: "different context different key",
			a: Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "c",
				Context: map[string]any{"k": "v1"}},
			b: Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "c",
				Context: map[string]any{"k": "v2"}},
			expect: false,
		},
		{
			name: "context key order does not matter",
			a: Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "c",
				Context: map[string]any{"a": "1", "b": "2"}},
			b: Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite, Condition: "c",
				Context: map[string]any{"b": "2", "a": "1"}},
			expect: true,
		},
		{
			name:   "no condition or context same key",
			a:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite},
			b:      Tuple{User: "u:1", Relation: "r", Object: "o:1", Action: ActionWrite},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka := tt.a.Key()
			kb := tt.b.Key()
			if tt.expect {
				assert.Equal(t, ka, kb)
			} else {
				assert.NotEqual(t, ka, kb)
			}
		})
	}
}
