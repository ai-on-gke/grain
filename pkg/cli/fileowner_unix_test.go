//go:build unix

package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

// foreignTokenFile is a file this test cannot read that another account
// -- root -- owns, standing in for a daemon's local API token on a host
// where grain does not run as "grain": google/scripts/cloudtop-install.sh runs it
// as whoever installed it. It returns the path and the owner's name.
func foreignTokenFile(t *testing.T) (string, string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode or owner")
	}
	root, err := user.LookupId("0")
	if err != nil {
		t.Skipf("no name for uid 0: %v", err)
	}
	// /etc/shadow is root's and unreadable to others; a file under /root
	// cannot even be stat'ed, so its directory names the owner.
	for _, p := range []string{"/etc/shadow", "/root/local-api-token"} {
		if _, err := os.ReadFile(p); !errors.Is(err, os.ErrPermission) {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			fi, err = os.Stat(filepath.Dir(p))
		}
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == 0 {
			return p, root.Username
		}
	}
	t.Skip("no root-owned file here that this account cannot read")
	return "", ""
}

// `grain access-code` run by the wrong account names the account that
// owns the code, not a "grain" account the host may not have.
func TestAccessCodeNamesTheTokenFilesOwner(t *testing.T) {
	path, owner := foreignTokenFile(t)
	t.Setenv(ui.LocalAPITokenFileEnv, path)
	t.Setenv(dataDirEnvVar, "")
	var out, errOut bytes.Buffer
	err := runAccessCode(nil, &out, &errOut)
	if err == nil {
		t.Fatalf("grain access-code read %s: %q", path, out.String())
	}
	msg := err.Error()
	for _, want := range []string{"(" + owner + " owns it)", "sudo -u " + owner + " grain access-code"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not say %q", msg, want)
		}
	}
	if owner != "grain" && strings.Contains(msg, "sudo -u grain ") {
		t.Errorf("error %q names a grain account rather than the file's owner", msg)
	}
}

// `grain list` refused for want of a readable token names the token
// file's owner as the account to run it as.
func TestAnUnauthorisedCallNamesTheTokenFilesOwner(t *testing.T) {
	path, owner := foreignTokenFile(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"sign in to use this server's API"}`))
	}))
	defer srv.Close()
	writeCLIEnv(t, "")
	t.Setenv(ui.LocalAPITokenFileEnv, path)

	err := runCLI([]string{"-server", srv.URL, "list"})
	if err == nil {
		t.Fatal("runCLI succeeded against a 401")
	}
	msg := err.Error()
	for _, want := range []string{path + " exists", "cannot read it", "sudo -u " + owner + " grain ..."} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not say %q", msg, want)
		}
	}
	if owner != "grain" && strings.Contains(msg, "sudo -u grain ") {
		t.Errorf("error %q names a grain account rather than the file's owner", msg)
	}
}
