// Package mapper compiles validated FGA mapping configurations into an executable
// Mapping and evaluates JSON events against it, producing OpenFGA relationship tuples.
//
// Mapping configurations are defined in YAML with three core components:
//   - Expr (github.com/expr-lang/expr) for conditional logic, data extraction, and variable binding
//   - Expr interpolation ({{ expr }}) for constructing tuple field strings (user, relation, object)
//   - Iterators for fan-out from a single event to multiple tuples
//
// Basic usage:
//
//	m, err := mapper.Compile(yamlBytes)
//	result, err := m.Evaluate(ctx, event)
//
// Compile is a one-shot convenience. To compile many sources with the same
// configuration, build a Compiler once and reuse it:
//
//	compiler := mapper.NewCompiler()
//	m, err := compiler.Compile(yamlBytes)
//	m, err = compiler.CompileFile("mapping.yaml")
//
// The compiler enforces safety limits: an evaluation timeout, a maximum number of
// tuples per event, and a maximum number of rules per configuration file. The
// tunable limits are set via functional options passed to Compile or NewCompiler:
//
//	m, err := mapper.Compile(yamlBytes, mapper.WithTimeout(50*time.Millisecond), mapper.WithTrace(true))
//
// Parsing and validation are delegated to the language package.
package mapper
