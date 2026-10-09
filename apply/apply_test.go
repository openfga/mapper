package apply

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper"
	"github.com/openfga/mapper/language"
)

type mockClient struct {
	calls    []language.TupleFilter
	results  map[mapper.Conflict][]language.Tuple
	readErr  error
	written  []language.Tuple
	writeErr error
}

func (m *mockClient) ReadTuples(_ context.Context, filter language.TupleFilter) ([]language.Tuple, error) {
	m.calls = append(m.calls, filter)
	if m.readErr != nil {
		return nil, m.readErr
	}
	key := mapper.Conflict{User: filter.User, Relation: filter.Relation, Object: filter.Object}
	return m.results[key], nil
}

func (m *mockClient) WriteTuples(_ context.Context, tuples []language.Tuple) error {
	m.written = tuples
	if m.writeErr != nil {
		return m.writeErr
	}
	return nil
}

func TestExecute_NoFilterOperations(t *testing.T) {
	c := &mockClient{}
	rec := New(c)

	result := &mapper.Result{
		Tuples: []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	assert.Equal(t, result.Tuples, c.written)
}

func TestExecute_EmptyResult(t *testing.T) {
	c := &mockClient{}
	rec := New(c)

	err := rec.Execute(t.Context(), &mapper.Result{})
	require.NoError(t, err)
	assert.Nil(t, c.written, "no write call should be made for empty result")
}

func TestExecute_PatchOperation(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:charlie", Relation: "member", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.NotNil(t, c.written)

	assert.Contains(t, c.written, language.Tuple{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite})
	assert.Contains(t, c.written, language.Tuple{User: "user:charlie", Relation: "member", Object: "org:1", Action: language.ActionDelete})
	assert.Len(t, c.written, 2, "alice is already correct, so only bob (write) and charlie (delete)")
}

func TestExecute_DeleteOperation(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:bob", Relation: "member", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.NotNil(t, c.written)

	assert.Contains(t, c.written, language.Tuple{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete})
	assert.Contains(t, c.written, language.Tuple{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionDelete})
	assert.Len(t, c.written, 2)
}

func TestExecute_CombinesNonFilteredWithFilterDeltas(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:old", Relation: "member", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		Tuples: []language.Tuple{
			{User: "user:direct", Relation: "viewer", Object: "doc:1", Action: language.ActionWrite},
		},
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:new", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.Len(t, c.written, 3)

	assert.Contains(t, c.written, language.Tuple{User: "user:direct", Relation: "viewer", Object: "doc:1", Action: language.ActionWrite})
	assert.Contains(t, c.written, language.Tuple{User: "user:new", Relation: "member", Object: "org:1", Action: language.ActionWrite})
	assert.Contains(t, c.written, language.Tuple{User: "user:old", Relation: "member", Object: "org:1", Action: language.ActionDelete})
}

func TestExecute_FilterDeduplication(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)

	assert.Len(t, c.calls, 1, "identical filters across operations should be read only once")
}

func TestExecute_ValidationFailure_PatchWithEmptyDesired(t *testing.T) {
	c := &mockClient{}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: nil,
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty desired-state tuples")
	assert.Empty(t, c.calls, "no reads should happen after validation failure")
	assert.Nil(t, c.written, "no writes should happen after validation failure")
}

func TestExecute_ValidationFailure_DesiredTupleUncoveredByAnyFilter(t *testing.T) {
	c := &mockClient{}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					// Covered: matches filter scope (relation=member, object=org:1).
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					// Uncovered: object org:2 does not match filter scope object org:1.
					{User: "user:alice", Relation: "member", Object: "org:2", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not covered")
	assert.Empty(t, c.calls, "no reads should happen after validation failure")
	assert.Nil(t, c.written, "no writes should happen after validation failure")
}

func TestExecute_DeleteWithEmptyDesired_IsValid(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
				Tuples: nil,
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
}

func TestExecute_ReadFailure_NoWrites(t *testing.T) {
	c := &mockClient{readErr: errors.New("FGA unavailable")}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FGA unavailable")
	assert.Nil(t, c.written, "no writes should happen after read failure")
}

func TestExecute_WriteFailure_IsWriteError(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
		},
		writeErr: errors.New("write failed"),
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")

	var writeErr *WriteError
	assert.True(t, errors.As(err, &writeErr), "write-phase errors should be wrapped as *WriteError")
}

