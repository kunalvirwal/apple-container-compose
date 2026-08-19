# AGENTS.md

## Project overview

This repository is a Go library and `acc` CLI for driving Apple's `container`
CLI. It has two layers:

- `pkg/container`: a thin, typed wrapper around `container` subprocesses.
- `pkg/compose`: Docker Compose parsing and orchestration built on top of that
  wrapper, using `compose-go`.
- `cmd/acc`: the `acc` Cobra CLI entry point. Command behavior belongs in
  `internal/cli`; the entry point only executes the root command.
- `examples/`: sample Compose input used for manual testing.

The module path is `github.com/kunalvirwal/apple-container-compose`, and the
required Go version is declared in `go.mod`.

The CLI owns terminal presentation: build output is purple and service log
lines receive stable, distinct ANSI colors based on their service prefix.
Preserve `--no-color` as the opt-out, and do not put terminal styling in the
public `pkg` SDK.

## Working in this repository

- Make focused changes and preserve the existing separation between raw
  `container` command construction and Compose-level orchestration.
- Pass `context.Context` through subprocess and orchestration paths. Preserve
  cancellation errors when wrapping command failures.
- Construct CLI invocations as argument slices; do not invoke a shell or build
  command strings by concatenation.
- Keep public option structs and exported functions documented.
- Use package sentinel errors where callers need to branch with `errors.Is`.
  Wrap errors with `%w` when adding context.
- Validate options before invoking the external CLI.
- Keep generated output deterministic. Sort values originating from maps before
  turning them into CLI arguments or serialized slices.
- Protect shared writers used by concurrent streaming operations.
- Do not add support for a Compose field unless it can be mapped accurately to
  Apple's `container` CLI. Return `ErrUnsupportedFeature` with useful context
  for unsupported input.

## Package-specific guidance

### `pkg/container`

`Client.run` and `Client.runStreaming` are the subprocess boundary. Higher-level
clients receive these functions through constructors so command generation can
be unit tested without launching real containers. Preserve that injection
pattern.

Translate stable, recognizable CLI failures to the package errors in
`errors.go`; otherwise retain command output in the returned error. Streaming
methods should both forward output to the supplied writer and retain output for
the return value/error path.

Use `ImageClient.Exists` for local-image checks. It calls `container image
inspect`, which must not trigger a registry pull. Do not use `container run` to
determine whether an image is present locally.

### `pkg/compose`

Load Compose files through the shared `loadProject` path so interpolation,
working-directory handling, and validation remain consistent. Service startup
uses dependency order; teardown uses its reverse. Container names currently
follow `<project>_<service>_1` and must stay consistent across up, logs, and
down.

The implementation intentionally supports only a subset of Compose. Check the
conversion code in `up.go` and `build.go` before assuming a field is honored.

`Up` has a deliberate image-resolution policy for services with `build:`:

- With `--build`, build all selected buildable services before any start.
- Without `--build`, inspect the local image first. Use it if it exists; build
  the service locally if it does not.
- Services without `build:` are passed directly to `container run`, allowing
  the runtime to pull a missing image.

Preserve this ordering so an image with `build:` never causes a registry pull
before the local build fallback is considered.

## Formatting and verification

Format changed Go files and run the full static checks:

```sh
gofmt -w path/to/changed.go
go test ./...
go vet ./...
```

If the environment cannot write to the default Go build cache, use:

```sh
GOCACHE=/tmp/apple-container-compose-go-cache go test ./...
GOCACHE=/tmp/apple-container-compose-go-cache go vet ./...
```

Add focused table-driven unit tests next to the package being changed. Prefer
fake `run` and `runStreaming` functions that capture argument slices and return
controlled output. Unit tests must not require the Apple container runtime,
network access, or running images.

## Runtime and manual testing

Real integration testing requires macOS with Apple's `container` executable
installed and its system service running. Treat commands that start, stop,
build, or delete containers as integration operations. Do not run them merely
to verify a unit-level change.

Use `go run ./cmd/acc --help` to inspect the CLI without touching the runtime.
Commands that execute Compose operations require a real Apple container system.

## Dependencies and commits

- Keep dependencies minimal and run `go mod tidy` only when imports or module
  requirements change. Review both `go.mod` and `go.sum` afterward.
- Do not edit `go.sum` manually.
- Follow the repository's concise, imperative commit style, such as
  `Implement Log streaming` or `Fix bugs in image builds ...`.
- Do not commit local build artifacts, runtime state, credentials, or machine-
  specific absolute paths.
