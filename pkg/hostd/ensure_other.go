//go:build !unix

package hostd

import "errors"

// EnsureBuiltins needs a unix host, the only kind grain hostd runs on.
func EnsureBuiltins(path, command string, builtins []BuiltinEntry) (EnsureResult, error) {
	return EnsureResult{}, errors.New("grain hostd runs on Linux only")
}
