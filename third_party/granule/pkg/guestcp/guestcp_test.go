package guestcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/guestexec"
)

// fakeCHV stands in for cloud-hypervisor's hybrid-vsock endpoint, the
// same way pkg/guestexec's own tests do: a unix socket that answers
// the CONNECT handshake and hands the connection to the real guest-side
// agent, so a copy here runs the production client, the production wire
// format and the production agent against a real shell. There is no VM,
// so the "guest" filesystem is the build machine's.
//
// (guestexec's copy of this carries failure modes -- a refused
// handshake, a session that hangs up mid-command -- that belong to the
// transport it tests rather than to copying, so this one stays the
// happy-path endpoint plus the one thing copying has to survive that
// exec does not: a guest that puts a pty in the middle.)
type fakeCHV struct {
	socket string

	// wrap makes every session run under a pty, standing in for a guest
	// that wraps its sessions the way the reference guest image's console
	// wrapper used to (CRLF on the way out, stderr folded into stdout, no
	// end-of-input unless something sends an EOT). The client never asks
	// for a terminal itself, so this is forced on guest-side by rewriting
	// the request -- which is exactly what a wrapper is.
	wrap bool
}

func startFakeCHV(t *testing.T, f *fakeCHV) *fakeCHV {
	t.Helper()
	if f == nil {
		f = &fakeCHV{}
	}
	// Short path: a unix socket's address has a hard length limit (~108
	// bytes) that a nested t.TempDir() under a long test name can exceed.
	dir, err := os.MkdirTemp("", "chv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f.socket = filepath.Join(dir, "vsock.sock")

	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeCHV) serve(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", 4242); err != nil {
		return
	}

	var r io.Reader = br
	if f.wrap {
		req, err := execwire.ReadRequest(br)
		if err != nil {
			return
		}
		req.TTY, req.Cols, req.Rows = true, 80, 24
		var head bytes.Buffer
		if err := execwire.WriteRequest(&head, req); err != nil {
			return
		}
		r = io.MultiReader(&head, br)
	}
	_ = agent.Serve(context.Background(), readWriter{r: r, w: conn})
}

// readWriter reads through whatever the endpoint above assembled while
// writing straight to the connection.
type readWriter struct {
	r io.Reader
	w io.Writer
}

func (rw readWriter) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw readWriter) Write(p []byte) (int, error) { return rw.w.Write(p) }

func testConfig(t *testing.T, f *fakeCHV) guestexec.Config {
	t.Helper()
	return guestexec.Config{Socket: f.socket, User: currentAccount(t), ConnectTimeout: 5 * time.Second}
}

// currentAccount names the account this test process runs as: the agent
// switches to whichever account a request names, and switching to a
// different one needs privileges a test runner does not have.
func currentAccount(t *testing.T) string {
	t.Helper()
	f, err := os.Open("/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	uid := strconv.Itoa(os.Getuid())
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) >= 7 && fields[2] == uid {
			return fields[0]
		}
	}
	t.Fatalf("no account in /etc/passwd for uid %s", uid)
	return ""
}

// awkwardPayload is deliberately not text: NULs, every byte value, CR
// and CRLF sequences, and a trailing byte with the high bit set. Copying
// text was never the hard part -- what the old `cat` pipelines got wrong
// was exactly this.
func awkwardPayload() []byte {
	var b bytes.Buffer
	b.WriteString("first line\r\nsecond line\n")
	for i := 0; i < 300; i++ {
		for v := 0; v < 256; v++ {
			b.WriteByte(byte(v))
		}
	}
	b.WriteString("\r\n\x00\x1a")
	b.WriteByte(0xff)
	return b.Bytes()
}

