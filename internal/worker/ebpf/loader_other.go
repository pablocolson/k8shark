//go:build !linux

package ebpf

// newSource is the non-Linux stub: the uprobe loader needs cilium/ebpf +
// CO-RE, which only work against a Linux kernel. Darwin/Windows builds (this
// repo's local dev loop, `make build`/`make test` on macOS) get this instead
// of a build failure, matching internal/worker/capture's AF_PACKET split.
func newSource(cfg Config) (Source, error) {
	return nil, ErrUnsupported
}
