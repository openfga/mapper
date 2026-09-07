# Mapper Package

> **User-facing language specification:** See [`docs/language-spec.md`](language-spec.md). This README covers internal architecture and design decisions.

## Overview

The `mapper` package compiles validated FGA mapping configurations into an executable `Mapping` and evaluates JSON events against it, producing OpenFGA relationship tuples. Parsing and validation are delegated to the [`language`](../language/README.md) package; `mapper` depends on `language`, never the reverse.

## Architecture

### Components

**Compiler → Mapping → Result**

1. **Compiler** — Holds configuration options (timeout, workload limits, tracing)
   - `NewCompiler(opts...)` creates a compiler with default workload limits, applying any options
   - `WithTimeout`, `WithMaxTuples`, `WithMaxRules`, `WithMaxIteratorItems`, `WithTrace` are functional `Option`s that override defaults; invalid values (e.g. a non-positive limit) are recorded and surfaced from `Compile`. `Err()` returns the same recorded error, letting callers detect misconfiguration before compiling.
   - `Compile(yaml)` parses and validates YAML (via `language.Parse`), compiles expressions, returns an immutable Mapping
   - `CompileFile(path)` reads a file and compiles it; `CompileReader(r)` reads from an `io.Reader`
   - `mapper.Compile(yaml, opts...)` is a package-level one-shot facade equivalent to `NewCompiler(opts...).Compile(yaml)`; use `NewCompiler` directly to compile many sources with the same configuration (as a long-lived consumer caching compiled Mappings would)

2. **Mapping** — Compiled, immutable mapping configuration
   - Thread-safe: multiple goroutines can call `Evaluate()` concurrently
   - Each `Evaluate()` call operates independently with its own state
   - `Evaluate(ctx, event)` runs the full pipeline and returns a Result

3. **Result** — Contains generated tuples and optional trace data
   - `Tuples []language.Tuple` — the relationship tuples produced (`mapper.Tuple` is an alias for `language.Tuple`)
   - `Trace *Trace` — execution trace (nil unless `WithTrace(true)`)

**Static analysis surface**

Alongside `Evaluate()`, a compiled `Mapping` exposes `Rules() []RuleSummary` — an event-independent view of the tuple *templates* a mapping declares, without evaluating against any event. It backs consumers that inspect a mapping's shape (e.g. the `validate` CLI's model checks). The view types are mapper-owned and deliberately narrow — they expose only the interpolated template strings, not the parse-time positions or actions carried by the underlying `language` structs:

- `RuleSummary` — a rule's name plus its `Tuples`, `IteratorTuples`, and `TupleFilters` templates
- `TupleTemplate` — `User`, `Relation`, `Object` template strings, plus `Condition` (literal condition name) and `Context` (key → template map)
- `TupleFilterTemplate` — `User`, `Relation`, `Object` template strings

### Evaluation Pipeline

For each rule in order:

1. **Evaluate Variables** — Sequential evaluation with scope that grows with each variable
   - Each variable can reference `input` (the event) and `variables` (prior variables)
   - Forward references are detected via AST analysis and rejected before evaluation
   - String results exceeding 4KB are truncated at UTF-8 boundaries

2. **Evaluate When Guard** — Determines if the rule should run
   - Empty `when` → always runs (true)
   - Non-bool results coerced: `nil`/`0`/`""`/empty → false, else true
   - When guard evaluates to false → rule skipped (not an error)
   - When guard compilation/evaluation error → `Evaluate` returns an error

3. **Iterator Fan-out** — Resolves the iterator source and loops over items (iterator rules only)
   - The `source` expression must resolve to an array; nil/missing → treated as empty (no tuples, no error)
   - Each item is bound to the `as` name and made available in tuple fields and when guards
   - Maximum 1000 items per iterator source; exceeding this returns `*EvalError`
   - Rules without an iterator are treated as a single pass (equivalent to a one-item array)

4. **Render Tuples** — Interpolated strings produce final tuples (once per iterator item, or once for non-iterator rules)
   - Pre-compiled at `Compiler.Compile()` time for syntax validation and reuse
   - Each tuple's User, Relation, and Object are rendered independently
   - Tuple-level when guards filter which tuples are emitted
   - Expressions that evaluate to `nil` (e.g., missing map key) → `EvalError`
   - Empty rendered strings → error
   - Action defaults to "write" if not specified

