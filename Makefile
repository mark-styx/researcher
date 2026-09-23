BINARY := researchguy
PKG := github.com/marklubin/researchguy
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X $(PKG)/cmd/researchguy.Version=$(VERSION)"

.PHONY: build install clean test

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/researchguy

install:
	go install $(LDFLAGS) ./cmd/researchguy

clean:
	rm -f $(BINARY)

test:
	go test ./...
