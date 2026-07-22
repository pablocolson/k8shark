# k8shark binary image — runs both the hub and the worker.
# CGO is required for AF_PACKET live capture (gopacket/afpacket), so the build
# happens on a Linux toolchain with C headers available (linux-libc-dev supplies
# the kernel uapi headers gopacket/afpacket needs). The eBPF bytecode is NOT
# compiled here: `go generate ./internal/worker/ebpf/...` (bpf2go on
# bpf/tls.bpf.c) is run at development time and the resulting tls_bpf*.go plus
# .o objects are committed and pulled in via go:embed, so the image needs no
# clang/llvm/libbpf toolchain. CO-RE relocates the embedded .o against node BTF
# at load time, no compiler needed at runtime either.
FROM golang:1.25-bookworm AS build
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends \
    linux-libc-dev \
    && rm -rf /var/lib/apt/lists/*

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=1 GOOS=linux
RUN go build -trimpath \
    -ldflags="-s -w -X github.com/pablocolson/k8shark/internal/config.Version=${VERSION}" \
    -o /out/k8shark ./cmd/k8shark

# Runtime: slim Debian (glibc for the cgo binary). No libpcap needed — AF_PACKET
# talks to the kernel directly.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/k8shark /usr/local/bin/k8shark
# Run non-root by default (matches the hub's securityContext.runAsUser). The
# worker DaemonSet overrides this via its in-cluster securityContext, so its
# privileged AF_PACKET/eBPF capture is unaffected.
USER 65532:65532
ENTRYPOINT ["k8shark"]
