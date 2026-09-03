package mapper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"

	"github.com/openfga/mapper/language"
)

// DefaultMaxIteratorItems is the default cap on the number of items an iterator
// source array may contain per evaluation. Override per-compile with
// WithMaxIteratorItems.
const DefaultMaxIteratorItems = 1000

// Fixed safety limits. Unlike the workload-dependent caps (max tuples, max rules,
// max iterator items), these bound pathological or malicious input rather than
// honest scale, so they are deliberately NOT configurable: exposing them as knobs
// would let a consumer weaken the sandbox's protection against untrusted mapping
// authors (e.g. raising maxVariableSize to exhaust memory). Change only with
// explicit approval.
const (
	maxVariableSize    = 4096  // 4KB max variable string size to prevent storing excessively large results
	maxExpressionSize  = 65536 // 64KB max expression length to prevent DoS
	maxJsonPathSegment = 100   // Max segments in json_path to prevent excessively deep traversal
	maxExprInError     = 200   // Max bytes of expression text stored in error messages
)

// stdlibOptions returns the expr.Option slice that registers the mapper's custom
// functions in the expression environment.
//
// The expression environment is a deliberately CLOSED sandbox. It exposes only
// json_path and fga_escape on top of expr-lang's pure built-ins — no file I/O,
// network, shell, or reflection — because mapping authors are potentially
// untrusted and their expressions run against attacker-influenced event data. The
// README's security contract ("no network, file I/O, or shell functions") holds
// only because this set is fixed and closed.
//
// Custom-function registration (a WithFunction option) is intentionally NOT
// exposed. It would let a consumer inject arbitrary Go — e.g. WithFunction("readFile",
// os.ReadFile) — punching a hole in the sandbox with no resource governance and no
// safe-input contract on the injected func. Should a real consumer need it, it must
// be designed as a bounded capability with an explicit "you now own sandbox safety"
// contract, not a raw func hook, and is out of scope for the initial public surface.
func stdlibOptions() []expr.Option {
	return []expr.Option{
		expr.Function("json_path", jsonPath),
		expr.Function("fga_escape", fgaEscape),
	}
}

// truncateExpr returns code unchanged if it fits within maxExprInError bytes.
// Otherwise, it truncates at the nearest valid UTF-8 boundary and appends "...".
func truncateExpr(code string) string {
	if len(code) <= maxExprInError {
		return code
	}
	truncated := code[:maxExprInError]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "..."
}

// compileExpr compiles an expression string with stdlib and AllowUndefinedVariables.
// Returns the compiled vm.Program. Wraps syntax errors in *EvalError.
// Validates expression length to prevent DoS via oversized expressions.
func compileExpr(code string, opts ...expr.Option) (*vm.Program, error) {
	// Prevent DoS via extremely large expressions
	if len(code) > maxExpressionSize {
		return nil, &EvalError{Expression: truncateExpr(code), Err: fmt.Errorf("expression exceeds max size of %d bytes", maxExpressionSize)}
	}

	stdlib := stdlibOptions()
	// AllowUndefinedVariables must be appended last: expr.Env sets Strict=true
	// (which validates map keys against the runtime values passed in env), but we
	// need Strict=false because events have dynamic shapes—different events have
	// different fields, and we can't know the full structure at compile time.
	// We use expr.Env for type information (is input a map?) while allowing
	// undefined fields (is input.user.email present?) to handle varying JSON shapes.
	allOpts := make([]expr.Option, 0, len(stdlib)+len(opts)+1)
	allOpts = append(allOpts, stdlib...)
	allOpts = append(allOpts, opts...)
	allOpts = append(allOpts, expr.AllowUndefinedVariables())

	program, err := expr.Compile(code, allOpts...)
	if err != nil {
		return nil, &EvalError{Expression: truncateExpr(code), Err: err}
	}
	return program, nil
}

// runExpr runs a compiled program against an env map.
// Wraps runtime errors in *EvalError.
func runExpr(program *vm.Program, env map[string]any, code string) (any, error) {
	result, err := expr.Run(program, env)
	if err != nil {
		return nil, &EvalError{Expression: truncateExpr(code), Err: err}
	}
	return result, nil
}

