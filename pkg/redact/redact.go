// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package redact removes credential material from text before it leaves
// the process holding it: the journal lines the grain-log capability
// serves (pkg/capability/grainlog), the output of a host sandbox's
// run_command, which carries the operator's own -host-sandbox-env
// (pkg/mcp, GSA1-12), and a run's transcript before it is stored
// (pkg/orchestrator). One package, so that every one of those -- and any
// stream of run events added later -- cuts the same shapes the same way
// rather than each keeping a list of its own that drifts.
package redact

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Redactor removes credential material from lines of text before they
// leave this host. What follows was written for its first caller, the
// grain-log capability's journal, and holds for every other one.
//
// This is the load-bearing half of the whole capability, and where it
// runs is the point. The journal carries whatever the daemon and every
// agent wrote to it, and a run that reads it carries what it read into a
// model's context off this host and into a sandbox with unrestricted
// outbound network. Neither of those can be taken back, and neither can
// be made safe from inside the sandbox: redaction the sandbox performs
// is redaction performed after the material has already arrived. So it
// happens here, in the process that reads journalctl, between the read
// and the answer -- the only place where "this never left the host" is a
// statement about the world rather than about an agent's good behaviour.
//
// It works two ways at once, because neither is enough alone:
//
//   - Known values: the material this host actually holds, handed in by
//     the caller that can read it (cmd/grain's grainLogRedactor reads
//     grain's own secret store and the git proxy's credential ladder).
//     This catches a secret whose *shape* is nothing in particular -- a
//     webhook secret, a password, an operator's own pasted string --
//     which no pattern could ever recognize.
//   - Shapes: Patterns below. This catches material this host does not
//     hold and cannot enumerate: a token another deployment logged into
//     a repository's CI output that an error message then quoted, an
//     Authorization header from a request grain made to somebody else, a
//     credential minted after this process started.
//
// What it cannot do is worth writing down, because a reviewer should be
// able to see the edge of it: apart from a PEM private key's body (see
// Redact), it works one line at a time, so material split across two
// journal entries is not matched; it cannot recognize a
// shapeless secret this host does not hold; and a value shorter than
// minKnownValue is left alone deliberately (see that constant). None of
// those is an argument against redacting -- they are the reason this
// capability is a grant a human attaches deliberately rather than
// something every run has.
type Redactor struct {
	// known is the caller's values, longest first so that a value which
	// contains another is replaced whole rather than leaving its tail
	// behind after the shorter one has been cut out of the middle of it.
	known []KnownValue
}

// KnownValue is one piece of credential material this host holds, and
// what to call it in the redacted text. The label is deliberately not
// secret -- it names the secret, never its value -- and saying which one
// was cut is what stops a run from reading "[redacted]" as noise and
// going looking for the real thing.
type KnownValue struct {
	Label string
	Value string
}

// minKnownValue is the shortest known value redacted literally.
//
// A four-character secret cannot be cut out of a log without also
// cutting those four characters out of every unrelated word that happens
// to contain them, which would leave the run reading a mangled journal
// and still would not have protected anything worth protecting. So short
// values are left as they are, and the shape patterns are what stands
// behind them. A deployment whose secrets are that short has a problem
// this package cannot fix.
const minKnownValue = 8

// New returns a Redactor over these known values plus the shape
// patterns, which are always on. A nil or empty list is a legitimate
// caller -- a host whose secret store holds nothing yet -- and gets the
// patterns alone; it is not the same thing as having failed to read the
// store, which is the caller's to refuse over (grainlog's Config.Redactor
// nil, and cmd/grain's own warning).
func New(known []KnownValue) *Redactor {
	kept := make([]KnownValue, 0, len(known))
	for _, v := range known {
		if len(v.Value) < minKnownValue || strings.TrimSpace(v.Value) == "" {
			continue
		}
		kept = append(kept, v)
	}
	sort.SliceStable(kept, func(i, j int) bool { return len(kept[i].Value) > len(kept[j].Value) })
	return &Redactor{known: kept}
}

