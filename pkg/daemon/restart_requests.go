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

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// A restart request is this daemon writing down, just before it brings
// itself down on purpose, that the start which follows is one it asked
// for. The restart-loop count (restarts.go) reads them back so that two
// upgrades inside its window are not reported as a crash loop: on
// 2026-10-17 two in-place upgrades a minute apart (18:07:44 and
// 18:08:32) logged "this service has started 2 times in the last 30
// minutes -- it is restarting rather than running", in the journal and
// on the health pane, when nothing had crashed and the queue was back
// within ten seconds each time (grain task 9).
//
// Two things ask for a restart deliberately, and both record one:
//
//   - an upgrade, just before it runs its restart command
//     (upgrade.Config.BeforeRestart);
//   - the UI's restart and reboot buttons, before their command runs.
//
// An upgrade's own status file (upgrade-status.json) is read as well,
// because it already records the same intent -- a PhaseOK upgrade whose
// restart step started -- and because the upgrade that lands *this*
// build was run by the previous one, which may predate this file and so
// never wrote to it. It only ever holds the latest upgrade, which is why
// it is not the whole of the record.

// restartRequestsFileName is the file under the data dir the requests
// are appended to: one line each, an RFC 3339 time and what asked.
const restartRequestsFileName = "restart-requests.log"

// restartRequestRetention is how long a request is kept. Far longer than
// restartWindow, which is all that ever reads them; the bound is only so
// the file cannot grow forever on a deployment that upgrades often.
const restartRequestRetention = 24 * time.Hour

// restartRequestReach is the furthest a request can be ahead of the
// start it explains. An upgrade's restart or the restart button brings
// the unit back in seconds; a reboot takes a minute or two. A request
// older than this that no start followed is a restart command that did
// not restart anything, and must not go on to excuse a crash later.
const restartRequestReach = 10 * time.Minute

// restartRequestSlack absorbs the journal's precision: --output=short-iso
// prints whole seconds, so a start at 18:07:44.9 reads as 18:07:44 and a
// request written at 18:07:44.3 would otherwise appear to follow it.
const restartRequestSlack = 2 * time.Second

// restartRequests is where one deployment's requests are kept, and how
// they are read back. The zero value (no data dir) records nothing and
// reads nothing, which is what a config built by a test has.
type restartRequests struct {
	dataDir string
	now     func() time.Time
}

func newRestartRequests(dataDir string) restartRequests {
	return restartRequests{dataDir: dataDir, now: time.Now}
}

func (r restartRequests) file() string {
	return filepath.Join(r.dataDir, restartRequestsFileName)
}

// record appends one request, dropping any older than
// restartRequestRetention as it goes. Rewritten atomically rather than
// appended to, so a reader never sees half a line and the prune and the
// append are one step.
func (r restartRequests) record(reason string) error {
	if r.dataDir == "" {
		return nil
	}
	now := r.now().UTC()
	kept, err := r.read(now.Add(-restartRequestRetention))
	if err != nil {
		// An unreadable file is replaced rather than left to block every
		// request after it.
		kept = nil
	}
	var buf bytes.Buffer
	for _, req := range kept {
		fmt.Fprintf(&buf, "%s\t%s\n", req.at.Format(time.RFC3339Nano), req.reason)
	}
	fmt.Fprintf(&buf, "%s\t%s\n", now.Format(time.RFC3339Nano), strings.ReplaceAll(reason, "\n", " "))
	tmp := r.file() + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.file())
}

// recordOrLog is record for a caller with nowhere to return an error to:
// the restart goes ahead regardless, and a request that could not be
// written costs at most a spurious warning on the next start.
func (r restartRequests) recordOrLog(reason string, logf func(string, ...any)) {
	if err := r.record(reason); err != nil {
		logf("grain daemon: recording that %s asked for a restart in %s: %v", reason, r.file(), err)
	}
}

type restartRequest struct {
	at     time.Time
	reason string
}

// read is every request at or after since, oldest first. A missing file
// is no requests.
func (r restartRequests) read(since time.Time) ([]restartRequest, error) {
	if r.dataDir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(r.file())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []restartRequest
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		stamp, reason, _ := strings.Cut(sc.Text(), "\t")
		at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(stamp))
		if err != nil || at.Before(since) {
			continue
		}
		out = append(out, restartRequest{at: at, reason: reason})
	}
	return out, sc.Err()
}

// times is every moment since `since` that this deployment asked for its
// own restart: the requests file, plus the latest upgrade's restart from
// its status file. Duplicates are harmless -- a start is deliberate if
// any request explains it (classifyStarts).
func (r restartRequests) times(since time.Time) []time.Time {
	var out []time.Time
	reqs, _ := r.read(since)
	for _, req := range reqs {
		out = append(out, req.at)
	}
	if r.dataDir != "" {
		if status, err := upgrade.ReadStatus(filepath.Join(r.dataDir, "upgrade-status.json")); err == nil {
			if at, ok := status.RestartRequestedAt(); ok && !at.Before(since) {
				out = append(out, at)
			}
		}
	}
	return out
}

// wrap makes a host action record a request before it runs -- the UI's
// restart and reboot buttons, whose next start is one an operator asked
// for through grain. nil stays nil (the action is unavailable here).
func (r restartRequests) wrap(reason string, action func(ctx context.Context) error) func(ctx context.Context) error {
	if action == nil {
		return nil
	}
	return func(ctx context.Context) error {
		r.recordOrLog(reason, log.Printf)
		return action(ctx)
	}
}
