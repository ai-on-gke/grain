package hostscriptcli

import (
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeEndpoint answers exec calls as the host-script server would: it
// echoes the decoded argv on stdout, one frame each, and exits 3.
func fakeEndpoint(t *testing.T, token string) (port int, seen *http.Header) {
	t.Helper()
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, "e refused\\012\nx 125\n")
			return
		}
		body, _ := io.ReadAll(r.Body)
		for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			if line == "" && len(body) == 0 {
				break
			}
			b, _ := hex.DecodeString(line)
			io.WriteString(w, "o ["+strings.ReplaceAll(string(b), "\n", "\\012")+"]\\012\n")
		}
		io.WriteString(w, "e done\\012\nx 3\n")
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ = strconv.Atoi(p)
	return port, &hdr
}

func place(t *testing.T, dir string, d Descriptor) {
	t.Helper()
	b, err := Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, d.Name+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(dir string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(dir, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRun(t *testing.T) {
	port, hdr := fakeEndpoint(t, "tok")
	dir := t.TempDir()
	place(t, dir, Descriptor{Name: "deploy", Description: "deploys <things> & more", Usage: "deploy ENV\nENV is \"prod\" or staging.", Port: port, AttemptID: "e1-1-1-0", Token: "tok"})
	place(t, dir, Descriptor{Name: "bad-token", Description: "refused", Port: port, AttemptID: "e1-1-1-1", Token: "nope"})

	code, out, errs := run(dir, "deploy", "two words", "a\nb", "")
	if code != 3 || out != "[two words]\n[a\nb]\n[]\n" || errs != "done\n" {
		t.Errorf("call = %d %q %q", code, out, errs)
	}
	if got := hdr.Get("Content-Type"); got != ArgvType {
		t.Errorf("Content-Type %q, want %q", got, ArgvType)
	}
	if code, _, errs := run(dir, "bad-token"); code != ExitProtocol || errs != "refused\n" {
		t.Errorf("a refused token = %d %q, want 125 and the server's reason", code, errs)
	}
	for _, args := range [][]string{{"deploy", "--grain-help"}, {"--help", "deploy"}} {
		code, out, _ := run(dir, args...)
		if code != 0 || !strings.Contains(out, "grain host-script deploy [arguments...]") ||
			!strings.Contains(out, "deploys <things> & more") || !strings.Contains(out, "ENV is \"prod\" or staging.") {
			t.Errorf("%v = %d %q", args, code, out)
		}
	}
	if code, out, _ := run(dir, "--list"); code != 0 || out != "bad-token\trefused\ndeploy\tdeploys <things> & more\n" {
		t.Errorf("--list = %d %q", code, out)
	}
	if code, _, errs := run(dir, "missing"); code != ExitProtocol || !strings.Contains(errs, `no host script "missing" is granted`) {
		t.Errorf("an ungranted script = %d %q", code, errs)
	}
	if code, _, _ := run(dir, "../deploy"); code != 2 {
		t.Errorf("a path as a name = %d, want 2", code)
	}
	if code, _, _ := run(dir); code != 2 {
		t.Errorf("no arguments = %d, want 2", code)
	}
}

// The descriptor is one field to a line with no HTML escaping, which
// FallbackScript's sed relies on.
func TestMarshalIsOneFieldToALine(t *testing.T) {
	b, err := Marshal(Descriptor{Name: "x", Description: "a <b>\nc", Port: 1, AttemptID: "e1", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"x\",\n  \"description\": \"a <b>\\nc\",\n  \"port\": 1,\n  \"attempt_id\": \"e1\",\n  \"token\": \"t\"\n}\n"
	if string(b) != want {
		t.Errorf("Marshal = %q, want %q", b, want)
	}
}
