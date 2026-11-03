package hypervisor

import (
	"log"
	"os"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

// diskDirect decides whether a disk image at path is attached with
// direct=on, under config.Config.DiskDirect's mode.
func diskDirect(mode, path string) bool {
	switch mode {
	case config.DiskDirectOn:
		return true
	case config.DiskDirectAuto:
		if err := probeDirect(path); err != nil {
			log.Printf("attaching %s without direct=on, so its writes go through this container's page cache: %v", path, err)
			return false
		}
		return true
	default:
		return false
	}
}

// probeDirect checks that every file cloud-hypervisor will open for the
// disk at path -- the image and, for a qcow2, its whole backing chain --
// can be opened with O_DIRECT, which is what direct=on asks of each. A
// filesystem without O_DIRECT support refuses the open with EINVAL, and
// cloud-hypervisor would refuse to boot on it.
func probeDirect(path string) error {
	chain, err := qcow2.Chain(path)
	if err != nil {
		return err
	}
	for _, p := range chain {
		fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC, 0)
		if err != nil {
			return &os.PathError{Op: "open O_DIRECT", Path: p, Err: err}
		}
		unix.Close(fd)
	}
	return nil
}
