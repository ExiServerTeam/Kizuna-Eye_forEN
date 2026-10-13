# Kizuna-Eye Makefile
# 仕様: build.sh と同じ成果物・同じビルドフラグを /opt/kizuna-eye/bin に出力する。

BIN_DIR ?= /opt/kizuna-eye/bin
# VERSION is the single source of truth (repo root). Never hardcode a version
# here: it would drift from build.sh / the web UI on the next release.
VERSION ?= $(shell cat VERSION 2>/dev/null || echo v0.0.0-unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X Kizuna-Eye/internal/api.Version=$(VERSION) -X Kizuna-Eye/internal/api.BuildTime=$(BUILD_TIME)

# ABI: host and every plugin MUST be built with the SAME toolchain and the SAME
# -trimpath setting. Without -trimpath the builder's GOMODCACHE path is embedded
# in each package's build ID, so a host built here (make) and a plugin built by
# build-all.sh / scripts/build-plugin.sh would have different hashes and the
# agent would fail to load every .so with
#     plugin was built with a different version of package Kizuna-Eye/...
# Keep these in step with build.sh / build-all.sh.
GOTOOLCHAIN ?= auto
PINNED_GO ?= go1.27.2
TRIMPATH := -trimpath
# -s -w strips symbols/DWARF (memory optimization for low-spec hosts).
# Set KIZUNA_DEBUG_BUILD=1 to keep debug symbols.
ifeq ($(KIZUNA_DEBUG_BUILD),1)
LDFLAGS += 
else
LDFLAGS := -s -w $(LDFLAGS)
endif

.PHONY: all build agent dashboard plugin-inspect plugin-sign test vet clean

all: build

build: agent dashboard plugin-inspect plugin-sign

agent:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 GOTOOLCHAIN=$(PINNED_GO) go build -buildvcs=false $(TRIMPATH) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/agent_linux ./cmd/agent

dashboard:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 GOTOOLCHAIN=$(PINNED_GO) go build -buildvcs=false $(TRIMPATH) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/dashboard_linux ./cmd/dashboard

plugin-inspect:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 GOTOOLCHAIN=$(PINNED_GO) go build -buildvcs=false $(TRIMPATH) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/plugin-inspect ./cmd/plugin-inspect

plugin-sign:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 GOTOOLCHAIN=$(PINNED_GO) go build -buildvcs=false $(TRIMPATH) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/plugin-sign ./cmd/plugin-sign

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN_DIR)/agent_linux $(BIN_DIR)/dashboard_linux $(BIN_DIR)/plugin-inspect $(BIN_DIR)/plugin-sign
