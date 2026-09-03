# OpenFGA Mapper

[![Go Reference](https://pkg.go.dev/badge/github.com/openfga/mapper.svg)](https://pkg.go.dev/github.com/openfga/mapper)
[![Release](https://img.shields.io/github/v/release/openfga/mapper?sort=semver&color=green)](https://github.com/openfga/mapper/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](./LICENSE)
[![Join our community](https://img.shields.io/badge/slack-cncf_%23openfga-40abb8.svg?logo=slack)](https://openfga.dev/community)
[![X](https://img.shields.io/twitter/follow/openfga?color=%23179CF0&logo=x&style=flat-square "@openfga on X")](https://x.com/openfga)

A Go module for mapping JSON events into [OpenFGA](https://openfga.dev) relationship tuples using a declarative YAML mapping language.

## Table of Contents

- [About](#about)
- [Resources](#resources)
- [Installation](#installation)
- [Getting Started](#getting-started)
- [Documentation](#documentation)
- [Contributing](#contributing)
- [License](#license)

## About

[OpenFGA](https://openfga.dev) is an open source Fine-Grained Authorization solution inspired by [Google's Zanzibar paper](https://research.google/pubs/pub48190/). It was created by the FGA team at [Auth0](https://auth0.com) based on [Auth0 Fine-Grained Authorization (FGA)](https://fga.dev), available under [a permissive license (Apache-2)](https://github.com/openfga/rfcs/blob/main/LICENSE) and welcomes community contributions.

This module turns JSON events into OpenFGA relationship tuples. A mapping is authored in a declarative YAML language, compiled once, and then evaluated against events. The engine is stateless — it produces tuples (and tuple filter operations for read-diff-write flows); it makes no OpenFGA API calls, leaving I/O to the consumer.

It is made up of two packages:

- **`mapper`** (module root) — compiles validated mapping configurations into an executable `Mapping` and evaluates events against it.
- **`language`** — parses and validates mapping YAML into a canonical `MappingConfig`. `mapper` depends on `language`, never the reverse.

## Resources

- [OpenFGA Documentation](https://openfga.dev/docs)
- [OpenFGA API Documentation](https://openfga.dev/api/service)
- [OpenFGA Community](https://openfga.dev/community)
- [Zanzibar Academy](https://zanzibar.academy)
- [Google's Zanzibar Paper (2019)](https://research.google/pubs/pub48190/)

## Installation

```bash
go get github.com/openfga/mapper
```

## Getting Started

Compile a mapping and evaluate an event against it:

```go
package main

import (
	"context"
	"fmt"

	"github.com/openfga/mapper"
)

func main() {
	yaml := []byte(`
version: "1"
rules:
  - name: "grant membership on user creation"
    when: input.type == "user.created"
    tuples:
      - user: "user:{{ input.data.email }}"
        relation: "member"
        object: "org:{{ input.data.org_id }}"
`)

	m, err := mapper.Compile(yaml)
	if err != nil {
		panic(err)
	}

	result, err := m.Evaluate(context.Background(), map[string]any{
		"type": "user.created",
		"data": map[string]any{
			"email":  "alice@example.com",
			"org_id": "acme",
		},
	})
	if err != nil {
		panic(err)
	}

	for _, t := range result.Tuples {
		fmt.Printf("%s %s %s\n", t.User, t.Relation, t.Object)
	}
}
```

`Compile` is a one-shot convenience. To compile many sources with the same configuration, build a `Compiler` once with `mapper.NewCompiler(opts...)` and reuse it.

## Documentation

- [Language specification](./docs/language-spec.md) — the canonical, user-facing mapping language spec.
- [Tuple write specification](./docs/tuple-write-spec.md) — how a consumer turns the engine result into OpenFGA API calls.
- [Engine architecture](./docs/engine.md) — internal design of the `mapper` package.
- [Language package](./language/README.md) — internal design of the `language` package.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md).

## License

This project is licensed under the Apache-2.0 license. See the [LICENSE](./LICENSE) file for more info.
