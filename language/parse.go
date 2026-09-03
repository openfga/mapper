package language

import (
	"fmt"
	"runtime/debug"
)

// Parse parses a YAML mapping file and returns the MappingConfig without compiling.
// Useful for inspecting rule structure (when guards, variables, tuple templates)
// without running full expression compilation.
func Parse(data []byte) (*MappingConfig, error) {
	return parseMappingSafe(data)
}

// parseMappingSafe calls parseMapping, recovering any panic from the underlying
// YAML library into a ValidationError so callers are not crashed by malformed input.
func parseMappingSafe(data []byte) (cfg *MappingConfig, err error) {
	defer func() {
		if r := recover(); r != nil {
			cfg = nil
			err = &ValidationError{Message: fmt.Sprintf("panic during parsing: %v\n%s", r, debug.Stack())}
		}
	}()
	return parseMapping(data)
}
