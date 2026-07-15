// Package config holds build-time metadata and shared runtime defaults for all
// k8shark components.
package config

const (
	// Name is the product/brand name.
	Name = "k8shark"

	// DefaultHubPort is the port the hub listens on for both the front API and
	// worker connections.
	DefaultHubPort = 8898

	// DefaultFrontPort is the port the front-end (nginx) serves on inside the
	// cluster.
	DefaultFrontPort = 80

	// DefaultNamespace is the k8s namespace k8shark installs into.
	DefaultNamespace = "k8shark"

	// EntryBufferSize is how many recent entries the hub keeps in memory.
	EntryBufferSize = 10000

	// Capture-depth defaults (worker). All are per-direction and hard bounds so
	// a garbled/encrypted stream can't drive an unbounded allocation.
	DefaultBodyCaptureBytes = 4096 // max L7 body snippet per direction
	DefaultRawCaptureBytes  = 2048 // max raw bytes hex-dumped per direction
	DefaultHeaderHexBytes   = 128  // L2/L3/L4 header dump cap
)

// Version is overridden at build time via -ldflags. Defaults to a dev marker.
var Version = "dev"

// Ver returns the current version string.
func Ver() string { return Version }
