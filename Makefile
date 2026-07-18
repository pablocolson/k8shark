# k8shark — build & dev targets
BINARY      := bin/k8shark
PKG         := ./cmd/k8shark
VERSION     ?= dev
REGISTRY    ?= ghcr.io/pablocolson
TAG         ?= latest
PLATFORM    ?= linux/amd64
PLATFORMS   ?= linux/amd64,linux/arm64

# macOS 26 (darwin) needs the external linker (LC_UUID) + an ad-hoc signature to
# run locally. On Linux this is a no-op path.
GOOS := $(shell go env GOOS)
LDFLAGS := -X github.com/pablocolson/k8shark/internal/config.Version=$(VERSION)
ifeq ($(GOOS),darwin)
LDFLAGS := -linkmode=external $(LDFLAGS)
endif

.PHONY: all build ui sign run-hub run-worker dev clean docker-build docker-push docker-buildx helm-lint tidy test test-ui gen

all: ui build

## build the Go binary (native)
build:
	GOFLAGS=-mod=mod GOTOOLCHAIN=local CGO_ENABLED=1 go build \
		-ldflags='$(LDFLAGS)' -o $(BINARY) $(PKG)
ifeq ($(GOOS),darwin)
	@codesign -s - -f $(BINARY) >/dev/null 2>&1 && echo "codesigned (darwin)" || true
endif
	@echo "built $(BINARY)"

## build the front-end
ui:
	cd ui && npm install --no-audit --no-fund && npm run build

## run the hub locally, serving the built UI at http://localhost:8898
run-hub: build
	$(BINARY) hub --serve-ui ui/dist --log-level debug

## run a demo worker against a local hub
run-worker: build
	$(BINARY) worker --hub ws://localhost:8898/ws/worker --node dev --demo --demo-rps 40

## build UI + start hub + demo worker together (local, no cluster)
dev: ui build
	@bash scripts/dev.sh

tidy:
	GOFLAGS=-mod=mod go mod tidy

## regenerate the eBPF TLS uprobe bytecode (internal/worker/ebpf). Needs a real
## clang with the bpf target, which Apple clang lacks — runs inside the Linux
## build image. Commit the regenerated tls_bpf*.go/.o; darwin builds use the
## committed output (loader_other.go stubs out the loader itself).
gen:
	docker run --rm -v "$(CURDIR)":/src -w /src/internal/worker/ebpf golang:1.24-bookworm bash -c \
		"apt-get update -qq && apt-get install -y -qq --no-install-recommends clang llvm libbpf-dev linux-libc-dev && GOFLAGS=-mod=mod go generate ./..."

test:
	GOFLAGS=-mod=mod GOTOOLCHAIN=local go test -race \
		$(if $(filter darwin,$(GOOS)),-ldflags='-linkmode=external',) ./...

## run the front-end unit tests (vitest)
test-ui:
	cd ui && npm ci --no-audit --no-fund && npm test

helm-lint:
	helm lint helm/k8shark

## build both container images (binary + front) for the target platform
docker-build:
	docker build --platform $(PLATFORM) -f build/k8shark.Dockerfile -t $(REGISTRY)/k8shark:$(TAG) .
	docker build --platform $(PLATFORM) -f build/front.Dockerfile -t $(REGISTRY)/k8shark-front:$(TAG) .

docker-push: docker-build
	docker push $(REGISTRY)/k8shark:$(TAG)
	docker push $(REGISTRY)/k8shark-front:$(TAG)

## build & push multi-arch images (amd64+arm64) via buildx
docker-buildx:
	docker buildx create --use --name k8shark-builder 2>/dev/null || docker buildx use k8shark-builder
	docker buildx build --platform $(PLATFORMS) --push \
		-f build/k8shark.Dockerfile -t $(REGISTRY)/k8shark:$(TAG) .
	docker buildx build --platform $(PLATFORMS) --push \
		-f build/front.Dockerfile   -t $(REGISTRY)/k8shark-front:$(TAG) .

clean:
	rm -rf bin ui/dist ui/node_modules
