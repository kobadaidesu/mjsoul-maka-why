GO ?= go
STATICCHECK := $(CURDIR)/.tools/staticcheck
STATICCHECK_VERSION := v0.8.1

.PHONY: build test lint tools
build:
	$(GO) build -o bin/mjcap ./cmd/mjcap

test:
	$(GO) test ./...

tools:
	GOBIN="$(CURDIR)/.tools" $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

lint:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	$(GO) vet ./...
	"$(STATICCHECK)" ./...
