package mapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRulesProjection(t *testing.T) {
	t.Parallel()

	yaml := []byte(`
version: "1"
rules:
  - name: "with-iterator"
    tuple_filters:
      - user: "user:{{ input.id }}"
        relation: "member"
        object: "org:{{ input.id }}"
    iterator:
      source: input.roles
      as: role
      tuples:
        - user: "user:{{ input.id }}"
          relation: "member"
          object: "org:{{ role }}"
          condition: "in_region"
          context:
            region: "{{ input.region }}"
    tuples:
      - user: "user:{{ input.id }}"
        relation: "owner"
        object: "org:acme"
`)

	m, err := NewCompiler().Compile(yaml)
	require.NoError(t, err)

	rules := m.Rules()
	require.Len(t, rules, 1)
	r := rules[0]
	assert.Equal(t, "with-iterator", r.Name)

	// Rule-level tuples.
	require.Len(t, r.Tuples, 1)
	assert.Equal(t, "user:{{ input.id }}", r.Tuples[0].User)
	assert.Equal(t, "owner", r.Tuples[0].Relation)
	assert.Equal(t, "org:acme", r.Tuples[0].Object)

	// Iterator-tuples projection branch.
	require.Len(t, r.IteratorTuples, 1)
	it := r.IteratorTuples[0]
	assert.Equal(t, "user:{{ input.id }}", it.User)
	assert.Equal(t, "member", it.Relation)
	assert.Equal(t, "org:{{ role }}", it.Object)
	assert.Equal(t, "in_region", it.Condition)
	require.Contains(t, it.Context, "region")
	assert.Equal(t, "{{ input.region }}", it.Context["region"])

	// Tuple-filter projection branch.
	require.Len(t, r.TupleFilters, 1)
	assert.Equal(t, "user:{{ input.id }}", r.TupleFilters[0].User)
	assert.Equal(t, "member", r.TupleFilters[0].Relation)
	assert.Equal(t, "org:{{ input.id }}", r.TupleFilters[0].Object)
}

// TestRulesContextIsDeepCopied asserts the Rules() contract: mutating a returned
// TupleTemplate.Context must not affect the compiled mapping's internal state.
// A direct reference assignment in tupleTemplateFrom would fail this.
func TestRulesContextIsDeepCopied(t *testing.T) {
	t.Parallel()

	yaml := []byte(`
version: "1"
rules:
  - name: "r0"
    tuples:
      - user: "user:{{ input.id }}"
        relation: "member"
        object: "org:acme"
        condition: "in_region"
        context:
          region: "{{ input.region }}"
`)

	m, err := NewCompiler().Compile(yaml)
	require.NoError(t, err)

	first := m.Rules()
	require.Len(t, first, 1)
	require.Len(t, first[0].Tuples, 1)
	require.Contains(t, first[0].Tuples[0].Context, "region")

	first[0].Tuples[0].Context["region"] = "mutated"

	second := m.Rules()
	require.Len(t, second, 1)
	require.Len(t, second[0].Tuples, 1)
	assert.Equal(t, "{{ input.region }}", second[0].Tuples[0].Context["region"],
		"mutating a returned Context must not leak into the compiled mapping")
}
