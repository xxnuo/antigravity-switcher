GO ?= go
APP_NAME ?= antigravity-switcher
DIST_DIR ?= dist
PACKAGE ?= .
GOFLAGS ?=
LDFLAGS ?= -s -w
ARGS ?=

HOST_OS := $(shell $(GO) env GOOS)
HOST_ARCH := $(shell $(GO) env GOARCH)
HOST_EXT := $(if $(filter windows,$(HOST_OS)),.exe,)
HOST_OUTPUT := $(DIST_DIR)/$(APP_NAME)-$(HOST_OS)-$(HOST_ARCH)$(HOST_EXT)
RELEASE_TARGETS := darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64

.PHONY: build test run clean release $(RELEASE_TARGETS:%=build-%)

build:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(HOST_OUTPUT) $(PACKAGE)

test:
	$(GO) test ./...

run:
	$(GO) run $(PACKAGE) $(ARGS)

release: $(RELEASE_TARGETS:%=build-%)

build-%:
	@target="$*"; \
	os=$${target%-*}; \
	arch=$${target#*-}; \
	ext=""; \
	if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	mkdir -p $(DIST_DIR); \
	CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o "$(DIST_DIR)/$(APP_NAME)-$$os-$$arch$$ext" $(PACKAGE)

clean:
	rm -rf $(DIST_DIR)
