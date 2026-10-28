//go:build !unix

package hostd

import "errors"

func checkOwnedBy(string, []uint32) error {
	return errors.New("grain hostd: allowlist ownership can only be checked on unix")
}

func socketDirRemoved(string) bool { return false }
