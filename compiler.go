package mapper

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"github.com/openfga/mapper/language"
)

// Compiler holds configuration policy and compiles mapping YAML into Mappings.
type Compiler struct {
	timeout      time.Duration
	maxTuples    int
	maxRules     int
	maxIterItems int
	trace        bool
	err          error
}

// Option configures a Compiler. Options are applied by NewCompiler and by the
// one-shot Compile facade. An option that receives an invalid value records the
// error on the Compiler; that error is surfaced from Compile rather than panicking.
type Option func(*Compiler)

// WithTimeout overrides the default evaluation timeout per Evaluate() call. The
// duration must be positive; a non-positive value is recorded as an error and
// surfaced from Compile.
func WithTimeout(d time.Duration) Option {
	return func(c *Compiler) {
		if d <= 0 {
			c.err = errors.Join(c.err, fmt.Errorf("timeout must be positive, got %v", d))
			return
		}
		c.timeout = d
	}
}

// WithMaxTuples overrides the default maximum tuples per event. The count must be
// positive; a non-positive value is recorded as an error and surfaced from Compile.
func WithMaxTuples(n int) Option {
	return func(c *Compiler) {
		if n <= 0 {
			c.err = errors.Join(c.err, fmt.Errorf("max tuples must be positive, got %d", n))
			return
		}
		c.maxTuples = n
	}
}

// WithMaxRules overrides the default cap on the number of rules a single mapping
// file may declare, enforced during validation (within Compile). The count must
// be positive; a non-positive value is recorded as an error and surfaced from Compile.
func WithMaxRules(n int) Option {
	return func(c *Compiler) {
		if n <= 0 {
			c.err = errors.Join(c.err, fmt.Errorf("max rules must be positive, got %d", n))
			return
		}
		c.maxRules = n
	}
}

// WithMaxIteratorItems overrides the default cap on the number of items an
// iterator source array may contain per evaluation. The count must be positive; a
// non-positive value is recorded as an error and surfaced from Compile.
func WithMaxIteratorItems(n int) Option {
	return func(c *Compiler) {
		if n <= 0 {
			c.err = errors.Join(c.err, fmt.Errorf("max iterator items must be positive, got %d", n))
			return
		}
		c.maxIterItems = n
	}
}

// WithTrace enables execution tracing in the compiled Mapping.
func WithTrace(enabled bool) Option {
	return func(c *Compiler) {
		c.trace = enabled
	}
}

