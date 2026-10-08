# Contributing to ACC

Issues, documentation improvements, and focused pull requests are welcome.

## Report a bug or suggest a feature

Use [GitHub Issues](https://github.com/kunalvirwal/apple-container-compose/issues).
For a bug report, include the ACC release or commit, macOS and `container`
versions, a minimal Compose file, the command you ran, and relevant output.
Remove credentials and other private information from examples and logs.

For feature requests, describe the Compose behavior you need and a concrete use
case. Check [Compose compatibility](docs/compatibility.md) for current limits.

## Build from source

Use the Go version declared in [go.mod](go.mod):

```sh
git clone https://github.com/kunalvirwal/apple-container-compose.git
cd apple-container-compose
make build
./acc --help
```

The help command does not touch the container runtime. Running Compose commands
requires a supported Mac with Apple's `container` installed and its system
service running.

## Verify changes

Format changed Go files with `gofmt`, then run the root module's checks:

```sh
make check
```

The CoreDNS plugin is a separate Go module; check it independently:

```sh
(cd coredns/acc-plugin && go test -race ./... && go vet ./...)
```

If the default Go build cache is not writable, use a temporary cache:

```sh
GOCACHE=/tmp/apple-container-compose-go-cache make check
(cd coredns/acc-plugin && GOCACHE=/tmp/apple-container-compose-go-cache go test -race ./... && GOCACHE=/tmp/apple-container-compose-go-cache go vet ./...)
```

Unit tests use fake command runners and must not require network access or the
Apple runtime. Add focused table-driven tests for behavior changes. Runtime
tests that start, stop, build, or delete containers are manual integration tests;
use the [examples](examples/README.md) deliberately and clean up their resources.

## Submit a pull request

Before taking up any active issues, please consult the active maintainers of the project 
and get the issue assigned to yourself prehand.

Keep changes focused, describe the resulting behavior, and include the checks
you ran. Preserve the separation between the container wrapper, Compose
orchestration, and CLI presentation. New Compose features need an accurate
mapping to Apple's runtime and corresponding compatibility documentation.

Keep dependencies minimal. Run `go mod tidy` only when imports or module
requirements change, and review both module files afterward. Keep build
artifacts, runtime state, credentials, and machine-specific paths out of commits.

Contributions are made under the project's [Apache License 2.0](LICENSE).
