package mapper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openfga/mapper/language"
)

const (
	// DefaultTimeout is the maximum duration for a single Evaluate() call.
	DefaultTimeout = 3 * time.Second

	// DefaultMaxTuples is the maximum number of tuples a single event can produce.
	DefaultMaxTuples = 40
)

// Mapping is a compiled mapping configuration ready to evaluate events.
// It is immutable and safe for concurrent use across multiple goroutines.
// Each Evaluate() call operates independently with its own evaluation state.
type Mapping struct {
	config       *language.MappingConfig // retained for Version(), RuleCount(), TestCount(), RunTests()
	rules        []compiledRule          // pre-compiled rules produced by Compiler.Compile()
	timeout      time.Duration
	maxTuples    int
	maxIterItems int
	trace        bool
}

// recordRuleError stamps the rule name on the innermost *EvalError (for
// DiagnosticsFrom) and records a RuleErrored trace entry when tracing is
// enabled. It does not return; the caller is responsible for returning the error.
func (m *Mapping) recordRuleError(result *Result, ruleName string, err error, start time.Time) {
	if evalErr, ok := errors.AsType[*EvalError](err); ok {
		evalErr.RuleName = ruleName
	}

	if m.trace {
		result.Trace.Rules = append(result.Trace.Rules, RuleTrace{
			Name:     ruleName,
			Status:   RuleErrored,
			EmittedN: 0,
			FilterN:  0,
			Error:    err,
		})
		result.Trace.Duration = time.Since(start)
	}
}

// renderInterp renders a pre-compiled interpolated string against an environment.
// Each expression segment is evaluated and its string representation concatenated
// with literal segments. Returns an error if any expression returns nil, or if
// the final result is an empty string.
func renderInterp(ci *compiledInterp, env map[string]any) (string, error) {
	var sb strings.Builder
	for _, seg := range ci.segments {
		if seg.program == nil {
			sb.WriteString(seg.literal)
			continue
		}
		val, err := runExpr(seg.program, env, seg.code)
		if err != nil {
			// runExpr already returns *EvalError with Expression=seg.code (truncated).
			// Stamp the tuple field name so DiagnosticsFrom can surface it, then wrap
			// with field context. Wrapping preserves the chain for errors.As.
			if ee, ok := errors.AsType[*EvalError](err); ok {
				ee.Field = ci.field
			}
			return "", fmt.Errorf("%s: %w", ci.field, err)
		}
		if val == nil {
			return "", &EvalError{
				Expression: truncateExpr(ci.raw),
				Field:      ci.field,
				Err:        fmt.Errorf("%s expression %q evaluated to nil", ci.field, seg.code),
			}
		}
		fmt.Fprint(&sb, val)
	}
	result := sb.String()
	if result == "" {
		return "", &EvalError{
			Expression: truncateExpr(ci.raw),
			Field:      ci.field,
			Err:        fmt.Errorf("%s field rendered to empty string", ci.field),
		}
	}
	return result, nil
}

// renderCompiledTuple renders a compiledTuple's fields against the provided environment.
func renderCompiledTuple(ct *compiledTuple, env map[string]any) (language.Tuple, error) {
	user, err := renderInterp(&ct.user, env)
	if err != nil {
		return language.Tuple{}, err
	}
	relation, err := renderInterp(&ct.relation, env)
	if err != nil {
		return language.Tuple{}, err
	}
	object, err := renderInterp(&ct.object, env)
	if err != nil {
		return language.Tuple{}, err
	}
	t := language.Tuple{
		User:      user,
		Relation:  relation,
		Object:    object,
		Action:    ct.action,
		Condition: ct.condition,
	}

	if len(ct.context) > 0 {
		t.Context = make(map[string]any, len(ct.context))
		for k := range ct.context {
			ci := ct.context[k]
			val, err := renderInterp(&ci, env)
			if err != nil {
				return language.Tuple{}, err
			}
			t.Context[k] = val
		}
	}

	return t, nil
}

