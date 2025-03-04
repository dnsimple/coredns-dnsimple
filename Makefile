# Determine which sed to use
ifeq ($(shell uname),Darwin)
	SED=gsed
else
	SED=sed -i
endif

PLUGIN_VERSION:=$(shell grep 'PluginVersion' ./version.go | awk '{ print $$3 }' | tr -d '"')

ifdef PACKAGER_VERSION
PKG_VERSION := $(PACKAGER_VERSION)
else
PKG_VERSION := $(PLUGIN_VERSION)
endif

.PHONY: version
version:
	@echo $(PKG_VERSION)

.PHONY: lint
lint: install-tools
	golangci-lint run

.PHONY: fmt
fmt: install-tools
	go fmt ./...
	gofumpt -w ./

.PHONY: install-tools
install-tools:
	@echo "Installing tools..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@go install mvdan.cc/gofumpt@latest

.PHONY: test
test:
	go test -v ./...

.PHONY: build
build:
	@echo "Building coredns-dnsimple..."
	@if [ ! -d "coredns" ]; then \
		git clone https://github.com/coredns/coredns.git; \
	fi
	@export PLUGIN_DIR=$(shell pwd); \
	cd coredns; \
	if ! grep -q "replace github.com/dnsimple/coredns-dnsimple" go.mod; then \
		$(SED) -i "/^go 1/a replace github.com/dnsimple/coredns-dnsimple => $$PLUGIN_DIR" go.mod; \
		$(SED) -i '/route53:route53/i dnsimple:github.com\/dnsimple\/coredns-dnsimple' plugin.cfg; \
	fi; \
	gitcommit=$(shell git describe --dirty --always); \
	go build -o coredns -ldflags="-s -w -X github.com/coredns/coredns/coremain.GitCommit=$$gitcommit" .
	@mkdir -p bin
	@cp coredns/coredns bin/coredns-dnsimple

.PHONY: docker-build
docker-build:
	@echo "Building Docker image"
	@if [ ! -d "coredns.docker" ]; then \
		git clone https://github.com/coredns/coredns.git coredns.docker; \
	fi
	cd coredns.docker; \
	if ! grep -q "replace github.com/dnsimple/coredns-dnsimple" go.mod; then \
		$(SED) -i "/^go /a replace github.com/dnsimple/coredns-dnsimple => ./plugin/dnsimple" go.mod; \
		$(SED) -i '/route53:route53/i dnsimple:github.com\/dnsimple\/coredns-dnsimple' plugin.cfg; \
	fi; \
	mkdir -p plugin/dnsimple; \
	cp ../*.go plugin/dnsimple; \
	cp ../go.mod plugin/dnsimple; \
	GOFLAGS=-mod=mod go generate; \
	go mod tidy; \
	cp ../Dockerfile.release Dockerfile; \
	cp ../bin/_docker/docker-entrypoint.sh docker-entrypoint.sh; \
	if [ -f ".dockerignore" ]; then \
		rm .dockerignore; \
	fi; \
	docker build --no-cache --build-arg PACKAGER_VERSION=$(PKG_VERSION) --no-cache -t dnsimple/coredns:$(PKG_VERSION) .

.PHONY: clean
clean:
	rm -rf coredns
	rm -f bin/coredns-dnsimple

.PHONY: start
start: build
	./bin/coredns-dnsimple -conf Corefile

.PHONY: release
release: lint fmt test
	@echo "Checking if current branch is main"
	if [ "$(shell git branch --show-current)" != "main" ]; then \
		echo "Not on main branch"; \
		echo "Please switch to main and try again"; \
		exit 1; \
	fi
	@echo "Checking if there are any uncommitted changes"
	if [ -n "$(shell git status --porcelain)" ]; then \
		echo "There are uncommitted changes"; \
		echo "Please commit or stash your changes and try again"; \
		exit 1; \
	fi
	@echo "Checking if version $(PLUGIN_VERSION) matches the provided $(VERSION)"
	if [ "$(PLUGIN_VERSION)" != "$(VERSION)" ]; then \
		echo "Version mismatch"; \
		echo "Please update the version in version.go to $(VERSION) or supply the correct version e.g. make release VERSION=1.2.3"; \
		exit 1; \
	fi
	@echo "Checking if version $(PLUGIN_VERSION) already exists"
	if git rev-parse v$(PLUGIN_VERSION) >/dev/null 2>&1; then \
		echo "Version $(PLUGIN_VERSION) already exists"; \
		echo "Please update the version in version.go and try again"; \
		exit 1; \
	fi
	@echo "Checking if CHANGELOG.md has been updated"
	if ! grep -q "## $(PLUGIN_VERSION)" CHANGELOG.md; then \
		echo "CHANGELOG.md has not been updated"; \
		echo "Please update CHANGELOG.md and try again"; \
		exit 1; \
	fi
	@echo "Releasing version $(PLUGIN_VERSION)"
	@echo "Tagging release"
	git tag -a v$(PLUGIN_VERSION) -s -m "Release v$(PLUGIN_VERSION)"
	@echo "Releasing to GitHub"
	git push origin v$(PLUGIN_VERSION)
	@echo "Releasing of v$(PLUGIN_VERSION) complete 🎉"
