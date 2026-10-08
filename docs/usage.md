# CLI usage

[Project overview](../README.md) · [Compose compatibility](compatibility.md) ·
[Examples](../examples/README.md)

## Commands

| Command | Action |
| --- | --- |
| `acc up` | Create or reconcile services and follow their logs. |
| `acc up -d` | Start services in the background. |
| `acc up --build` | Build all selected buildable services before startup. |
| `acc build` | Build images for services with a `build:` definition. |
| `acc logs -f` | Follow service logs. |
| `acc down` | Stop and remove project services and unused ACC-owned project networks. |

Commands accept service names. Startup includes the selected services'
dependencies:

```sh
acc up -d web
acc logs -f web
acc down web
```

Run `acc --help` or `acc <command> --help` for available flags.

## Project configuration

ACC discovers `compose.yaml`, `compose.yml`, `docker-compose.yaml`, or
`docker-compose.yml` in the working directory, in that order. Use `--file` to
choose another file:

```sh
acc --file ./examples/basic.compose.yaml up -d
```

Only one Compose file is supported per invocation. The file option has no `-f`
shorthand; `acc logs -f` means follow logs.

| Flag | Purpose |
| --- | --- |
| `--project-name`, `-p` | Override the Compose project name. |
| `--project-directory` | Set the project working directory. |
| `--env-file` | Supply an environment file for interpolation; may be repeated. |
| `--no-color` | Disable colored output. |

By default, the project name comes from the top-level `name:` field, or the
Compose file's parent directory. Relative build contexts and bind sources are
resolved against the project directory.

## Image builds

For services with `build:`, `acc up` inspects the local image first. It uses that
image when present and builds the service locally when it is absent. This check
does not pull the image from a registry.

Use `acc up --build` to force builds for all selected buildable services before
startup, or `acc build` to build without starting services. For services without
`build:`, the runtime can pull a missing image.

## Storage and cleanup

`acc down` retains volumes by default. To remove eligible ACC-owned volumes too:

```sh
acc down --volumes
```

External volumes, volumes referenced by remaining containers (including stopped
containers), and unrelated detached volumes are retained. Detached anonymous
volumes are also retained; anonymous-volume deletion is limited to those attached
to containers removed by this invocation.

With service names supplied, volume cleanup applies only to the removed
containers' attached, unused ACC-owned volumes:

```sh
acc down web --volumes
```

Use `--remove-orphans` with `up` or `down` to remove ACC-owned containers for
services that were removed from the current Compose file. External networks are
never deleted by ACC. Project CoreDNS and its networks are retained while project
containers still need them.

When recreating service containers, `acc up` reuses their anonymous volumes by
default. Use `acc up --renew-anon-volumes` to create fresh anonymous volumes.

## Service discovery

The CLI manages a CoreDNS container per project for service-name and
network-alias resolution on shared networks. It uses the
`docker.io/kunalvirwal/acc-coredns:v2` image, which the runtime may pull on first
use. Compose `dns` entries configure upstream nameservers for external queries.

To disable ACC-managed service discovery and pass Compose `dns` settings directly
to the runtime:

```sh
acc up --no-coredns
```

Under this flag only the containers mentioned in the compose file will be started 
and not the CoreDNS container. If up is run for reconcilation and the compose earlier 
used coredns then the CoreDNS will be stopped and all the containers will be restarted 
without the dns entry of the coreDNS. If the compose had orphan conatiners then up 
reconcilation with `--no-coredns` will still leave coreDNS running because the orphans are 
still using it as the default DNS nameserver. To delete the coreDNS under these circumstances 
all the orphan containers must be deleted using the `--remove-orphans` flag.

The lifecycle of a coreDNS conatainer in a compose is coupled to the existances of all the containers of a 
running compose and even if one container stays across restarts, the original coreDNS will stay. 
Any newer compose runs will get a different coreDNS created for it if this one can not be reused due to
a change of network configurations of the compose.

The registry is stored at `~/.acc/coredns/state.json` and managed by ACC. See the
[CoreDNS documentation](../coredns/README.md) for resolution behavior and
operational details.
