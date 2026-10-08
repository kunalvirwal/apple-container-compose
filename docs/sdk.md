# Go packages

[Project overview](../README.md) · [Compose compatibility](compatibility.md)

ACC exposes two public packages for applications that need to drive Apple's
installed `container` CLI:

| Package | Purpose |
| --- | --- |
| [`pkg/container`](https://pkg.go.dev/github.com/kunalvirwal/apple-container-compose/pkg/container) | Typed wrappers for container, image, network, volume, and system commands. |
| [`pkg/compose`](https://pkg.go.dev/github.com/kunalvirwal/apple-container-compose/pkg/compose) | Compose loading, builds, startup, teardown, and log streaming. |

## Installation

Use the Go version required by [go.mod](../go.mod):

```sh
go get github.com/kunalvirwal/apple-container-compose
```

Runtime operations require Apple's `container` executable on `PATH` and its
system service running. The packages invoke it using argument slices and do not
use a Docker daemon.

## Compose integration

Construct a client with `compose.NewComposeClient`. The client exposes `Up`,
`Down`, `BuildImages`, and `Logs`; each accepts a `context.Context`, a Compose
file path, parsing options, and operation-specific options.

Use `ParseOptions` for the project name, working directory, and interpolation
environment. Supply output writers and diagnostic callbacks through the
operation options. Use `errors.Is` with package sentinel errors when callers
need to distinguish failures such as `compose.ErrUnsupportedFeature`.

The Compose package supports dependency-ordered orchestration and the same
[Compose subset](compatibility.md) as the CLI. `WithContainerClient` supplies an
existing runtime client; `WithServiceRegistry` provides an optional integration
for service runtime records.

## CLI integrations

Terminal styling and automatic CoreDNS lifecycle management belong to the ACC
CLI. The public packages are independent of those integrations. A default
Compose client performs no ACC registry or filesystem-state management; an
application can provide its own registry integration when needed.

See the package reference links above for exported types and methods.
