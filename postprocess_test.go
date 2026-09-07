package mapper

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfga/mapper/language"
)

func TestResult_PostProcess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		tuples          []language.Tuple
		wantTuples      []language.Tuple
		wantErr         bool
		wantPostProcess *PostProcessResult
	}{
		{
			name:            "empty tuples — no metadata",
			tuples:          []language.Tuple{},
			wantTuples:      []language.Tuple{},
			wantErr:         false,
			wantPostProcess: nil,
		},
		{
			name: "single tuple — no metadata",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantErr:         false,
			wantPostProcess: nil,
		},
		{
			name: "no duplicates no conflicts — no metadata",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "owner", Object: "org:1", Action: language.ActionWrite},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "owner", Object: "org:1", Action: language.ActionWrite},
			},
			wantErr:         false,
			wantPostProcess: nil,
		},
		{
			name: "identical tuples deduplicated",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantErr: false,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
				Conflicts: nil,
			},
		},
		{
			name: "duplicate mid-slice — first occurrence kept, order preserved",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:carol", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:carol", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantErr: false,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
				Conflicts: nil,
			},
		},
		{
			name: "two writes on same key — deduped, no conflict",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantErr: false,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
				Conflicts: nil,
			},
		},
		{
			name: "two deletes on same key — deduped, no conflict",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
			wantErr: false,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
				},
				Conflicts: nil,
			},
		},
		{
			name: "write then delete on same key — conflict",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
			wantErr: true,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: nil,
				Conflicts: []Conflict{
					{User: "user:alice", Relation: "member", Object: "org:1"},
				},
			},
		},
		{
			name: "delete then write on same key — conflict (order independent)",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			},
			wantErr: true,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: nil,
				Conflicts: []Conflict{
					{User: "user:alice", Relation: "member", Object: "org:1"},
				},
			},
		},
		{
			name: "write+delete on second key — conflict reported for second key",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
			wantErr: true,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: nil,
				Conflicts: []Conflict{
					{User: "user:bob", Relation: "member", Object: "org:1"},
				},
			},
		},
		{
			name: "write and delete on different keys — no conflict",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
			wantTuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "member", Object: "org:1", Action: language.ActionDelete},
			},
			wantErr:         false,
			wantPostProcess: nil,
		},
		{
			name: "multiple independent conflicts — all reported",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
				{User: "user:bob", Relation: "viewer", Object: "org:2", Action: language.ActionDelete},
				{User: "user:bob", Relation: "viewer", Object: "org:2", Action: language.ActionWrite},
			},
			wantErr: true,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: nil,
				Conflicts: []Conflict{
					{User: "user:alice", Relation: "member", Object: "org:1"},
					{User: "user:bob", Relation: "viewer", Object: "org:2"},
				},
			},
		},
		{
			name: "dedup and conflict together",
			tuples: []language.Tuple{
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				{User: "user:bob", Relation: "viewer", Object: "org:2", Action: language.ActionWrite},
				{User: "user:bob", Relation: "viewer", Object: "org:2", Action: language.ActionDelete},
			},
			wantErr: true,
			wantPostProcess: &PostProcessResult{
				RemovedTuples: []language.Tuple{
					{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
				},
				Conflicts: []Conflict{
					{User: "user:bob", Relation: "viewer", Object: "org:2"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Result{Tuples: tt.tuples, Trace: &Trace{}}
			err := r.postProcess()
			if tt.wantErr {
				require.Error(t, err)
				var ce *ConflictError
				require.True(t, errors.As(err, &ce), "error should be *ConflictError, got %T: %v", err, err)
				assert.NotEmpty(t, ce.User)
				assert.NotEmpty(t, ce.Relation)
				assert.NotEmpty(t, ce.Object)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantTuples, r.Tuples)
			}
			assert.Equal(t, tt.wantPostProcess, r.Trace.PostProcess)
		})
	}
}

func TestResult_PostProcessWithoutTrace(t *testing.T) {
	t.Parallel()
	t.Run("dedup still works without tracing", func(t *testing.T) {
		r := &Result{Tuples: []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		}}
		err := r.postProcess()
		require.NoError(t, err)
		assert.Equal(t, []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
		}, r.Tuples)
		assert.Nil(t, r.Trace, "trace should remain nil")
	})

	t.Run("conflict returns error without tracing", func(t *testing.T) {
		r := &Result{Tuples: []language.Tuple{
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionWrite},
			{User: "user:alice", Relation: "member", Object: "org:1", Action: language.ActionDelete},
		}}
		err := r.postProcess()
		require.Error(t, err)
		var ce *ConflictError
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, "user:alice", ce.User)
		assert.Equal(t, "member", ce.Relation)
		assert.Equal(t, "org:1", ce.Object)
		assert.Nil(t, r.Trace, "trace should remain nil")
	})
}
