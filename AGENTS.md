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

- Keep explanations clear and concise: state the direct answer first, then
  include only the technical detail needed to justify it.
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

Compose-created containers carry the `io.github.kunalvirwal.acc.project` and
`io.github.kunalvirwal.acc.service` labels. `Down` must discover containers by
these labels: remove current-file services by default and include removed
services only with `--remove-orphans`.

`Up` creates plain Compose networks using their Compose-resolved runtime names
(including the implicit `<project>_default` network), labels them with ACC
project/network ownership labels and `acc.network.coredns=false`, and attaches each newly-created service
to every network it declares using repeated `container run --network` flags.
Advanced network and service-network options must not be silently ignored.
`Up` must reject an existing project-managed network name unless both ownership
labels match the project. `Down` removes only matching ACC-owned project
networks after all project containers have been removed.

Bind mounts map to `container run --mount type=bind,...`. Resolve relative
sources against the Compose project directory before passing their absolute
paths to the runtime; targets must be absolute container paths. Create a
missing source directory only when `bind.create_host_path` is true (the Compose
default); otherwise fail before starting any containers. Emit `readonly` only
for a read-only mount. Compose mount `consistency` values cannot be mapped to
Apple Container, so ignore them and emit a non-fatal warning.
SELinux bind relabeling (`bind.selinux` or short-syntax `z`/`Z`) is also
ignored with a non-fatal warning because it has no Apple Container mapping.
Bind propagation cannot be mapped to Apple Container's VM-backed binds and
must produce a fatal diagnostic before startup.
Unknown short-syntax volume options must produce a fatal diagnostic instead of
being silently ignored by compose-go's normalization.

Named volumes map to `container run --mount type=volume,...`. Create each
selected, non-external Compose volume before starting containers, using its
explicit `name:` when set or `<project>_<volume>` otherwise. Project-managed
volumes carry the `io.github.kunalvirwal.acc.project` and
`io.github.kunalvirwal.acc.volume` labels. Retain them on `down` by default and
remove only these labeled volumes with `down --volumes`. Propagate user-defined
Compose volume labels at creation, but reject either ACC ownership-label key as
reserved with a fatal diagnostic.
Merge service-level `volume.labels` into their named volume's labels at
creation. Reject conflicting values for the same key. For anonymous service
volumes, apply their `volume.labels` directly. `volume.nocopy` and
`volume.subpath` are unsupported and must produce a fatal diagnostic before
startup.
Only the omitted/default or explicit `local` named-volume driver is supported;
other driver values must produce a fatal diagnostic before startup.
For `local`, permit only `driver_opts.size` (at least 1 MiB) and
`driver_opts.journal` (`ordered`, `writeback`, or `journal`, optionally with a
valid size suffix); reject every other key or invalid value with a fatal
diagnostic before startup.

Apple named volumes may be shared only when every service mount is read-only.
Reject a project where multiple services reference the same named volume and
any of those mounts is writable; recommend a bind mount for shared writable
storage.

Anonymous service volumes (for example `- /cache`) are also created explicitly
so they can carry ACC ownership labels. Name them
`acc-<project>-anon-<uuid>` and set the volume label to
the generated volume name before mounting them. Do not rely on Apple's automatic
anonymous-volume creation because it cannot attach ACC labels. With
`down --volumes`, remove only anonymous volumes attached to containers that
this invocation removes; retain detached anonymous volumes.

Tmpfs mounts map to `container run --mount type=tmpfs,...`, including long-form
Compose `tmpfs.size` and `tmpfs.mode` and service-level `tmpfs:` shorthand
size/mode options. Non-fatal diagnostics use
`UpOptions.OnWarning`; unsupported features that must prevent startup use
`UpOptions.OnFatalWarning` and return `ErrUnsupportedFeature`.

The implementation intentionally supports only a subset of Compose. Check the
conversion code in `up.go` and `build.go` before assuming a field is honored.
`volumes_from` is explicitly unsupported and must produce a fatal diagnostic
before startup. Service volume types other than `bind`, `volume`, and `tmpfs`
must do the same.

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
