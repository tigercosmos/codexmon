BINARY := codexmon
PKG := ./cmd/codexmon
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/tigercosmos/codexmon/internal/cli.Version=$(VERSION)

# Install location for `make install` (override e.g. `make install PREFIX=$HOME/.local`).
PREFIX ?= /usr/local
BINDIR := $(DESTDIR)$(PREFIX)/bin

# Where to install the agent skill. Resolve the *invoking* user's home even under
# sudo, so `sudo make install` puts the skill in their ~/.claude, not root's.
USER_HOME := $(shell if [ -n "$$SUDO_USER" ]; then eval echo "~$$SUDO_USER"; else echo "$$HOME"; fi)
SKILLS_DIR ?= $(USER_HOME)/.claude/skills

.PHONY: all build install install-skill install-go uninstall test race vet fmt fmt-check staticcheck lint cover dist snapshot clean

all: fmt-check vet build test

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# Install the binary to $(BINDIR) (default /usr/local/bin) AND the agent skill to
# $(SKILLS_DIR). Use `sudo make install` if /usr/local isn't writable.
install: build
	@mkdir -p "$(BINDIR)"
	install -m 0755 $(BINARY) "$(BINDIR)/$(BINARY)"
	@echo "installed $(BINARY) $(VERSION) -> $(BINDIR)/$(BINARY)"
	@$(MAKE) --no-print-directory install-skill

# Install just the agent skill into $(SKILLS_DIR)/codexmon.
install-skill:
	@mkdir -p "$(SKILLS_DIR)/$(BINARY)"
	@cp -R skills/$(BINARY)/. "$(SKILLS_DIR)/$(BINARY)/"
	@echo "installed skill -> $(SKILLS_DIR)/$(BINARY)/"

uninstall:
	rm -f "$(BINDIR)/$(BINARY)"
	rm -rf "$(SKILLS_DIR)/$(BINARY)"
	@echo "removed $(BINDIR)/$(BINARY) and $(SKILLS_DIR)/$(BINARY)"

# Install the binary into the Go bin dir ($GOBIN or ~/go/bin) instead.
install-go:
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
