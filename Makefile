BINARY := researcher
PKG := github.com/marklubin/researcher
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X $(PKG)/cmd/researcher.Version=$(VERSION)"

.PHONY: build install clean test

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/researcher

install:
	go install $(LDFLAGS) ./cmd/researcher

clean:
	rm -f $(BINARY)

test:
	go test ./...
