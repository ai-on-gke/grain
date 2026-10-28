package hypervisor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// socketProbeTimeout bounds the connect used to tell a live unix socket
// from one left behind by a dead process. Both ends are on the same
// machine (the same container, in fact), so a socket with a listener
// answers immediately or not at all.
var socketProbeTimeout = 250 * time.Millisecond

// RemoveStaleSocket unlinks the unix socket at path so this boot can bind
// it again, and refuses to touch one something is still listening on.
//
// A VM container is left holding two named sockets -- cloud-hypervisor's
// API socket and the guest's vsock socket -- and neither can be bound a
// second time while the file is still there, so a fresh boot has to
// remove whatever an earlier one left behind. Removing one that a
// *running* VM still owns unlinks the name out from under it: the VMM
// carries on with no way in, and "granule exec", "granule cp", "granule
// resize" and the graceful-shutdown path all fail with ENOENT for the
// rest of that container's life. That is what a second "granule run" in a
// running VM's container used to do -- including a bare "granule", since
// "run" is the default mode -- cleaning both sockets up before finding
// out cloud-hypervisor would refuse to start anyway. what names the
// socket in the error, e.g. "cloud-hypervisor's API socket".
func RemoveStaleSocket(path, what string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking %s at %s: %w", what, path, err)
	}

	// A listener answers this connect; a socket whose process is gone
	// refuses it. Nothing is written, so a live VM is undisturbed by
	// having been asked.
	if conn, err := net.DialTimeout("unix", path, socketProbeTimeout); err == nil {
		conn.Close()
		return fmt.Errorf("%s at %s is still live: a VM is already running in this container, and one container runs one VM -- "+
			"reach the running one with \"granule exec\"/\"granule resize\" rather than starting another", what, path)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale %s at %s: %w", what, path, err)
	}
	return nil
}
