package daemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/policy"
)

// The local audit log (LocalAuditPath) is the workstation owner's record
// of what local policy refused, so nothing an HTTP
// request reaches may truncate, rewrite or remove it. Only the daemon's
// own startup opens it, append-only, and the handle it opens can do
// nothing but append and close: no route, and no package a route is
// served from, names the file or opens it.
func TestLocalAuditLogIsReachedByNoRoute(t *testing.T) {
	// Where the file may be named: the daemon opening it once (run.go),
	// the field holding it (config.go), and the package that writes it.
	allowed := []string{"pkg/daemon/config.go", "pkg/daemon/run.go", "pkg/policy/audit.go"}
	root := filepath.Join("..", "..")
	markers := []string{"hub-audit", "LocalAuditPath", "OpenAuditLog", "ReadAuditLog"}
	for _, dir := range []string{"pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, m := range markers {
				if strings.Contains(string(b), m) && !slices.Contains(allowed, rel) {
					t.Errorf("%s names the local audit log (%s): only the daemon's startup may open it", rel, m)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	var methods []string
	typ := reflect.TypeOf(&policy.AuditLog{})
	for i := range typ.NumMethod() {
		methods = append(methods, typ.Method(i).Name)
	}
	if want := []string{"Close", "Write"}; !slices.Equal(methods, want) {
		t.Errorf("policy.AuditLog methods = %v, want only %v: nothing holding the log may truncate or rewrite it", methods, want)
	}
}
