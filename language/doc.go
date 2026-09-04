// Package language parses and validates FGA mapping configurations.
//
// It transforms mapping YAML into a MappingConfig, checks structural constraints
// via MappingConfig.Validate, and stamps source positions onto the parsed structs
// so downstream consumers (the mapper package, IDE tooling) can attach diagnostics
// without reaching into the YAML AST. It performs no expression compilation or
// evaluation — that is the mapper package's responsibility.
package language