// The two directions, over both a byte-transparent guest and one that
// wraps every session in a pty. The point of the whole package is that
// the caller does not have to know which of those they have, so both
// have to be asserted rather than assumed.
func TestCopy_RoundTripsAFileInBothDirections(t *testing.T) {
	payload := awkwardPayload()

	for _, tc := range []struct {
		name     string
		wrap     bool
		encoding string
	}{
		{name: "plain guest, encoding chosen by the probe", encoding: EncodeAuto},
		{name: "plain guest, base64 forced", encoding: EncodeBase64},
		{name: "plain guest, raw", encoding: EncodeRaw},
		{name: "pty-wrapped guest, encoding chosen by the probe", wrap: true, encoding: EncodeAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFakeCHV(t, &fakeCHV{wrap: tc.wrap})
			cfg := testConfig(t, f)
			dir := t.TempDir()
			guestPath := filepath.Join(dir, "payload.bin")

			var stdout, stderr bytes.Buffer
			in := Options{Direction: ToGuest, Path: guestPath, Encoding: tc.encoding}
			if err := Copy(context.Background(), cfg, in, bytes.NewReader(payload), &stdout, &stderr); err != nil {
				t.Fatalf("copy in: %v (stderr %q)", err, stderr.String())
			}
			got, err := os.ReadFile(guestPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("copy in stored %d bytes, want the %d it was given (equal: %v)", len(got), len(payload), bytes.Equal(got, payload))
			}

			var out bytes.Buffer
			stderr.Reset()
			back := Options{Direction: FromGuest, Path: guestPath, Encoding: tc.encoding}
			if err := Copy(context.Background(), cfg, back, nil, &out, &stderr); err != nil {
				t.Fatalf("copy out: %v (stderr %q)", err, stderr.String())
			}
			if !bytes.Equal(out.Bytes(), payload) {
				t.Fatalf("copy out returned %d bytes, want the %d that were stored", out.Len(), len(payload))
			}
		})
	}
}

// A guest path that starts with a dash is a file with an awkward name,
// not an option for the guest-side command: copied in and back out, it
// has to come back as the bytes that went in, through every encoding.
func TestCopy_ADashLeadingPathIsAFileNotAnOption(t *testing.T) {
	payload := awkwardPayload()
	for _, encoding := range []string{EncodeBase64, EncodeRaw} {
		t.Run(encoding, func(t *testing.T) {
			f := startFakeCHV(t, nil)
			cfg := testConfig(t, f)
			cfg.Dir = t.TempDir()

			var stderr bytes.Buffer
			in := Options{Direction: ToGuest, Path: "-n", Encoding: encoding, Mode: "0600"}
			if err := Copy(context.Background(), cfg, in, bytes.NewReader(payload), io.Discard, &stderr); err != nil {
				t.Fatalf("copy in: %v (stderr %q)", err, stderr.String())
			}
			info, err := os.Stat(filepath.Join(cfg.Dir, "-n"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want 0600", info.Mode().Perm())
			}

			var out bytes.Buffer
			stderr.Reset()
			back := Options{Direction: FromGuest, Path: "-n", Encoding: encoding}
			if err := Copy(context.Background(), cfg, back, nil, &out, &stderr); err != nil {
				t.Fatalf("copy out: %v (stderr %q)", err, stderr.String())
			}
			if !bytes.Equal(out.Bytes(), payload) {
				t.Fatalf("copy out returned %d bytes, want the %d that were stored", out.Len(), len(payload))
			}
		})
	}
}

// The point of -mode: a binary copied in is executable when the copy
// returns, rather than after a second exec session the caller has to
// remember. Asserted over a wrapped guest too, since the mode rides on
// the same command line the encoding does.
func TestCopy_SetsTheModeOfTheFileItWrites(t *testing.T) {
	payload := awkwardPayload()

	for _, tc := range []struct {
		name string
		wrap bool
		mode string
		want os.FileMode
	}{
		{name: "an executable", mode: "0755", want: 0o755},
		{name: "a private file", mode: "0600", want: 0o600},
		{name: "an executable, pty-wrapped guest", wrap: true, mode: "0755", want: 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFakeCHV(t, &fakeCHV{wrap: tc.wrap})
			cfg := testConfig(t, f)
			guestPath := filepath.Join(t.TempDir(), "app")

			var stderr bytes.Buffer
			opts := Options{Direction: ToGuest, Path: guestPath, Mode: tc.mode}
			if err := Copy(context.Background(), cfg, opts, bytes.NewReader(payload), io.Discard, &stderr); err != nil {
				t.Fatalf("copy in: %v (stderr %q)", err, stderr.String())
			}

			info, err := os.Stat(guestPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.want {
				t.Errorf("stored file is %04o, want %04o", got, tc.want)
			}
			// The chmod is appended to the same command line that does
			// the writing, so a mistake there could set the mode of a
			// file that got none of the bytes.
			got, err := os.ReadFile(guestPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("stored %d bytes, want the %d it was given", len(got), len(payload))
			}
		})
	}
}

// Dropping the mode would leave the caller an unrunnable binary and
// nothing to say why, so the three shapes that have no file at the guest
// end to chmod fail instead.
func TestCopy_RefusesAModeThatWouldGoNowhere(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)
	path := filepath.Join(t.TempDir(), "app")

	for _, tc := range []struct {
		name string
		opts Options
	}{
		{name: "a copy out", opts: Options{Direction: FromGuest, Path: path, Mode: "0755"}},
		{name: "an archive in", opts: Options{Direction: ToGuest, Path: path, Archive: true, Mode: "0755"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Copy(context.Background(), cfg, tc.opts, bytes.NewReader(nil), io.Discard, io.Discard)
			if err == nil {
				t.Fatal("the copy succeeded, silently ignoring the mode it was given")
			}
			if !strings.Contains(err.Error(), "mode") {
				t.Errorf("error %q does not mention the mode", err)
			}
		})
	}
}