// KnownCount reports how many known values this Redactor is holding, for
// the audit line -- never which, and never their values.
func (r *Redactor) KnownCount() int {
	if r == nil {
		return 0
	}
	return len(r.known)
}

// Redact returns the lines with every known value and every recognized
// credential shape replaced, and how many replacements it made. The
// count is reported to the run and written to the audit line: an
// operator reading "47 values redacted" learns something about what the
// journal is carrying that the redacted text itself cannot tell them.
//
// Unlike Line, it sees consecutive lines, so it also cuts a PEM private
// key's body: once a line opens a private key block, every line after it
// is replaced up to and including the one that closes it -- the key
// material itself, which no single-line shape can recognize -- bounded
// by maxPEMBlockLines so that a block whose END never arrives (a
// truncated write, a tail that ends mid-key) cannot swallow the rest of
// the log.
func (r *Redactor) Redact(lines []string) ([]string, int) {
	out := make([]string, len(lines))
	total := 0
	inKey := 0
	for i, line := range lines {
		if inKey > 0 {
			out[i] = "[redacted: private key -- this entry's whole line]"
			total++
			inKey--
			if pemEnd.MatchString(line) {
				inKey = 0
			}
			continue
		}
		redacted, n := r.Line(line)
		out[i] = redacted
		total += n
		if pemBegin.MatchString(line) && !pemEnd.MatchString(line) {
			inKey = maxPEMBlockLines
		}
	}
	return out, total
}

// pemBegin and pemEnd bracket a private key block. Only private keys:
// a certificate or a public key is not secret, and cutting one would
// redact the evidence of a TLS failure along with nothing worth hiding.
var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
)

// maxPEMBlockLines is how many lines after a private key's BEGIN line
// Redact will cut while waiting for its END. A 4096-bit RSA key is about
// fifty lines of base64; this leaves room for larger keys and for a
// journal's own wrapping without letting one unterminated header eat an
// entire tail.
const maxPEMBlockLines = 200

// Line redacts one line: known values first, then shapes. Known values
// first because a value this host holds is redacted under its own name
// ("[redacted: secret claude-oauth-token]"), which is more use to a run
// than the shape it happens to match.
func (r *Redactor) Line(line string) (string, int) {
	n := 0
	if r != nil {
		for _, v := range r.known {
			count := strings.Count(line, v.Value)
			if count == 0 {
				continue
			}
			line = strings.ReplaceAll(line, v.Value, fmt.Sprintf("[redacted: %s]", v.Label))
			n += count
		}
	}
	redacted, shapes := redactShapes(line)
	return redacted, n + shapes
}

// pattern is one credential shape and what replaces it. The replacement
// is a regexp expansion, so a pattern that keeps a prefix -- the
// "Authorization:" itself, the scheme of a URL -- writes ${1} for it and
// the line still reads as the line it was.
type pattern struct {
	label string
	re    *regexp.Regexp
	with  string
}

// wholeLine is the marker a pattern's `with` carries when what matched
// means the entire line has to go rather than the match alone: a PEM
// private key's own header, whose following lines are the key itself and
// whose line has nothing else on it worth keeping.
const wholeLine = "\x00whole-line"