// renderCompiledFilterField renders an optional filter interpolation field.
// Returns "" when ci is nil (wildcard — the field was omitted in YAML).
func renderCompiledFilterField(ci *compiledInterp, env map[string]any) (string, error) {
	if ci == nil {
		return "", nil
	}
	return renderInterp(ci, env)
}

// anyRenderedFilterPatch reports whether any rendered filter has action: patch.
func anyRenderedFilterPatch(filters []language.TupleFilter) bool {
	for _, f := range filters {
		if f.Action == language.FilterActionPatch {
			return true
		}
	}
	return false
}

// renderCompiledTupleFilters renders the compiled filter templates against the
// provided environment. Omitted fields remain empty (wildcards).
func renderCompiledTupleFilters(filters []compiledTupleFilter, env map[string]any) ([]language.TupleFilter, error) {
	result := make([]language.TupleFilter, 0, len(filters))
	for _, cf := range filters {
		tf := language.TupleFilter{Action: cf.action}
		var err error

		if tf.User, err = renderCompiledFilterField(cf.user, env); err != nil {
			return nil, err
		}
		if tf.Relation, err = renderCompiledFilterField(cf.relation, env); err != nil {
			return nil, err
		}
		if tf.Object, err = renderCompiledFilterField(cf.object, env); err != nil {
			return nil, err
		}

		result = append(result, tf)
	}
	return result, nil
}

// evaluateRuleTuples evaluates all tuples for a rule, applying tuple-level when guards
// and rendering interpolated fields. Returns the slice of matched and rendered tuples.
//
// iterItem is an optional map containing iterator variables (nil if no iterator).
// When present, its keys are merged into the render environment and when guard extras.
func evaluateRuleTuples(
	ctx context.Context,
	tuples []compiledTuple,
	input map[string]any,
	variables map[string]any,
	iterItem map[string]any,
) ([]language.Tuple, error) {
	result := make([]language.Tuple, 0, len(tuples))

	env := map[string]any{
		"input":     input,
		"variables": variables,
	}
	for k, v := range iterItem {
		env[k] = v
	}

	for _, ct := range tuples {
		// Check context deadline
		if err := ctx.Err(); err != nil {
			return nil, &EvalError{Expression: "tuple", Err: err}
		}

		// Evaluate tuple-level when guard (if present).
		if ct.when != nil {
			matched, err := evaluateWhenGuard(ctx, ct.when, ct.whenCode, input, variables, iterItem)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
		}

		tuple, err := renderCompiledTuple(&ct, env)
		if err != nil {
			return nil, err
		}

		result = append(result, tuple)
	}

	return result, nil
}

// TupleTemplate is the pre-render shape of a tuple, exposing only the fields a
// static analyzer needs to check a mapping against an authorization model. The
// User, Relation, and Object fields may still contain {{ }} interpolation; a fully
// interpolated field cannot be validated statically and is skipped by the analyzer.
type TupleTemplate struct {
	User      string
	Relation  string
	Object    string
	Condition string            // FGA condition name (empty if none)
	Context   map[string]string // FGA context keys → interpolation templates (nil if none)
}

// TupleFilterTemplate is the pre-render shape of a tuple filter for static analysis.
// Empty fields are wildcards; interpolated fields cannot be validated statically.
type TupleFilterTemplate struct {
	User     string
	Relation string
	Object   string
}

// RuleSummary is a read-only view of a rule's tuple templates for static analysis.
// It carries mapper-owned template types (not the language package's parse structs),
// exposing only the fields needed to validate a mapping against an authorization
// model — no source positions or other internal parse state leak through.
type RuleSummary struct {
	Name           string
	Tuples         []TupleTemplate
	IteratorTuples []TupleTemplate
	TupleFilters   []TupleFilterTemplate
}