// An empty file is the edge the encoder's own flush decides: base64's
// final group and the line breaker both have a "nothing to end" case.
func TestCopy_HandlesAnEmptyFile(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)
	guestPath := filepath.Join(t.TempDir(), "empty")

	if err := Copy(context.Background(), cfg, Options{Direction: ToGuest, Path: guestPath}, bytes.NewReader(nil), io.Discard, io.Discard); err != nil {
		t.Fatalf("copy in: %v", err)
	}
	got, err := os.ReadFile(guestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("stored %d bytes for an empty copy", len(got))
	}

	var out bytes.Buffer
	if err := Copy(context.Background(), cfg, Options{Direction: FromGuest, Path: guestPath}, nil, &out, io.Discard); err != nil {
		t.Fatalf("copy out: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("copy out returned %q for an empty file", out.String())
	}
}

// A directory is the shape most of the hand-built recipes took, and a
// tar on each side is the only way it fits down one stream. Out and back
// in, so the two halves are checked against each other rather than
// against an archive this test built itself.
func TestCopy_RoundTripsADirectoryAsTar(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)

	src := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"top.txt":           []byte("hello\n"),
		"nested/binary.bin": awkwardPayload(),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(src, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var archive, stderr bytes.Buffer
	out := Options{Direction: FromGuest, Path: src, Archive: true}
	if err := Copy(context.Background(), cfg, out, nil, &archive, &stderr); err != nil {
		t.Fatalf("copy out: %v (stderr %q)", err, stderr.String())
	}
	if archive.Len() == 0 {
		t.Fatal("copy out produced an empty archive")
	}

	// A directory that does not exist yet: unpacking into one is a copy,
	// not an error the caller has to go and fix with a separate exec.
	dst := filepath.Join(t.TempDir(), "restored", "tree")
	stderr.Reset()
	in := Options{Direction: ToGuest, Path: dst, Archive: true}
	if err := Copy(context.Background(), cfg, in, bytes.NewReader(archive.Bytes()), io.Discard, &stderr); err != nil {
		t.Fatalf("copy in: %v (stderr %q)", err, stderr.String())
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s came back as %d bytes, want %d", name, len(got), len(want))
		}
	}
}

// The guest's own complaint has to reach the caller as the message it
// was: an exit code alone leaves "no such file" indistinguishable from a
// broken transport.
func TestCopy_ReportsAGuestSideFailure(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)

	var out, stderr bytes.Buffer
	err := Copy(context.Background(), cfg, Options{Direction: FromGuest, Path: "/nonexistent/nope.bin"}, nil, &out, &stderr)
	if err == nil {
		t.Fatal("copying a file that does not exist in the guest succeeded")
	}
	if stderr.Len() == 0 {
		t.Errorf("nothing reached stderr; the guest's own message is what says why (error was %v)", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout got %q for a copy that failed", out.String())
	}
}

