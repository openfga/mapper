package language

import (
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// nodePosition computes a Position from an ast.Node's token position and value.
// For scalar nodes that are single-line in the source, computes EndColumn based on value length.
// For non-scalar nodes or multiline scalars, only StartLine and StartColumn are reliable.
func nodePosition(node ast.Node) Position {
	if node == nil {
		return Position{}
	}

	token := node.GetToken()
	if token == nil {
		return Position{}
	}

	pos := Position{
		StartLine:   token.Position.Line,
		StartColumn: token.Position.Column,
	}

	// For scalar nodes, compute the end position based on the raw source
	if scalar, ok := node.(*ast.StringNode); ok {
		// Use raw token source to detect actual source newlines, not escaped sequences
		if token.Origin != "" && !strings.Contains(token.Origin, "\n") {
			// Single-line scalar in source: compute EndColumn from decoded value length
			pos.EndLine = pos.StartLine
			pos.EndColumn = pos.StartColumn + len(scalar.Value)
		} else {
			// Multiline scalar: end position is not reliably computable from decoded value
			pos.EndLine = pos.StartLine
			pos.EndColumn = pos.StartColumn
		}
	} else {
		// For non-scalar nodes, just set the start position
		// The token position should be accurate
		pos.EndLine = pos.StartLine
		pos.EndColumn = pos.StartColumn
	}

	return pos
}

// resolveNodePath resolves a dot-separated field path against an AST node tree.
// Supported path segments:
//   - bare keys: "version", "name" — look up in mapping nodes
//   - numeric indices: "[0]", "[1]" — index into sequence nodes
//   - quoted strings: ["Grant Access"] — search mapping nodes by key value
//
// Returns nil if the path cannot be resolved.
func resolveNodePath(root ast.Node, path string) ast.Node {
	if root == nil || path == "" {
		return nil
	}

	segments := parsePathSegments(path)
	node := root

	for _, seg := range segments {
		node = resolveSegment(node, seg)
		if node == nil {
			return nil
		}
	}

	return node
}

type pathSegment struct {
	key   string // bare key or quoted key
	index int    // numeric index, -1 if not an index
}

// parsePathSegments splits a path like "rules[0].tuples[1].user" into segments.
func parsePathSegments(path string) []pathSegment {
	var segments []pathSegment
	i := 0
	for i < len(path) {
		if path[i] == '.' {
			i++
			continue
		}
		if path[i] == '[' {
			// Find closing bracket
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				break
			}
			inner := path[i+1 : i+j]
			i += j + 1

			// Numeric index?
			if n, err := strconv.Atoi(inner); err == nil {
				segments = append(segments, pathSegment{index: n})
				continue
			}

			// Quoted string — strip surrounding quotes
			if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
				unquoted, err := strconv.Unquote(inner)
				if err != nil {
					// Fall back to raw content without quotes
					unquoted = inner[1 : len(inner)-1]
				}
				segments = append(segments, pathSegment{key: unquoted, index: -1})
				continue
			}

			// Bare bracket content (shouldn't normally happen)
			segments = append(segments, pathSegment{key: inner, index: -1})
			continue
		}

		// Bare key — read until dot or bracket
		j := i
		for j < len(path) && path[j] != '.' && path[j] != '[' {
			j++
		}
		segments = append(segments, pathSegment{key: path[i:j], index: -1})
		i = j
	}
	return segments
}

// resolveSegment resolves a single path segment against an ast.Node.
func resolveSegment(node ast.Node, seg pathSegment) ast.Node {
	if seg.index >= 0 {
		// Numeric index into sequence
		if seqNode, ok := node.(*ast.SequenceNode); ok {
			if seg.index >= len(seqNode.Values) {
				return nil
			}
			return seqNode.Values[seg.index]
		}
		return nil
	}

	// Key lookup in mapping
	if mapNode, ok := node.(*ast.MappingNode); ok {
		for _, valueNode := range mapNode.Values {
			// Extract key value from the key node
			keyValue := getScalarValue(valueNode.Key)
			if keyValue == seg.key {
				return valueNode.Value
			}
		}
	}
	return nil
}

// getScalarValue extracts the string value from an ast.Node if it's a scalar.
// Returns empty string for non-scalars or nil nodes.
func getScalarValue(node ast.Node) string {
	if node == nil {
		return ""
	}
	if scalar, ok := node.(*ast.StringNode); ok {
		return scalar.Value
	}
	return ""
}
