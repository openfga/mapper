# Contributing to OpenFGA Mapper

A big welcome and thank you for considering contributing to OpenFGA. It's people like you that make it a reality for users in our community.

Reading and following these guidelines will help us make the contribution process easy and effective for everyone involved. It also communicates that you agree to respect the time of the developers managing and developing these open source projects. In return, we will reciprocate that respect by addressing your issue, assessing changes, and helping you finalize your pull requests.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Contributor License Agreement](#contributor-license-agreement)
- [Getting Started](#getting-started)
  - [Making Changes](#making-changes)
  - [Development](#development)
  - [Submitting Pull Requests](#submitting-pull-requests)
- [Getting in Touch](#getting-in-touch)

## Code of Conduct

By participating and contributing to this project, you are expected to uphold our [Code of Conduct](https://github.com/openfga/.github/blob/main/CODE_OF_CONDUCT.md).

## Contributor License Agreement

Before we can accept your contribution, you will need to sign our Contributor License Agreement (CLA). When you open a pull request for the first time, a bot will guide you through the process.

## Getting Started

### Making Changes

When contributing to this repository, the first step is to open [an issue](https://github.com/openfga/mapper/issues) to discuss the change you wish to make before making it. Before submitting a new issue, please search open and closed issues, and the [OpenFGA discussions](https://github.com/orgs/openfga/discussions), to make sure it is not a duplicate.

### Development

This is a standard Go module. It requires the Go version declared in [`go.mod`](./go.mod).

Common tasks are available through the [`Makefile`](./Makefile):

```bash
make test    # run tests with the race detector and coverage
make lint    # run golangci-lint
make fmt     # format the code
make vet     # run go vet
make audit   # run govulncheck
make check   # run all of the above
```

The repository is organised as two packages:

- `mapper` (module root) — compiles validated mapping configurations into an executable `Mapping` and evaluates events against it. See [`docs/engine.md`](./docs/engine.md).
- `language` — parses and validates mapping YAML into a canonical `MappingConfig`. See [`language/README.md`](./language/README.md).

The user-facing language specification lives in [`docs/language-spec.md`](./docs/language-spec.md).

### Submitting Pull Requests

Please make sure to follow the existing code style and include tests for your changes. Pull request titles must follow the [Conventional Commits](https://www.conventionalcommits.org/) format, as it is validated in CI.

## Getting in Touch

### Have a question or problem?

Please do not open issues for general support or usage questions. Instead, join us in the [OpenFGA discussions](https://github.com/orgs/openfga/discussions) or the [OpenFGA community](https://openfga.dev/community).

### Vulnerability Reporting

Please do not report security vulnerabilities on the public GitHub issue tracker. The [Responsible Disclosure Program](https://github.com/openfga/.github/blob/main/SECURITY.md) details the procedure for disclosing security issues.