// tupleTemplateFrom projects a parsed tuple onto the narrow static-analysis view,
// copying the context map so callers cannot mutate parse state.
func tupleTemplateFrom(pt language.ParsedTuple) TupleTemplate {
	t := TupleTemplate{
		User:      pt.User,
		Relation:  pt.Relation,
		Object:    pt.Object,
		Condition: pt.Condition,
	}
	if len(pt.Context) > 0 {
		t.Context = make(map[string]string, len(pt.Context))
		for k, v := range pt.Context {
			t.Context[k] = v
		}
	}
	return t
}

func tupleTemplatesFrom(pts []language.ParsedTuple) []TupleTemplate {
	if len(pts) == 0 {
		return nil
	}
	out := make([]TupleTemplate, len(pts))
	for i, pt := range pts {
		out[i] = tupleTemplateFrom(pt)
	}
	return out
}

// Rules returns a read-only summary of each rule's tuple templates for static
// analysis (e.g., model validation). The returned values are copies — callers
// cannot mutate the compiled mapping's internal state.
func (m *Mapping) Rules() []RuleSummary {
	summaries := make([]RuleSummary, len(m.config.Rules))
	for i, r := range m.config.Rules {
		s := RuleSummary{
			Name:   r.Name,
			Tuples: tupleTemplatesFrom(r.Tuples),
		}
		if r.Iterator != nil {
			s.IteratorTuples = tupleTemplatesFrom(r.Iterator.Tuples)
		}
		if len(r.TupleFilters) > 0 {
			s.TupleFilters = make([]TupleFilterTemplate, len(r.TupleFilters))
			for j, f := range r.TupleFilters {
				s.TupleFilters[j] = TupleFilterTemplate{
					User:     f.User,
					Relation: f.Relation,
					Object:   f.Object,
				}
			}
		}
		summaries[i] = s
	}
	return summaries
}

// Version returns the schema version declared in the mapping file.
func (m *Mapping) Version() string {
	return m.config.Version
}

// RuleCount returns the number of rules in the compiled mapping.
func (m *Mapping) RuleCount() int {
	return len(m.config.Rules)
}

// TestCount returns the number of embedded test cases in the compiled mapping.
func (m *Mapping) TestCount() int {
	return len(m.config.Tests)
}