After all rules: deduplicate and detect write/delete conflicts (`postProcess()`).

## Expression Language

The mapper uses [expr-lang/expr](https://github.com/expr-lang/expr) v1.17+ for expression evaluation. Expressions are sandboxed—no file I/O, network, or shell access.

### Built-in String Functions

- `lower(s)` — convert string to lowercase
- `upper(s)` — convert string to uppercase
- `trim(s)` — remove leading/trailing whitespace
- `split(s, sep)` — split string and return `[]string`
- `join(arr, sep)` — join array elements with separator
- `replace(s, old, new)` — replace substring
- `hasPrefix(s, prefix)` — check string prefix
- `hasSuffix(s, suffix)` — check string suffix
- `contains` — substring containment operator (`"hello" contains "world"`)

### Built-in Collection Functions

- `len(x)` — length of string, array, or map
- `in` — membership operator (`"x" in array`, `"key" in map`)
- `??` — null coalescing operator (`value ?? "default"`)

### Custom Functions

#### `json_path(obj, path)`

Traverses nested map/slice structures using dot-separated paths.

**Examples:**
```go
json_path(input.data, "user.email")           → "alice@example.com"
json_path(input.items, "0.name")              → "first item" (array index)
json_path(input.data, "missing.key")          → nil
json_path(input.data, "user.email") ?? "n/a"  → safe with null coalescing
```

**Behavior:**
- Returns `nil` for missing paths (safe for use with `??` operator)
- Supports map keys and array indices (e.g., `"items.0"`)
- Max depth: 100 segments (prevents excessively deep traversal)
- Empty segments (e.g., `"a..b"`) return `nil`

#### `fga_escape(s)`

Percent-encodes characters that are invalid in OpenFGA tuple fields, making any arbitrary string safe for use in any tuple field position (user ID, object ID, userset ID, relation).

**Examples:**
```go
fga_escape("idp|abc123")        → "idp|abc123"   (pipe is allowed, no change)
fga_escape("user#admin")          → "user%23admin"
fga_escape("org:special")         → "org%3Aspecial"
fga_escape("hello world")         → "hello%20world"
fga_escape("already%23encoded")   → "already%2523encoded" (no double-encoding)
```

**Characters escaped:** `#`, `:`, `@`, `*`, space, `%`, and Unicode control characters — the union of all forbidden characters across all OpenFGA tuple field contexts (based on server-side validation in `openfga/pkg/tuple/tuple.go`). `%` is escaped for encoding safety (prevents double-encoding ambiguity).

**Encoding format:** `%XX` for characters ≤ 0xFF, `%UXXXX` for wider Unicode control characters. Uppercase hex digits.

**Behavior:**
- Strings with no forbidden characters are returned unchanged
- Empty strings are returned as-is (the mapper's empty-string check catches this downstream)

**Security:**
- No network, file I/O, or shell functions available
- Expression size limited to 64KB
- Variable results truncated to 4KB
- Iterator source arrays limited to 1000 items

## Interpolation Rendering

### Syntax

Tuple User, Relation, and Object fields are interpolated strings (`{{ expr }}` syntax) rendered at evaluation time. Expressions have access to:

```
{{ input.field }}              — access event fields
{{ variables.name }}           — access computed variables
{{ input.data.user_id }}       — nested event fields
{{ role_name }}                — iterator variable (when rule has an iterator with as: role_name)
```

### Compilation

Interpolations are compiled once during `Compiler.Compile()`:
- Syntax errors are caught immediately, with `Field` and `Position` on the resulting `*EvalError`
- Each unique interpolation string is compiled once and reused across evaluations
- If an expression evaluates to `nil` at runtime (e.g., a missing map key), the field fails with `*EvalError`; use `?? "default"` to provide a fallback
- Only `input`, `variables`, iterator variables, and `json_path()` are practically useful in interpolations — the full expr-lang is available but no external calls (file I/O, network, shell) exist

**Example:**
```yaml
rules:
  - name: "user tuple"
    variables:
      org_id: "input.organization.id"
    tuples:
      - user: "u:{{ variables.org_id }}:{{ input.email }}"
        relation: "member"
        object: "org:{{ variables.org_id }}"
```

### Tuple-Level When Guards

Each tuple can have an optional `when` field (Expr expression) that filters which tuples are emitted:

```yaml
tuples:
  - user: "u:{{ input.user_id }}"
    relation: "admin"
    object: "o:{{ input.org_id }}"
    when: "input.role == 'admin'"  # Only emit if role is admin
```

If the when guard evaluates to false, the tuple is skipped (not rendered). Empty when guards always pass.

### Output Size

Interpolated fields draw from event data and variables (variables are capped at 4KB). Rendered field output itself is unbounded for now.

## Iterator / Fan-out

An iterator rule emits one set of tuples per item in a source array, enabling fan-out from a single event field. Tuples within the iterator are evaluated once per item; static tuples at the rule level (optional) are evaluated once after the iterator.

### YAML Syntax

The iterator carries its own `tuples` list. Rule-level `tuples` become optional when an iterator is present and are evaluated once (without the iterator variable) after the loop.

```yaml
rules:
  - name: "sync user roles"
    when: "len(input.roles) > 0"
    variables:
      org_id: "input.org_id ?? \"default\""
    iterator:
      source: input.roles   # Expr expression resolving to an array
      as: role              # Name bound in tuple fields and when guards
      tuples:               # Emitted once per iterator item
        - when: "role != \"guest\""
          user: "user:{{ input.user_id }}"
          relation: "member"
          object: "org:{{ variables.org_id }}:role:{{ role }}"
    tuples:                 # Optional: emitted once after iterator completes
      - user: "user:{{ input.user_id }}"
        relation: "member"
        object: "org:{{ variables.org_id }}"
```

### Behaviour

- `source` is evaluated as an Expr expression after variables are computed, so variables are available: `source: variables.computed_list`
- `nil` or missing source → empty iteration (zero iterator-scoped tuples emitted, no error); rule-level static `tuples` are still evaluated once
- **Iterator-scoped tuples:** The `as` name is available in `iterator.tuples` fields and when guards as a top-level identifier — reference it as `{{ role }}` in tuple fields and `role != "guest"` in when guards (no prefix)
- **Static tuples:** Rule-level `tuples` are optional when an iterator is present; they are evaluated once with no iterator variable in scope, so accessing the iterator name evaluates to `nil` and produces an `EvalError`
- The names `input` and `variables` are reserved and may not be used as `as` names
- Iterator variables are scoped to `iterator.tuples` only; they are not available in rule-level `tuples`

### Limits

Workload limits are configurable per-compile via `Option`s; they bound honest scale rather than malicious input:

| Limit | Value | Enforcement |
|-------|-------|-------------|
| Max items in iterator source | `WithMaxIteratorItems` (default 1000) | `evaluateIteratorSource` returns `*EvalError` |
| Max total emitted tuples | `WithMaxTuples` (default 40) | `Mapping.Evaluate` returns `*EvalError` |
| Max rules per mapping file | `WithMaxRules` (default 100) | `language.Validate` returns `*ValidationError` (enforced during validation) |

Fixed safety limits (in `expr.go`) bound pathological or malicious input and are deliberately **not** configurable — exposing them as knobs would let a consumer weaken the sandbox against untrusted mapping authors: max variable string size (4KB), max expression size (64KB), and max `json_path` segments (100).

The expression environment is a deliberately **closed** sandbox: it exposes only `json_path` and `fga_escape` on top of expr-lang's pure built-ins — no file I/O, network, shell, or reflection. Custom-function registration is intentionally not exposed, since a raw `WithFunction` hook would punch a hole in the sandbox with no resource governance or safe-input contract (see the doc comment on `stdlibOptions` in `expr.go`).

## Error Types

The mapper produces two error types (both distinguishable via `errors.As()`); it also surfaces `*language.ValidationError` unchanged from the parse phase.

- **`*EvalError`** — Expression evaluation error (syntax, runtime, forward refs, limits exceeded); for compile-time interpolation errors also includes `Field` (tuple field name) and `Position` (YAML source location)
- **`*ConflictError`** — Write/delete conflict on the same (user, relation, object) key, detected during `postProcess()` after deduplication
- **`*language.ValidationError`** — see the [`language` README](../language/README.md#validation-errors); returned when `Compile()` delegates parsing/validation

The tables below enumerate every condition that produces each mapper error type, with example `Error()` output taken from `diagnostic_test.go`, `types_test.go`, and `mapping_test.go`. Message text may be reworded over time; `Category` and `Field` (surfaced via `Diagnostic`, below) are the stable, programmatically-checked contract — the [user-facing spec](language-spec.md#error-types) links back here for this detail.

### EvalError Scenarios

Compile-time (surfaced during `Compile()`, before any event is evaluated) — sites: `compiler.go` (`parseInterpolation`), `expr.go` (`compileExpr`, `checkForwardRefs`):

| Scenario | Trigger | Example Message |
|----------|---------|------------------|
| Unclosed interpolation | `{{` in a tuple/filter field with no matching `}}` | `unclosed {{ in user field` |
| Empty interpolation | `{{ }}` with no expression inside | `empty expression {{ }} in user field` |
| Expression too large | An expression exceeds the 64KB size limit | `expression exceeds max size of 65536 bytes` |
| Forward reference | A variable references a variable defined later in the same `variables` block | `variable "a" references "b" which is defined later` |
| Expression syntax error | Invalid Expr syntax in a when guard, variable, iterator source, or interpolation | (wrapped expr-lang parse error, e.g. `unexpected token: '}'`) |

Runtime (surfaced during `Evaluate()` against a specific event) — sites: `mapping.go` (`renderInterp`, `Evaluate`), `iterator.go` (`evaluateIteratorSource`), `expr.go` (`json_path`, `fga_escape`):

| Scenario | Trigger | Example Message |
|----------|---------|------------------|
| Nil event | `Evaluate` called with a nil event | `event must not be nil` |
| Evaluation timeout | Context deadline exceeded mid-evaluation | `context deadline exceeded` |
| Nil interpolation result | A `user`/`relation`/`object`/context expression evaluates to `nil` (e.g. a missing map key) | `user expression "input.missing" evaluated to nil` |
| Empty rendered field | A tuple field renders to `""` | `user field rendered to empty string` |
| Iterator source too large | Iterator `source` resolves to more than 1000 items | `iterator source has 1001 items, exceeding maximum of 1000` |
| Max tuples exceeded | Deduplicated tuple count for the event exceeds the configured limit (40 by default) | `event produced 41 tuples, exceeding limit of 40` |
| Patch filter with zero tuples | A rule has an `action: patch` tuple filter but produced no tuples at runtime | `rule has patch tuple_filters but produced no tuples; an empty desired state would delete all matching tuples` |
| `json_path` bad arity | `json_path()` called with other than 2 arguments | `json_path requires exactly 2 arguments, got 1` |
| `json_path` bad path type | `json_path()`'s second argument is not a string | `json_path path must be a string, got int` |
| `json_path` depth exceeded | Dot-separated path has more than 100 segments | `json_path exceeds maximum of 100 segments` |
| `fga_escape` bad arity | `fga_escape()` called with other than 1 argument | `fga_escape requires exactly 1 argument, got 2` |
| `fga_escape` bad argument type | `fga_escape()`'s argument is not a string | `fga_escape argument must be a string, got int` |

Compile-time errors carry `Field` (the tuple field name, e.g. `"user"`) and a `Position`. Runtime errors typically have `Field` empty (or set to the rule name by the mapper) and `Position` zero, since there's no compile-time YAML location to point to.

### ConflictError Scenarios

`ConflictError` is produced by `postProcess()` (`types.go`) after deduplication. OpenFGA identifies a stored relationship by its `(user, relation, object)` triple — condition and context are payload, not identity — so a single URO carrying more than one incompatible desired state cannot be expressed in one Write batch and is rejected. The `Kind` field distinguishes the two cases.

| Scenario | `Kind` | Example Message |
|----------|--------|------------------|
| Write + delete on the same (user, relation, object), no condition | `ConflictWriteDelete` | `conflict: tuple (u:1, r, o:1) has both a write and a delete action` |
| Write + delete on the same URO, write carries a condition | `ConflictWriteDelete` | `conflict: tuple (u:1, viewer, doc:x) with condition "my_cond" has both a write and a delete action` |
| Two writes on the same URO whose condition or context differ | `ConflictCompetingWrites` | `conflict: tuple (u:1, viewer, doc:x) has competing write actions with differing condition or context` |

A `write` and a `delete` on the same URO conflict regardless of whether their conditions match, since a Write batch cannot both add and remove the same relationship. Two writes on the same URO with identical condition and context deduplicate to one; if their condition or context differ they are competing desired states and conflict. Repeated deletes on the same URO likewise deduplicate. This means changing a tuple's condition is a genuine conflict, not a silently-distinct tuple — the mapping must express it as an explicit delete followed by a write.

## Diagnostics

The `Diagnostic` struct provides a uniform, consumer-facing representation of errors, spanning both mapper errors and `language.ValidationError`:

```go
diags := mapper.DiagnosticsFrom(err)  // extract Diagnostics from any error tree
text := diags.String()                // grouped human-readable output
```

`DiagnosticsFrom()` walks `errors.Join` trees and converts each typed error into a `Diagnostic` with category, field, message, and position (value type, zero = unknown). `Category` is a named string type with constants `CategoryValidation`, `CategoryEval`, `CategoryConflict`, and `CategoryUnknown` (JSON values `"validation"`, `"eval"`, `"conflict"`, `"unknown"`).

`Diagnostics` is a named type (`[]Diagnostic`) with a `String()` method (satisfies `fmt.Stringer`) that uses `line:col-line:col:` format (Go/GCC convention) for positions, grouped by category.

## Agent conventions

- **TupleFilterOperation:** Groups a rule's rendered `[]TupleFilter` with its desired-state `[]Tuple`. One per rule that has `tuple_filters`. The consumer uses these to drive read-diff-write against FGA.
- **Evaluation order:** `when` guard evaluated before variables. Rule-level when guards only have `input` in scope; direct `variables` member access is rejected at compile time via `hasVariablesRef()`. Tuple-level when guards retain access to both `input` and `variables`.
- **Expression compilation:** All expressions compiled at `Compile()` time; variable forward references caught via `checkForwardRefs()`; when guard and interpolation expressions compiled with `AllowUndefinedVariables` since event shape is dynamic.
- **Tuple context:** Context values are compiled as interpolations via `compileInterpField()`.

## Design Decisions

### Why evaluate variables in declaration order?

All expressions, variables included, are compiled once during `Compiler.Compile()`. Variables are then *evaluated* in declaration order during `Evaluate()`: each variable's value is added to scope before the next runs, so variable B can reference variable A. Forward references (B referencing a later-declared C) are rejected at compile time by `checkForwardRefs()` rather than silently evaluating to `nil`.

### Why use `AllowUndefinedVariables`?

Events have dynamic shapes—different events have different fields. We use `expr.Env(env)` for type information (is `input` a map?) while allowing undefined fields (does `input.user.email` exist?) to handle varying JSON structures. The alternative (strict mode) would require knowing the exact event schema at compile time.

### Why detect forward references?

Without detection, `variables.b` referencing a later-defined `variables.c` would silently evaluate to `nil` (expr-lang returns `nil` for missing map keys). This leads to confusing behavior. AST-based detection catches these errors before evaluation begins, providing clear error messages.

### Why truncate variables to 4KB?

Variables are bounded to prevent memory exhaustion from pathological inputs. Truncation happens after evaluation, preserving the computed value up to the limit while preventing unbounded growth.

### Why treat a nil/missing iterator source as empty rather than an error?

A missing field most often means the event simply doesn't have that data — not a bug. Treating it as empty allows a single mapping to handle both "event has roles" and "event has no roles" without requiring a rule-level `when` guard. Authors who want to distinguish the cases can add an explicit `when` guard on the rule.

## Usage Example

```go
// One-shot: compile a single source with options.
m, err := mapper.Compile(yamlBytes,
    mapper.WithTimeout(20*time.Millisecond),
    mapper.WithMaxTuples(40),
    mapper.WithTrace(true),
)

result, err := m.Evaluate(ctx, map[string]any{
    "type": "user.created",
    "data": map[string]any{
        "email": "alice@example.com",
    },
})

// result.Tuples contains the generated relationship tuples
// result.Trace contains execution trace (if enabled)
```

To compile many sources with the same configuration, build a `Compiler` once and reuse it:

```go
compiler := mapper.NewCompiler(mapper.WithTrace(true))
m, err := compiler.CompileFile("mapping.yaml")
```
