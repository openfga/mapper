package language

import (
	"fmt"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseMappingNodes parses YAML bytes into an AST and returns the root node of the first document.
// This is a test helper for testing node traversal utilities.
func parseMappingNodes(data []byte) (ast.Node, error) {
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if file == nil || len(file.Docs) == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}
	// Get the first document's Body node
	doc := file.Docs[0]
	if doc.Body == nil {
		return nil, fmt.Errorf("empty YAML document")
	}
	return doc.Body, nil
}

const testYAML = `version: "1"
rules:
  - name: "Test Rule"
    when: input.type == "user.created"
    variables:
      user_email: input.data.email
    iterator:
      source: input.data.roles
      as: role
    tuples:
      - user: "user:{{ .input.id }}"
        relation: "member"
        object: "org:{{ .input.org }}"
      - user: "user:{{ .input.id }}"
        relation: "admin"
        object: "org:{{ .input.org }}"
  - name: "Second Rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
tests:
  - name: "Test Case 1"
    input:
      type: "test"
    expect_tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`

func TestParseMappingNodes(t *testing.T) {
	t.Parallel()
	root, err := parseMappingNodes([]byte(testYAML))
	require.NoError(t, err)
	require.NotNil(t, root)
}

func TestParseMappingNodesInvalidYAML(t *testing.T) {
	t.Parallel()
	_, err := parseMappingNodes([]byte(`{invalid: [yaml`))
	require.Error(t, err)
}

func TestResolveNodePath(t *testing.T) {
	t.Parallel()
	root, err := parseMappingNodes([]byte(testYAML))
	require.NoError(t, err)

	tests := []struct {
		name     string
		path     string
		wantNil  bool
		wantVal  string
		wantLine int
	}{
		{
			name:     "top-level key",
			path:     "version",
			wantVal:  "1",
			wantLine: 1,
		},
		{
			name:     "rule name",
			path:     "rules[0].name",
			wantVal:  "Test Rule",
			wantLine: 3,
		},
		{
			name:     "rule when",
			path:     `rules[0].when`,
			wantVal:  `input.type == "user.created"`,
			wantLine: 4,
		},
		{
			name:     "tuple user",
			path:     "rules[0].tuples[0].user",
			wantVal:  "user:{{ .input.id }}",
			wantLine: 11,
		},
		{
			name:     "tuple relation",
			path:     "rules[0].tuples[0].relation",
			wantVal:  "member",
			wantLine: 12,
		},
		{
			name:     "second tuple object",
			path:     "rules[0].tuples[1].object",
			wantVal:  "org:{{ .input.org }}",
			wantLine: 16,
		},
		{
			name:     "second rule",
			path:     "rules[1].name",
			wantVal:  "Second Rule",
			wantLine: 17,
		},
		{
			name:     "iterator source",
			path:     "rules[0].iterator.source",
			wantVal:  "input.data.roles",
			wantLine: 8,
		},
		{
			name:     "iterator as",
			path:     "rules[0].iterator.as",
			wantVal:  "role",
			wantLine: 9,
		},
		{
			name:     "test case name",
			path:     "tests[0].name",
			wantVal:  "Test Case 1",
			wantLine: 23,
		},
		{
			name:    "nonexistent key",
			path:    "rules[0].nonexistent",
			wantNil: true,
		},
		{
			// rules is a SequenceNode; a quoted-key segment cannot index into it.
			// Callers must use numeric indices for sequence traversal.
			name:    "quoted key on sequence returns nil",
			path:    `rules["Test Rule"].tuples[0].user`,
			wantNil: true,
		},
		{
			name:    "out of bounds index",
			path:    "rules[99]",
			wantNil: true,
		},
		{
			name:    "empty path",
			path:    "",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := resolveNodePath(root, tt.path)
			if tt.wantNil {
				assert.Nil(t, node)
				return
			}
			require.NotNil(t, node, "expected node for path %q", tt.path)
			if tt.wantVal != "" {
				val := getScalarValue(node)
				assert.Equal(t, tt.wantVal, val)
			}
			if tt.wantLine > 0 {
				token := node.GetToken()
				require.NotNil(t, token)
				assert.Equal(t, tt.wantLine, token.Position.Line)
			}
		})
	}
}

