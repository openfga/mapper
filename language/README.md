# Language Package

> **User-facing language specification:** See [`docs/language-spec.md`](../docs/language-spec.md). This README covers internal architecture and design decisions.

## Overview

The `language` package parses and validates FGA mapping configurations. It transforms mapping YAML into a canonical `MappingConfig`, checks structural constraints via `MappingConfig.Validate()`, and stamps source positions onto the parsed structs so downstream consumers (the [`mapper`](../docs/engine.md) package, IDE tooling) can attach diagnostics without reaching into the YAML AST.

It performs no expression compilation or evaluation — that is the [`mapper`](../docs/engine.md) package's responsibility. `language` is the leaf of the two-package split: it has no dependency on `mapper`.

## Versioning

The mapping format version is a single integer string (`"1"`, `"2"`, etc.) — no minor versions.

`parseMapping()` is a version router: parses AST, extracts version, dispatches to `parseV1()` / `parseV2()` etc. Each version parser (`parse_v1.go`, `parse_v2.go`) owns: YAML unmarshaling, strict field checking, variable extraction, and normalization to canonical `MappingConfig`. `MappingConfig.Validate()` validates canonical types — shared across versions. The `mapper` package's `Compiler` has zero version awareness; it only accepts canonical types.

Adding a new version: create `parse_vN.go`, add a case to the version switch in `parseMapping()`. Never drop support for a published version.

## Canonical Types

- **Variables YAML format:** YAML mapping (not list) with order preserved via custom `UnmarshalYAML`.
- **TupleAction:** `"write"` (default) or `"delete"` — validated at parse time.
- **TupleFilterAction:** `"patch"` (default) or `"delete"` — separate type from `TupleAction` since semantics differ (patch/delete operate on FGA read results, not individual tuples).
- **ParsedTupleFilter:** YAML input for a tuple filter. `User`, `Relation`, `Object` are optional interpolated strings; `Action` defaults to `"patch"`. Validated to have at least one field set; all-concrete (non-interpolated, non-type-prefix) three-field filters are rejected.
- **TupleFilter:** Rendered tuple filter (mapper output). Carries `User`, `Relation`, and `Object` fields (any may be empty to act as a wildcard) plus `Action`. Filter-level `Action` is independent of tuple-level `TupleAction`.
- **Rule-level action:** `Rule.Action` (`"write"` or `"delete"`) propagates to all tuples during `validateRule()`, before `validateTupleTemplate()` runs. When set, tuple-level `action` is forbidden. Rule-level `action: delete` with `patch` tuple filters is a `ValidationError`.
- **Tuple condition and context:** `ParsedTuple.Condition` (string, literal FGA condition name) and `ParsedTuple.Context` (map of interpolated strings). Validation: `context` requires `condition`; `condition` forbidden on `action: delete` tuples.
- **Tuple.Key():** Returns a length-prefixed string key encoding all tuple fields for use as a comparable map key. `Tuple` is non-comparable due to `Context map[string]any`. All dedup, diff, and test comparison logic uses `Key()`.
- **Strict YAML parsing:** `parseMapping()` passes `yaml.DisallowUnknownField()` to the single `NodeToValue` call. `Rule.Variables` is `yaml:"-"` (extracted from AST for ordering); a sink field `RawVariables any` tagged `yaml:"variables,omitempty"` absorbs the YAML key during unmarshal — its value is never read. Unknown field errors are converted to `*ValidationError` with position via `toValidationError()`.

## Validation Errors

`*ValidationError` is the sole error type produced by this package. It is distinguishable via `errors.As()` and always carries a `Position`.