// Evaluate runs all rules against the given event and returns the result.
//
// For each rule:
//  1. Evaluate variables (sequential, with access to input and prior variables)
//  2. Evaluate rule when guard (skip rule if false)
//  3. Fan-out via iterator (or single pass if no iterator), rendering tuples per item
//
// After all rules:
//  4. Deduplicate tuples and detect write/delete conflicts
//  5. Enforce maxTuples limit
func (m *Mapping) Evaluate(ctx context.Context, event map[string]any) (*Result, error) {
	if event == nil {
		return nil, &EvalError{Expression: "", Err: fmt.Errorf("event must not be nil")}
	}

	// Apply the mapping's timeout, but never extend a caller's existing shorter deadline.
	deadline, hasDeadline := ctx.Deadline()
	mappingDeadline := time.Now().Add(m.timeout)
	if !hasDeadline || mappingDeadline.Before(deadline) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}

	start := time.Now()
	result := &Result{}

	if m.trace {
		result.Trace = &Trace{
			Rules: make([]RuleTrace, 0, len(m.rules)),
		}
	}

	// Track the distinct tuples rendered so far so a runaway iterator is stopped
	// as soon as it exceeds maxTuples, rather than after materializing the whole
	// source. Keyed by full identity, the distinct count never exceeds the final
	// post-dedup total, so this only fires when the final check would also fail.
	seenForBudget := make(map[string]struct{})
	checkBudget := func(batch []language.Tuple) error {
		for _, t := range batch {
			seenForBudget[t.Key()] = struct{}{}
		}
		if n := len(seenForBudget); n > m.maxTuples {
			return &EvalError{
				Expression: "maxTuples",
				Err:        fmt.Errorf("event produced %d tuples, exceeding limit of %d", n, m.maxTuples),
			}
		}
		return nil
	}

	for _, cr := range m.rules {
		// Check deadline before each rule
		if err := ctx.Err(); err != nil {
			evalErr := &EvalError{
				Expression: cr.name,
				Err:        err,
			}
			m.recordRuleError(result, cr.name, evalErr, start)
			return result, evalErr
		}

		// Step 1: Evaluate when guard (before variables — skipped rules don't
		// need variable expressions to succeed).
		matched, err := evaluateWhenGuard(ctx, cr.when, cr.whenCode, event, nil)
		if err != nil {
			m.recordRuleError(result, cr.name, err, start)
			return result, fmt.Errorf("rule %q when: %w", cr.name, err)
		}

		if !matched {
			if m.trace {
				result.Trace.Rules = append(result.Trace.Rules, RuleTrace{
					Name:     cr.name,
					Status:   RuleSkipped,
					EmittedN: 0,
					FilterN:  0,
				})
			}
			continue
		}

		// Step 2: Evaluate variables (only for matched rules).
		variables, err := evaluateVariables(ctx, cr.variables, event)
		if err != nil {
			m.recordRuleError(result, cr.name, err, start)
			return result, fmt.Errorf("rule %q variables: %w", cr.name, err)
		}

		// Step 3: Render tuple filters (if present, before iterator).
		var renderedFilters []language.TupleFilter
		hasTupleFilters := len(cr.tupleFilters) > 0

		if hasTupleFilters {
			filterEnv := map[string]any{
				"input":     event,
				"variables": variables,
			}
			var filterErr error
			renderedFilters, filterErr = renderCompiledTupleFilters(cr.tupleFilters, filterEnv)
			if filterErr != nil {
				m.recordRuleError(result, cr.name, filterErr, start)
				return result, fmt.Errorf("rule %q tuple_filters: %w", cr.name, filterErr)
			}
		}

		// Step 4: Iterator fan-out (or direct tuple evaluation).
		var ruleTuples []language.Tuple

		if cr.iterator != nil {
			items, err := evaluateIteratorSource(ctx, cr.iterator.source, cr.iterator.sourceCode, event, variables, m.maxIterItems)
			if err != nil {
				m.recordRuleError(result, cr.name, err, start)
				return result, fmt.Errorf("rule %q iterator: %w", cr.name, err)
			}

			iterItem := make(map[string]any, 1)

			for _, item := range items {
				iterItem[cr.iterator.as] = item
				batch, err := evaluateRuleTuples(ctx, cr.iterator.tuples, event, variables, iterItem)
				if err != nil {
					m.recordRuleError(result, cr.name, err, start)
					return result, fmt.Errorf("rule %q iterator.tuples: %w", cr.name, err)
				}
				ruleTuples = append(ruleTuples, batch...)
				if err := checkBudget(batch); err != nil {
					m.recordRuleError(result, cr.name, err, start)
					return result, err
				}
			}

			// Static tuples (optional; evaluated once with no iterItem)
			if len(cr.tuples) > 0 {
				batch, err := evaluateRuleTuples(ctx, cr.tuples, event, variables, nil)
				if err != nil {
					m.recordRuleError(result, cr.name, err, start)
					return result, fmt.Errorf("rule %q tuples: %w", cr.name, err)
				}
				ruleTuples = append(ruleTuples, batch...)
				if err := checkBudget(batch); err != nil {
					m.recordRuleError(result, cr.name, err, start)
					return result, err
				}
			}
		} else if len(cr.tuples) > 0 {
			batch, err := evaluateRuleTuples(ctx, cr.tuples, event, variables, nil)
			if err != nil {
				m.recordRuleError(result, cr.name, err, start)
				return result, fmt.Errorf("rule %q tuples: %w", cr.name, err)
			}
			ruleTuples = append(ruleTuples, batch...)
			if err := checkBudget(batch); err != nil {
				m.recordRuleError(result, cr.name, err, start)
				return result, err
			}
		}

		// Route output: tuple_filters rules produce TupleFilterOperations,
		// non-filter rules produce result.Tuples.
		if hasTupleFilters {
			if anyRenderedFilterPatch(renderedFilters) && len(ruleTuples) == 0 {
				evalErr := &EvalError{
					Expression: cr.name,
					Err:        fmt.Errorf("rule has patch tuple_filters but produced no tuples; an empty desired state would delete all matching tuples"),
				}
				m.recordRuleError(result, cr.name, evalErr, start)
				return result, evalErr
			}

			result.TupleFilterOperations = append(result.TupleFilterOperations, TupleFilterOperation{
				Filters: renderedFilters,
				Tuples:  ruleTuples,
			})
		} else {
			result.Tuples = append(result.Tuples, ruleTuples...)
		}

		if m.trace {
			result.Trace.Rules = append(result.Trace.Rules, RuleTrace{
				Name:     cr.name,
				Status:   RuleMatched,
				EmittedN: len(ruleTuples),
				FilterN:  len(renderedFilters),
			})
		}
	}

	// Post-process: deduplicate then detect write/delete conflicts.
	if err := result.postProcess(); err != nil {
		return result, err
	}

	// Enforce maxTuples limit against the final deduplicated set.
	totalTuples := len(result.Tuples)
	for _, op := range result.TupleFilterOperations {
		totalTuples += len(op.Tuples)
	}
	if totalTuples > m.maxTuples {
		return result, &EvalError{
			Expression: "maxTuples",
			Err: fmt.Errorf(
				"event produced %d tuples, exceeding limit of %d",
				totalTuples,
				m.maxTuples,
			),
		}
	}

	if m.trace {
		result.Trace.Duration = time.Since(start)
	}

	return result, nil
}

