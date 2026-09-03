package mapper

import (
	"encoding/json"
	"testing"

	"github.com/openfga/mapper/language"
)

func FuzzCompile(f *testing.F) {
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
		cfg, err := language.Parse(data)
		if err != nil {
			return
		}

		// Exercise expression compilation.
		compiler := NewCompiler()
		mapping, err := compiler.Compile(data)
		if err != nil {
			return
		}

		// Evaluate with the compiled mapping's own test inputs where possible,
		// otherwise use a minimal event.
		if len(cfg.Tests) > 0 {
			for _, tc := range cfg.Tests {
				mapping.Evaluate(t.Context(), tc.Input) //nolint:errcheck
			}
		} else {
			mapping.Evaluate(t.Context(), map[string]any{"type": "fuzz"}) //nolint:errcheck
		}
	})
}

func FuzzEvaluate(f *testing.F) {
	validMapping := []byte("version: \"1\"\nrules:\n  - name: t\n    when: input.type == \"created\"\n    variables:\n      id: input.user_id\n    tuples:\n      - user: \"user:{{ variables.id }}\"\n        relation: member\n        object: \"org:default\"")

	eventSeeds := []string{
		`{"type": "created", "user_id": "idp|abc123"}`,
		`{"type": "deleted"}`,
		`{}`,
		`{"type": "created", "items": [{"id": "a"}, {"id": "b"}]}`,
		`{"type": "created", "nested": {"deep": {"value": 42}}}`,
		`{"type": "created", "user_id": null}`,
		`not json`,
		`[]`,
		`""`,
	}

	for _, e := range eventSeeds {
		f.Add(validMapping, []byte(e))
	}

	f.Fuzz(func(t *testing.T, mappingData []byte, eventData []byte) {
		compiler := NewCompiler()
		mapping, err := compiler.Compile(mappingData)
		if err != nil {
			return
		}

		var event map[string]any
		if err := json.Unmarshal(eventData, &event); err != nil {
			return
		}

		mapping.Evaluate(t.Context(), event) //nolint:errcheck
	})
}
