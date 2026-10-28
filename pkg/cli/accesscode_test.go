package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func TestAccessCodeShowsAndRotatesTheCode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, dir)
	code, err := ui.EnsureLocalAPIToken(ui.LocalAPITokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatalf("grain access-code: %v (%s)", err, errOut.String())
	}
	if strings.TrimSpace(out.String()) != code {
		t.Errorf("grain access-code printed %q, want %q", out.String(), code)
	}

	out.Reset()
	if err := runAccessCode([]string{"rotate"}, &out, &errOut); err != nil {
		t.Fatalf("grain access-code rotate: %v (%s)", err, errOut.String())
	}
	rotated := strings.TrimSpace(out.String())
	if rotated == "" || rotated == code {
		t.Fatalf("grain access-code rotate printed %q (old %q)", rotated, code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ui.LocalAPITokenFileName))
	if strings.TrimSpace(string(b)) != rotated {
		t.Errorf("the file holds %q after rotate, want %q", b, rotated)
	}
	if fi, _ := os.Stat(filepath.Join(dir, ui.LocalAPITokenFileName)); fi.Mode().Perm() != 0o600 {
		t.Errorf("rotate left the file mode %o, want 0600", fi.Mode().Perm())
	}
}

func TestAccessCodeRotateNeedsAnExistingInstallation(t *testing.T) {
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, t.TempDir())
	var out, errOut bytes.Buffer
	if err := runAccessCode([]string{"rotate"}, &out, &errOut); err == nil {
		t.Errorf("rotate with no code file succeeded: %q", out.String())
	}
}
