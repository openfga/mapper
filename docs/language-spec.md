# FGA Mapping Language Specification

Version: 1

This document is the canonical specification for the FGA mapping language.

## Overview

The mapping language transforms arbitrary JSON events into [OpenFGA](https://openfga.dev/) Relationship Tuples using a declarative YAML configuration. A mapping file defines an ordered list of rules, each of which can filter events by a when guard, compute intermediate values, iterate over collections, and render one or more tuples.

## Document Structure

A mapping file is a YAML document with the following top-level fields:

```yaml
version: "1"        # Required. Schema version.
rules: []              # Required. Ordered list of rules (1-100).
tests: []              # Optional. Embedded test cases.
```

### Minimal Example

```yaml
version: "1"
rules:
  - name: "Grant membership"
    tuples:
      - user: "user:{{ input.user_id }}"
        relation: "member"
        object: "org:{{ input.org_id }}"
```

## Rules

Rules are evaluated in order. Each rule has the following fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Human-readable identifier (must be unique within the file for clarity, though not enforced at runtime). |
| `when` | string | No | Expr expression. When absent or empty, the rule always runs. |
| `action` | string | No | `"write"` or `"delete"`. When set, applies to all tuples in the rule; tuple-level `action` is forbidden. |
| `variables` | mapping | No | Ordered key-value pairs where keys are variable names and values are Expr expressions. |
| `iterator` | object | No | Fan-out configuration (see [Iterator](#iterator)). |
| `tuple_filters` | list | No | Filters declaring which existing FGA tuples to read for patch/delete operations (see [Tuple Filters](#tuple-filters)). Each filter has an `action` (`patch` or `delete`). Requires a live FGA connection. |
| `tuples` | list | Conditional | Tuple templates. Required when no iterator and no `tuple_filters` is present. Optional (evaluated once, without iterator context) when an iterator is present. |

### When

An [Expr expression](#expression-language) evaluated as a boolean gate. If the when guard evaluates to false, the entire rule (including its iterator and tuples) is skipped. Rule-level when guards are evaluated before variables, so only `input` is in scope -- direct member access on `variables` (e.g. `variables.foo` or `variables["foo"]`) is rejected at compile time. Tuple-level when guards (within `tuples` or `iterator.tuples`) are evaluated after variables and have full access to both `input` and `variables`.

Non-boolean results are coerced:

| Value | Coerced to |
|-------|-----------|
| `nil` | `false` |
| `false` | `false` |
| `0` (int, int64, float64) | `false` |
| `""` (empty string) | `false` |
| `[]` (empty array) | `false` |
| `{}` (empty map) | `false` |
| Everything else | `true` |

### Rule-Level Action

When a rule sets `action`, all tuples produced by that rule (both rule-level and iterator) inherit the specified action. Tuple-level `action` is forbidden when a rule-level action is set -- any tuple that explicitly specifies `action` produces a `ValidationError`.

When omitted, each tuple controls its own action individually (defaulting to `"write"` if not specified).

The rule-level `action` applies to desired-state tuples only; it does not affect tuple filter actions (`patch`/`delete`), which have independent semantics. A rule with `action: delete` and `patch` tuple filters is a `ValidationError` because delete tuples are contradictory as desired state for a patch operation.

### Variables

Variables are an ordered YAML mapping. Each variable is an Expr expression evaluated sequentially. A variable can reference `input` and any previously defined variable via `variables.<name>`.

```yaml
variables:
  raw_id: input.data.user_id
  safe_id: replace(variables.raw_id, "|", ":")
```

**Constraints:**
- Forward references are rejected at evaluation time (AST-based detection before variable evaluation).
- Duplicate variable names within the same rule are rejected at validation time.
- String results exceeding 4KB are truncated at a UTF-8 boundary.

### Iterator

An iterator fans out tuple generation over an array. When present, the iterator must contain its own `tuples` list (the per-item tuples). The rule-level `tuples` list becomes optional and, if present, is evaluated once after the iterator loop completes (without the iterator variable in scope).

```yaml
iterator:
  source: input.roles         # Expr expression resolving to an array
  as: role                    # Name bound in tuple fields and when guards
  tuples:                     # Required. Evaluated once per item.
    - user: "user:{{ input.user_id }}"
      relation: "member"
      object: "role:{{ role }}"
tuples:                       # Optional. Evaluated once (no iterator variable).
  - user: "user:{{ input.user_id }}"
    relation: "active"
    object: "org:{{ input.org_id }}"
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source` | string | Yes | Expr expression that must resolve to an array. |
| `as` | string | Yes | Name bound to the current item. Must not be `input` or `variables`. |
| `tuples` | list | Yes | Tuple templates evaluated per iteration item. |

**Behavior:**
- `source` is evaluated after variables, so variables are available: `source: variables.computed_list`.
- `nil` or missing source resolves to an empty array (zero iterations, no error).
- The `as` name is a top-level key in both the interpolation context and tuple-level when guards — reference it as `{{ role }}` in tuple fields and `role != "guest"` in when guards (no prefix).
- Maximum 1000 items per iterator source.

### Tuple Filters

A rule with `tuple_filters` declares that the rule operates on existing FGA tuples using read-diff-write semantics. The `tuple_filters` field specifies which tuples to read from FGA using partial-key filters that map directly to the [OpenFGA Read API](https://openfga.dev/docs/api/service#/Relationships/Read). Each filter has an `action` (`patch` or `delete`) that determines how the consumer processes it: `patch` (default) reads matching tuples from FGA, diffs against the rule's desired-state tuples, and writes the delta; `delete` reads matching tuples and removes them all. See [`tuple-write-spec.md`](tuple-write-spec.md) for the full execution model.

```yaml
- name: Sync org members
  tuple_filters:
    - object: 'org:{{ input.org_id }}'
      relation: member
  iterator:
    source: input.members
    as: member
    tuples:
      - user: 'user:{{ member.id }}'
        relation: member
        object: 'org:{{ input.org_id }}'
```

A tuple filter is a partial-key query that maps to the [FGA Read API](https://openfga.dev/docs/api/service#/Relationships/Read). Any combination of `user`, `relation`, and `object` can be set; unset fields act as wildcards. Supported patterns:

| Filter Fields | Matches | Example |
|---------------|---------|---------|
| `relation` + `object` | All users with this relation to this object | `relation: member, object: org:123` |
| `user` + `relation` | All objects where this user has this relation | `user: user:alice, relation: member` |
| `user` + `object` | All relations between this user and object | `user: user:alice, object: org:123` |
| `user` + `relation` + `object` type prefix | All of a user's relations to a resource type | `user: user:alice, relation: member, object: 'org:'` |
| `object` only | All relations on this object | `object: org:123` |
| `object` type prefix only | All tuples for a resource type | `object: 'org:'` |
| `user` only | All relations involving this user | `user: user:alice` |

#### Tuple Filter Field Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tuple_filters` | list | No | List of filters (see below). Each filter is a partial-key query with an action. |

Each tuple filter supports:

| Field | Type | Description |
|-------|------|-------------|
| `user` | string | Interpolated string for the user URN. Optional; omit to match all users. |
| `relation` | string | Interpolated string for the relation name. Optional; omit to match all relations. |
| `object` | string | Interpolated string for the object URN. Optional; omit to match all objects. Object type prefixes (e.g., `org:`) are valid and match all objects of that type. |
| `action` | string | `"patch"` (default) or `"delete"`. `patch` diffs desired-state tuples against FGA and writes the delta. `delete` removes everything matching the filter from FGA. |

**Constraints:**
- At least one filter required in `tuple_filters`; empty lists are rejected at validation time.
- Each filter must have at least one field set (user, relation, or object).
- A filter with all three fields set to specific, concrete values (e.g., `user: user:alice, relation: member, object: org:123`) is rejected — that describes a single tuple, not a range of tuples; use tuple-level `action: write`/`action: delete` instead. A filter is valid when any field uses an object type prefix (e.g., `object: org:`), even if all three fields are set, since a type prefix matches multiple objects.
- If all filters in a rule have `action: delete`, the rule must not define any tuple templates — `delete` means "remove everything matching", so defining desired-state tuples is contradictory. This is a structural check: if the rule's YAML contains `tuples` (at rule level or in an iterator), it is rejected with a `ValidationError` at parse time regardless of whether those tuples would produce output at runtime.
- If any filter has `action: patch`, the rule must produce at least one tuple. When the rule has no iterator and no rule-level tuple templates, this is a structural guarantee of zero tuples and is rejected at parse time with a `ValidationError`. When tuple count depends on runtime factors (iterator source length, tuple-level when guards), this is an eval-time check — the engine produces the tuple filter operation with zero tuples and the consumer fails before reading from FGA (fail fast).
- Mixing `patch` and `delete` filters in a single rule is allowed. Tuples are the desired state for `patch` filters; `delete` filters operate independently (delete everything matching).
- If any filter has `action: patch`, having tuples with `action: delete` (whether set at rule level or tuple level) is a `ValidationError` — tuples in a `patch` rule define the complete desired state to replicate in FGA, so delete actions inside that state are contradictory.
- Maximum 3 filters per rule.
- Requires a live FGA connection; mapping files with tuple filters cannot be used in file-output mode.

**Interpolation rendering:** Tuple filter fields (`user`, `relation`, `object`) are interpolated strings, rendered the same way as tuple fields (see [Interpolation Syntax](#interpolation-syntax)). Filters are rendered after variables and when guards but **before** the iterator runs (see [Evaluation Order](#evaluation-order)), so `input` and `variables` are available but the iterator variable is not in scope. If a filter expression evaluates to nil (e.g., references an undefined key), rule evaluation fails with an `EvalError`.

**Behavior:**
- Each filter is evaluated independently with templates rendered against the event.
- Multiple filters in the same rule are queried independently and results are combined as a union — any tuple matching any filter is included in the read results. Example: `tuple_filters: [{relation: member, object: 'org:123'}, {relation: viewer, object: 'org:123'}]` reads all tuples where (relation=member AND object=org:123) OR (relation=viewer AND object=org:123).
- `action: patch` filters: the consumer reads matching tuples from FGA, diffs against the rule's desired-state tuples, and writes the delta. The rule must produce at least one tuple (eval-time error if empty).
- `action: delete` filters: the consumer reads matching tuples from FGA and deletes them all. No desired-state tuples are involved.
- Each rule with `tuple_filters` produces a tuple filter operation in the engine result (rendered filters + desired-state tuples). The consumer drives execution — see [`tuple-write-spec.md`](tuple-write-spec.md).

### Tuple Template

Each tuple template defines one OpenFGA relationship tuple to emit:

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `user` | string | Yes | -- | Interpolated string for the user URN. |
| `relation` | string | Yes | -- | Interpolated string for the relation name. |
| `object` | string | Yes | -- | Interpolated string for the object URN. |
| `action` | string | No | `"write"` | `"write"` or `"delete"`. Validated at parse time. |
| `when` | string | No | -- | Expr expression. When false, this specific tuple is skipped. |
| `condition` | string | No | -- | FGA condition name. A reference to a condition defined in the authorization model. Forbidden on `action: delete` tuples. |
| `context` | mapping | No | -- | FGA condition context. Key-value pairs where values are interpolated strings. Requires `condition` to be set. |

Templates that render to an empty string produce an error.

#### Condition and Context

Tuples can carry an optional FGA condition name and associated context. The condition is a reference (by name) to a condition defined in the OpenFGA authorization model. Context provides key-value data that is persisted with the tuple and evaluated at query time by the FGA server.

```yaml
tuples:
  - user: "user:{{ input.user_id }}"
    relation: viewer
    object: "doc:{{ input.doc_id }}"
    condition: in_allowed_ip_range
    context:
      allowed_range: "{{ input.ip_address }}"
      device_type: "laptop"
```

Context values are interpolated strings using the same `{{ expr }}` syntax as tuple fields, with access to `input`, `variables`, and iterator variables.

**Constraints:**
- `context` without `condition` is a validation error.
- `condition` on `action: delete` tuples is a validation error (deletes are unconditional in FGA).
- The condition name is a literal string (not an interpolated expression).
- The condition is not part of the tuple identity key in FGA -- the primary key remains (user, relation, object). Writing a tuple with a different condition than what is stored returns a 409 Conflict, so changing a condition requires delete+write (handled automatically by the reconciler when using tuple filters).

## Expression Language

The mapping language uses [expr-lang/expr](https://github.com/expr-lang/expr) for all expressions (when guards, variables, iterator sources, tuple-level when guards, tuple field interpolations). Expressions are sandboxed -- no file I/O, network, or shell access.

### Context

Expressions have access to:

| Name | Description |
|------|-------------|
| `input` | The raw JSON event (map). |
| `variables` | Map of previously evaluated variables (grows per variable; not available in the first variable). |
| `<as>` | Iterator variable (only within iterator `tuples` and their when guards). |

### String Functions

| Function | Signature | Description |
|----------|-----------|-------------|
| `lower` | `lower(s string) string` | Convert to lowercase. |
| `upper` | `upper(s string) string` | Convert to uppercase. |
| `trim` | `trim(s string) string` | Remove leading/trailing whitespace. |
| `split` | `split(s, sep string) []string` | Split string by separator. |
| `join` | `join(arr []string, sep string) string` | Join array with separator. |
| `replace` | `replace(s, old, new string) string` | Replace all occurrences of a substring. |
| `hasPrefix` | `hasPrefix(s, prefix string) bool` | Check string prefix. |
| `hasSuffix` | `hasSuffix(s, suffix string) bool` | Check string suffix. |

### Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `contains` | Substring containment. | `"hello" contains "ell"` |
| `in` | Membership (array or map). | `"x" in array`, `"key" in map` |
| `??` | Null coalescing. | `value ?? "default"` |

### Custom Functions

#### `json_path(obj, path)`

Traverses nested map/slice structures using a dot-separated path string.

```
json_path(input.data, "user.email")          -> "alice@example.com"
json_path(input.items, "0.name")             -> "first item" (array index)
json_path(input.data, "missing.key")         -> nil
json_path(input.data, "user.email") ?? "n/a" -> safe with null coalescing
```

- Returns `nil` for missing paths (safe for use with `??`).
- Supports map keys and integer array indices (e.g., `"items.0"`).
- Maximum 100 path segments.
- Empty segments (e.g., `"a..b"`) return `nil`.

#### `fga_escape(s)`

Percent-encodes characters that are invalid in OpenFGA tuple fields, making any arbitrary string safe for use in any tuple field position.

```
fga_escape("idp|abc123")        -> "idp|abc123"   (pipe is allowed)
fga_escape("user#admin")          -> "user%23admin"
fga_escape("org:special")         -> "org%3Aspecial"
fga_escape("hello world")         -> "hello%20world"
fga_escape("already%23encoded")   -> "already%2523encoded"
```

- Escapes `#`, `:`, `@`, `*`, space, `%`, and Unicode control characters.
- These are the union of all forbidden characters across all OpenFGA tuple field contexts (user ID, object ID, userset ID, relation), based on the OpenFGA server's validation rules.
- `%` is escaped to prevent double-encoding ambiguity.
- Encoding format: `%XX` (uppercase hex). Strings with no forbidden characters are returned unchanged.
- Useful when event data contains characters that would be rejected by the OpenFGA API (e.g., `#` in identifiers, spaces in display names).

## Interpolation Syntax

Tuple `user`, `relation`, and `object` fields are interpolated strings using `{{ expr }}` syntax. Expressions are compiled once at compilation time (syntax errors surface during `Compile()`) and reused across evaluations.

### Interpolation Context

| Syntax | Description |
|--------|-------------|
| `{{ input.field }}` | Event field. |
| `{{ variables.name }}` | Computed variable. |
| `{{ role }}` | Iterator variable (when `as: role`). |

Literal text outside `{{ }}` is passed through unchanged. Multiple expressions can appear in the same field (e.g., `"user:{{ input.user_id }}"`).

### Restrictions

- All stdlib functions (`lower`, `upper`, `trim`, `split`, `join`, `replace`, `hasPrefix`, `hasSuffix`, `fga_escape`, `json_path`) are available within interpolations — they share the same Expr environment as when guards and variables. Arithmetic and control flow are also syntactically available but not useful for string rendering.
- If an expression evaluates to `nil` (e.g., a missing map key), the field fails with an `EvalError`. Use `?? "default"` to provide a fallback.
- Expressions follow the same size and timeout limits as when guards and variable expressions.

## Evaluation Semantics

### Evaluation Order

For each rule, in order:

1. **When guard** -- Evaluated with `input` only (variables are not in scope). If false, the entire rule is skipped and no further evaluation occurs for this rule.
2. **Variables** -- Evaluated sequentially; each can reference `input` and prior variables. Only evaluated if the when guard passed.
3. **Tuple filters** -- If present, render all filters (interpolated strings evaluated against the event).
4. **Iterator** -- If present, resolve the source array and loop:
   - For each item, bind it to the `as` name and evaluate `iterator.tuples`.
   - After the loop, evaluate rule-level `tuples` (if any) once without the iterator variable.
5. **Tuples** -- If no iterator, evaluate rule-level `tuples` once.

After all rules:

6. **Deduplication** -- Remove duplicate tuples (same user, relation, object, action, condition, and context). First occurrence wins; order is preserved.
7. **Conflict detection** -- If the same (user, relation, object, condition) key has both a `write` and `delete` action, return a `ConflictError`. Condition (and context) are part of this identity key, so a write and a delete with different conditions on the same (user, relation, object) are treated as distinct and do not conflict.
8. **Max tuples check** -- If the deduplicated tuple count exceeds the configured limit, return an `EvalError`.

### Scope Rules

- Rule-level when guards only have access to `input`. Direct member access on `variables` (for example, `variables.foo`) in rule-level when guards is rejected at compile time. Tuple-level when guards (on individual tuples or iterator tuples) retain full access to both `input` and `variables`.
- Variable expressions are compiled during `Compile()` along with the rest of the rule, and the resulting compiled expressions are evaluated at runtime. The set of available variable names grows with each variable.
- Forward references (variable A referencing later-defined variable B) are detected via AST analysis and rejected before evaluation.
- Iterator variables are scoped to `iterator.tuples` only. They are not available in rule-level `tuples` or in other rules.
- `input` and `variables` are reserved names and cannot be used as iterator `as` names.

### Type Coercion

When guards use the coercion rules described in the [When](#when) section. Expression results that are not boolean are coerced rather than producing an error.

## Tuple Filter Semantics

The **engine remains stateless** — it does not read from or write to FGA. When a rule contains `tuple_filters`, the engine evaluates the filter templates and produces a tuple filter operation (see [Engine Result](#engine-result) for how filtered and non-filtered tuples are routed). The consumer is responsible for orchestrating FGA I/O — see [`tuple-write-spec.md`](tuple-write-spec.md) for the execution model.

## Post-Processing

After all rules have been evaluated:

### Deduplication

Tuples are deduplicated by their full key: (user, relation, object, action, condition, and context). The first occurrence is retained; duplicates are dropped. Original insertion order is preserved.

### Write/Delete Conflict Detection

If the same (user, relation, object) triple appears with both `action: write` and `action: delete`, a `ConflictError` is returned. When tracing is disabled, the engine returns on the first conflict (fast path). When tracing is enabled, all conflicts are collected before returning.

## Engine Result

Evaluation produces a result with two categories of output:

| Output | Description |
|--------|-------------|
| **Tuples** | Non-filtered tuples (write/delete actions from rules without `tuple_filters`). |
| **Tuple filter operations** | One per rule that has `tuple_filters`. Each groups the rendered filters and the rule's desired-state tuples. |
| **Trace** | Execution trace (if tracing is enabled). |

Each **tuple filter operation** contains:

| Field | Description |
|-------|-------------|
| **Filters** | Rendered tuple filters for this rule. Each has `user`, `relation`, and `object` fields (any may be empty to act as a wildcard) plus `action` (`patch` or `delete`). Each filter maps to a single FGA Read API call. The `action` determines how the read results are processed: `patch` diffs against desired-state tuples, `delete` removes everything. |
| **Tuples** | Desired-state tuples produced by this rule (empty for delete-only rules). |

Tuples from rules **without** `tuple_filters` go into the result's tuples list. Tuples from rules **with** `tuple_filters` go into the corresponding tuple filter operation as the desired state for that operation's `patch` filters. Each operation groups the filters and desired-state tuples for a single rule, preserving the per-rule association needed for correct diffing. See [Evaluation Order](#evaluation-order) for when tuple filters are rendered.

## Safety Limits

| Limit | Value | Enforcement |
|-------|-------|-------------|
| Max expression length | 64KB | Rejected before compilation. |
| Max variable string result | 4KB | Truncated at UTF-8 boundary after evaluation. |
| Max iterator source items | 1000 | `EvalError` returned. |
| Max total tuples per event | 40 (default, configurable via `WithMaxTuples`) | `EvalError` returned after deduplication. |
| Max rules per mapping file | 100 | `ValidationError` at parse time. |
| Max `json_path` segments | 100 | Error returned from `json_path`. |
| Max tuple filters per rule | 3 | `ValidationError` at parse time. |
| Evaluation timeout | 3s (default, configurable via `WithTimeout`) | Context deadline exceeded. |
| Supported version | `"1"` | `ValidationError` at parse time. |

## Error Types

All error types are distinguishable programmatically:

| Type | When | Notable Fields |
|------|------|---------------|
| EvalError | Expression syntax/runtime errors (including interpolation), forward references, limit violations (max tuples, max iterator items, timeout). | Expression, RuleName, Err |
| ValidationError | YAML structural validation errors (missing fields, invalid values, version mismatch). | Field, Message, Position |
| ConflictError | Write/delete conflict on the same (user, relation, object) key. | User, Relation, Object |

`Category` (see [Diagnostics](#diagnostics)) and `Field` are the stable, programmatically-checked parts of the error contract; message text is illustrative and may be reworded over time -- consumers should branch on category/field, not match on message text. For the full enumeration of every condition that triggers each error type, with example messages, see [`language/README.md`](../language/README.md#validation-errors) (ValidationError) and [`docs/engine.md`](engine.md#error-types) (EvalError, ConflictError).

### Source Positions

ValidationError (and EvalError for interpolation/compile-time errors) include a Position with 1-based line/column range data. A value of 0 means the position is unknown (e.g., runtime errors where the YAML source location is unavailable).

| Field | Description |
|-------|-------------|
| StartLine, StartColumn | Start of the error range (1-based; 0 = unknown). |
| EndLine, EndColumn | End of the error range (1-based; 0 = unknown). |

### Error Collection

The compiler collects all validation and interpolation errors before returning. This means users see every structural issue at once rather than fixing them one at a time.

### Diagnostics

A Diagnostic is a structured, consumer-facing representation of a single error suitable for rendering in any context (CLI, IDE, API response):

| Field | Type | Description |
|-------|------|-------------|
| Severity | string | Always `"error"` currently. |
| Category | string | `"validation"`, `"eval"`, `"conflict"`, or `"unknown"`. |
| Field | string | YAML field path, tuple field name, or rule name (if available). |
| Message | string | Human-readable description. |
| Position | Position | Source location (zero value = unknown, omitted from JSON). |

Diagnostics can be extracted from any error tree and rendered as either a grouped human-readable string (using `line:col-line:col:` format) or serialised as a JSON array for API responses.

## Embedded Tests

Mapping files can include test cases that verify expected tuple output and tuple filters:

```yaml
tests:
  - name: "user.created creates membership"
    input:
      type: "user.created"
      data:
        object:
          user_id: "u_alice"
        context:
          tenant:
            tenant_id: "ten_acme"
    expect_tuples:
      - user: "user:u_alice"
        relation: "member"
        object: "tenant:ten_acme"
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Test case name. |
| `input` | object | Yes | The JSON event to evaluate. |
| `expect_tuples` | list | No | Expected tuple output (order-independent matching). Omit for rules that only test tuple filters. |
| `expect_tuple_filters` | list | No | Expected rendered tuple filters (order-independent matching). Each filter should have `user`, `relation`, and/or `object` fields, plus optionally `action`. |
| `assert_writes_covered_by_filter` | boolean | No | When `true`, verifies that every expected write tuple is covered by at least one rendered filter. A tuple is covered if every field set in the filter matches the tuple's corresponding field — `user` and `relation` match exactly, and `object` matches exactly unless the filter uses a type prefix (e.g., `org:`), in which case the tuple's object must start with that prefix. Useful for catching narrow filter bugs. |

Each expected tuple supports the same `user`, `relation`, `object`, and `action` fields as rule tuples. Action defaults to `"write"` if omitted.

### Tuple Filter Coverage Validation

When a rule has `tuple_filters`, the test can optionally include `assert_writes_covered_by_filter: true` (see [Test Field Reference](#embedded-tests)) to validate that every tuple produced by the rule would be cleaned up by future tuple filter operations. A write tuple is **covered** by a filter if, for every field set in the filter, the write tuple's corresponding field matches — `user` and `relation` match exactly, and `object` matches exactly unless the filter uses a type prefix (e.g., `org:`), in which case the tuple's object must start with that prefix:

```yaml
tests:
  - name: "Sync org members — safety check"
    input:
      org_id: "org_123"
      members:
        - id: "alice"
        - id: "bob"
    expect_tuples:
      - user: "user:alice"
        relation: member
        object: "org:org_123"
      - user: "user:bob"
        relation: member
        object: "org:org_123"
    expect_tuple_filters:
      - relation: member
        object: "org:org_123"
    assert_writes_covered_by_filter: true
```

If a write tuple is not covered by any filter, the test fails with an explicit error identifying the uncovered tuple. This catches bugs where a tuple is produced but would never be cleaned up by future tuple filter operations — it becomes an orphan that accumulates silently in FGA.

Embedded tests are run programmatically on a compiled `Mapping` via `RunTests(ctx)`, or `RunTestsFiltered(ctx, filter, failFast)` for filtering and fail-fast. The test runner:
- Evaluates each test's `input` against the compiled mapping.
- Compares actual tuples to `expect_tuples` using order-independent matching (if provided).
- Compares rendered tuple filters to `expect_tuple_filters` using order-independent matching (if provided).
- Validates write tuple coverage against filters when `assert_writes_covered_by_filter: true`.
- Supports substring-based test name filtering via the `filter` argument.
- Supports stopping after the first failure via the `failFast` argument.

## Examples

### Basic: Route by Event Type

```yaml
version: "1"
rules:
  - name: "Grant membership on user creation"
    when: input.type == "user.created"
    variables:
      user_id: replace(input.data.object.user_id, "|", ":")
      tenant_id: input.data.context.tenant.tenant_id
    tuples:
      - user: "user:{{ variables.user_id }}"
        relation: "member"
        object: "tenant:{{ variables.tenant_id }}"

  - name: "Revoke membership on user deletion"
    when: input.type == "user.deleted"
    action: delete
    variables:
      user_id: replace(input.data.object.user_id, "|", ":")
      tenant_id: input.data.context.tenant.tenant_id
    tuples:
      - user: "user:{{ variables.user_id }}"
        relation: "member"
        object: "tenant:{{ variables.tenant_id }}"
```

### Rule-Level Action: Bulk Revocation

When all tuples in a rule share the same action, set it once at rule level. Tuple-level `action` is forbidden when rule-level action is set.

```yaml
version: "1"
rules:
  - name: "Revoke all org access on member removal"
    when: input.type == "organization.member.deleted"
    action: delete
    variables:
      org_id: input.data.object.organization.id
      user_id: input.data.object.user.user_id
    tuples:
      - user: "user:{{ variables.user_id }}"
        relation: "member"
        object: "org:{{ variables.org_id }}"
      - user: "user:{{ variables.user_id }}"
        relation: "viewer"
        object: "org:{{ variables.org_id }}"
      - user: "user:{{ variables.user_id }}"
        relation: "editor"
        object: "org:{{ variables.org_id }}"
```

### Iterator: Fan-out Over a Collection

```yaml
version: "1"
rules:
  - name: "Register identity providers"
    when: len(input.data.object.identities) > 0
    variables:
      user_id: replace(input.data.object.user_id, "|", ":")
    iterator:
      source: input.data.object.identities
      as: identity
      tuples:
        - user: "user:{{ variables.user_id }}"
          relation: "authenticated_via"
          object: "connection:{{ identity.connection }}"
```

### Nested Field Traversal with json_path

```yaml
version: "1"
rules:
  - name: "Add user to organization"
    when: input.type == "organization.member.added"
    variables:
      org_id: json_path(input.data.object, "organization.id")
      user_id: json_path(input.data.object, "user.user_id")
    tuples:
      - user: "user:{{ variables.user_id }}"
        relation: "member"
        object: "org:{{ variables.org_id }}"
```

### Tuple-Level When Guards

```yaml
version: "1"
rules:
  - name: "Sync user roles"
    when: len(input.roles) > 0
    iterator:
      source: input.roles
      as: role
      tuples:
        - when: 'role != "guest"'
          user: "user:{{ input.user_id }}"
          relation: "member"
          object: "role:{{ role }}"
```

### Tuple Filters: Ensure Desired State in FGA

Replace all memberships for an organization with the current list from the event. The tuple filters declare which existing tuples to compare against; the iterator produces the desired state.

```yaml
version: "1"
rules:
  - name: "Sync org members"
    when: input.type == "org.members.updated"
    variables:
      org_id: input.data.organization.id
    tuple_filters:
      - relation: member
        object: 'org:{{ variables.org_id }}'
    iterator:
      source: input.data.members
      as: member
      tuples:
        - user: 'user:{{ member.user_id }}'
          relation: member
          object: 'org:{{ variables.org_id }}'
```

When this rule runs on an event with 5 members:
1. The engine evaluates `tuple_filters`, producing a rendered filter: `relation: member, object: org:123, action: patch`
2. The engine evaluates the iterator, producing 5 tuples (one per member)
3. The engine returns a tuple filter operation with the filter and 5 desired-state tuples

The consumer then reads, diffs, and writes — see [`tuple-write-spec.md`](tuple-write-spec.md) for the execution model.

Example with multiple tuple filters (patching both `member` and `viewer` relations):

```yaml
version: "1"
rules:
  - name: "Sync org access"
    when: input.type == "org.access.updated"
    variables:
      org_id: input.data.organization.id
    tuple_filters:
      - relation: member
        object: 'org:{{ variables.org_id }}'
      - relation: viewer
        object: 'org:{{ variables.org_id }}'
    iterator:
      source: input.data.members
      as: member
      tuples:
        - user: 'user:{{ member.user_id }}'
          relation: member
          object: 'org:{{ variables.org_id }}'
        - when: 'member.viewer'
          user: 'user:{{ member.user_id }}'
          relation: viewer
          object: 'org:{{ variables.org_id }}'
```

Full revocation (delete all tuples matching a filter) uses `action: delete` to explicitly remove everything matching:

```yaml
version: "1"
rules:
  - name: "Revoke all org access on deletion"
    when: input.type == "org.deleted"
    tuple_filters:
      - action: delete
        object: 'org:{{ input.org_id }}'
```

The `action: delete` filter reads everything matching from FGA and deletes it. No tuples are needed — in fact, producing tuples when all filters are `action: delete` is a validation error.

### Tuple Filters with Tuple-Level When Guards

Combine tuple filters with iterator and tuple-level when guards to filter which tuples are part of the desired state:

```yaml
version: "1"
rules:
  - name: "Sync active org members only"
    tuple_filters:
      - relation: member
        object: 'org:{{ input.org_id }}'
    iterator:
      source: input.members
      as: member
      tuples:
        - when: 'member.active'
          user: 'user:{{ member.user_id }}'
          relation: member
          object: 'org:{{ input.org_id }}'
```

Here, only active members are included in the desired state. Inactive members that exist in FGA will be removed by the consumer's patch operation (see [`tuple-write-spec.md`](tuple-write-spec.md)).
