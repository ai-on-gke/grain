//go:build !unix

package ui

// FileOwner is the account scripts/setup.sh creates: a host with no
// Unix owners to ask has no other answer.
func FileOwner(string) string { return "grain" }
