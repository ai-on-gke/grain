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

package gitproxy

// A push the host rejected a ref of, with the fix its ambient source
// knows added to what the pusher sees.
//
// A host answers a push it refuses part of with a 200: receive-pack's
// report-status, "ng <ref> <reason>" for each ref it would not move, and
// often the whole story as "remote:" text on the progress band. None of
// that is a refusal status, so refusals.go never sees it and the text
// went through as the host wrote it -- which, for a login without the
// `workflow` scope, names the scope and not the command that adds it,
// nor the account to run it as. AmbientCommandSource.PushRefusedHint is
// the source's answer to that text; this file reads the report as it
// streams past and, when a ref was rejected and the source has something
// to say, adds it as more "remote:" lines before the final flush -- or,
// to a client that asked for no progress band, at the end of the ng
// line, which git prints in parentheses after the ref. Every other byte
// the host sent still reaches the pusher, in order and as it arrives:
// only the flush that ends the answer is held while the hint is asked
// for.
//
// A grain-made push through the proxy (an auto-port, portpick) is a git
// client like any other, and its error carries git's stderr, so the same
// lines reach the task conversation from there.

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// pushMessageLimit bounds how much of the host's own text is kept to ask
// the hint about; the text itself is passed through whatever its length.
const pushMessageLimit = 64 << 10

// WorkflowScopeRefused reports whether message is a GitHub host refusing
// a push that creates or updates a file under .github/workflows because
// the OAuth token it came with has no `workflow` scope:
//
//	refusing to allow an OAuth App to create or update workflow
//	`.github/workflows/ci.yml` without `workflow` scope
//
// A GitHub App's refusal ("without `workflows` permission") is not one:
// no scope fixes that, only the App's own permissions.
func WorkflowScopeRefused(message string) bool {
	msg := strings.ToLower(message)
	return strings.Contains(msg, "refusing to allow") && strings.Contains(msg, "`workflow` scope")
}

// PushRefusedHint is what credential's ambient source adds to a push
// its host rejected with message, or "" when it has nothing to say: the
// credential is not ambient, its source has no PushRefusedHint, or the
// hint does not recognise message.
func PushRefusedHint(credential Credential, message string) string {
	if credential.Ambient == nil {
		return ""
	}
	host := hostName(credential.Ambient.Host)
	if host == "" {
		host = model.DefaultGitHost
	}
	src, ok := resolvedSource(host, credential.Ambient.From)
	if !ok || src.PushRefusedHint == nil {
		return ""
	}
	return strings.TrimSpace(src.PushRefusedHint(host, message))
}

// withPushRefusedHint is upstream, a receive-pack answer to a push sent
// under credential, with the source's hint added should a ref turn out
// to be rejected. A credential whose source has no hint to give gets
// upstream back untouched.
func withPushRefusedHint(credential Credential, upstream UpstreamResponse) UpstreamResponse {
	if credential.Ambient == nil {
		return upstream
	}
	host := hostName(credential.Ambient.Host)
	if host == "" {
		host = model.DefaultGitHost
	}
	if src, ok := resolvedSource(host, credential.Ambient.From); !ok || src.PushRefusedHint == nil {
		return upstream
	}
	var body io.ReadCloser = upstream.Stream
	if body == nil {
		body = io.NopCloser(bytes.NewReader(upstream.Body))
	}
	upstream.Stream = &pushResultFilter{
		src:  body,
		in:   bufio.NewReader(body),
		hint: func(message string) string { return PushRefusedHint(credential, message) },
	}
	upstream.Body = nil
	return upstream
}

// pushResultFilter passes a receive-pack answer through pkt-line by
// pkt-line, noting the refs it rejects and the text it prints. Anything
// it cannot read as pkt-lines it passes on as it is, and stops looking.
type pushResultFilter struct {
	src  io.Closer
	in   *bufio.Reader
	hint func(message string) string

	out      bytes.Buffer
	done     bool
	decided  bool
	sideband bool
	report   []byte // band 1 bytes not yet read as whole pkt-lines
	rejected bool
	message  strings.Builder
}

