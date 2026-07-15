//go:build !linux

package capture

import "errors"

// ErrUnsupported is returned when live capture is requested on a non-Linux host.
var ErrUnsupported = errors.New("live capture is only supported on linux; use --demo")

func newLive(iface string, snaplen int) (PacketSource, error) {
	return nil, ErrUnsupported
}
