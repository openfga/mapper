package language

import (
	"testing"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		// Minimal valid mapping
		"version: \"1\"\nrules:\n  - name: t\n    tuples:\n      - user: \"u:x\"\n        relation: r\n        object: \"o:y\"",
		// With when guard and variables
		"version: \"1\"\nrules:\n  - name: t\n    when: input.type == \"created\"\n    variables:\n      id: input.user_id\n    tuples:\n      - user: \"user:{{ variables.id }}\"\n        relation: member\n        object: \"org:default\"",
		// With iterator
		"version: \"1\"\nrules:\n  - name: t\n    iterator:\n      source: input.items\n      as: item\n      tuples:\n        - user: \"user:{{ item }}\"\n          relation: member\n          object: \"org:default\"",
		// With rule-level action: delete
		"version: \"1\"\nrules:\n  - name: t\n    action: delete\n    tuples:\n      - user: \"u:x\"\n        relation: r\n        object: \"o:y\"",
		// With tuple condition and context
		"version: \"1\"\nrules:\n  - name: t\n    tuples:\n      - user: \"u:x\"\n        relation: r\n        object: \"o:y\"\n        condition: in_allowed_ip_range\n        context:\n          allowed_range: \"{{ input.ip }}\"\n          device_type: laptop",
		// With tuple_filters
		"version: \"1\"\nrules:\n  - name: t\n    tuple_filters:\n      - relation: member\n        object: \"org:{{ input.org_id }}\"\n    iterator:\n      source: input.members\n      as: m\n      tuples:\n        - user: \"user:{{ m.id }}\"\n          relation: member\n          object: \"org:{{ input.org_id }}\"",
		// With embedded tests
		"version: \"1\"\nrules:\n  - name: t\n    tuples:\n      - user: \"u:x\"\n        relation: r\n        object: \"o:y\"\ntests:\n  - name: smoke\n    input:\n      type: test\n    expect_tuples:\n      - user: \"u:x\"\n        relation: r\n        object: \"o:y\"",
		// Tuple-level when guard
		"version: \"1\"\nrules:\n  - name: t\n    iterator:\n      source: input.roles\n      as: role\n      tuples:\n        - when: \"role != \\\"guest\\\"\"\n          user: \"u:{{ input.id }}\"\n          relation: member\n          object: \"role:{{ role }}\"",
		// Empty
		"",
		// Invalid YAML
		"not: yaml: valid: :",
		// Unsupported version
		"version: \"999\"\nrules: []",
	}

	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Call parseMapping directly — no recover() wrapper, so panics surface as crashes.
		_, _ = parseMapping(data)
	})
}
