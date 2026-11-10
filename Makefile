GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/theatarr/theatarr/internal/buildinfo.Version=$(VERSION)

.PHONY: build test e2e race vet fmt lint image check ui ui-check

# bin/theatarr: the service and its tools.
build:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/theatarr ./cmd/theatarr

test:
	$(GO) test ./...

e2e:
	$(GO) test -v ./internal/e2e

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

# Race detector; needs cgo (CI runs it).
race:
	CGO_ENABLED=1 $(GO) test -race ./...

# staticcheck, as in CI.
lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...

# Local image; never pushed (releases are built by .github/workflows/release.yml). The Dockerfile builds the SPA in a
# node stage.
image:
	docker build --build-arg VERSION=$(VERSION) -t theatarr:$(VERSION) .

# What CI runs.
check: vet test
	THEATARR_TEST_MOUNT_HELPER=1 $(GO) test -count=1 ./internal/library
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# The web UI (web/, Node 20): build the SPA into internal/webui/dist, which `make build` then embeds. Without it the
# binary serves a "UI not built" page; nothing in check, test or build needs node.
web/node_modules/.package-lock.json: web/package-lock.json web/package.json
	cd web && npm ci --no-audit --no-fund

ui: web/node_modules/.package-lock.json
	cd web && npm run build

# Typecheck (with API types generated from internal/web/openapi.json), lint and unit tests.
ui-check: web/node_modules/.package-lock.json
	cd web && npm run check
