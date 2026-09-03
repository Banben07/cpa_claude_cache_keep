.PHONY: test build

PLUGIN_ID := claude-cache-keepalive
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

ifeq ($(GOOS),darwin)
EXT := dylib
else ifeq ($(GOOS),windows)
EXT := dll
else
EXT := so
endif

OUT := dist/$(GOOS)/$(GOARCH)/$(PLUGIN_ID).$(EXT)

test:
	go test ./...

build:
	mkdir -p "$(dir $(OUT))"
	CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared -o "$(OUT)" .
	@echo "built $(OUT)"
	@echo "copy into CPA plugins dir, e.g. ~/.cli-proxy-api/plugins/$(GOOS)/$(GOARCH)/$(PLUGIN_ID).$(EXT)"