func TestExecute_ReadFailure_IsNotWriteError(t *testing.T) {
	c := &mockClient{readErr: errors.New("read failed")}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)

	var writeErr *WriteError
	assert.False(t, errors.As(err, &writeErr), "read-phase errors must not be *WriteError")
}

func TestExecute_ValidationFailure_IsNotWriteError(t *testing.T) {
	c := &mockClient{}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: nil,
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)

	var writeErr *WriteError
	assert.False(t, errors.As(err, &writeErr), "validation errors must not be *WriteError")
}

func TestExecute_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	c := &mockClient{readErr: context.Canceled}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(ctx, result)
	require.Error(t, err)
}

func TestExecute_DeduplicatesCombinedTuples(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:stale", Relation: "member", Object: "org:1"},
			},
			{Relation: "viewer", Object: "org:1"}: {
				{User: "user:stale", Relation: "viewer", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		Tuples: []language.Tuple{
			{User: "user:new", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		},
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:new", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
			{
				Filters: []language.TupleFilter{
					{Relation: "viewer", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.NotNil(t, c.written)

	writeCount := 0
	for _, tup := range c.written {
		if tup.User == "user:new" && tup.Action == language.ActionWrite {
			writeCount++
		}
	}
	assert.Equal(t, 1, writeCount, "duplicate tuple from non-filtered + patch delta should be deduplicated")
	assert.Len(t, c.written, 3)
}

func TestExecute_Conflicts(t *testing.T) {
	tests := []struct {
		name         string
		storeState   map[mapper.Conflict][]language.Tuple
		result       *mapper.Result
		wantKind     mapper.ConflictKind
		wantUser     string
		wantRelation string
		wantObject   string
	}{
		{
			name: "cross-boundary write/delete: non-filtered write vs filter delta delete",
			storeState: map[mapper.Conflict][]language.Tuple{
				{Relation: "member", Object: "org:1"}: {
					{User: "user:alice", Relation: "member", Object: "org:1"},
					{User: "user:bob", Relation: "member", Object: "org:1"},
				},
			},
			result: &mapper.Result{
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
				TupleFilterOperations: []mapper.TupleFilterOperation{
					{
						Filters: []language.TupleFilter{
							{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
						},
						Tuples: []language.Tuple{
							{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
						},
					},
				},
			},
			wantKind: mapper.ConflictWriteDelete, wantUser: "user:alice", wantRelation: "member", wantObject: "org:1",
		},
		{
			name: "competing writes same URO different conditions: direct tuples",
			result: &mapper.Result{
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condA", Action: language.ActionWrite},
					{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condB", Action: language.ActionWrite},
				},
			},
			wantKind: mapper.ConflictCompetingWrites, wantUser: "user:alice", wantRelation: "member", wantObject: "org:1",
		},
		{
			name: "competing desired conditions in patch operation: one already in store",
			// Store has condA. Desired state has both condA and condB on the same URO.
			// diffFilter would strip condA as a no-op, so only write(condB) reaches
			// combineTuples — the conflict would be missed without the validate check.
			storeState: map[mapper.Conflict][]language.Tuple{
				{Relation: "member", Object: "org:1"}: {
					{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condA"},
				},
			},
			result: &mapper.Result{
				TupleFilterOperations: []mapper.TupleFilterOperation{
					{
						Filters: []language.TupleFilter{
							{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
						},
						Tuples: []language.Tuple{
							{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condA", Action: language.ActionWrite},
							{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condB", Action: language.ActionWrite},
						},
					},
				},
			},
			wantKind: mapper.ConflictCompetingWrites, wantUser: "user:alice", wantRelation: "member", wantObject: "org:1",
		},
		{
			name: "competing writes same URO different conditions: cross-boundary",
			storeState: map[mapper.Conflict][]language.Tuple{
				{Relation: "member", Object: "org:1"}: {},
			},
			result: &mapper.Result{
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condA", Action: language.ActionWrite},
				},
				TupleFilterOperations: []mapper.TupleFilterOperation{
					{
						Filters: []language.TupleFilter{
							{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
						},
						Tuples: []language.Tuple{
							{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condB", Action: language.ActionWrite},
						},
					},
				},
			},
			wantKind: mapper.ConflictCompetingWrites, wantUser: "user:alice", wantRelation: "member", wantObject: "org:1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &mockClient{results: tc.storeState}
			rec := New(c)

			err := rec.Execute(t.Context(), tc.result)
			require.Error(t, err)

			var conflictErr *mapper.ConflictError
			require.True(t, errors.As(err, &conflictErr))
			assert.Equal(t, tc.wantKind, conflictErr.Kind)
			assert.Equal(t, tc.wantUser, conflictErr.User)
			assert.Equal(t, tc.wantRelation, conflictErr.Relation)
			assert.Equal(t, tc.wantObject, conflictErr.Object)
			assert.Nil(t, c.written, "no writes should happen when a conflict is detected")
		})
	}
}

func TestExecute_MultiPatchFiltersScopesDesiredTuples(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
			},
			{Relation: "viewer", Object: "org:1"}: {
				{User: "user:charlie", Relation: "viewer", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	// Two patch filters with different relations. Desired tuples include both relations.
	// Each filter should only see desired tuples matching its scope.
	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
					{Relation: "viewer", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					{User: "user:charlie", Relation: "viewer", Object: "org:1", Action: language.ActionWrite},
					{User: "user:dave", Relation: "viewer", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.NotNil(t, c.written)

	// member filter: alice exists, bob is new -> write bob (no delete for alice)
	assert.Contains(t, c.written, language.Tuple{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite})
	// viewer filter: charlie exists, dave is new -> write dave (no delete for charlie)
	assert.Contains(t, c.written, language.Tuple{User: "user:dave", Relation: "viewer", Object: "org:1", Action: language.ActionWrite})

	// Without scoping, charlie would be deleted by the member filter (not in member desired)
	// and alice would be deleted by the viewer filter (not in viewer desired). Verify neither happens.
	for _, tup := range c.written {
		if tup.Action == language.ActionDelete {
			t.Errorf("unexpected delete: %+v (cross-scope desired tuples should not cause deletions)", tup)
		}
	}
	assert.Len(t, c.written, 2, "only bob and dave should be written")
}

func TestScopeDesired(t *testing.T) {
	desired := []language.Tuple{
		{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		{User: "user:bob", Relation: "viewer", Object: "org:1", Action: language.ActionWrite},
		{User: "user:charlie", Relation: "member", Object: "org:2", Action: language.ActionWrite},
	}

	tests := []struct {
		name     string
		filter   language.TupleFilter
		expected []language.Tuple
	}{
		{
			name:     "wildcard filter matches all",
			filter:   language.TupleFilter{Action: language.FilterActionPatch},
			expected: desired,
		},
		{
			name:   "filter by relation",
			filter: language.TupleFilter{Relation: "member", Action: language.FilterActionPatch},
			expected: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:charlie", Relation: "member", Object: "org:2", Action: language.ActionWrite},
			},
		},
		{
			name:   "filter by relation and object",
			filter: language.TupleFilter{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
			expected: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
		},
		{
			name:     "filter by user",
			filter:   language.TupleFilter{User: "user:bob", Action: language.FilterActionPatch},
			expected: []language.Tuple{{User: "user:bob", Relation: "viewer", Object: "org:1", Action: language.ActionWrite}},
		},
		{
			name:     "no matches",
			filter:   language.TupleFilter{User: "user:nobody", Action: language.FilterActionPatch},
			expected: nil,
		},
		{
			name:   "type-prefix object matches all objects of that type",
			filter: language.TupleFilter{Object: "org:", Action: language.FilterActionPatch},
			expected: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "viewer", Object: "org:1", Action: language.ActionWrite},
				{User: "user:charlie", Relation: "member", Object: "org:2", Action: language.ActionWrite},
			},
		},
		{
			name:   "type-prefix object with relation filter",
			filter: language.TupleFilter{Relation: "member", Object: "org:", Action: language.FilterActionPatch},
			expected: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:charlie", Relation: "member", Object: "org:2", Action: language.ActionWrite},
			},
		},
		{
			name:     "type-prefix object does not match different type",
			filter:   language.TupleFilter{Object: "group:", Action: language.FilterActionPatch},
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scopeDesired(tc.filter, desired)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestExecute_MixedPatchAndDeleteFilters(t *testing.T) {
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:charlie", Relation: "member", Object: "org:1"},
			},
			{Relation: "admin", Object: "org:1"}: {
				{User: "user:old-admin", Relation: "admin", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
					{Relation: "admin", Object: "org:1", Action: language.FilterActionDelete},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.NotNil(t, c.written)

	assert.Contains(t, c.written, language.Tuple{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite})
	assert.Contains(t, c.written, language.Tuple{User: "user:charlie", Relation: "member", Object: "org:1", Action: language.ActionDelete})
	assert.Contains(t, c.written, language.Tuple{User: "user:old-admin", Relation: "admin", Object: "org:1", Action: language.ActionDelete})
	assert.Len(t, c.written, 3)
}

func TestExecute_DirectAndFilterDerivedDeleteSameURO_Deduplicated(t *testing.T) {
	// A direct-delete tuple (no condition) and a filter-derived delete of the same URO
	// (with a condition from the store) must be deduplicated to one delete before write.
	// The FGA delete API ignores condition — both map to the same delete key, and a batch
	// with duplicate keys is rejected by the server.
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1", Condition: "condX"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		Tuples: []language.Tuple{
			// Direct delete: no condition — distinct diffKey from filter-derived delete(condX).
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
		},
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				// Delete filter reads the store and derives delete(alice, member, org:1, condX).
				Filters: []language.TupleFilter{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.FilterActionDelete},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)

	var deletes []language.Tuple
	for _, tup := range c.written {
		if tup.Action == language.ActionDelete {
			deletes = append(deletes, tup)
		}
	}
	assert.Len(t, deletes, 1, "direct delete and filter-derived delete on same URO should be deduplicated to one")
}

func TestExecute_OverlappingPatchOperationsConflict(t *testing.T) {
	// Two patch operations target the same filter scope, each desiring a different subset
	// of already-stored tuples. Each diff produces no writes (the desired tuple is already
	// present) but does produce a delete (the other tuple isn't desired). Without satisfied-
	// claim tracking, both deletes go through and both relationships are removed.
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1"},
				{User: "user:bob", Relation: "member", Object: "org:1"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)

	var conflictErr *mapper.ConflictError
	require.True(t, errors.As(err, &conflictErr))
	assert.Equal(t, mapper.ConflictWriteDelete, conflictErr.Kind)
	assert.Nil(t, c.written, "no writes should happen when a conflict is detected")
}

func TestExecute_ValidationFailure_DesiredTupleCoveredOnlyByDeleteFilter(t *testing.T) {
	// A desired tuple that falls within a delete filter's scope but matches no patch filter
	// must fail validation — the delete filter will never write it.
	c := &mockClient{}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
					{Relation: "admin", Object: "org:1", Action: language.FilterActionDelete},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					// Covered only by the delete filter's scope — no patch filter will write it.
					{User: "user:alice", Relation: "admin", Object: "org:1", Action: language.ActionWrite},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not covered")
	assert.Empty(t, c.calls, "no reads should happen after validation failure")
	assert.Nil(t, c.written, "no writes should happen after validation failure")
}

func TestExecute_ConditionChangeDeltaIsNotConflict(t *testing.T) {
	// A patch filter diff that replaces a tuple's condition (delete old + write new on the same
	// user/relation/object) must not be treated as a cross-boundary conflict.
	c := &mockClient{
		results: map[mapper.Conflict][]language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1"}: {
				{User: "user:alice", Relation: "member", Object: "org:1", Condition: "cond_old"},
			},
		},
	}
	rec := New(c)

	result := &mapper.Result{
		TupleFilterOperations: []mapper.TupleFilterOperation{
			{
				Filters: []language.TupleFilter{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.FilterActionPatch},
				},
				Tuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Condition: "cond_new"},
				},
			},
		},
	}

	err := rec.Execute(t.Context(), result)
	require.NoError(t, err)
	require.NotNil(t, c.written)

	assert.Contains(t, c.written, language.Tuple{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete, Condition: "cond_old"})
	assert.Contains(t, c.written, language.Tuple{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite, Condition: "cond_new"})
	assert.Len(t, c.written, 2)
}