func (f *pushResultFilter) Read(p []byte) (int, error) {
	for f.out.Len() == 0 {
		if f.done {
			return f.in.Read(p)
		}
		if err := f.step(); err != nil {
			if f.out.Len() == 0 {
				return 0, err
			}
			f.done = true
		}
	}
	return f.out.Read(p)
}

func (f *pushResultFilter) Close() error { return f.src.Close() }

// step moves one pkt-line from in to out. It returns in's error once
// nothing more can be read, with whatever was read of a short line
// already in out.
func (f *pushResultFilter) step() error {
	var head [4]byte
	n, err := io.ReadFull(f.in, head[:])
	f.out.Write(head[:n])
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return err
	}
	size, perr := strconv.ParseUint(string(head[:]), 16, 16)
	if perr != nil {
		f.done = true
		return nil
	}
	if size == 0 {
		// The flush that ends the answer: the band-1 report inside it
		// has its own, which never reaches here.
		f.out.Truncate(f.out.Len() - 4)
		f.addHint()
		f.out.WriteString("0000")
		f.done = true
		return nil
	}
	if size < 4 {
		return nil
	}
	payload := make([]byte, size-4)
	n, err = io.ReadFull(f.in, payload)
	if err != nil {
		f.out.Write(payload[:n])
		f.done = true
		return nil
	}
	if !f.decided {
		f.decided = true
		f.sideband = len(payload) > 0 && payload[0] >= 1 && payload[0] <= 3
	}
	if !f.sideband {
		// No progress band to print a hint on: it goes on the ng line
		// itself, which git prints in parentheses after the ref.
		if reason, ok := f.readReportLine(payload); ok && f.hint != nil {
			if hint := f.hint(reason); hint != "" {
				line := strings.TrimRight(string(payload), "\n") + " -- grain: " + strings.Join(strings.Fields(hint), " ")
				if len(line) > 65000 {
					line = line[:65000]
				}
				writePkt(&f.out, line+"\n")
				return nil
			}
		}
		writePkt(&f.out, string(payload))
		return nil
	}
	f.out.Write(payload)
	switch payload[0] {
	case 1:
		f.report = append(f.report, payload[1:]...)
		f.readReport()
	case 2, 3:
		f.note(string(payload[1:]))
	}
	return nil
}

// readReport reads every whole pkt-line in report.
func (f *pushResultFilter) readReport() {
	for len(f.report) >= 4 {
		size, err := strconv.ParseUint(string(f.report[:4]), 16, 16)
		if err != nil {
			f.report = nil
			return
		}
		if size < 4 {
			f.report = f.report[4:]
			continue
		}
		if uint64(len(f.report)) < size {
			return
		}
		f.readReportLine(f.report[4:size])
		f.report = f.report[size:]
	}
}

// readReportLine notes one report-status line: "ng <ref> <reason>" is a
// rejected ref, and its reason is returned with true.
func (f *pushResultFilter) readReportLine(line []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(line, []byte("ng "))
	if !ok {
		return "", false
	}
	f.rejected = true
	_, reason, _ := bytes.Cut(rest, []byte(" "))
	f.note(string(reason))
	return string(reason), true
}

func (f *pushResultFilter) note(text string) {
	if f.message.Len() >= pushMessageLimit {
		return
	}
	f.message.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		f.message.WriteString("\n")
	}
}

// addHint writes the source's hint to out as progress-band lines, when
// a ref was rejected and the source has one.
func (f *pushResultFilter) addHint() {
	if !f.sideband || !f.rejected || f.hint == nil {
		return
	}
	hint := f.hint(f.message.String())
	if hint == "" {
		return
	}
	for _, line := range strings.Split(hint, "\n") {
		// "grain: " marks the line as the proxy's, not the host's.
		line = "grain: " + line
		for len(line) > 900 {
			writePkt(&f.out, "\x02"+line[:900])
			line = line[900:]
		}
		writePkt(&f.out, "\x02"+line+"\n")
	}
}
