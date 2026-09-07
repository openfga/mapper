package mapper

import (
	"testing"
)

// benchmarkMapping is a representative organization-membership mapping. It is
// inlined so the benchmark is self-contained, matching the fixture style used
// across the tests.
var benchmarkMapping = []byte(`
version: "1"
rules:
  - name: "Organization membership"
    when: input.type == "organization.member.added"
    tuples:
      - user: "user:{{ input.data.object.user.user_id }}"
        relation: "member"
        object: "organization:{{ input.data.object.organization.id }}"
`)

func BenchmarkFullCompile(b *testing.B) {
	b.ReportAllocs()
	compiler := NewCompiler()
	for i := 0; i < b.N; i++ {
		_, _ = compiler.Compile(benchmarkMapping)
	}
}

func BenchmarkEvaluate(b *testing.B) {
	compiler := NewCompiler()
	mapping, err := compiler.Compile(benchmarkMapping)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}

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