// jsonPath traverses a nested map/slice structure using a dot-separated path string.
// Returns nil if any segment is missing. Max depth is 100 segments to prevent excessively deep traversal.
//
//	json_path(obj, "data.user.email") → value or nil
func jsonPath(params ...any) (any, error) {
	if len(params) != 2 {
		return nil, fmt.Errorf("json_path requires exactly 2 arguments, got %d", len(params))
	}

	obj := params[0]
	pathStr, ok := params[1].(string)
	if !ok {
		return nil, fmt.Errorf("json_path path must be a string, got %T", params[1])
	}

	if obj == nil || pathStr == "" {
		return nil, nil
	}

	segments := strings.Split(pathStr, ".")

	// Prevent excessively deep traversal
	if len(segments) > maxJsonPathSegment {
		return nil, fmt.Errorf("json_path exceeds maximum of %d segments", maxJsonPathSegment)
	}

	current := obj

	for _, segment := range segments {
		// Empty segment (from paths like "a..b" or leading/trailing dots) → nil
		if segment == "" {
			return nil, nil
		}

		// Try to traverse as a map
		if m, ok := current.(map[string]any); ok {
			current, ok = m[segment]
			if !ok {
				return nil, nil
			}
			continue
		}

		// Try to traverse as a slice with integer index
		if arr, ok := current.([]any); ok {
			idx, err := strconv.Atoi(segment)
			if err != nil || idx < 0 || idx >= len(arr) {
				return nil, nil
			}
			current = arr[idx]
			continue
		}

		// Can't traverse further
		return nil, nil
	}

	return current, nil
}

// fgaEscape percent-encodes characters that are invalid in OpenFGA tuple fields.
// The escaped characters are the union of all forbidden characters across all
// tuple field contexts (user ID, object ID, userset ID, relation), plus '%'
// itself for encoding safety:
//
//	'#', ':', '@', '*', ' ', '%', and Unicode control characters.
//
// This makes the output safe for use in any tuple field position.
// See openfga/openfga/pkg/tuple/tuple.go for the server-side validation rules.
func fgaEscape(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, fmt.Errorf("fga_escape requires exactly 1 argument, got %d", len(params))
	}

	s, ok := params[0].(string)
	if !ok {
		return nil, fmt.Errorf("fga_escape argument must be a string, got %T", params[0])
	}

	if s == "" {
		return s, nil
	}

	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		if isFgaForbidden(r) {
			if r <= 0xFF {
				fmt.Fprintf(&b, "%%%02X", r)
			} else {
				fmt.Fprintf(&b, "%%U%04X", r)
			}
		} else {
			b.WriteRune(r)
		}
	}

	return b.String(), nil
}

// isFgaForbidden returns true for characters that are invalid in any OpenFGA
// tuple field context. This is the union of forbidden characters across:
//   - User ID: #, :, space, control
//   - Object ID: #, :, space, control
//   - Userset ID: #, :, *, space, control
//   - Relation: #, :, @, space, control
//
// Plus '%' for percent-encoding safety (prevents double-encoding ambiguity).
func isFgaForbidden(r rune) bool {
	if unicode.IsControl(r) {
		return true
	}
	switch r {
	case '#', ':', '@', '*', ' ', '%':
		return true
	}
	return false
}

// checkForwardRefs parses each variable expression and checks whether it
// references a variable that is defined later in the list. Returns all
// forward-reference errors joined together so users see every issue at once.
func checkForwardRefs(vars language.Variables) error {
	// Build a map of variable name → position in the list.
	pos := make(map[string]int, len(vars))
	for i, v := range vars {
		pos[v.Name] = i
	}

	var errs []error
	for i, v := range vars {
		field := fmt.Sprintf("variables.%s", v.Name)

		// Enforce size limit before parsing to prevent DoS
		if len(v.Expression) > maxExpressionSize {
			errs = append(errs, &EvalError{
				Expression: truncateExpr(v.Expression),
				Field:      field,
				Err:        fmt.Errorf("expression exceeds max size of %d bytes", maxExpressionSize),
			})
			continue
		}

		tree, err := parser.Parse(v.Expression)
		if err != nil {
			// Syntax errors will be caught later during compilation.
			continue
		}

		visitor := &varRefVisitor{}
		ast.Walk(&tree.Node, visitor)

		// Deduplicate references to avoid duplicate errors if a variable
		// references the same forward-ref multiple times (e.g., variables.b + variables.b)
		seen := make(map[string]bool)
		for _, ref := range visitor.refs {
			if seen[ref] {
				continue
			}
			seen[ref] = true

			j, defined := pos[ref]
			if defined && j > i {
				errs = append(errs, &EvalError{
					Expression: truncateExpr(v.Expression),
					Field:      field,
					Err:        fmt.Errorf("variable %q references %q which is defined later", v.Name, ref),
				})
			}
		}
	}
	return errors.Join(errs...)
}

