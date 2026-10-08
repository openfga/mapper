package apply

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openfga/mapper/language"
)

func TestDiffFilter(t *testing.T) {
	tests := []struct {
		name       string
		action     language.TupleFilterAction
		existing   []language.Tuple
		desired    []language.Tuple
		wantWrite  []language.Tuple
		wantDelete []language.Tuple
	}{
		{
			name:   "patch with overlap keeps only deltas",
			action: language.FilterActionPatch,
			existing: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:bob", Relation: "member", Object: "org:1"},
				{User: "user:charlie", Relation: "member", Object: "org:1"},
			},
			desired: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:bob", Relation: "member", Object: "org:1"},
				{User: "user:dave", Relation: "member", Object: "org:1"},
			},
			wantWrite: []language.Tuple{
				{User: "user:dave", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantDelete: []language.Tuple{
				{User: "user:charlie", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
		},
		{
			name:   "patch with no overlap writes all desired and deletes all existing",
			action: language.FilterActionPatch,
			existing: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
			desired: []language.Tuple{
				{User: "user:bob", Relation: "member", Object: "org:1"},
			},
			wantWrite: []language.Tuple{
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantDelete: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
		},
		{
			name:   "patch with full overlap is a no-op",
			action: language.FilterActionPatch,
			existing: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
			desired: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
			wantWrite:  nil,
			wantDelete: nil,
		},
		{
			name:     "patch with empty existing writes all desired",
			action:   language.FilterActionPatch,
			existing: nil,
			desired: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
			wantWrite: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantDelete: nil,
		},
		{
			name:   "delete returns all existing as deletes",
			action: language.FilterActionDelete,
			existing: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:bob", Relation: "member", Object: "org:1"},
			},
			desired:   nil,
			wantWrite: nil,
			wantDelete: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
		},
		{
			name:       "delete with empty existing is a no-op",
			action:     language.FilterActionDelete,
			existing:   nil,
			desired:    nil,
			wantWrite:  nil,
			wantDelete: nil,
		},
		{
			name:   "delete ignores desired tuples",
			action: language.FilterActionDelete,
			existing: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
			desired: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:bob", Relation: "member", Object: "org:1"},
			},
			wantWrite: nil,
			wantDelete: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
		},
		{
			name:   "patch ignores action field on input tuples when comparing",
			action: language.FilterActionPatch,
			existing: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			desired: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantWrite:  nil,
			wantDelete: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toWrite, toDelete := diffFilter(tc.action, tc.existing, tc.desired)
			assert.Equal(t, tc.wantWrite, toWrite)
			assert.Equal(t, tc.wantDelete, toDelete)
		})
	}
}

func TestDiffFilter_ConditionChange(t *testing.T) {
	// Same (U, R, O) but different condition -> delete old + write new
	existing := []language.Tuple{
		{User: "user:alice", Relation: "viewer", Object: "doc:1", Condition: "cond_a",
			Context: map[string]any{"k": "v1"}},
	}
	desired := []language.Tuple{
		{User: "user:alice", Relation: "viewer", Object: "doc:1", Condition: "cond_b",
			Context: map[string]any{"k": "v2"}},
	}

	toWrite, toDelete := diffFilter(language.FilterActionPatch, existing, desired)

	assert.Len(t, toWrite, 1)
	assert.Len(t, toDelete, 1)
	assert.Equal(t, "cond_b", toWrite[0].Condition)
	assert.Equal(t, "v2", toWrite[0].Context["k"])
	assert.Equal(t, "cond_a", toDelete[0].Condition)
	assert.Equal(t, "v1", toDelete[0].Context["k"])
}

func TestDiffFilter_ConditionUnchanged(t *testing.T) {
	// Same (U, R, O) and same condition+context -> no delta
	existing := []language.Tuple{
		{User: "user:alice", Relation: "viewer", Object: "doc:1", Condition: "cond_a",
			Context: map[string]any{"k": "v1"}},
	}
	desired := []language.Tuple{
		{User: "user:alice", Relation: "viewer", Object: "doc:1", Condition: "cond_a",
			Context: map[string]any{"k": "v1"}},
	}

	toWrite, toDelete := diffFilter(language.FilterActionPatch, existing, desired)

	assert.Empty(t, toWrite)
	assert.Empty(t, toDelete)
}

func TestDiffFilter_ConditionAdded(t *testing.T) {
	// Existing has no condition, desired adds one -> delete old + write new
	existing := []language.Tuple{
		{User: "user:alice", Relation: "viewer", Object: "doc:1"},
	}
	desired := []language.Tuple{
		{User: "user:alice", Relation: "viewer", Object: "doc:1", Condition: "cond_a"},
	}

	toWrite, toDelete := diffFilter(language.FilterActionPatch, existing, desired)

	assert.Len(t, toWrite, 1)
	assert.Len(t, toDelete, 1)
	assert.Equal(t, "cond_a", toWrite[0].Condition)
	assert.Empty(t, toDelete[0].Condition)
}
