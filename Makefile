GO ?= go
GOFMT ?= gofmt
BIN ?= acc

.PHONY: build test vet fmt check help clean

build:
	$(GO) build -o $(BIN) ./cmd/acc

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GOFMT) -w $$(git ls-files '*.go')

check: test vet

help:
	$(GO) run ./cmd/acc --help

clean:
	rm -f $(BIN)
