# Apple Container Compose

**Run Compose projects on macOS with Apple's container runtime.**

[![Release](https://img.shields.io/github/v/release/kunalvirwal/apple-container-compose)](https://github.com/kunalvirwal/apple-container-compose/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/kunalvirwal/apple-container-compose.svg)](https://pkg.go.dev/github.com/kunalvirwal/apple-container-compose)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

Apple Container Compose (`acc`) runs your Compose services using
[Apple's Container](https://github.com/apple/container). Define your application
in YAML, build its images, and manage its services from the terminal. No Docker
daemon is required.

ACC provides a command-line tool and Go packages for container management and
Compose orchestration.

## Features

- **Familiar commands:** start, build, inspect logs, and tear down Compose projects.
- **Local builds:** build missing service images from their Dockerfiles.
- **Service discovery:** resolve service names and aliases through managed CoreDNS.
- **Storage and networking:** project networks, bind mounts, volumes, and tmpfs.
- **Readable output:** colored service logs and build output, with a `--no-color` option.

## Install

Requires Apple silicon and macOS 26 (Tahoe) or later. Install from the
[Homebrew tap](https://github.com/kunalvirwal/homebrew-tap), then start Apple's
container service:

```sh
brew install kunalvirwal/tap/acc
container system start
```

See [Apple's runtime setup guide](https://github.com/apple/container#get-started)
for prerequisites. Binary archives and checksums are available on the
[releases page](https://github.com/kunalvirwal/apple-container-compose/releases).

## Quick start

Save this as `compose.yaml`:

```yaml
name: test

services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
```

Start the project in the background:

```sh
acc up -d
```

Open [localhost:8080](http://localhost:8080), then follow the service logs:

```sh
acc logs -f web
```

Press `Ctrl+C` to leave the log stream. Remove the project when you're done:

```sh
acc down
```

## Project status

ACC is under active development and supports a subset of Compose, currently
with one container per service. See [Compose compatibility](docs/compatibility.md)
before adapting an existing project.

## Documentation
- [Examples](/examples/) - for pricise infromation on what compose features are supported see relevant testing examples.
- [CLI usage](docs/usage.md) - commands, configuration, builds, and cleanup.
- [Compose compatibility](docs/compatibility.md) - supported features and limits.
- [Go packages](docs/sdk.md) - use the runtime and Compose APIs in your applications.
- [Service discovery](coredns/README.md) - the ACC CoreDNS plugin and its behavior.

## Contributing

Issues and pull requests are welcome. Read the [contributing guide](CONTRIBUTING.md)
for setup and testing, or [open an issue](https://github.com/kunalvirwal/apple-container-compose/issues)
to report a bug or suggest a feature.

## License

[Apache License 2.0](LICENSE).