// Packing a directory out is the one command line that is a pipeline
// with its failing stage first: `tar -cf - -C dir . | base64`. A shell
// reports the last stage's status, and `base64` succeeds on the empty
// input a `tar` that could not open its directory leaves it -- so this
// used to write an empty archive and exit 0, which is a backup script
// happily replacing yesterday's copy with nothing.
//
// Asserted over the pty-wrapping guest too, since that is where the
// encoding (and so the pipeline) is not optional.
func TestCopy_AFailedPackIsNotAnEmptyArchiveAndSuccess(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		name := "byte-transparent guest"
		if wrapped {
			name = "pty-wrapped guest"
		}
		t.Run(name, func(t *testing.T) {
			f := startFakeCHV(t, &fakeCHV{wrap: wrapped})
			cfg := testConfig(t, f)
			missing := filepath.Join(t.TempDir(), "never-created")

			var archive, stderr bytes.Buffer
			opts := Options{Direction: FromGuest, Path: missing, Archive: true, Encoding: EncodeBase64}
			err := Copy(context.Background(), cfg, opts, nil, &archive, &stderr)
			if err == nil {
				t.Fatalf("packing a directory that does not exist succeeded, writing %d bytes (guest said %q)", archive.Len(), stderr.String())
			}
			// Only over a byte-transparent guest: a pty folds stderr
			// into stdout, which is the whole reason the payload is
			// encoded, and tar's complaint goes wherever the wrapper
			// put it.
			if !wrapped && stderr.Len() == 0 {
				t.Errorf("nothing reached stderr; tar's own message is what says why (error was %v)", err)
			}
		})
	}
}

// The other half of the same guard: the status carried out of the
// pipeline has to be `tar`'s own when it fails, and still zero when
// nothing did. TestCopy_RoundTripsADirectoryAsTar covers the payload;
// this covers a pack whose exit status has to survive the plumbing.
func TestCopy_APackThatWorksStillReportsSuccess(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "only.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var archive, stderr bytes.Buffer
	opts := Options{Direction: FromGuest, Path: src, Archive: true, Encoding: EncodeBase64}
	if err := Copy(context.Background(), cfg, opts, nil, &archive, &stderr); err != nil {
		t.Fatalf("packing a directory that exists failed: %v (stderr %q)", err, stderr.String())
	}
	if !bytes.Contains(archive.Bytes(), []byte("only.txt")) {
		t.Errorf("the archive does not hold the file that was in the directory (%d bytes)", archive.Len())
	}
}

// A directory where a file was meant fails on the way in with nothing
// about it -- `cat > /some/dir` never mentions tar -- so the guest-side
// guard says which flag would have copied it.
func TestCopy_RefusesADirectoryWithoutTar(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)
	dir := t.TempDir()

	for _, dirn := range []Direction{ToGuest, FromGuest} {
		var stderr bytes.Buffer
		err := Copy(context.Background(), cfg, Options{Direction: dirn, Path: dir}, bytes.NewReader([]byte("x")), io.Discard, &stderr)
		if err == nil {
			t.Fatalf("direction %d: copying a directory without -tar succeeded", dirn)
		}
		if !strings.Contains(stderr.String(), "-tar") {
			t.Errorf("direction %d: stderr %q does not name -tar as the way to copy a directory", dirn, stderr.String())
		}
	}
}

