package orchestrator_test

import (
	"context"
	"os"
	"testing"

	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A host sandbox's directory holds a checkout that may be private, so
// only this account may read it -- when it is made and when a rebuild
// makes it again (GSA1-18).
func TestHostSandboxDirectoryIsPrivate(t *testing.T) {
	ctx := context.Background()
	sb, err := orchestrator.NewHostSandboxes(t.TempDir()).Acquire(ctx, "1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sb.Release(ctx) })
	root, err := sb.(interface{ Root() (string, error) }).Root()
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("sandbox directory %s: mode %o, want no access for group or others", when, perm)
		}
	}
	check("as acquired")
	if err := sb.(interface{ Rebuild(context.Context) error }).Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	check("after a rebuild")
}