// patterns are the credential shapes redacted regardless of whether this
// host holds the value. Each one is deliberately narrow: a pattern loose
// enough to match a git SHA or a build id would redact the very lines a
// run is reading the journal for, and a redaction that eats the evidence
// is its own kind of false clean.
var patterns = []pattern{
	// Whole headers first: whatever follows "Authorization:" is the
	// credential, whatever shape it happens to be in. The scheme keyword
	// is kept where there is one -- "Basic" and "Bearer" say something
	// about a failing request and are not themselves secret -- and the
	// value after it goes.
	{
		"authorization header",
		regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)((?:basic|bearer|token|digest|apikey)\s+)?\S+`),
		"${1}${2}[redacted: authorization]",
	},
	{"bearer token", regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}[redacted: bearer token]"},
	// A credential in a URL's userinfo -- the shape a git remote with a
	// token in it takes, which is exactly what an error quoting a failed
	// clone prints.
	{"url credentials", regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`), "${1}[redacted: url credentials]@"},
	// GitHub's own token prefixes: personal (ghp_), OAuth (gho_), user
	// (ghu_), server/installation (ghs_), refresh (ghr_), and the
	// fine-grained shape.
	{"github token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`), "[redacted: github token]"},
	{"github token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{16,}`), "[redacted: github token]"},
	// The agent frameworks' own credentials (secrets.OpenAIAPIKeySecret
	// and the Anthropic keys that share the prefix), and Google's.
	{"api key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), "[redacted: api key]"},
	{"api key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{20,}`), "[redacted: api key]"},
	// Google OAuth: the access token `gcloud auth print-access-token`
	// prints (ya29.), and the refresh token a gcloud login keeps under
	// CLOUDSDK_CONFIG (1//) -- what a host sandbox carrying its
	// operator's gcloud login hands any run that asks (GSA1-12).
	{"google oauth token", regexp.MustCompile(`\bya29\.[A-Za-z0-9._-]{20,}`), "[redacted: google oauth token]"},
	{"google oauth token", regexp.MustCompile(`(^|[^A-Za-z0-9_/])1//0[A-Za-z0-9_-]{30,}`), "${1}[redacted: google oauth token]"},
	// Other providers whose tokens announce themselves: AWS access key
	// ids, GitLab personal tokens and Slack's.
	{"aws access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[redacted: aws access key]"},
	{"gitlab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`), "[redacted: gitlab token]"},
	{"slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), "[redacted: slack token]"},
	// The private half of this deployment's own secret store
	// (pkg/secrets' key file), the one value whose loss is unrecoverable.
	{"age secret key", regexp.MustCompile(`\bAGE-SECRET-KEY-1[0-9A-Za-z]{20,}`), "[redacted: age secret key]"},
	// A token minted for a CI job's api grants (pkg/capability/ciapi),
	// and the release gate's before it (rg_).
	{"ci api token", regexp.MustCompile(`\b(?:ga|rg)_[0-9a-f]{16}_[0-9a-f]{64}\b`), "[redacted: ci api token]"},
	// A signed token, which carries its own claims and is worth cutting
	// whether or not this host minted it.
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[redacted: jwt]"},
	// A PEM block's header takes its whole line. The key material itself
	// is on the lines after it and is not matched here -- base64 with no
	// distinguishing shape, which nothing can tell from ordinary output
	// one line at a time -- so Redact, which sees the lines that follow,
	// cuts those up to the block's END. What the marker also buys a
	// reader is the announcement: a run that sees it knows a key was
	// being written to the journal, which is a fact worth reporting on
	// its own.
	{"private key", pemBegin, wholeLine},
}

func redactShapes(line string) (string, int) {
	n := 0
	for _, p := range patterns {
		matches := p.re.FindAllString(line, -1)
		if len(matches) == 0 {
			continue
		}
		n += len(matches)
		if p.with == wholeLine {
			return fmt.Sprintf("[redacted: %s -- this entry's whole line]", p.label), n
		}
		line = p.re.ReplaceAllString(line, p.with)
	}
	return line, n
}

// Text is Redact over a whole block of text -- a command's output, a
// transcript -- split on newlines and joined back the same way, so a
// private key's body is cut however many lines it spans. A nil Redactor
// still cuts the shapes, the same as Line's.
func (r *Redactor) Text(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	lines, n := r.Redact(strings.Split(text, "\n"))
	if n == 0 {
		return text, 0
	}
	return strings.Join(lines, "\n"), n
}
