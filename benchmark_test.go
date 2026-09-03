package mapper

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkFullCompile(b *testing.B) {
	data, err := os.ReadFile("../../examples/mappings/organization.yaml")
	require.NoError(b, err)

	b.ReportAllocs()
	compiler := NewCompiler()
	for i := 0; i < b.N; i++ {
		_, _ = compiler.Compile(data)
	}
}

func BenchmarkEvaluate(b *testing.B) {
	mappingData, err := os.ReadFile("../../examples/mappings/organization.yaml")
	require.NoError(b, err)

	compiler := NewCompiler()
	mapping, err := compiler.Compile(mappingData)
	require.NoError(b, err)

	matchingEvent := map[string]any{
		"type": "organization.member.added",
		"data": map[string]any{
			"object": map[string]any{
				"organization": map[string]any{"id": "org_abc"},
				"user":         map[string]any{"user_id": "idp|xyz"},
			},
		},
	}

	noMatchEvent := map[string]any{
		"type": "unknown.event.type",
	}

	b.Run("match", func(b *testing.B) {
		b.ReportAllocs()
		ctx := b.Context()
		for i := 0; i < b.N; i++ {
			_, err := mapping.Evaluate(ctx, matchingEvent)
			if err != nil {
				b.Fatalf("mapping.Evaluate (match): %v", err)
			}
		}
	})

	b.Run("no-match", func(b *testing.B) {
		b.ReportAllocs()
		ctx := b.Context()
		for i := 0; i < b.N; i++ {
			_, err := mapping.Evaluate(ctx, noMatchEvent)
			if err != nil {
				b.Fatalf("mapping.Evaluate (no-match): %v", err)
			}
		}
	})
}
