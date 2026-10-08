# Compose compatibility

[Project overview](../README.md) · [CLI usage](usage.md) ·
[Examples](../examples/README.md)

ACC supports a subset of Compose. Parsing uses
[compose-go](https://github.com/compose-spec/compose-go), but successful parsing
does not mean every field's behavior is implemented. Unsupported-feature
validation is being expanded; some fields can still be ignored.

This guide describes ACC's current mappings and validation, rather than every
capability of Apple's runtime.

If any feature is mentioned in the example composes provided in this repo, then those fields are sure to be supported.

## Supported mappings

| Feature | Behavior |
| --- | --- |
| Images and local builds | Use `image:` or build a Dockerfile from a local context; build arguments, no-cache, pull, and a single platform are mapped. |
| Commands and environment | Pass normalized service `command` as positional arguments after the image and resolved service environment as repeated `--env` flags. |
| Published ports | Explicit host-to-container port mappings, including TCP and UDP. |
| CPU and memory | Map service limits or `deploy.resources.limits`; fractional CPU counts round up with a warning. |
| Dependencies | Start services in dependency order and tear them down in reverse order. |
| Networks | Plain project networks, the implicit default network, explicit runtime names, and pre-existing external networks. |
| Network attachments | Attach services to every declared network; the CLI supplies network-scoped service discovery and aliases through CoreDNS. |
| Bind mounts | Resolve relative sources against the project directory; honor read-only and `bind.create_host_path` settings. |
| Named volumes | Create project-managed volumes with ownership labels; support explicit names, external volumes, and user labels. |
| Anonymous volumes | Create explicitly with ACC ownership labels and reuse them during container recreation by default. |
| Tmpfs | Support long-form mounts and service-level shorthand, including `size` and `mode`. |

## Current limits

- **One Compose file per invocation.** Multiple-file merging is not implemented.
- **One container per service.** Names follow `<project>_<service>_1`.
  `deploy.replicas` and service `scale` do not create additional instances, and
  there is no CLI `--scale` flag.
- **Startup order does not guarantee readiness.** ACC does not run Compose
  `healthcheck` probes or wait for `depends_on.condition: service_healthy`.
  Native workload health-check support in Apple's runtime is tracked in
  [upstream issue #1918](https://github.com/apple/container/issues/1918).
- **Command handling has limits.** ACC drops empty or whitespace-only command
  arguments and does not automatically wrap commands in a shell. Use an explicit
  shell command such as `["sh", "-c", "echo hello"]` when shell evaluation is needed.
- **Shared named volumes must be read-only.** If multiple services reference the
  same named volume and any mount is writable, ACC rejects the project. Use a
  bind mount for shared writable storage.
- **Platform selection is limited to one build platform.** The build mapping uses
  the first `build.platforms` entry, or the service platform when it is omitted.

## Rejected options

These configured options are rejected before service startup:

| Area | Rejected configuration |
| --- | --- |
| Networks | Drivers, driver options, IPAM, and other advanced top-level network settings. |
| Service networks | Non-empty `network_mode`, static IPv4/IPv6 addresses, and attachment options other than aliases. |
| Mounts | `volumes_from`, mount types other than bind/volume/tmpfs, and unknown short-syntax volume options. |
| Bind mounts | Non-empty `bind.propagation`, including propagation supplied through short syntax. |
| Volume mounts | `volume.nocopy: true` and non-empty `volume.subpath`. |
| Named-volume drivers | Any driver other than omitted/default or `local`. |
| Named-volume options | Any `driver_opts` key other than `size` or `journal`, and invalid values for those keys. |
| Volume labels | ACC's project/volume ownership keys, and conflicting label values for the same named volume. |

Validation failures for these unsupported features return
`compose.ErrUnsupportedFeature`; the CLI displays the associated diagnostic.

### Local volume driver options

| Option | Accepted values |
| --- | --- |
| `driver_opts.size` | At least 1 MiB, with an optional size suffix. |
| `driver_opts.journal` | `ordered`, `writeback`, or `journal`, optionally followed by `:<size>`, for example `ordered:64M`. An explicit journal size must also be at least 1 MiB. |

These are volume creation options. Tmpfs `size` and `mode` are separate mount
options.

## Ignored options with warnings

Mount `consistency` and bind SELinux relabeling (`bind.selinux` or short-syntax
`z`/`Z`) have no ACC runtime mapping. They are ignored with non-fatal warnings.

For SDK users, `UpOptions.OnWarning` receives non-fatal warnings and
`UpOptions.OnFatalWarning` receives unsupported-feature diagnostics.

## Service discovery

Automatic CoreDNS management belongs to the CLI. The default configuration
resolves service names and aliases between services on shared networks and uses
Compose `dns` entries for external-name forwarding. `dns_search` and `dns_opt`
are not implemented by the ACC CoreDNS integration. See the
[CoreDNS guide](../coredns/README.md) for its remaining limitations.
