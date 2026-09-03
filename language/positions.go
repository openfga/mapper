package language

import (
	"fmt"

	"github.com/goccy/go-yaml/ast"
)

// stampPositions records the source position of every expression-bearing field
// onto the parsed structs, resolved once against the YAML AST at parse time.
//
// It exists so the mapper's compile phase can attach diagnostics by reading plain
// struct fields (rule.WhenPos, pt.UserPos, ...) instead of reaching back into the
// AST and the resolveNodePath DSL — both of which are parse-time (language) details.
// The lookup paths here mirror the compile-phase lookups exactly, so the positions
// observed by the mapper are unchanged.
func stampPositions(config *MappingConfig, root ast.Node) {
	if root == nil {
		return
	}
	for i := range config.Rules {
		stampRulePositions(&config.Rules[i], i, root)
	}
}

func stampRulePositions(rule *Rule, index int, root ast.Node) {
	rulePrefix := fmt.Sprintf("rules[%d]", index)

	rule.WhenPos = nodePosition(resolveNodePath(root, rulePrefix+".when"))

	for j := range rule.Variables {
		v := &rule.Variables[j]
		v.Position = nodePosition(resolveNodePath(root, fmt.Sprintf("%s.variables.%s", rulePrefix, v.Name)))
	}

	for j := range rule.TupleFilters {
		f := &rule.TupleFilters[j]
		filterPrefix := fmt.Sprintf("%s.tuple_filters[%d]", rulePrefix, j)
		f.UserPos = nodePosition(resolveNodePath(root, filterPrefix+".user"))
		f.RelationPos = nodePosition(resolveNodePath(root, filterPrefix+".relation"))
		f.ObjectPos = nodePosition(resolveNodePath(root, filterPrefix+".object"))
	}

	if rule.Iterator != nil {
		rule.Iterator.SourcePos = nodePosition(resolveNodePath(root, rulePrefix+".iterator.source"))
		for j := range rule.Iterator.Tuples {
			prefix := fmt.Sprintf("%s.iterator.tuples[%d]", rulePrefix, j)
			stampTuplePositions(&rule.Iterator.Tuples[j], prefix, root)
		}
	}

	for j := range rule.Tuples {
		prefix := fmt.Sprintf("%s.tuples[%d]", rulePrefix, j)
		stampTuplePositions(&rule.Tuples[j], prefix, root)
	}
}

func stampTuplePositions(pt *ParsedTuple, lookupPrefix string, root ast.Node) {
	pt.WhenPos = nodePosition(resolveNodePath(root, lookupPrefix+".when"))
	pt.UserPos = nodePosition(resolveNodePath(root, lookupPrefix+".user"))
	pt.RelationPos = nodePosition(resolveNodePath(root, lookupPrefix+".relation"))
	pt.ObjectPos = nodePosition(resolveNodePath(root, lookupPrefix+".object"))

	if len(pt.Context) > 0 {
		pt.ContextPos = make(map[string]Position, len(pt.Context))
		for k := range pt.Context {
			pt.ContextPos[k] = nodePosition(resolveNodePath(root, lookupPrefix+".context."+k))
		}
	}
}
