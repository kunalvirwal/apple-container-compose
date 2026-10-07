# ACC Compose examples

Build the CLI once before running an example:

```sh
make build
```

Each example uses its own Compose project name, so its resources remain
separate from the others. Tear an example down with the same file:

```sh
./acc down --file ./examples/<example>.compose.yaml --volumes
```

With services selected, `down --volumes` removes only ACC-owned named and
anonymous volumes attached to containers removed by that invocation. Volumes
referenced by any remaining container (including stopped containers) are kept,
as are external volumes and unrelated detached volumes. For example:

```sh
./acc down app --file ./examples/volumes.compose.yaml --volumes
```

- `basic.compose.yaml`: image, port publishing, environment, and attached logs.
- `build.compose.yaml`: local Dockerfile build through `acc up`.
- `volumes.compose.yaml`: bind, named, anonymous, and tmpfs mounts. Alternative
  options are documented inline.
- `networks.compose.yaml`: isolated, multi-network, and implicit-default
  services; network-scoped aliases; and the state records that will feed the
  per-project CoreDNS container.