// TestResult captures the outcome of a single embedded test case.
type TestResult struct {
	Name   string
	Passed bool
	// Expected is the set of tuples the test case declared.
	Expected []language.Tuple
	// Actual is the set of tuples evaluation produced.
	Actual []language.Tuple
	// ExpectedTupleFilters is the set of tuple filters the test case declared.
	ExpectedTupleFilters []language.TupleFilter
	// ActualTupleFilters is the set of tuple filters evaluation produced.
	ActualTupleFilters []language.TupleFilter
	// Error is populated if the test failed due to an evaluation error.
	Error error
	// Duration is the wall time taken to evaluate this test case.
	Duration time.Duration
	// Trace holds rule-level execution details; nil unless the mapping was compiled with WithTrace(true).
	Trace *Trace
	// Input is the event that was evaluated, preserved for display in verbose failure output.
	Input map[string]any
}

// FilteredTestRun holds the outcome of a filtered test execution.
type FilteredTestRun struct {
	// Results contains outcomes for tests that were executed.
	Results []TestResult
	// Filtered is the number of tests skipped because they did not match the --run filter.
	Filtered int
	// Skipped is the number of matching tests that were not run because fail-fast triggered.
	Skipped int
}

// Stopped reports whether fail-fast actually prevented matching tests from running.
func (r FilteredTestRun) Stopped() bool { return r.Skipped > 0 }

// NotRun returns the total number of tests that did not execute (filtered + skipped).
func (r FilteredTestRun) NotRun() int { return r.Filtered + r.Skipped }

// RunTests executes the embedded test cases from the mapping configuration
// and returns results for each case.
func (m *Mapping) RunTests(ctx context.Context) []TestResult {
	return m.RunTestsFiltered(ctx, "", false).Results
}

