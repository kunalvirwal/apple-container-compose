# ACC CoreDNS plugin

The ACC CoreDNS plugin provides Compose-style, network-scoped service discovery
for containers managed by ACC.

Apple Container runs each container in its own lightweight virtual machine. It
does not provide Docker's embedded, per-network DNS resolver. This plugin is
compiled into an ACC CoreDNS image to provide the service-name and alias lookup
behavior that Compose applications expect.

The plugin is deliberately small. CoreDNS continues to provide DNS transport,
packet handling, logging, errors, and forwarding. The ACC plugin only decides
whether a requesting container is allowed to resolve an ACC-managed service
name and which network-specific address to return.

Currently `acc` utilises a coredns image with acc plugin built-in, `docker.io/kunalvirwal/acc-coredns:v1`.

## How ACC uses the plugin

ACC's intended runtime model is one CoreDNS container for each Compose project:

```text
                         global ACC state.json
                                  |
                                  | read-only mount
                                  v
Compose services --> project CoreDNS + ACC plugin --> upstream DNS
      |                   |
      |                   +-- attached to every project network
      +-- configured to use the project CoreDNS instance
```

The CoreDNS instance is scoped to one Compose project, but the registry is
global. That distinction matters for shared external networks: services from
different projects can discover each other only when their state entries share
a network.

The plugin never calls ACC, Apple Container, or a container runtime API. ACC is
the sole writer of `state.json`; the plugin reads validated snapshots from its
mounted configuration directory.


## Configuration

The plugin accepts one absolute state-file path and no options:

```text
acc /config/state.json
```

The ACC image includes this Corefile by default:

```text
.:53 {
    acc /config/state.json
    forward . /etc/resolv.conf
    log
    errors
}
```

The host must mount the parent directory at `/config`, rather than mounting
`state.json` as a single file. ACC publishes changes by atomically replacing
the file, and a single-file mount can retain the previous inode.

## Defaults

| Setting | Default | Notes |
| --- | --- | --- |
| State file | `/config/state.json` | Set by the bundled Corefile. |
| Reload interval | 5 second | Reload detection compares modification time. |
| DNS TTL | 120 seconds | Used for returned A and AAAA records. |
| DNS listener | `:53` over UDP and TCP | Set by the bundled Corefile. |
| Supported answer types | `A`, `AAAA` | Other query types receive NODATA for a visible name. |
| External names | Forwarded | Names absent from ACC state continue to `forward`. |
| Shared DNS cache | Disabled | See [Caching](#caching). |

## State format

The current schema version is `1`.

```json
{
  "version": 1,
  "containers": [
    {
      "id": "demo_api_1",
      "service": "api",
      "networks": {
        "demo_db": {
          "addresses": ["10.10.0.3"]
        },
        "shared_backend": {
          "addresses": ["10.20.0.3", "fd00:20::3"],
          "aliases": ["backend-api"]
        }
      }
    }
  ]
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `version` | Yes | Must be the integer `1`. |
| `containers` | Yes | An array; use `[]` for an empty registry. |
| `containers[].id` | Yes | Unique, stable container identity. Replica containers have distinct IDs. |
| `containers[].service` | Yes | Compose service name registered on every attached network. |
| `containers[].networks` | Yes | Map keyed by runtime network identity, not a logical Compose network key. |
| `addresses` | Yes | One or more unicast IPv4 and/or IPv6 addresses on that network. |
| `aliases` | No | Additional names scoped only to that network attachment. |

Names are case-insensitive and a trailing dot is normalized. Compose-compatible
ASCII names may contain letters, digits, hyphens, underscores, and dots.

The registry rejects incomplete or ambiguous state: unsupported versions,
unknown fields, missing IDs/networks/addresses, invalid names or addresses,
duplicate container IDs, and an address shared by different containers. Private
IPv4 and IPv6 ULA addresses are accepted; unspecified, loopback, multicast,
link-local, and scoped addresses are not.

## Resolution behavior

The source IP of the DNS request identifies the requester. The plugin then:

1. Finds every network attached to that requester in `state.json`.
2. Searches those networks for the queried service name or network-scoped alias.
3. Returns all replica addresses from the first matching shared network.

This makes aliases network-scoped and returns the target address from a network
both containers share.

```text
db network:       db, api
backend network:  api, frontend

db       -> api        returns api's db-network address
db       -> frontend   NXDOMAIN
frontend -> api        returns api's backend-network address
frontend -> db         NXDOMAIN
```

When a requester can see the same name on more than one network, ACC sorts the
runtime network identities lexically and selects the first matching network.
This is deterministic, but is not a claim of Docker endpoint-order
compatibility.

| Query condition | Result |
| --- | --- |
| Managed name on a shared network | `NOERROR` with the matching A/AAAA records. |
| Managed name, but no shared network | `NXDOMAIN`. |
| Visible name with no records for the requested family | `NOERROR` / NODATA. |
| Name absent from ACC state | Passed to the next CoreDNS plugin, normally `forward`. |
| Managed name from an unknown source IP | Currently forwarded; see [Limitations](#limitations). |

IPv4 and IPv6 records can coexist on one attachment. A queries return only
IPv4 addresses and AAAA queries return only IPv6 addresses.

## State updates

ACC must update the file safely:

1. Serialize concurrent state writers.
2. Write a complete replacement to a temporary file in the same directory.
3. Flush and close the temporary file.
4. Atomically rename it over `state.json`.
5. Ensure the replacement has a newer modification time.

The plugin checks `state.json` once per 5 second. On a changed modification time,
it parses and validates the entire file before replacing the in-memory
registry. Failed reads or invalid JSON leave the previous registry active.

The plugin emits an informational log for an initial successful load and for
each accepted update. Failed updates are logged as errors and retain the
last-known-good registry.

> The state.json is stored in `~/.acc/coredns/state.json`. Manually modifying the contents of state.json will break the internal networking of all the composes running using acc-coredns on the system. The contents are not meant to be edited manually and should be only edited by acc.

## Caching

Do not place CoreDNS's ordinary `cache` plugin before `acc`.

Standard CoreDNS cache keys do not contain the requesting source IP or its
network memberships. A cached answer for one container could therefore expose
an address to another container that does not share the target's network.

The bundled Corefile does not enable caching. Client-side caching still uses
the 30-second DNS TTL. A future ACC-aware cache must include requester
visibility in its key and invalidate entries when the registry changes.

## Operations

The plugin needs a valid initial state before CoreDNS starts:

```json
{"version":1,"containers":[]}
```

After startup, inspect plugin activity with the container runtime's log
command. A normal startup records an initial state load; replacing the state
file records a state update. CoreDNS's `log` directive records DNS queries,
while the ACC plugin records registry load failures and updates.

The nested module can be verified independently:

```sh
cd coredns/acc-plugin
go test -race ./...
go vet ./...
```

## Limitations

- ACC currently relies on Apple Container preserving the original source IP
  between a client and the project CoreDNS container. This requires runtime
  integration validation.
- A managed name queried by an unknown source IP currently continues to the
  downstream resolver. It never returns an ACC internal address, but the final
  policy for this case is not settled.
- Only A and AAAA service records are produced.
- Standard shared DNS caching is intentionally unsupported.
- The plugin does not create networks, start CoreDNS, configure `--dns`, or
  write state. Those responsibilities belong to ACC's pending Compose runtime
  integration.
