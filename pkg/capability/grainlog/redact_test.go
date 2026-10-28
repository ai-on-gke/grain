package grainlog_test

// Redaction, which is the whole basis on which this capability is
// granted: what these tests hold is that a journal line carrying
// credential material does not leave this host carrying it.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
)

// A value this host actually holds is cut out and named, whatever shape
// it is in. This is the half no pattern could do: a webhook secret or a
// pasted password looks like nothing in particular, and the only reason
// it can be recognized at all is that the process doing the recognizing
// is the one holding it.
func TestRedactorCutsAValueThisHostHolds(t *testing.T) {
	r := grainlog.NewRedactor([]grainlog.KnownValue{
		{Label: "secret acme-webhook/value", Value: "correct-horse-battery-staple"},
	})

	line, n := r.Line("2026-09-07T10:00:00+0000 host grain[1]: posting with correct-horse-battery-staple ok")
	if strings.Contains(line, "correct-horse-battery-staple") {
		t.Fatalf("the secret survived redaction: %q", line)
	}
	if n != 1 {
		t.Errorf("redacted %d values, want 1", n)
	}
	if !strings.Contains(line, "secret acme-webhook/value") {
		t.Errorf("the redaction does not say what was cut, so a run reads it as noise: %q", line)
	}
	// And what was around it is still there, because a run reading this
	// is reading it for the rest of the line.
	if !strings.Contains(line, "posting with") || !strings.Contains(line, "ok") {
		t.Errorf("redaction ate the line around the secret: %q", line)
	}
}

// Every occurrence, not just the first: a line that repeats a token
// twice leaks it just as thoroughly as one that prints it once.
func TestRedactorCutsEveryOccurrence(t *testing.T) {
	r := grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret one", Value: "s3cret-value-here"}})

	line, n := r.Line("tried s3cret-value-here, retried s3cret-value-here")
	if strings.Contains(line, "s3cret-value-here") {
		t.Fatalf("a repeated secret survived: %q", line)
	}
	if n != 2 {
		t.Errorf("counted %d redactions, want 2", n)
	}
}

// A value that contains another is redacted whole. Cutting the shorter
// one first would leave the longer one's head and tail in the line,
// which is a leak that looks like a redaction.
func TestRedactorCutsTheLongestValueFirst(t *testing.T) {
	r := grainlog.NewRedactor([]grainlog.KnownValue{
		{Label: "short", Value: "abcdefgh"},
		{Label: "long", Value: "abcdefgh-and-then-some-more"},
	})

	line, _ := r.Line("token abcdefgh-and-then-some-more used")
	if strings.Contains(line, "and-then-some-more") {
		t.Fatalf("the longer value was left half in the line: %q", line)
	}
}

// The shapes, which are what stands behind the known values: material
// this host does not hold and could never enumerate -- a token another
// deployment logged, a header from a request grain made to somebody
// else, a credential minted after this process started.
func TestRedactorCutsCredentialShapesItWasNeverToldAbout(t *testing.T) {
	r := grainlog.NewRedactor(nil)

	for _, tc := range []struct{ name, line, leaked string }{
		{"github pat", "clone failed for ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789", "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"},
		{"fine-grained pat", "token github_pat_11ABCDEFG0abcdefghijklmnop expired", "github_pat_11ABCDEFG0abcdefghijklmnop"},
		{"installation token", "proxy minted ghs_0123456789abcdefghijABCDEF", "ghs_0123456789abcdefghijABCDEF"},
		{"authorization header", `GET /x Authorization: Basic dXNlcjpwYXNzd29yZA== 401`, "dXNlcjpwYXNzd29yZA=="},
		{"bearer", "calling api with Bearer sometokenvaluehere12345", "sometokenvaluehere12345"},
		{"openai key", "OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuv set", "sk-proj-abcdefghijklmnopqrstuv"},
		{"google key", "key AIzaSyA0123456789abcdefghijklmnopqrstu rejected", "AIzaSyA0123456789abcdefghijklmnopqrstu"},
		{"age key", "read AGE-SECRET-KEY-1QQPQRSTUVWXYZ0123456789ABCDEFGH from disk", "AGE-SECRET-KEY-1QQPQRSTUVWXYZ0123456789ABCDEFGH"},
		{"jwt", "id_token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NX0.dBjftJeZ4CVPmB92K27uhbUJU1p1r wrong", "eyJhbGciOiJIUzI1NiJ9"},
		{"url credentials", "fatal: could not read https://grain:ghp_secretvalue@github.com/acme/x", "ghp_secretvalue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, n := r.Line(tc.line)
			if strings.Contains(line, tc.leaked) {
				t.Fatalf("%s survived redaction: %q", tc.name, line)
			}
			if n == 0 {
				t.Errorf("nothing was counted as redacted, but something was cut: %q", line)
			}
		})
	}
}