// NewCompiler creates a Compiler with default settings, applying any options.
func NewCompiler(opts ...Option) *Compiler {
	c := &Compiler{
		timeout:      DefaultTimeout,
		maxTuples:    DefaultMaxTuples,
		maxRules:     language.DefaultMaxRules,
		maxIterItems: DefaultMaxIteratorItems,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Err returns any configuration errors recorded while applying options
// (e.g. a non-positive limit), or nil if the Compiler is well-configured. It lets
// callers detect misconfiguration before attempting a Compile; Compile returns the
// same error.
func (c *Compiler) Err() error {
	if c == nil {
		return fmt.Errorf("compiler is nil")
	}
	return c.err
}

// Compile is a one-shot convenience that builds a Compiler with the given options
// and compiles src in a single call. Use NewCompiler directly when compiling
// multiple sources with the same configuration.
func Compile(src []byte, opts ...Option) (*Mapping, error) {
	return NewCompiler(opts...).Compile(src)
}

// interpSegment is a parsed segment of an interpolated string.
// Exactly one of literal or program is set.
type interpSegment struct {
	literal string      // non-empty for literal text
	program *vm.Program // non-nil for an expression segment
	code    string      // original expression text (for error messages)
}

// compiledInterp is a pre-compiled interpolated string (e.g., "user:{{ input.id }}").
type compiledInterp struct {
	segments []interpSegment
	raw      string            // original YAML value (for error messages)
	field    string            // "user", "relation", "object" (for error context)
	position language.Position // source position (for diagnostic context)
}

// compiledVariable is a pre-compiled variable expression.
type compiledVariable struct {
	name    string
	program *vm.Program
	code    string // original expression text (for error messages)
}

// compiledTuple is a pre-compiled tuple template.
type compiledTuple struct {
	when      *vm.Program // nil if no when guard
	whenCode  string      // original when expression (for error messages)
	user      compiledInterp
	relation  compiledInterp
	object    compiledInterp
	action    language.TupleAction
	condition string                    // FGA condition name (literal, no compilation)
	context   map[string]compiledInterp // FGA context values (compiled interpolations); nil if no context
}

// compiledTupleFilter is a pre-compiled tuple filter template.
type compiledTupleFilter struct {
	user     *compiledInterp // nil if not set in YAML (wildcard)
	relation *compiledInterp
	object   *compiledInterp
	action   language.TupleFilterAction
}

// compiledIterator is a pre-compiled iterator.
type compiledIterator struct {
	source     *vm.Program
	sourceCode string
	as         string
	tuples     []compiledTuple
}

// compiledRule holds pre-compiled expressions for a rule.
type compiledRule struct {
	name         string
	when         *vm.Program // nil if no when guard (always runs)
	whenCode     string
	variables    []compiledVariable
	tupleFilters []compiledTupleFilter
	iterator     *compiledIterator
	tuples       []compiledTuple
}

// staticEnv returns a minimal env map used for compile-time type-hinting.
// iterAsName is intentionally NOT included: iterator variables have dynamic types
// (string, map, etc.) that are not known at compile time. AllowUndefinedVariables()
// in compileExpr accepts them as interface{}, allowing all comparisons at runtime.
func staticEnv(_ string) map[string]any {
	return map[string]any{
		"input":     map[string]any{},
		"variables": map[string]any{},
	}
}

// parseInterpolation parses and pre-compiles an interpolated string.
// The string may contain {{ expr }} segments; each expression is compiled with expr-lang.
// Literal text between expressions is preserved as-is.
// iterAsName is passed to staticEnv but intentionally ignored there — iterator variables
// have dynamic types unknown at compile time and are accepted via AllowUndefinedVariables.
func parseInterpolation(s, field string, pos language.Position, iterAsName string) (*compiledInterp, error) {
	ci := &compiledInterp{raw: s, field: field, position: pos}
	env := staticEnv(iterAsName)
	remaining := s

	for {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			if remaining != "" {
				ci.segments = append(ci.segments, interpSegment{literal: remaining})
			}
			break
		}

		if start > 0 {
			ci.segments = append(ci.segments, interpSegment{literal: remaining[:start]})
		}
		remaining = remaining[start+2:]

		end := strings.Index(remaining, "}}")
		if end < 0 {
			return nil, &EvalError{
				Expression: truncateExpr(s),
				Err:        fmt.Errorf("unclosed {{ in %s field", field),
			}
		}
		code := strings.TrimSpace(remaining[:end])
		remaining = remaining[end+2:]

		if code == "" {
			return nil, &EvalError{
				Expression: truncateExpr(s),
				Err:        fmt.Errorf("empty expression {{ }} in %s field", field),
			}
		}

		program, err := compileExpr(code, expr.Env(env))
		if err != nil {
			return nil, err
		}
		ci.segments = append(ci.segments, interpSegment{program: program, code: code})
	}

	return ci, nil
}

// compilationContext carries shared compilation state for deduplicating errors
// when the same invalid interpolation string appears in multiple tuple positions.
type compilationContext struct {
	failedInterps map[string]bool // raw string -> already reported an error
	errs          []error
}

func newCompilationContext() *compilationContext {
	return &compilationContext{
		failedInterps: make(map[string]bool),
	}
}

func (c *compilationContext) addErr(err error) {
	c.errs = append(c.errs, err)
}

// compileInterpField compiles an interpolated string field, deduplicating errors
// so that the same invalid string only produces one error across the whole mapping.
// Returns a zero-valued compiledInterp on error (compilation will fail anyway).
func (c *compilationContext) compileInterpField(s, field string, pos language.Position, iterAsName string) compiledInterp {
	ci, err := parseInterpolation(s, field, pos, iterAsName)
	if err != nil {
		if !c.failedInterps[s] {
			c.failedInterps[s] = true
			// Annotate EvalErrors with the tuple field name and YAML source position
			// so diagnostic consumers (IDE, CLI) can pinpoint the exact location.
			if ee, ok := err.(*EvalError); ok {
				ee.Field = field
				ee.Position = pos
			}
			c.errs = append(c.errs, err)
		}
		return compiledInterp{raw: s, field: field, position: pos}
	}
	return *ci
}

// compileAllRules compiles all rules in the mapping config into compiled form.
// Collects and returns all errors at once rather than failing on the first one.
func compileAllRules(config *language.MappingConfig) ([]compiledRule, error) {
	ctx := newCompilationContext()
	rules := make([]compiledRule, len(config.Rules))

	for i := range config.Rules {
		rules[i] = compileRule(&config.Rules[i], ctx)
	}

	if err := errors.Join(ctx.errs...); err != nil {
		return nil, err
	}
	return rules, nil
}

// compileRule compiles a single rule into compiled form, appending errors to ctx.
func compileRule(rule *language.Rule, ctx *compilationContext) compiledRule {
	cr := compiledRule{name: rule.Name}

	// Compile when guard
	if rule.When != "" {
		// Reject variables references in rule-level when guards — variables are
		// not in scope because when is evaluated before the variables block.
		if hasVariablesRef(rule.When) {
			ctx.addErr(&language.ValidationError{
				Message:  fmt.Sprintf("rule %q: rule-level when guards cannot reference variables entries (e.g. variables.foo); use input fields directly", rule.Name),
				Position: rule.WhenPos,
			})
		}

		prog, err := compileExpr(rule.When, expr.Env(staticEnv("")))
		if err != nil {
			if ee, ok := err.(*EvalError); ok {
				ee.RuleName = rule.Name
				ee.Field = "when"
				ee.Position = rule.WhenPos
			}
			ctx.addErr(err)
		} else {
			cr.when = prog
			cr.whenCode = rule.When
		}
	}

	// Variable source positions, keyed by name, stamped at parse time.
	varPos := make(map[string]language.Position, len(rule.Variables))
	for _, v := range rule.Variables {
		varPos[v.Name] = v.Position
	}

	// Check forward references: stamp each *EvalError with rule context and add
	// to the error list. This is not short-circuited — variable compilation
	// continues below so that syntax/size errors are also surfaced in the same pass.
	if fwdErr := checkForwardRefs(rule.Variables); fwdErr != nil {
		// checkForwardRefs already sets EvalError.Field = "variables.<name>" on
		// each leaf, so we can extract the variable name directly from Field for
		// position lookup — no expression-keyed reverse map needed.
		var leaves []error
		if joined, ok := fwdErr.(interface{ Unwrap() []error }); ok {
			leaves = joined.Unwrap()
		} else {
			leaves = []error{fwdErr}
		}
		for _, leaf := range leaves {
			if ee, ok := leaf.(*EvalError); ok {
				ee.RuleName = rule.Name
				varName := strings.TrimPrefix(ee.Field, "variables.")
				ee.Position = varPos[varName]
			}
			ctx.addErr(leaf)
		}
	}

	// Compile all variables regardless of forward-ref errors above so that
	// syntax/size errors in individual expressions are surfaced in the same pass.
	for _, v := range rule.Variables {
		prog, err := compileExpr(v.Expression, expr.Env(staticEnv("")))
		if err != nil {
			if ee, ok := err.(*EvalError); ok {
				ee.RuleName = rule.Name
				ee.Field = fmt.Sprintf("variables.%s", v.Name)
				ee.Position = v.Position
			}
			ctx.addErr(err)
			continue
		}
		cr.variables = append(cr.variables, compiledVariable{
			name:    v.Name,
			program: prog,
			code:    v.Expression,
		})
	}

	// Compile tuple filters (rendered with input + variables, no iterator scope)
	for j := range rule.TupleFilters {
		f := &rule.TupleFilters[j]
		cf := compiledTupleFilter{action: f.Action}

		if f.User != "" {
			ci := ctx.compileInterpField(f.User, "user", f.UserPos, "")
			cf.user = &ci
		}
		if f.Relation != "" {
			ci := ctx.compileInterpField(f.Relation, "relation", f.RelationPos, "")
			cf.relation = &ci
		}
		if f.Object != "" {
			ci := ctx.compileInterpField(f.Object, "object", f.ObjectPos, "")
			cf.object = &ci
		}
		cr.tupleFilters = append(cr.tupleFilters, cf)
	}

	// Compile iterator
	if rule.Iterator != nil {
		ci := &compiledIterator{as: rule.Iterator.As}

		if rule.Iterator.Source != "" {
			prog, err := compileExpr(rule.Iterator.Source, expr.Env(staticEnv("")))
			if err != nil {
				if ee, ok := err.(*EvalError); ok {
					ee.RuleName = rule.Name
					ee.Field = "iterator.source"
					ee.Position = rule.Iterator.SourcePos
				}
				ctx.addErr(err)
			} else {
				ci.source = prog
				ci.sourceCode = rule.Iterator.Source
			}
		}

		// Iterator tuples include the iterator's "as" variable in scope
		for j := range rule.Iterator.Tuples {
			ct := compileTuple(&rule.Iterator.Tuples[j], rule.Iterator.As, rule.Name, ctx)
			ci.tuples = append(ci.tuples, ct)
		}

		cr.iterator = ci
	}

	// Compile rule-level tuples (no iterator scope)
	for j := range rule.Tuples {
		ct := compileTuple(&rule.Tuples[j], "", rule.Name, ctx)
		cr.tuples = append(cr.tuples, ct)
	}

	return cr
}

// compileTuple compiles a single ParsedTuple into compiled form.
func compileTuple(pt *language.ParsedTuple, iterAsName string, ruleName string, ctx *compilationContext) compiledTuple {
	ct := compiledTuple{action: pt.Action}

	// Compile tuple-level when guard
	if pt.When != "" {
		prog, err := compileExpr(pt.When, expr.Env(staticEnv(iterAsName)))
		if err != nil {
			if ee, ok := err.(*EvalError); ok {
				ee.RuleName = ruleName
				ee.Field = "tuple.when"
				ee.Position = pt.WhenPos
			}
			ctx.addErr(err)
		} else {
			ct.when = prog
			ct.whenCode = pt.When
		}
	}

	// Compile user, relation, object interpolations
	ct.user = ctx.compileInterpField(pt.User, "user", pt.UserPos, iterAsName)
	ct.relation = ctx.compileInterpField(pt.Relation, "relation", pt.RelationPos, iterAsName)
	ct.object = ctx.compileInterpField(pt.Object, "object", pt.ObjectPos, iterAsName)

	// FGA condition name (literal pass-through, no compilation)
	ct.condition = pt.Condition

	// Compile context interpolations
	if len(pt.Context) > 0 {
		ct.context = make(map[string]compiledInterp, len(pt.Context))
		for k, v := range pt.Context {
			ct.context[k] = ctx.compileInterpField(v, "context."+k, pt.ContextPos[k], iterAsName)
		}
	}

	return ct
}

// Compile parses and validates a YAML mapping configuration,
// returning an immutable Mapping ready for evaluation.
func (c *Compiler) Compile(data []byte) (*Mapping, error) {
	if c == nil {
		return nil, fmt.Errorf("compiler is nil")
	}

	if c.err != nil {
		return nil, c.err
	}

	config, err := language.Parse(data)
	if err != nil {
		return nil, err
	}

	validationErr := config.Validate(language.WithMaxRules(c.maxRules))
	compiledRules, compileErr := compileAllRules(config)

	if err := errors.Join(validationErr, compileErr); err != nil {
		return nil, err
	}

	// The YAML AST was only needed for source-position lookup during parsing
	// (stamped onto the structs) and Validate. Release it now so it can be GC'd;
	// Mappings are typically long-lived and the node tree is non-trivial in size.
	config.ClearRoot()

	return &Mapping{
		config:       config,
		rules:        compiledRules,
		timeout:      c.timeout,
		maxTuples:    c.maxTuples,
		maxIterItems: c.maxIterItems,
		trace:        c.trace,
	}, nil
}

// CompileFile reads a YAML mapping file from disk and compiles it.
func (c *Compiler) CompileFile(path string) (*Mapping, error) {
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("reading mapping file: %w", err)
	}
	return c.Compile(data)
}

// CompileReader reads a YAML mapping from r and compiles it. It is a convenience
// over reading the stream into memory and calling Compile.
func (c *Compiler) CompileReader(r io.Reader) (*Mapping, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading mapping: %w", err)
	}
	return c.Compile(data)
}
