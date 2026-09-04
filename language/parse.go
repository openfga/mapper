package language

import (
	"fmt"
)

// Parse parses a YAML mapping file and returns the MappingConfig without compiling.
// Useful for inspecting rule structure (when guards, variables, tuple templates)
// without running full expression compilation.
func Parse(data []byte) (*MappingConfig, error) {
	return parseMappingSafe(data)
}

// parseMappingSafe calls parseMapping, recovering any panic from the underlying
// YAML library into a ValidationError so callers are not crashed by malformed input.
func parseMappingSafe(data []byte) (*MappingConfig, error) {
	return recoverParse(func() (*MappingConfig, error) {
		return parseMapping(data)
	})
}

// recoverParse runs fn and converts any panic into a ValidationError. The message
// is kept concise (panic value only, no stack trace) so it is safe to return to
// callers: a stack trace can leak internal file paths and be arbitrarily large.
func recoverParse(fn func() (*MappingConfig, error)) (cfg *MappingConfig, err error) {
	defer func() {
		if r := recover(); r != nil {
			cfg = nil
			err = &ValidationError{Message: fmt.Sprintf("panic during parsing: %v", r)}
		}
	}()
	return fn()
}
