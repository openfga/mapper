package language

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// positionsFixture exercises every field that stampPositions wires: the rule
// when guard, a variable, tuple-filter user/relation/object, the iterator source,
// iterator-tuple user/relation/object plus a context entry, and a rule-level
// tuple's when/user/relation/object. Line/column numbers in the assertions below
// are 1-based and refer to this literal, so keep the two in sync when editing.
const positionsFixture = `version: "1"
rules:
  - name: "r0"
    when: input.type == "x"
    variables:
      v1: input.a
    tuple_filters:
      - user: "user:{{ input.id }}"
        relation: "member"
        object: "org:{{ input.org }}"
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
      - when: input.active
        user: "user:{{ input.id }}"
        relation: "owner"
        object: "org:acme"
`

func TestStampPositionsWiresEveryField(t *testing.T) {
	cfg, err := Parse([]byte(positionsFixture))
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 1)

	rule := cfg.Rules[0]

	// Rule when guard.
	assert.Equal(t, Position{StartLine: 4, StartColumn: 11, EndLine: 4, EndColumn: 11}, rule.WhenPos, "rule.WhenPos")

	// Variable entry.
	require.Len(t, rule.Variables, 1)
	assert.Equal(t, Position{StartLine: 6, StartColumn: 11, EndLine: 6, EndColumn: 11}, rule.Variables[0].Position, "variable position")

	// Tuple-filter fields.
	require.Len(t, rule.TupleFilters, 1)
	filter := rule.TupleFilters[0]
	assert.Equal(t, Position{StartLine: 8, StartColumn: 15, EndLine: 8, EndColumn: 34}, filter.UserPos, "tuple filter UserPos")
	assert.Equal(t, Position{StartLine: 9, StartColumn: 19, EndLine: 9, EndColumn: 25}, filter.RelationPos, "tuple filter RelationPos")
	assert.Equal(t, Position{StartLine: 10, StartColumn: 17, EndLine: 10, EndColumn: 36}, filter.ObjectPos, "tuple filter ObjectPos")

	// Iterator source.
	require.NotNil(t, rule.Iterator)
	assert.Equal(t, Position{StartLine: 12, StartColumn: 15, EndLine: 12, EndColumn: 15}, rule.Iterator.SourcePos, "iterator.SourcePos")

	// Iterator tuple fields.
	require.Len(t, rule.Iterator.Tuples, 1)
	iterTuple := rule.Iterator.Tuples[0]
	assert.Equal(t, Position{StartLine: 15, StartColumn: 17, EndLine: 15, EndColumn: 36}, iterTuple.UserPos, "iterator tuple UserPos")
	assert.Equal(t, Position{StartLine: 16, StartColumn: 21, EndLine: 16, EndColumn: 27}, iterTuple.RelationPos, "iterator tuple RelationPos")
	assert.Equal(t, Position{StartLine: 17, StartColumn: 19, EndLine: 17, EndColumn: 33}, iterTuple.ObjectPos, "iterator tuple ObjectPos")
	require.Contains(t, iterTuple.ContextPos, "region")
	assert.Equal(t, Position{StartLine: 20, StartColumn: 21, EndLine: 20, EndColumn: 39}, iterTuple.ContextPos["region"], "iterator tuple ContextPos[region]")

	// Rule-level tuple fields.
	require.Len(t, rule.Tuples, 1)
	tuple := rule.Tuples[0]
	assert.Equal(t, Position{StartLine: 22, StartColumn: 15, EndLine: 22, EndColumn: 15}, tuple.WhenPos, "rule tuple WhenPos")
	assert.Equal(t, Position{StartLine: 23, StartColumn: 15, EndLine: 23, EndColumn: 34}, tuple.UserPos, "rule tuple UserPos")
	assert.Equal(t, Position{StartLine: 24, StartColumn: 19, EndLine: 24, EndColumn: 24}, tuple.RelationPos, "rule tuple RelationPos")
	assert.Equal(t, Position{StartLine: 25, StartColumn: 17, EndLine: 25, EndColumn: 25}, tuple.ObjectPos, "rule tuple ObjectPos")
}
