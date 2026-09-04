package language

import (
	"testing"
)

// benchmarkMapping is a representative mapping exercising rules, variables,
// an iterator, and embedded tests. It is inlined so the benchmark is
// self-contained and matches the fixture style used across this package.
var benchmarkMapping = []byte(`
version: "1"
rules:
  - name: "Map organization membership"
    when: input.type == "organization.member.added"
    variables:
      member_id: "input.data.user_id"
      org_id: "input.data.org_id"
    tuples:
      - user: "user:{{ .member_id }}"
        relation: "member"
        object: "organization:{{ .org_id }}"
  - name: "Sync user roles"
    when: len(input.data.roles) > 0
    iterator:
      source: input.data.roles
      as: role_name
      tuples:
        - user: "user:{{ .input.data.user_id }}"
          relation: "assignee"
          object: "role:{{ .role_name }}"
tests:
  - name: "Membership maps correctly"
    input:
      type: "organization.member.added"
      data:
        user_id: "alice"
        org_id: "acme"
        roles: []
    expect_tuples:
      - user: "user:alice"
        relation: "member"
        object: "organization:acme"
`)

func BenchmarkParsing(b *testing.B) {
	b.Run("parse", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = parseMapping(benchmarkMapping)
		}
	})

	b.Run("parse-mapping", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, err := parseMapping(benchmarkMapping)
			if err != nil {
				b.Fatalf("parseMapping: %v", err)
			}
		}
	})
}