// RunTestsFiltered executes embedded test cases with optional name filtering and fail-fast support.
//
// filter is a case-sensitive substring; empty string runs all tests.
// If failFast is true, execution stops after the first failure or error.
func (m *Mapping) RunTestsFiltered(ctx context.Context, filter string, failFast bool) FilteredTestRun {
	run := FilteredTestRun{
		Results: make([]TestResult, 0, len(m.config.Tests)),
	}

	stopped := false

	for _, tc := range m.config.Tests {
		if filter != "" && !strings.Contains(tc.Name, filter) {
			run.Filtered++
			continue
		}

		if stopped {
			run.Skipped++
			continue
		}

		tr := TestResult{
			Name:                 tc.Name,
			Expected:             tc.ExpectTuples,
			ExpectedTupleFilters: tc.ExpectTupleFilters,
			Input:                tc.Input,
		}

		start := time.Now()
		result, err := m.Evaluate(ctx, tc.Input)
		tr.Duration = time.Since(start)

		if result != nil {
			tr.Trace = result.Trace

			allTuples := append([]language.Tuple(nil), result.Tuples...)
			var allFilters []language.TupleFilter
			for _, op := range result.TupleFilterOperations {
				allTuples = append(allTuples, op.Tuples...)
				allFilters = append(allFilters, op.Filters...)
			}
			tr.Actual = allTuples
			tr.ActualTupleFilters = allFilters
		}

		if err != nil {
			tr.Passed = false
			tr.Error = err
		} else {
			tr.Passed = tuplesMatch(tc.ExpectTuples, tr.Actual)

			if tr.Passed && len(tc.ExpectTupleFilters) > 0 {
				tr.Passed = tupleFiltersMatch(tc.ExpectTupleFilters, tr.ActualTupleFilters)
			}

			if tr.Passed && tc.AssertWritesCoveredByFilter {
				tr.Passed = allWritesCoveredByFilters(tr.Actual, tr.ActualTupleFilters)
			}
		}

		run.Results = append(run.Results, tr)

		if failFast && !tr.Passed {
			stopped = true
		}
	}

	return run
}

// tupleFiltersMatch checks if two slices of TupleFilter are equivalent, ignoring order.
func tupleFiltersMatch(expected, actual []language.TupleFilter) bool {
	if len(expected) != len(actual) {
		return false
	}
	counts := make(map[language.TupleFilter]int)
	for _, f := range expected {
		counts[f]++
	}
	for _, f := range actual {
		counts[f]--
		if counts[f] < 0 {
			return false
		}
	}
	return true
}

// isObjectTypePrefix reports whether a string is an object type prefix (e.g., "org:").
// The type portion must be non-empty, so ":" alone is not a valid prefix.
// Duplicated from the language package's validation helper: it is a frozen predicate
// over the FGA tuple format, cheaper to copy than to widen the language SDK surface.
func isObjectTypePrefix(s string) bool {
	if len(s) < 2 {
		return false
	}
	idx := strings.IndexByte(s, ':')
	return idx > 0 && idx == len(s)-1
}

// isTupleCoveredByFilter reports whether a tuple is covered by a filter.
func isTupleCoveredByFilter(t language.Tuple, f language.TupleFilter) bool {
	if f.User != "" && f.User != t.User {
		return false
	}
	if f.Relation != "" && f.Relation != t.Relation {
		return false
	}
	if f.Object != "" {
		if isObjectTypePrefix(f.Object) {
			if !strings.HasPrefix(t.Object, f.Object) {
				return false
			}
		} else if f.Object != t.Object {
			return false
		}
	}
	return true
}

// allWritesCoveredByFilters checks that every write tuple is covered by at least one filter.
func allWritesCoveredByFilters(tuples []language.Tuple, filters []language.TupleFilter) bool {
	for _, t := range tuples {
		if t.Action != language.ActionWrite {
			continue
		}
		covered := false
		for _, f := range filters {
			if isTupleCoveredByFilter(t, f) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// tuplesMatch checks if two slices of tuples are equivalent, ignoring order.
func tuplesMatch(expected, actual []language.Tuple) bool {
	if len(expected) != len(actual) {
		return false
	}

	tupleCount := make(map[string]int)
	for _, tuple := range expected {
		tupleCount[tuple.Key()]++
	}

	for _, tuple := range actual {
		k := tuple.Key()
		tupleCount[k]--
		if tupleCount[k] < 0 {
			return false
		}
	}

	return true
}