The `Category` (surfaced via the mapper's `Diagnostic`) and `Field` are the stable, programmatically-checked contract; message text may be reworded over time. The [user-facing spec](../docs/language-spec.md#error-types) links back here for this detail.

### ValidationError Scenarios

All structural checks below run during `Validate()`/parsing (`parser.go`), before any event is evaluated, and all include a `Position`. `{i}`/`{j}` denote array indices in the field path.

| Scenario | Field Path | Example Message |
|----------|-----------|------------------|
| Missing `version` | `version` | `is required` |
| `version` wrong type | `version` | `must be a string, got 123` |
| Unsupported `version` | `version` | `unsupported version "2" (supported: "1")` |
| Unknown top-level or nested YAML field | `{field name}` | (from strict YAML unknown-field decoding, via `toValidationError()`) |
| No rules | `rules` | `must contain at least one rule` |
| Too many rules (default >100, override with `WithMaxRules`) | `rules` | `exceeds maximum of 100 rules` |
| Rule missing `name` | `rules[{i}].name` | `is required` |
| Invalid rule-level `action` | `rules[{i}].action` | `invalid action "freeze" (must be "write" or "delete")` |
| Tuple-level `action` set alongside rule-level `action` | `rules[{i}].tuples[{j}].action` | `tuple-level action is not allowed when rule has action "write"; remove the tuple-level action` |
| Iterator present with no `iterator.tuples` | `rules[{i}].iterator.tuples` | `must contain at least one tuple` |
| Rule has neither `tuples`, `iterator`, nor `tuple_filters` | `rules[{i}].tuples` | `must contain at least one tuple` |
| `tuples` empty with a `patch` tuple filter present | `rules[{i}].tuples` | `must contain at least one tuple when tuple_filters includes a patch filter` |
| Tuple missing `user` | `rules[{i}].tuples[{j}].user` | `is required` |
| Tuple missing `relation` | `rules[{i}].tuples[{j}].relation` | `is required` |
| Tuple missing `object` | `rules[{i}].tuples[{j}].object` | `is required` |
| Invalid tuple-level `action` | `rules[{i}].tuples[{j}].action` | `invalid action "pause" (must be "write" or "delete")` |
| `context` set without `condition` | `rules[{i}].tuples[{j}].context` | `context requires a condition name` |
| `condition` set on an `action: delete` tuple | `rules[{i}].tuples[{j}].condition` | `condition is not allowed on delete tuples` |
| Empty variable name | `rules[{i}].variables[{j}].name` | `is required` |
| Empty variable expression | `rules[{i}].variables[{j}].expression` | `is required` |
| Duplicate variable name | `rules[{i}].variables[{j}]` | `duplicate variable name "x" (first defined at index 0)` |
| Iterator missing `source` | `rules[{i}].iterator.source` | `is required` |
| Iterator missing `as` | `rules[{i}].iterator.as` | `is required` |
| Iterator `as` shadows `input`/`variables` | `rules[{i}].iterator.as` | `iterator "as" name "input" shadows the built-in input scope; choose a different name` |
| More than 3 tuple filters | `rules[{i}].tuple_filters` | `exceeds maximum of 3 filters` |
| Tuple filter with no fields set | `rules[{i}].tuple_filters[{j}]` | `must have at least one field set (user, relation, or object)` |
| Tuple filter with all three fields concrete (no type prefix) | `rules[{i}].tuple_filters[{j}]` | `filter with all three fields set to concrete values describes a single tuple, not a range; use tuple-level action instead` |
| Invalid tuple filter `action` | `rules[{i}].tuple_filters[{j}].action` | `invalid action "ignore" (must be "patch" or "delete")` |
| `delete` filters combined with rule-level tuple templates | `rules[{i}].tuple_filters` | (rejected — see `validateTupleFilters()`) |
| Test case missing `name` | `tests[{i}].name` | `is required` |

## Source Positions

`ValidationError` (and, downstream, the mapper's compile-time `EvalError`) includes a `Position` struct with 1-based line/column range data. A value of 0 means the position is unknown.

```go
type Position struct {
    StartLine   int `json:"startLine"`   // 1-based, 0 = unknown
    StartColumn int `json:"startColumn"` // 1-based, 0 = unknown
    EndLine     int `json:"endLine"`     // 1-based, 0 = unknown
    EndColumn   int `json:"endColumn"`   // 1-based, 0 = unknown
}
```

Positions are resolved via a dual-parse approach: YAML is parsed once into structs for data and once into a `yaml.Node` tree for line/column lookup. Positions are stamped onto the parsed structs at parse time (`positions.go`, `yamlpos.go`), so consumers read line/column data from plain struct fields and never touch the node tree.

`MappingConfig` retains the AST (`root`) after parsing because `Validate()` needs it to resolve positions for structural errors, and `Validate()` must stay idempotent (callable repeatedly with positions intact). Once the final `Validate()` has run, call `ClearRoot()` to release the tree for garbage collection — `mapper.Compile()` does this automatically before returning a long-lived `Mapping`.

## Usage Example

```go
cfg, err := language.Parse(yamlBytes)
if err != nil {
    // Parse reports syntax errors only (malformed YAML, unknown version).
}

// Parse does not validate. Run Validate to check the mapping is well-formed;
// it returns (or joins) *language.ValidationError values.
if err := cfg.Validate(); err != nil {
    // Inspect the validation errors.
}
// cfg is now validated and ready to hand to mapper.Compile.
```
