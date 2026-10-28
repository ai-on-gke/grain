//go:build !linux && !darwin

package hosttop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/hosttop"
)

// On a platform hosttop has no invocation for, Read refuses rather than
// guessing at flags -- and refuses without running anything, so the answer
// is the same whether or not something named `top` happens to be on PATH.
func TestReadReportsAnUnsupportedPlatform(t *testing.T) {
	_, err := hosttop.Read(context.Background(), 0)
	if err == nil {
		t.Fatal("Read returned no error on a platform with no top invocation")
	}
	if !strings.Contains(err.Error(), "only available on Linux and macOS") {
		t.Errorf("Read error = %v, want one naming the platforms it supports", err)
	}
}
