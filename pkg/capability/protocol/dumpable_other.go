//go:build !linux

package protocol

// MakeDumpable is a no-op outside Linux.
func MakeDumpable() {}

// DropCapabilities is a no-op outside Linux.
func DropCapabilities() error { return nil }
