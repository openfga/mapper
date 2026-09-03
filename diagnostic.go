package mapper

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openfga/mapper/language"
)

// Severity represents the severity of a diagnostic.
type Severity string

const (
	// SeverityError indicates a fatal error that prevents compilation or evaluation.
	SeverityError Severity = "error"
)

// Category classifies which stage of the pipeline produced a diagnostic.
type Category string

const (
	// CategoryValidation covers structural validation and YAML parse errors.
	CategoryValidation Category = "validation"
	// CategoryEval covers expression compilation and evaluation errors.
	CategoryEval Category = "eval"
	// CategoryConflict covers write/delete conflicts detected during evaluation.
	CategoryConflict Category = "conflict"
	// CategoryUnknown covers errors that could not be classified.
	CategoryUnknown Category = "unknown"
)

// Diagnostic is a structured representation of a single error suitable for
// rendering in any consumer (CLI, IDE, browser editor, API response).
type Diagnostic struct {
	Severity Severity          `json:"severity"`
	Category Category          `json:"category"`
	Field    string            `json:"field,omitempty"`   // YAML field path or tuple field name
	Message  string            `json:"message"`           // human-readable description
	Position language.Position `json:"position,omitzero"` // zero value = unknown, omitted from JSON
}

// Diagnostics is a named slice of Diagnostic values. It satisfies fmt.Stringer,
// so fmt.Println(diags) and fmt.Sprintf("%v", diags) produce the grouped
// human-readable output rather than Go's default slice representation.
// JSON marshalling is unaffected — it encodes as a JSON array.
type Diagnostics []Diagnostic

// DiagnosticsFrom extracts all structured diagnostics from an error tree.
// Handles errors.Join trees, wrapped errors, and all mapper error types.
// Returns nil for nil errors.
func DiagnosticsFrom(err error) Diagnostics {
	if err == nil {
		return nil
	}
	return collectDiagnostics(err)
}

func collectDiagnostics(err error) Diagnostics {
	// Multi-error (errors.Join): recurse into children
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var diags Diagnostics
		for _, child := range joined.Unwrap() {
			diags = append(diags, collectDiagnostics(child)...)
		}
		return diags
	}

	// Type-switch on known error types before checking single-wrap,
	// because our error types implement Unwrap() too.
	switch e := err.(type) {
	case *language.ValidationError:
		return Diagnostics{{
			Severity: SeverityError,
			Category: CategoryValidation,
			Field:    e.Field,
			Message:  e.Message,
			Position: e.Position,
		}}
	case *EvalError:
		// Field preference: explicit tuple Field (compile-time interp errors) > RuleName (evaluation errors).
		field := e.Field
		if field == "" {
			field = e.RuleName
		}
		return Diagnostics{{
			Severity: SeverityError,
			Category: CategoryEval,
			Field:    field,
			Message:  e.Error(),
			Position: e.Position,
		}}
	case *ConflictError:
		return Diagnostics{{
			Severity: SeverityError,
			Category: CategoryConflict,
			Message:  e.Error(),
		}}
	}

	// Single-wrap (fmt.Errorf %w): recurse into inner error
	if wrapper, ok := err.(interface{ Unwrap() error }); ok {
		return collectDiagnostics(wrapper.Unwrap())
	}

	// Unknown error type — attempt to extract position from YAML parse errors.
	msg := err.Error()
	if pos, ok := parseYAMLErrorPosition(msg); ok {
		return Diagnostics{{
			Severity: SeverityError,
			Category: CategoryValidation,
			Message:  msg,
			Position: pos,
		}}
	}
	return Diagnostics{{
		Severity: SeverityError,
		Category: CategoryUnknown,
		Message:  msg,
	}}
}

var yamlLineColRe = regexp.MustCompile(`\[(\d+):(\d+)\]`)

// parseYAMLErrorPosition extracts position from YAML parse error messages
// using the "[line:col]" format.
func parseYAMLErrorPosition(msg string) (language.Position, bool) {
	m := yamlLineColRe.FindStringSubmatch(msg)
	if m == nil {
		return language.Position{}, false
	}
	line, err1 := strconv.Atoi(m[1])
	col, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || line < 1 || col < 1 {
		return language.Position{}, false
	}
	return language.Position{StartLine: line, StartColumn: col}, true
}

// categoryOrder defines the display order for known categories in String.
var categoryOrder = []Category{CategoryValidation, CategoryEval, CategoryConflict, CategoryUnknown}

// renderGroup writes a single category group to b.
func renderGroup(b *strings.Builder, cat Category, group []Diagnostic) {
	fmt.Fprintf(b, "\n%s (%d):\n", cat, len(group))
	for _, diag := range group {
		b.WriteString("  ")
		if diag.Position.StartLine > 0 {
			fmt.Fprintf(b, "%d:%d", diag.Position.StartLine, diag.Position.StartColumn)
			if diag.Position.EndLine > 0 {
				fmt.Fprintf(b, "-%d:%d", diag.Position.EndLine, diag.Position.EndColumn)
			}
			b.WriteString(": ")
		}
		if diag.Field != "" {
			fmt.Fprintf(b, "%s: ", diag.Field)
		}
		b.WriteString(diag.Message)
		b.WriteByte('\n')
	}
}

// String renders diagnostics as a human-readable multi-line string grouped by
// category, satisfying fmt.Stringer.
func (d Diagnostics) String() string {
	if len(d) == 0 {
		return ""
	}

	// Group by category
	groups := make(map[Category][]Diagnostic)
	for _, diag := range d {
		groups[diag.Category] = append(groups[diag.Category], diag)
	}

	var b strings.Builder

	// Header
	if len(d) == 1 {
		fmt.Fprintf(&b, "1 error found:\n")
	} else {
		fmt.Fprintf(&b, "%d errors found:\n", len(d))
	}

	// Render known categories in defined order.
	rendered := make(map[Category]bool, len(categoryOrder))
	for _, cat := range categoryOrder {
		if group, ok := groups[cat]; ok {
			rendered[cat] = true
			renderGroup(&b, cat, group)
		}
	}

	// Render any remaining categories not in categoryOrder, sorted for determinism.
	var extra []Category
	for cat := range groups {
		if !rendered[cat] {
			extra = append(extra, cat)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, cat := range extra {
		renderGroup(&b, cat, groups[cat])
	}

	return b.String()
}
