# Kizuna-Eye Makefile
# 仕様: build.sh と同じ成果物を /opt/kizuna-eye/bin に出力する。

BIN_DIR ?= /opt/kizuna-eye/bin
VERSION ?= v0.7.1
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X Kizuna-Eye/internal/api.Version=$(VERSION) -X Kizuna-Eye/internal/api.BuildTime=$(BUILD_TIME)

.PHONY: all build agent dashboard plugin-inspect test vet clean

all: build

build: agent dashboard plugin-inspect

agent:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/agent_linux ./cmd/agent

dashboard:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/dashboard_linux ./cmd/dashboard

plugin-inspect:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/plugin-inspect ./cmd/plugin-inspect

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN_DIR)/agent_linux $(BIN_DIR)/dashboard_linux $(BIN_DIR)/plugin-inspect
