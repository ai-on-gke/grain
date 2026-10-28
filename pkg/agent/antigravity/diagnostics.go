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

package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
)

const (
	diagnosticReadLimit = 64 << 10
	diagnosticTextLimit = 16 << 10
	diagnosticLineLimit = 40
	diagnosticEventName = "grain_diagnostic"
)

// diagnosticReadTimeout bounds the read below, which is deliberately not
// bounded by the run's own context. Generous: the cost of waiting is a
// moment at the end of a run that has already failed, and the cost of
// giving up early is losing the only record of why it failed.
const diagnosticReadTimeout = 30 * time.Second

// agy writes glog records to --log-file, including provider errors that
// never reach stream-json or stderr. Keep complete records only: a tail
// starting halfway through a record, or an unprefixed continuation of a
// multiline credential, must not become a new transcript entry.
var diagnosticRecord = regexp.MustCompile(`^[IWEF]\d{4}\s`)

// prepareDiagnosticLog gives each exec (including resumes) its own 0600
// file in the run's private HOME. The raw log is deleted with that HOME;
// only the bounded, redacted excerpt below survives in the transcript.
// A logging failure must not replace the failure we are trying to debug.
func prepareDiagnosticLog(ctx context.Context, host agent.Host, home string) (string, string) {
	path, err := host.CreateTemp(ctx, home, "agy-diagnostic-*.log")
	if err != nil {
		return "", "AGY diagnostic log unavailable: could not create capture file."
	}
	return path, ""
}

func readDiagnosticLog(ctx context.Context, host agent.Host, path string, secrets []string) string {
	// Detached from the run's own cancellation, and bounded on its own
	// instead. This read is reached with ctx already cancelled about as
	// often as not -- an operator stopping the run, a deadline, a turn
	// cap -- and a run that was cut off is exactly the run whose private
	// log holds the only account of why. agent.LocalHost happens to
	// ignore the context for a filesystem read, so the detaching is
	// belt-and-braces there; it is the contract this is written against
	// rather than an accident of that implementation, and
	// diagnostics_test.go's cancelAwareHost is what holds it. The same
	// reasoning as the HOME removal in writeHomeFiles, which outlives
	// the run's context for the same reason.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diagnosticReadTimeout)
	defer cancel()
	// The refusal to read anything that is not a regular file lives in
	// the Host (agent.Host.ReadTail, and both implementations of it):
	// opening a substituted FIFO could hang a run after its deadline
	// expired, and it has to be refused on the machine the file is on.
	//
	// A run whose machine went away mid-failure reads the same as one
	// whose capture file was never written: there is nothing to say
	// about the failure, which is worth saying plainly rather than
	// replacing the failure being debugged with this one.
	lines, truncated, err := diagnosticRecords(ctx, host, path, secrets)
	if err != nil {
		return "AGY diagnostic log unavailable: capture is not a readable regular file."
	}
	// Drop whole records, never slice a credential before redacting it.
	if len(lines) > diagnosticLineLimit {
		lines = lines[len(lines)-diagnosticLineLimit:]
		truncated = true
	}
	var kept []string
	bytes := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if bytes+len(lines[i])+1 > diagnosticTextLimit {
			truncated = true
			continue
		}
		kept = append(kept, lines[i])
		bytes += len(lines[i]) + 1
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	lines = kept
	if len(lines) == 0 && !truncated {
		return ""
	}
	note := "AGY diagnostic log (redacted; complete records from the final 64 KiB):"
	if truncated {
		note += "\n[older or oversized records omitted]"
	}
	return note + "\n" + strings.Join(lines, "\n")
}

// diagnosticRecords is the tail of agy's log at path as complete,
// redacted glog records, oldest first, and whether anything before or
// around them was dropped to get there. It is the one reader of agy's log
// format: readDiagnosticLog bounds and renders its result for a failed
// exec's transcript, and turnDeadline reads it while a turn is open to
// tell a provider retry from a slow model. Both depend on the same
// redaction, so a record quoted in either is safe to show.
func diagnosticRecords(ctx context.Context, host agent.Host, path string, secrets []string) ([]string, bool, error) {
	b, truncated, err := host.ReadTail(ctx, path, diagnosticReadLimit)
	if err != nil {
		return nil, false, err
	}
	text := string(b)
	if truncated {
		// Discard even a possibly complete first line. It is better to
		// lose one record than publish the suffix of a secret or message.
		_, text, _ = strings.Cut(text, "\n")
	}
	// A killed process can leave a partial final record, including a partial
	// credential that no longer matches the full known value or token shape.
	if text != "" && !strings.HasSuffix(text, "\n") {
		if end := strings.LastIndexByte(text, '\n'); end >= 0 {
			text = text[:end+1]
		} else {
			text = ""
		}
		truncated = true
	}
	// Include encodings commonly seen in URLs and JSON errors. Unlike
	// the journal redactor, do not exempt short keys:
	// a configured value is a credential even in a local/test deployment.
	var values []string
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		values = append(values, secret, url.QueryEscape(secret), url.PathEscape(secret), string(encoded[1:len(encoded)-1]))
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	redactor := grainlog.NewRedactor(nil)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if !diagnosticRecord.MatchString(line) {
			continue
		}
		// Shapes first: a short known key may be a substring of a longer
		// token. Replacing it first would destroy that token's recognizable
		// shape and leave the rest of the credential in the transcript.
		line, _ = redactor.Line(line)
		for _, value := range values {
			line = strings.ReplaceAll(line, value, "[redacted: AGY credential]")
		}
		lines = append(lines, line)
	}
	return lines, truncated, nil
}

// A separate event preserves the diagnostics in both the live mirror and
// the stored transcript, without pretending they are model output or
// feeding log messages into the capacity/quota classifiers. A previous
// exec's log must never decide a later exec's outcome or retry policy.
func appendDiagnostic(capture *strings.Builder, live io.Writer, execNumber int, text string) {
	if text == "" {
		return
	}
	event, _ := json.Marshal(rawEvent{
		Event:      diagnosticEventName,
		Diagnostic: fmt.Sprintf("! AGY exec %d diagnostics\n%s", execNumber, text),
	})
	appendCapture(capture, string(event))
	if live != nil {
		// The process may have died in the middle of a JSON line.
		fmt.Fprintf(live, "\n%s\n", event)
	}
}