func TestResolveNodePathNilRoot(t *testing.T) {
	t.Parallel()
	assert.Nil(t, resolveNodePath(nil, "version"))
}

func TestNodePosition(t *testing.T) {
	t.Parallel()
	root, err := parseMappingNodes([]byte(testYAML))
	require.NoError(t, err)

	t.Run("nil node returns zero position", func(t *testing.T) {
		pos := nodePosition(nil)
		assert.Equal(t, Position{}, pos)
	})

	t.Run("scalar value", func(t *testing.T) {
		node := resolveNodePath(root, "version")
		require.NotNil(t, node)
		pos := nodePosition(node)
		assert.Equal(t, 1, pos.StartLine)
		assert.Equal(t, 10, pos.StartColumn)
		assert.Equal(t, 1, pos.EndLine)
		assert.Equal(t, 11, pos.EndColumn) // "1" is 1 char
	})

	t.Run("longer scalar", func(t *testing.T) {
		node := resolveNodePath(root, "rules[0].tuples[0].user")
		require.NotNil(t, node)
		pos := nodePosition(node)
		assert.Equal(t, 11, pos.StartLine)
		assert.Equal(t, pos.StartLine, pos.EndLine)
		assert.Equal(t, pos.StartColumn+len("user:{{ .input.id }}"), pos.EndColumn)
	})
}

func TestNodePositionMultiline(t *testing.T) {
	t.Parallel()
	yaml := `key: |
  line one
  line two
  line three
`
	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	node := resolveNodePath(root, "key")
	require.NotNil(t, node)

	pos := nodePosition(node)
	assert.Equal(t, 1, pos.StartLine)
	// For multiline scalars, endLine equals startLine (position not reliably computable)
	assert.Equal(t, pos.StartLine, pos.EndLine)
}

func TestNodePositionDoubleQuotedEscapedNewline(t *testing.T) {
	t.Parallel()
	// Issue 2a: Double-quoted string with escaped newlines should be treated as single-line
	// The source contains only one line, even though the decoded value has newlines
	yaml := `key: "line1\nline2\nline3"`

	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	node := resolveNodePath(root, "key")
	require.NotNil(t, node)

	pos := nodePosition(node)
	assert.Equal(t, 1, pos.StartLine)
	// Should be single-line since no actual newline in source
	assert.Equal(t, 1, pos.EndLine)
	// EndColumn should be StartColumn + decoded value length
	assert.Equal(t, pos.StartColumn+len("line1\nline2\nline3"), pos.EndColumn)
}

func TestNodePositionNonScalarNodes(t *testing.T) {
	t.Parallel()
	yaml := `version: "1"
rules:
  - name: "Test Rule"
    variables:
      user_email: input.data.email
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`
	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	// Test position of mapping node (rules)
	rulesNode := resolveNodePath(root, "rules")
	require.NotNil(t, rulesNode)
	pos := nodePosition(rulesNode)
	assert.Greater(t, pos.StartLine, 0, "mapping node should have line number")
	assert.Greater(t, pos.StartColumn, 0, "mapping node should have column number")

	// Test position of sequence node (tuples)
	tuplesNode := resolveNodePath(root, "rules[0].tuples")
	require.NotNil(t, tuplesNode)
	pos = nodePosition(tuplesNode)
	assert.Greater(t, pos.StartLine, 0, "sequence node should have line number")
	assert.Greater(t, pos.StartColumn, 0, "sequence node should have column number")
}

