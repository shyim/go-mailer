# Contributing to gomailer

Thanks for your interest in improving gomailer! Issues and pull requests are
welcome. This guide covers the project layout and the local workflow.

## Requirements

- **Go 1.26+**
- [`golangci-lint`](https://golangci-lint.run) v2 (the repo is lint-clean and CI
  enforces it)
- Optional, for the docs site: [`uvx`](https://docs.astral.sh/uv/) or
  `pip install zensical`

## Project layout

gomailer is a **multi-module workspace** — each concrete network transport and
the OpenTelemetry adapter is its own Go module, so consumers pull in only what
they use. There are four `go.mod` files:

| Module | Path |
|--------|------|
| Core (abstractions, MIME, null, composites, DSN, middleware) | `.` |
| SMTP / ESMTP transport | `transport/smtp` |
| `sendmail` transport | `transport/sendmail` |
| OpenTelemetry adapter | `middleware/otelmw` |

During development the submodules resolve the core via the published version (a
`require github.com/shyim/go-mailer@vX.Y.Z` line). If you change the core API and
need a submodule to build against your local changes, add a temporary `replace`
directive — **but do not commit it**:

```sh
cd transport/smtp
go mod edit -replace github.com/shyim/go-mailer=../..
# ... work, test ...
go mod edit -dropreplace github.com/shyim/go-mailer   # before committing
```

## Building and testing

Each module is tested independently. To run everything:

```sh
go test ./...                            # core
(cd transport/smtp     && go test ./...)
(cd transport/sendmail && go test ./...)
(cd middleware/otelmw  && go test ./...)
```

Run the race detector on the concurrency-sensitive code before sending a change
that touches transports, the throttle, or the message builder:

```sh
go test -race ./...
(cd transport/smtp && go test -race ./...)
```

## Linting and formatting

The codebase is `gofmt`-clean and passes `golangci-lint` with zero issues; please
keep it that way. Run, from each module directory:

```sh
gofmt -l .          # should print nothing
go vet ./...
golangci-lint run ./...
```

`golangci-lint run --fix ./...` will apply the formatter (including import
grouping) for you.

## Pull requests

- Keep changes focused; one logical change per PR.
- Add or update tests for any behavior change. The SMTP transport is tested
  against an in-process fake server, and mailer code is tested with the
  `mailertest` recording transport — follow those patterns.
- Update the docs under [`docs/`](docs/) when you change public behavior, and the
  doc-comments on any exported symbol you touch.
- Make sure `go build`, `go vet`, `go test`, and `golangci-lint` pass for every
  affected module. CI runs all four modules across a matrix.

## Documentation

The site is built with [Zensical](https://zensical.org) from the Markdown under
`docs/`. Preview it locally:

```sh
uvx zensical serve     # live preview
uvx zensical build     # one-off build into ./site
```

## Reporting bugs and requesting features

Open an issue with a clear description and, for bugs, a minimal reproduction (the
message you built, the transport/DSN, and the observed vs. expected result).
Please don't include real credentials in DSNs or logs.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
