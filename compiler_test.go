package mapper

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompilerErr(t *testing.T) {
	t.Parallel()

	t.Run("nil compiler reports error", func(t *testing.T) {
		t.Parallel()
		var c *Compiler
		require.Error(t, c.Err())
	})

	t.Run("well-configured compiler has no error", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, NewCompiler().Err())
		assert.NoError(t, NewCompiler(
			WithTimeout(time.Second),
			WithMaxTuples(10),
			WithMaxRules(10),
			WithMaxIteratorItems(10),
			WithTrace(true),
		).Err())
	})

	t.Run("non-positive option values are recorded", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name string
			opt  Option
			want string
		}{
			{"timeout", WithTimeout(0), "timeout must be positive"},
			{"max tuples", WithMaxTuples(0), "max tuples must be positive"},
			{"max rules", WithMaxRules(-1), "max rules must be positive"},
			{"max iterator items", WithMaxIteratorItems(0), "max iterator items must be positive"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				c := NewCompiler(tt.opt)
				err := c.Err()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("multiple bad options are joined", func(t *testing.T) {
		t.Parallel()
		c := NewCompiler(WithMaxRules(0), WithMaxIteratorItems(-5))
		err := c.Err()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max rules must be positive")
		assert.Contains(t, err.Error(), "max iterator items must be positive")
	})
}

func TestCompileSurfacesOptionError(t *testing.T) {
	t.Parallel()

	// A recorded option error must short-circuit Compile before parsing, and be the
	// same error Err() reports.
	c := NewCompiler(WithMaxRules(0))
	_, err := c.Compile([]byte(`version: "1"`))
	require.Error(t, err)
	assert.Equal(t, c.Err(), err)
	assert.Contains(t, err.Error(), "max rules must be positive")
}

func TestWithMaxRulesEnforced(t *testing.T) {
	t.Parallel()

	yaml := []byte(`
version: "1"
rules:
  - name: "r0"
    tuples:
      - user: "user:1"
        relation: "member"
        object: "org:acme"
  - name: "r1"
    tuples:
      - user: "user:2"
        relation: "member"
        object: "org:acme"
`)

	// Default cap admits two rules.
	_, err := NewCompiler().Compile(yaml)
	require.NoError(t, err)

	// A caller-tightened cap of one rejects the second rule at parse time.
	_, err = NewCompiler(WithMaxRules(1)).Compile(yaml)
	require.Error(t, err)
	verrs := allValidationErrors(err)
	require.Len(t, verrs, 1)
	assert.Equal(t, "rules", verrs[0].Field)
	assert.Contains(t, verrs[0].Message, "exceeds maximum of 1 rules")
}

func TestWithMaxIteratorItemsEnforced(t *testing.T) {
	t.Parallel()

	yaml := []byte(`
version: "1"
rules:
  - name: "fan-out"
    iterator:
      source: input.roles
      as: role
      tuples:
        - user: "user:{{ input.id }}"
          relation: "member"
          object: "org:{{ role }}"
`)

	event := map[string]any{
		"id":    "1",
		"roles": []any{"admin", "editor"},
	}

	// Default cap admits a two-item source.
	m, err := NewCompiler().Compile(yaml)
	require.NoError(t, err)
	res, err := m.Evaluate(t.Context(), event)
	require.NoError(t, err)
	assert.Len(t, res.Tuples, 2)

	// A caller-tightened cap of one rejects the two-item source at evaluation time.
	m, err = NewCompiler(WithMaxIteratorItems(1)).Compile(yaml)
	require.NoError(t, err)
	_, err = m.Evaluate(t.Context(), event)
	require.Error(t, err)
	evalErrs := allEvalErrors(err)
	require.NotEmpty(t, evalErrs)
	assert.Contains(t, err.Error(), "exceeding maximum of 1")
}

func TestCompileReader(t *testing.T) {
	t.Parallel()

	yaml := `
version: "1"
rules:
  - name: "r0"
    tuples:
      - user: "user:{{ input.id }}"
        relation: "member"
        object: "org:acme"
`

	t.Run("compiles from a reader", func(t *testing.T) {
		t.Parallel()
		m, err := NewCompiler().CompileReader(strings.NewReader(yaml))
		require.NoError(t, err)
		require.NotNil(t, m)

		res, err := m.Evaluate(t.Context(), map[string]any{"id": "1"})
		require.NoError(t, err)
		require.Len(t, res.Tuples, 1)
		assert.Equal(t, "user:1", res.Tuples[0].User)
	})

	t.Run("propagates a read error", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("boom")
		_, err := NewCompiler().CompileReader(errReader{err: sentinel})
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Contains(t, err.Error(), "reading mapping")
	})

	t.Run("surfaces a recorded option error", func(t *testing.T) {
		t.Parallel()
		_, err := NewCompiler(WithMaxRules(0)).CompileReader(strings.NewReader(yaml))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max rules must be positive")
	})
}

// errReader is an io.Reader that always fails, used to exercise CompileReader's
// read-error path.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