func TestNodePositionWithNilToken(t *testing.T) {
	t.Parallel()
	// Test nodePosition defensive case with a node that has no token
	// In practice, all goccy nodes have tokens, but test the defensive path

	// Use a null node which should have a token but test the fallback
	yaml := `key: null`
	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	nullNode := resolveNodePath(root, "key")
	require.NotNil(t, nullNode)

	// Verify nodePosition handles it gracefully
	pos := nodePosition(nullNode)
	// Null node should still have position info
	assert.Greater(t, pos.StartLine, 0)
}

func TestNodePositionMultilineScalar(t *testing.T) {
	t.Parallel()
	// Test nodePosition with multiline block scalar
	// Block scalars are truly multiline in the source
	yaml := `key: |
  line1
  line2
  line3`

	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	keyNode := resolveNodePath(root, "key")
	require.NotNil(t, keyNode)

	pos := nodePosition(keyNode)

	// Position should be valid
	assert.Greater(t, pos.StartLine, 0)
	assert.Greater(t, pos.StartColumn, 0)
	// For multiline block scalars, endLine equals startLine (not computable)
	assert.Equal(t, pos.StartLine, pos.EndLine)
}

func TestParseMappingNodesErrorHandling(t *testing.T) {
	t.Parallel()
	// Test parseMappingNodes error handling for various YAML inputs
	tests := []struct {
		name      string
		yaml      string
		expectErr bool
	}{
		{
			name:      "invalid syntax",
			yaml:      `{ invalid yaml [`,
			expectErr: true,
		},
		{
			name:      "valid YAML",
			yaml:      `key: value`,
			expectErr: false,
		},
		{
			name:      "empty input",
			yaml:      ``,
			expectErr: true,
		},
		{
			name:      "whitespace only",
			yaml:      `   `,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := parseMappingNodes([]byte(tt.yaml))
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, node)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, node)
			}
		})
	}
}

func TestGetScalarValueWithNonStringNode(t *testing.T) {
	t.Parallel()
	yaml := `version: "1"
rules:
  - name: "Test Rule"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`
	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	// Get a non-scalar node (the rules array)
	rulesNode := resolveNodePath(root, "rules")
	require.NotNil(t, rulesNode)

	// getScalarValue should return empty string for non-scalar nodes
	val := getScalarValue(rulesNode)
	assert.Empty(t, val)

	// Test with nil node
	val = getScalarValue(nil)
	assert.Empty(t, val)
}

func TestResolveNodePathVariablesPosition(t *testing.T) {
	t.Parallel()
	yaml := `version: "1"
rules:
  - name: "Test Rule"
    variables:
      user_email: input.data.email
      org_id: input.data.org_id ?? "default"
    tuples:
      - user: "u:x"
        relation: "r"
        object: "o:x"
`
	root, err := parseMappingNodes([]byte(yaml))
	require.NoError(t, err)

	tests := []struct {
		name     string
		path     string
		wantNil  bool
		wantVal  string
		wantLine int
	}{
		{
			name:     "first variable key",
			path:     "rules[0].variables",
			wantNil:  false,
			wantLine: 5,
		},
		{
			name:    "second variable value",
			path:    "rules[0].variables[1]",
			wantNil: true, // variable indices don't work like array indices
		},
		{
			name:    "variable by quoted key (first)",
			path:    `rules[0].variables["user_email"]`,
			wantVal: "input.data.email",
			wantNil: false,
		},
		{
			name:    "variable by quoted key (second)",
			path:    `rules[0].variables["org_id"]`,
			wantVal: `input.data.org_id ?? "default"`,
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := resolveNodePath(root, tt.path)
			if tt.wantNil {
				assert.Nil(t, node)
				return
			}
			require.NotNil(t, node, "expected node for path %q", tt.path)
			if tt.wantVal != "" {
				val := getScalarValue(node)
				assert.Equal(t, tt.wantVal, val)
			}
			if tt.wantLine > 0 {
				token := node.GetToken()
				require.NotNil(t, token)
				assert.Equal(t, tt.wantLine, token.Position.Line)
			}
		})
	}
}