// A PEM header takes its whole line: what follows on it is key material
// and nothing on it is worth keeping. The lines after it are not matched
// -- base64 with no distinguishing shape -- which is a limit the package
// states rather than papers over, and the marker is what tells a run to
// go and look at why a key was being printed at all.
func TestRedactorCutsAPrivateKeyHeaderLineWhole(t *testing.T) {
	r := grainlog.NewRedactor(nil)

	line, n := r.Line("grain[1]: writing -----BEGIN RSA PRIVATE KEY----- to disk")
	if strings.Contains(line, "PRIVATE KEY") || strings.Contains(line, "writing") {
		t.Fatalf("the line was not replaced whole: %q", line)
	}
	if n == 0 {
		t.Error("the private key line was not counted as a redaction")
	}
}

// Redact sees consecutive lines, so a private key's body -- the lines
// Line alone cannot tell from ordinary output -- goes with its header, up
// to and including the END line, and the log resumes after it.
func TestRedactorCutsAPrivateKeyBlockWhole(t *testing.T) {
	r := grainlog.NewRedactor(nil)

	got, n := r.Redact([]string{
		"grain[1]: -----BEGIN RSA PRIVATE KEY-----",
		"MIIEowIBAAKCAQEAsecretkeymaterialline1",
		"MIIEowIBAAKCAQEAsecretkeymaterialline2",
		"-----END RSA PRIVATE KEY-----",
		"grain[1]: after the key",
	})
	for _, line := range got[:4] {
		if strings.Contains(line, "MIIE") || strings.Contains(line, "PRIVATE KEY-----") {
			t.Errorf("key material survived: %q", line)
		}
	}
	if got[4] != "grain[1]: after the key" {
		t.Errorf("the line after the block was cut: %q", got[4])
	}
	if n != 4 {
		t.Errorf("counted %d redactions, want 4", n)
	}
}

// A certificate is not secret, and a header with no END must not swallow
// the whole tail.
func TestRedactorBoundsAnUnterminatedPrivateKeyBlock(t *testing.T) {
	r := grainlog.NewRedactor(nil)

	cert := []string{"-----BEGIN CERTIFICATE-----", "MIIBcertbody", "-----END CERTIFICATE-----"}
	if got, _ := r.Redact(cert); got[1] != "MIIBcertbody" {
		t.Errorf("a certificate body was cut: %q", got[1])
	}

	lines := []string{"-----BEGIN PRIVATE KEY-----"}
	for i := 0; i < 1000; i++ {
		lines = append(lines, "ordinary line")
	}
	got, _ := r.Redact(lines)
	if got[len(got)-1] != "ordinary line" {
		t.Errorf("an unterminated block swallowed the whole tail")
	}
}

// An ordinary journal line comes through untouched. A redactor loose
// enough to eat a commit SHA, a task id or a duration would take the
// evidence with the secret, which is its own kind of false clean.
func TestRedactorLeavesOrdinaryLinesAlone(t *testing.T) {
	r := grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret one", Value: "s3cret-value-here"}})

	for _, line := range []string{
		"2026-09-07T10:00:00+0000 host grain[1]: orchestrator: filed task 226 to check the upgrade to main",
		"2026-09-07T10:00:01+0000 host grain[1]: upgrade: built ad19958617bd5a0c2f5a1b0e9c8d7e6f5a4b3c2d in 41.2s",
		"2026-09-07T10:00:02+0000 host grain[1]: gitproxy: GET /acme/widgets/info/refs 200 in 84ms",
	} {
		got, n := r.Line(line)
		if got != line || n != 0 {
			t.Errorf("an ordinary line was redacted (%d cuts):\nbefore: %s\nafter:  %s", n, line, got)
		}
	}
}

// A value too short to redact without mangling every line that happens
// to contain those characters is left alone deliberately -- see
// minKnownValue. The test exists so that the decision is visible rather
// than discovered.
func TestRedactorIgnoresValuesTooShortToCutSafely(t *testing.T) {
	r := grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret tiny", Value: "abc"}})

	if got := r.KnownCount(); got != 0 {
		t.Fatalf("KnownCount = %d, want the too-short value dropped", got)
	}
	line, n := r.Line("abc is in absconded and abcess and every other word")
	if n != 0 || !strings.Contains(line, "absconded") {
		t.Errorf("a three-character value mangled the line: %q", line)
	}
}

// Redact reports what it did over a whole slice, which is what the tool
// tells the run and what the audit line records.
func TestRedactCountsAcrossLines(t *testing.T) {
	r := grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret one", Value: "s3cret-value-here"}})

	lines, n := r.Redact([]string{"nothing here", "a s3cret-value-here", "and ghp_AbCdEfGhIjKlMnOpQrStUvWx again"})
	if n != 2 {
		t.Errorf("Redact counted %d, want 2", n)
	}
	if len(lines) != 3 || lines[0] != "nothing here" {
		t.Errorf("Redact = %q, want three lines with the first untouched", lines)
	}
}