// hasVariablesRef returns true if the expression string contains any
// `variables.X` member access. Used to reject variables references in
// rule-level conditions at compile time.
func hasVariablesRef(code string) bool {
	if len(code) > maxExpressionSize {
		return false // oversized expressions are rejected by compileExpr
	}
	tree, err := parser.Parse(code)
	if err != nil {
		return false // syntax errors are caught elsewhere
	}
	visitor := &varRefVisitor{}
	ast.Walk(&tree.Node, visitor)
	return len(visitor.refs) > 0
}

// varRefVisitor collects names from `variables.X` member accesses.
// It detects dot notation (variables.foo) and bracket notation with string
// literals (variables["foo"]) since both produce a MemberNode with a
// StringNode property. Dynamic bracket access like variables[someVar] is
// not detected; this is acceptable because expr-lang mapping authors use
// dot notation in practice.
type varRefVisitor struct {
	refs []string
}

func (v *varRefVisitor) Visit(node *ast.Node) {
	if m, ok := (*node).(*ast.MemberNode); ok {
		if ident, ok := m.Node.(*ast.IdentifierNode); ok && ident.Value == "variables" {
			if prop, ok := m.Property.(*ast.StringNode); ok {
				v.refs = append(v.refs, prop.Value)
			}
		}
	}
}

// evaluateVariables evaluates a rule's pre-compiled variables sequentially.
//
// Each compiled variable program can reference:
//   - input: the raw event map
//   - variables: a map of previously evaluated variables (grows with each step)
//   - All stdlib functions (lower, upper, trim, json_path, etc.)
//
// Forward references are detected at compile time (Compiler.Compile); this
// function only runs pre-compiled programs.
//
// String results exceeding maxVariableSize bytes are truncated at the nearest
// UTF-8 boundary.
func evaluateVariables(
	ctx context.Context,
	vars []compiledVariable,
	input map[string]any,
) (map[string]any, error) {
	// result accumulates evaluated variables and is shared by reference in the
	// expr-lang env. This is safe because evaluation is sequential within a
	// single goroutine — do not evaluate variables concurrently.
	result := make(map[string]any)

	for _, cv := range vars {
		// Check context deadline
		if err := ctx.Err(); err != nil {
			return nil, &EvalError{Expression: cv.name, Err: err}
		}

		// Build environment with input and previously evaluated variables.
		// "variables" points to result, which grows with each iteration.
		env := map[string]any{
			"input":     input,
			"variables": result,
		}

		// Run the pre-compiled program.
		val, err := runExpr(cv.program, env, cv.code)
		if err != nil {
			return nil, err
		}

		// Truncate string results exceeding maxVariableSize bytes,
		// backing off to the last valid UTF-8 boundary.
		if s, ok := val.(string); ok && len(s) > maxVariableSize {
			truncated := s[:maxVariableSize]
			for !utf8.ValidString(truncated) && len(truncated) > 0 {
				truncated = truncated[:len(truncated)-1]
			}
			val = truncated
		}

		result[cv.name] = val
	}

	return result, nil
}

// evaluateWhenGuard evaluates a pre-compiled when guard program.
//
// A nil program means the rule always runs (returns true).
// code is the original expression string used only for error messages.
// Non-bool results are coerced via toBool: nil/0/""/empty → false, else true.
//
// extras is a variadic list of additional map[string]any to merge into the
// environment (e.g., iterator variables). All extra maps are merged in order,
// allowing later maps to override earlier ones.
func evaluateWhenGuard(
	ctx context.Context,
	program *vm.Program,
	code string,
	input map[string]any,
	variables map[string]any,
	extras ...map[string]any,
) (bool, error) {
	// Nil program means "no when guard" — rule always runs.
	if program == nil {
		return true, nil
	}

	// Check context deadline
	if err := ctx.Err(); err != nil {
		return false, &EvalError{Expression: code, Err: err}
	}

	// Build environment with input and variables
	env := map[string]any{
		"input":     input,
		"variables": variables,
	}

	// Merge any extra maps (e.g., iterator variables)
	for _, extra := range extras {
		for k, v := range extra {
			env[k] = v
		}
	}

	// Run the pre-compiled program.
	result, err := runExpr(program, env, code)
	if err != nil {
		return false, err
	}

	// Coerce to bool
	return toBool(result), nil
}

// toBool coerces a value to bool for when guard evaluation.
//
// Falsy: nil, false, 0 (int/int64/float64), "", empty []any, empty map[string]any.
// Truthy: everything else. These are the types expr-lang produces at runtime;
// input maps may also contain int64 from JSON unmarshaling.
func toBool(v any) bool {
	if v == nil {
		return false
	}

	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case string:
		return val != ""
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}
