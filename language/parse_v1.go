package language

import (
	"fmt"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

const versionV1 = "1"

// parseV1 unmarshals a v1 YAML mapping from the pre-parsed AST root node.
// It performs strict field checking and extracts ordered variables from the AST.
// Returns a canonical MappingConfig ready for Validate() and compilation.
func parseV1(root ast.Node) (*MappingConfig, error) {
	var config MappingConfig
	if err := yaml.NodeToValue(root, &config, yaml.DisallowUnknownField()); err != nil {
		return nil, toValidationError(err)
	}
	config.root = root

	if err := extractVariablesForRules(&config, root); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	stampPositions(&config, root)

	return &config, nil
}

// extractVariablesForRules extracts variables from the AST for each rule in the config.
func extractVariablesForRules(config *MappingConfig, root ast.Node) error {
	for i := range config.Rules {
		vars, err := extractVariables(root, i)
		if err != nil {
			return err
		}
		config.Rules[i].Variables = vars
	}
	return nil
}

// extractVariables extracts the variables mapping for a rule at the given index from the AST.
// Returns nil if no variables are found.
func extractVariables(root ast.Node, ruleIndex int) (Variables, error) {
	// Navigate to rules[ruleIndex].variables node
	rulesNode := resolveNodePath(root, "rules")
	if rulesNode == nil {
		return nil, nil
	}

	seqNode, ok := rulesNode.(*ast.SequenceNode)
	if !ok {
		return nil, nil
	}

	if ruleIndex >= len(seqNode.Values) {
		return nil, nil
	}

	ruleNode := seqNode.Values[ruleIndex]
	if ruleNode == nil {
		return nil, nil
	}

	variablesNode := resolveNodePath(ruleNode, "variables")
	if variablesNode == nil {
		return nil, nil
	}

	return parseVariablesFromNode(variablesNode)
}

// parseVariablesFromNode parses variables from a mapping node.
// Returns nil if the node is null or empty.
func parseVariablesFromNode(node ast.Node) (Variables, error) {
	if node == nil {
		return nil, nil
	}

	// Check if it's a null node
	if _, ok := node.(*ast.NullNode); ok {
		return nil, nil
	}

	// Should be a mapping node
	mapNode, ok := node.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("variables must be a mapping")
	}

	vars := make(Variables, 0, len(mapNode.Values))
	for _, mapValue := range mapNode.Values {
		// Check key is a string using explicit type assertion, not value equality
		keyNode, ok := mapValue.Key.(*ast.StringNode)
		if !ok {
			return nil, fmt.Errorf("variable name must be a string at line %d", mapValue.Key.GetToken().Position.Line)
		}

		// Check value is a string (or null) using explicit type assertion
		var exprValue string
		if valNode, ok := mapValue.Value.(*ast.StringNode); ok {
			exprValue = valNode.Value
		} else if !isNullNode(mapValue.Value) {
			return nil, fmt.Errorf("variable expression must be a string at line %d", mapValue.Value.GetToken().Position.Line)
		}

		vars = append(vars, Variable{
			Name:       keyNode.Value,
			Expression: exprValue,
		})
	}

	return vars, nil
}

// isNullNode checks if an AST node is a null node.
func isNullNode(node ast.Node) bool {
	_, ok := node.(*ast.NullNode)
	return ok
}
