BINARY := codexmon
PKG := ./cmd/codexmon
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/tigercosmos/codexmon/internal/cli.Version=$(VERSION)

.PHONY: all build install test race vet fmt fmt-check staticcheck lint cover dist snapshot clean

all: fmt-check vet build test

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

install:
	go install -ldflags "$(LDFLAGS)" $(PKG)

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

lint: fmt-check vet staticcheck

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Cross-platform release archives into ./dist (no goreleaser needed).
dist:
	./scripts/dist.sh

# Local goreleaser dry run (requires goreleaser); does not publish.
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf $(BINARY) coverage.out dist