// A local read that fails partway must not look like a file that ended
// there: guestexec's stdin pump treats any read error as end-of-input,
// so without the check in copyToGuest a truncated copy exits 0 and says
// nothing.
func TestCopy_TruncatedSourceIsNotASilentSuccess(t *testing.T) {
	f := startFakeCHV(t, nil)
	cfg := testConfig(t, f)
	guestPath := filepath.Join(t.TempDir(), "truncated.bin")

	src := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("a"), 4096)), errReader{errors.New("disk went away")})
	err := Copy(context.Background(), cfg, Options{Direction: ToGuest, Path: guestPath}, src, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("a source that failed mid-stream was reported as a successful copy")
	}
	if !strings.Contains(err.Error(), "disk went away") {
		t.Errorf("error %q does not say what actually failed", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// The probe is what makes -encode auto more than a guess, so what it
// concludes is worth asserting directly rather than only through a copy
// that happens to work.
func TestChooseEncoding_ProbesTheGuest(t *testing.T) {
	if _, err := exec.LookPath("base64"); err != nil {
		t.Skip("this build machine has no base64, which is the case being asserted against")
	}

	for _, tc := range []struct {
		name        string
		wrap        bool
		wantWrapped bool
	}{
		{name: "plain guest", wantWrapped: false},
		{name: "pty-wrapped guest", wrap: true, wantWrapped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFakeCHV(t, &fakeCHV{wrap: tc.wrap})
			encoding, wrapped, err := chooseEncoding(context.Background(), testConfig(t, f))
			if err != nil {
				t.Fatal(err)
			}
			if encoding != EncodeBase64 {
				t.Errorf("encoding = %q, want %q for a guest that has base64", encoding, EncodeBase64)
			}
			if wrapped != tc.wantWrapped {
				t.Errorf("wrapped = %v, want %v -- the probe's own newline is what detects it", wrapped, tc.wantWrapped)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want Options
	}{
		{
			name: "into the guest",
			args: []string{"-", "/srv/app.tar"},
			want: Options{Direction: ToGuest, Path: "/srv/app.tar", Encoding: EncodeAuto},
		},
		{
			name: "out of the guest",
			args: []string{"/srv/app.tar", "-"},
			want: Options{Direction: FromGuest, Path: "/srv/app.tar", Encoding: EncodeAuto},
		},
		{
			name: "a directory in",
			args: []string{"-tar", "-", "/srv/app"},
			want: Options{Direction: ToGuest, Path: "/srv/app", Archive: true, Encoding: EncodeAuto},
		},
		{
			name: "an explicit encoding",
			args: []string{"-encode", "raw", "/srv/app.tar", "-"},
			want: Options{Direction: FromGuest, Path: "/srv/app.tar", Encoding: EncodeRaw},
		},
		{
			name: "a binary in",
			args: []string{"-mode", "0755", "-", "/usr/local/bin/app"},
			want: Options{Direction: ToGuest, Path: "/usr/local/bin/app", Encoding: EncodeAuto, Mode: "0755"},
		},
		{
			// Octal either way round, and normalised, since what comes
			// out of here is rendered into the guest's command line.
			name: "a mode without its leading zero",
			args: []string{"-mode", "755", "-", "/usr/local/bin/app"},
			want: Options{Direction: ToGuest, Path: "/usr/local/bin/app", Encoding: EncodeAuto, Mode: "0755"},
		},
		{
			name: "a setuid mode",
			args: []string{"-mode", "4755", "-", "/usr/local/bin/app"},
			want: Options{Direction: ToGuest, Path: "/usr/local/bin/app", Encoding: EncodeAuto, Mode: "4755"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("ParseArgs(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseArgs_Rejections(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{name: "no paths", args: nil, says: "exactly two paths"},
		{name: "one path", args: []string{"/srv/app.tar"}, says: "exactly two paths"},
		{name: "three paths", args: []string{"-", "/a", "/b"}, says: "exactly two paths"},
		{name: "neither side is a stream", args: []string{"/a", "/b"}, says: `has to be "-"`},
		{name: "both sides are a stream", args: []string{"-", "-"}, says: "neither of them names anything in the guest"},
		{name: "an unknown encoding", args: []string{"-encode", "rot13", "-", "/a"}, says: "-encode"},
		{name: "an unknown flag", args: []string{"-recursive", "-", "/a"}, says: "recursive"},
		{name: "a mode that is not octal", args: []string{"-mode", "u+x", "-", "/a"}, says: "-mode"},
		{name: "a decimal mode", args: []string{"-mode", "0999", "-", "/a"}, says: "-mode"},
		{name: "a mode with more bits than a mode has", args: []string{"-mode", "77777", "-", "/a"}, says: "-mode"},
		{name: "a mode on a copy out", args: []string{"-mode", "0755", "/a", "-"}, says: "no file to chmod"},
		{name: "a mode on an archive", args: []string{"-mode", "0755", "-tar", "-", "/a"}, says: "its own mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArgs(tc.args)
			if err == nil {
				t.Fatalf("ParseArgs(%q) succeeded", tc.args)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error %q does not mention %q", err, tc.says)
			}
		})
	}
}

// "granule cp -h" is a question, and cmd/granule-shim answers it on stdout with
// a zero status rather than as a fatal log line prefixed "granule: cp:".
// It tells the two apart by unwrapping to flag.ErrHelp, so an error
// here that stopped wrapping it would quietly put that answer back on
// the failure path -- with nothing in this package failing to say so.
func TestParseArgs_HelpIsDistinguishableFromAMistake(t *testing.T) {
	for _, arg := range []string{"-h", "-help", "--help"} {
		_, err := ParseArgs([]string{arg})
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("ParseArgs(%q) error = %v, want one wrapping flag.ErrHelp", arg, err)
			continue
		}
		// The flag package's own dump is a list of flags with no word
		// about the "-" that makes the whole command work, hence
		// carrying the synopsis with it.
		if !strings.Contains(err.Error(), Usage) {
			t.Errorf("ParseArgs(%q) error = %v, want it to carry the usage", arg, err)
		}
	}
	if _, err := ParseArgs([]string{"-recursive", "-", "/a"}); errors.Is(err, flag.ErrHelp) {
		t.Error("an unknown flag was reported as a help request, which would exit 0 and copy nothing")
	}
}

// The guest-side command line is where a quoting mistake turns into an
// arbitrary shell command, so a path with a quote in it is checked
// rather than trusted.
func TestCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     Options
		encoding string
		want     string
	}{
		{
			name:     "file in, encoded",
			opts:     Options{Direction: ToGuest, Path: "/srv/app.tar"},
			encoding: EncodeBase64,
			want:     "if [ -d '/srv/app.tar' ]; then echo 'granule cp: /srv/app.tar is a directory: name the file to write, or pass -tar to unpack an archive into it' >&2; exit 2; fi; exec base64 -d > '/srv/app.tar'",
		},
		{
			// The `exec` is gone here on purpose: it would replace the
			// shell that still has the chmod to run.
			name:     "file in with a mode, encoded",
			opts:     Options{Direction: ToGuest, Path: "/usr/local/bin/app", Mode: "0755"},
			encoding: EncodeBase64,
			want:     "if [ -d '/usr/local/bin/app' ]; then echo 'granule cp: /usr/local/bin/app is a directory: name the file to write, or pass -tar to unpack an archive into it' >&2; exit 2; fi; base64 -d > '/usr/local/bin/app' && chmod '0755' '/usr/local/bin/app'",
		},
		{
			name:     "file in with a mode, raw",
			opts:     Options{Direction: ToGuest, Path: "/usr/local/bin/app", Mode: "0755"},
			encoding: EncodeRaw,
			want:     "if [ -d '/usr/local/bin/app' ]; then echo 'granule cp: /usr/local/bin/app is a directory: name the file to write, or pass -tar to unpack an archive into it' >&2; exit 2; fi; cat > '/usr/local/bin/app' && chmod '0755' '/usr/local/bin/app'",
		},
		{
			name:     "file out, raw",
			opts:     Options{Direction: FromGuest, Path: "/srv/app.tar"},
			encoding: EncodeRaw,
			want:     "if [ -d '/srv/app.tar' ]; then echo 'granule cp: /srv/app.tar is a directory: pass -tar to copy it as an archive' >&2; exit 2; fi; exec cat '/srv/app.tar'",
		},
		{
			name:     "directory in, encoded",
			opts:     Options{Direction: ToGuest, Path: "/srv/app", Archive: true},
			encoding: EncodeBase64,
			want:     "mkdir -p '/srv/app' && base64 -d | tar -xf - -C '/srv/app'",
		},
		{
			// The one pipeline whose failing stage is not the last, so
			// the status has to be carried out by hand -- see pipeline.
			name:     "directory out, encoded",
			opts:     Options{Direction: FromGuest, Path: "/srv/app", Archive: true},
			encoding: EncodeBase64,
			want:     `exec 4>&1; st=$( { { tar -cf - -C '/srv/app' .; echo $? >&3; } | base64 >&4; } 3>&1 ); rest=$?; if [ "$st" -ne 0 ]; then exit "$st"; fi; exit "$rest"`,
		},
		{
			name:     "directory out, raw",
			opts:     Options{Direction: FromGuest, Path: "/srv/app", Archive: true},
			encoding: EncodeRaw,
			want:     "tar -cf - -C '/srv/app' .",
		},
		{
			name:     "a path that would otherwise end the quoting",
			opts:     Options{Direction: ToGuest, Path: "/srv/it's here", Archive: true},
			encoding: EncodeRaw,
			want:     `mkdir -p '/srv/it'\''s here' && tar -xf - -C '/srv/it'\''s here'`,
		},
		{
			// A relative path that looks like an option is still a path:
			// "./" in front keeps cat, chmod and friends from reading it
			// as one, while the message still names what was asked for.
			name:     "a relative path that starts with a dash, in",
			opts:     Options{Direction: ToGuest, Path: "-n", Mode: "0644"},
			encoding: EncodeRaw,
			want:     "if [ -d './-n' ]; then echo 'granule cp: -n is a directory: name the file to write, or pass -tar to unpack an archive into it' >&2; exit 2; fi; cat > './-n' && chmod '0644' './-n'",
		},
		{
			name:     "a relative path that starts with a dash, out",
			opts:     Options{Direction: FromGuest, Path: "--version"},
			encoding: EncodeBase64,
			want:     "if [ -d './--version' ]; then echo 'granule cp: --version is a directory: pass -tar to copy it as an archive' >&2; exit 2; fi; exec base64 './--version'",
		},
		{
			name:     "a relative directory that starts with a dash",
			opts:     Options{Direction: ToGuest, Path: "-p", Archive: true},
			encoding: EncodeRaw,
			want:     "mkdir -p './-p' && tar -xf - -C './-p'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandLine(tc.opts, tc.encoding); got != tc.want {
				t.Errorf("commandLine =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// The wrapping is not cosmetic: a pty in canonical mode drops whatever a
// line holds past 4096 bytes, which is a copy that fails on exactly the
// guests the encoding exists to survive.
func TestEncodeStream_WrapsAndRoundTrips(t *testing.T) {
	payload := awkwardPayload()

	var encoded bytes.Buffer
	if err := encodeStream(&encoded, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSuffix(encoded.String(), "\n"), "\n") {
		if len(line) > b64LineWidth {
			t.Fatalf("line %d is %d bytes, past the %d-byte wrap", i, len(line), b64LineWidth)
		}
	}
	if !strings.HasSuffix(encoded.String(), "\n") {
		t.Error("the encoded stream does not end on a line boundary, which is where an EOT has to land")
	}

	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, &whitespaceStripper{r: &encoded}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded %d bytes, want the %d encoded", len(decoded), len(payload))
	}
}

// What comes back off a wrapped session is the guest's own base64 with
// CRLF where it wrote LF -- and Go's decoder does not ignore anything
// but LF and CR on its own, so the stripping is what makes the decode
// work at all when a wrapper pads with spaces or tabs.
func TestWhitespaceStripper_DropsWhatAPtyAdds(t *testing.T) {
	payload := awkwardPayload()
	clean := base64.StdEncoding.EncodeToString(payload)

	var noisy strings.Builder
	for i, c := range clean {
		if i > 0 && i%40 == 0 {
			noisy.WriteString(" \t\r\n")
		}
		noisy.WriteRune(c)
	}
	noisy.WriteString("\r\n")

	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, &whitespaceStripper{r: strings.NewReader(noisy.String())}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded %d bytes, want %d", len(decoded), len(payload))
	}
}

func TestWhitespaceStripper_PassesTheErrorOn(t *testing.T) {
	want := errors.New("connection went away")
	r := &whitespaceStripper{r: io.MultiReader(strings.NewReader(" \n\t"), errReader{want})}
	if _, err := io.ReadAll(r); !errors.Is(err, want) {
		t.Fatalf("read error = %v, want %v", err, want)
	}
}
