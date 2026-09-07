# Build with the repository root as the build context:
#   container build -f coredns/Dockerfile -t docker.io/kunalvirwal/acc-coredns:latest .
#
# The ACC plugin is compiled into CoreDNS. It cannot be added to a stock
# CoreDNS binary at runtime.
ARG COREDNS_VERSION=v1.14.6

FROM golang:1.26.4-alpine AS builder
ARG COREDNS_VERSION

RUN apk add --no-cache git

WORKDIR /src
RUN git clone --branch "${COREDNS_VERSION}" --depth 1 \
    https://github.com/coredns/coredns.git coredns

# Keep the plugin and its deliberately small registration list in this
# repository. ACC executes before forward.
COPY coredns/plugin.cfg /src/coredns/plugin.cfg
COPY coredns/acc-plugin /src/coredns/acc-plugin

WORKDIR /src/coredns
RUN go mod edit \
        -require=github.com/kunalvirwal/apple-container-compose/coredns/acc-plugin@v0.0.0 \
        -replace=github.com/kunalvirwal/apple-container-compose/coredns/acc-plugin=./acc-plugin \
    && go generate \
    && CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags='-s -w' -o /out/coredns .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && mkdir -p /config

COPY --from=builder /out/coredns /usr/local/bin/coredns
COPY coredns/Corefile /etc/coredns/Corefile

EXPOSE 53/tcp
EXPOSE 53/udp

# ACC mounts a directory at /config containing state.json. Mounting the whole
# directory is required: ACC publishes state updates through atomic rename.
ENTRYPOINT ["/usr/local/bin/coredns"]
CMD ["-conf", "/etc/coredns/Corefile"]
