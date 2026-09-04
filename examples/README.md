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

- `basic.compose.yaml`: image, port publishing, environment, and attached logs.
- `build.compose.yaml`: local Dockerfile build through `acc up`.
- `volumes.compose.yaml`: bind, named, anonymous, and tmpfs mounts. Alternative
  options are documented inline.
- `networks.compose.yaml`: project networks and a service attached to two of them.

