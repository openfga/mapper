package language

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkParsing(b *testing.B) {
	data, err := os.ReadFile("../../examples/mappings/organization.yaml")
	require.NoError(b, err)

	b.Run("parse", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = parseMapping(data)
		}
	})

	b.Run("parse-mapping", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, err := parseMapping(data)
			if err != nil {
				b.Fatalf("parseMapping: %v", err)
			}
		}
	})
}
