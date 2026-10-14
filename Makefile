GO := env GOTOOLCHAIN=go$(shell sed -n 's/^go //p' go.mod) go
VERSION ?= unreleased
RACE ?= -race
OUTPUT ?= bin/nodit
BUILDINFO := $(shell $(GO) list -m)/internal/buildinfo

.PHONY: build test

build:
	@test -n "$(AUTH_ISSUER)" || { echo 'AUTH_ISSUER is required' >&2; exit 2; }
	@test -n "$(API_RESOURCE)" || { echo 'API_RESOURCE is required' >&2; exit 2; }
	@test -n "$(PRODUCT_DOMAIN)" || { echo 'PRODUCT_DOMAIN is required' >&2; exit 2; }
	@$(GO) build -trimpath -ldflags '-X $(BUILDINFO).AuthIssuer=$(AUTH_ISSUER) -X $(BUILDINFO).APIResource=$(API_RESOURCE) -X $(BUILDINFO).ProductDomain=$(PRODUCT_DOMAIN) -X $(BUILDINFO).Version=$(VERSION)' -o $(OUTPUT) ./cmd/nodit

test:
	$(GO) test $(RACE) ./...
	$(GO) vet ./...
