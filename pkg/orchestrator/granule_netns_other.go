//go:build !linux

package orchestrator

import "errors"

// netnsBridgePorts needs Linux network namespaces; elsewhere the caller
// falls back to asking inside the container.
func netnsBridgePorts(pid int) ([]string, error) {
	return nil, errors.New("finding a process's bridge ports needs Linux")
}
