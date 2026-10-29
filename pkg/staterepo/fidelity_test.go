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

package staterepo_test

// What a dump has to be able to carry.
//
// The rest of the suite checks that the right rows move in the right
// direction at the right moment. This file checks the thing underneath
// all of it: that a value written into the database and read back out of
// a clone of the repository is the same value -- the same bytes, in the
// same SQLite storage class, NULL where it was NULL and empty where it
// was empty.
//
// It is worth its own file because the failures here are silent. A row
// that does not import at all fails loudly, at COMMIT, in front of
// `grain state check`; a row that imports with different bytes in it
// looks exactly like a successful restore.

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A run's transcript is whatever came off somebody else's stdout, and
// SQLite does not police what goes into a TEXT column, so bytes that are
// not valid UTF-8 do reach the export. encoding/json cannot carry one --
// it substitutes U+FFFD and reports no error -- so a dump that handed
// them to it held different bytes than the database and every restore
// from it was quietly corrupt.
func TestADumpCarriesTextThatIsNotValidUTF8(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	// A truncated multi-byte sequence and a lone continuation byte:
	// what a transcript captured mid-character looks like.
	transcript := "make test\n\xe2\x82quipped\xff\xfe\n"
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "a1b2-1", TaskID: "a1b2", Sandbox: "grain-a1b2-1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := store.SetRunTranscript(ctx, "a1b2-1", transcript); err != nil {
		t.Fatalf("writing the transcript: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	_, restored := openDB(t)
	if err := staterepo.Import(ctx, restored, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	var got, class string
	if err := restored.QueryRowContext(ctx,
		"SELECT `transcript`, typeof(`transcript`) FROM `task_run` WHERE `id` = 'a1b2-1'").
		Scan(&got, &class); err != nil {
		t.Fatalf("reading the restored transcript: %v", err)
	}
	if got != transcript {
		t.Fatalf("the round trip changed the transcript:\n got %q\nwant %q", got, transcript)
	}
	// And it is still text. Base64 is how those bytes cross the file, but
	// they have to land back in the storage class they left: a transcript
	// that came back a blob would compare equal on the bytes and be a
	// different database.
	if class != "text" {
		t.Fatalf("the transcript came back as %s, not text", class)
	}
}

// The other side of the same coin: a column that really is binary stays
// binary, and every one of the 256 byte values survives.
func TestADumpCarriesABlobWholeAndStillABlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	content := make([]byte, 256)
	for i := range content {
		content[i] = byte(i)
	}
	if _, err := store.AddAttachment(ctx, model.Attachment{
		TaskID: "a1b2", Filename: "screenshot.png", ContentType: "image/png",
		Size: int64(len(content)), Content: content, CreatedAt: now,
	}); err != nil {
		t.Fatalf("attaching: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	_, restored := openDB(t)
	if err := staterepo.Import(ctx, restored, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	var got []byte
	var class string
	if err := restored.QueryRowContext(ctx,
		"SELECT `content`, typeof(`content`) FROM `task_attachment` WHERE `task_id` = 'a1b2'").
		Scan(&got, &class); err != nil {
		t.Fatalf("reading the restored attachment: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("the round trip changed the attachment: %d bytes, want %d", len(got), len(content))
	}
	if class != "blob" {
		t.Fatalf("the attachment came back as %s, not blob", class)
	}
}

// Everything else, all at once: a database with a row in every shape
// this schema can produce, exported, imported into an empty database and
// compared column by column with typeof() alongside every value.
//
// Written this way rather than as a list of fields to check because the
// list would be the thing that went stale. A column a later schema adds
// is covered by this the day it is added, and a column whose values stop
// surviving the round trip is named by the failure.
func TestARestoredDatabaseIsTheSameDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	fill(t, ctx, store, db)

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	_, restored := openDB(t)
	if err := staterepo.Import(ctx, restored, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	sameDatabase(t, ctx, db, restored, "a database restored from its own dump")
}

// And the fixed point that makes an export safe to run on a timer: a
// database restored from a dump exports to byte-identical files. If it
// did not, a deployment restored from a clone would commit a diff of its
// own restore on its very first sync -- and every host that restored
// would produce a different one.
func TestARestoreExportsToTheSameBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	fill(t, ctx, store, db)

	first := t.TempDir()
	if err := staterepo.Export(ctx, db, first); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	_, restored := openDB(t)
	if err := staterepo.Import(ctx, restored, first); err != nil {
		t.Fatalf("importing: %v", err)
	}
	second := t.TempDir()
	if err := staterepo.Export(ctx, restored, second); err != nil {
		t.Fatalf("re-exporting: %v", err)
	}
	sameDump(t, first, second)
}

// sameDump fails unless two exported directories hold the same files
// with the same bytes.
func sameDump(t *testing.T, a, b string) {
	t.Helper()
	names := func(dir string) []string {
		entries, err := os.ReadDir(filepath.Join(dir, staterepo.TablesDir))
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	left, right := names(a), names(b)
	if !equalStrings(left, right) {
		t.Fatalf("the two dumps hold different files:\n%v\n%v", left, right)
	}
	for _, name := range left {
		x := read(t, filepath.Join(a, staterepo.TablesDir, name))
		y := read(t, filepath.Join(b, staterepo.TablesDir, name))
		if x != y {
			t.Fatalf("%s differs between the two dumps:\n%s\n---\n%s", name, x, y)
		}
	}
}

// The claim fill's own doc comment makes, as a test: a row in every
// table the export writes, not merely in the ones the tests above
// happen to reach.
//
// It is here because that claim went stale silently and expensively. It
// was written when the schema had rather fewer tables than it has now,
// and by 2026-09-06 thirty-five of fifty-two tables held nothing after a
// fill -- every one of a week's worth of new ones among them (boards,
// panels, custom fields and their values, the extra write targets,
// release trains, the pipeline pause). Everything above still passed,
// because a table with no rows in it round-trips perfectly. So the round
// trip that exists to catch a value coming back with different bytes in
// it -- the silent failure, the one a restore does not announce -- was
// not looking at any of them.
//
// Failing here is not a bug in the schema: it means a table was added
// and fill was not told how to write a row in it. Add one, in the
// section of fill that matches what the table is (settings, grain's own
// record, or a child of a task), and regenerate the golden dump if the
// row belongs there too.
func TestFillWritesARowInEveryTableTheDumpCarries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	fill(t, ctx, store, db)
	var empty []string
	tables := realTables(t, ctx, db)
	if len(tables) == 0 {
		t.Fatal("the schema has no tables, so nothing here is checking anything")
	}
	for _, table := range tables {
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteTable(table)).Scan(&rows); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if rows == 0 {
			empty = append(empty, table)
		}
	}
	if len(empty) > 0 {
		t.Errorf("fill writes no row into %s.\n"+
			"Every table here is exported and imported by a restore, so a table fill leaves empty is one "+
			"the round-trip tests in this file do not check at all. Write a row in it (see this test's own "+
			"doc comment), or say in fill's doc comment why this one cannot have one.",
			strings.Join(empty, ", "))
	}
}

// quoteTable is the identifier quoting the package's own SQL uses,
// spelled out here because this file is the external test package and
// cannot reach quote().
func quoteTable(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// fill writes a row in every shape the schema can hold: NULL and empty
// string side by side, text with newlines and control characters and
// astral-plane runes in it, a REAL sort key, a 64-bit id, a blob, a
// timestamp with sub-second precision -- and a row in every table the
// export writes, on both sides of the settings/observation line and in
// both tiers, so the tests above cover the whole dump rather than the
// two tables everything else happens to touch
// (TestFillWritesARowInEveryTableTheDumpCarries holds it to that).
func fill(t *testing.T, ctx context.Context, store *model.Store, db *sql.DB) {
	t.Helper()
	// Text that has caused trouble in one system or another: an emoji
	// (outside the basic plane), a right-to-left mark, a NUL, a tab, CRLF,
	// a quote, a backslash, and the HTML-ish characters encoding/json
	// escapes.
	awkward := "quote \" backslash \\ tab\t crlf\r\n nul\x00 emoji \U0001F600 rtl ‏ " +
		"html <&> and a very long line " + strings.Repeat("x", 4000)

	tk := task("a1b2")
	tk.Title = awkward
	tk.Body = ""
	// The template it was filed from (grain/task-2536), in
	// task_filed_template.
	tk.TemplateID = "template-7"
	// The extra repositories a task may have work landed in
	// (grain/task-86): one with a base branch of its own and one without,
	// since the column defaults to the empty string and "no branch named"
	// has to come back as that rather than as NULL.
	tk.Writes = []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "owner", Name: "docs"}, Base: "release-2026-09"},
		{Repo: model.RepoRef{Owner: "owner", Name: "infra"}},
	}
	// And the values it carries for this deployment's own fields
	// (grain/task-105), one of them a set: a field declared `multiple`
	// stores a row per value with the value in the key, which is the one
	// shape here a single value would not exercise.
	tk.CustomFields = map[string][]string{
		"customer": {"acme", awkward},
		"due-date": {"2026-09-06"},
	}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	// A second task with no target at all, which is a NULL where the
	// first one has a value.
	plain := task("c3d4")
	plain.Target = nil
	sub := now.Add(1234567 * time.Nanosecond)
	plain.CreatedAt = &sub
	if err := store.PutTask(ctx, plain); err != nil {
		t.Fatalf("putting a task: %v", err)
	}

	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: "a1b2", CreatedAt: sub, Body: awkward,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
	})
	if err != nil {
		t.Fatalf("commenting: %v", err)
	}
	// One attachment on the task itself (a NULL comment_id) and one on
	// the comment (an integer one).
	blob := make([]byte, 512)
	for i := range blob {
		blob[i] = byte(i * 7)
	}
	for _, att := range []model.Attachment{
		{TaskID: "a1b2", Filename: "body.bin", ContentType: "application/octet-stream",
			Size: int64(len(blob)), Content: blob, CreatedAt: sub},
		{TaskID: "a1b2", CommentID: &commentID, Filename: "", ContentType: "",
			Size: 0, Content: []byte{}, CreatedAt: sub},
	} {
		if _, err := store.AddAttachment(ctx, att); err != nil {
			t.Fatalf("attaching: %v", err)
		}
	}

	if err := store.StartRun(ctx, model.Run{
		ID: "a1b2-1", TaskID: "a1b2", Sandbox: "grain-a1b2-1", Attempt: 1, StartedAt: sub,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := store.SetRunTranscript(ctx, "a1b2-1", awkward+"\n\xff\xfe raw bytes"); err != nil {
		t.Fatalf("writing a transcript: %v", err)
	}
	if err := store.FinishRun(ctx, "a1b2-1", sub.Add(time.Minute), "succeeded", awkward); err != nil {
		t.Fatalf("finishing a run: %v", err)
	}
	// A second attempt, suspended (task_suspension), with every nullable
	// column of the suspension set.
	if err := store.StartRun(ctx, model.Run{
		ID: "a1b2-2", TaskID: "a1b2", Sandbox: "grain-a1b2-2", Attempt: 2, StartedAt: sub.Add(2 * time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	resumeAt := sub.Add(time.Hour)
	if err := store.Suspend(ctx, model.Suspension{
		RunID: "a1b2-2", Kind: model.SuspendRequested, Detail: awkward, ResumeOn: `{"tasks":["c3d4"]}`,
		ResumeAt: &resumeAt, ModelHint: awkward, CreatedAt: sub.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("suspending a run: %v", err)
	}
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: "a1b2", Capability: "gemini-key", Resource: awkward, WaitingAt: sub,
	}); err != nil {
		t.Fatalf("recording a grant wait: %v", err)
	}
	// A run still live, so the finished columns are NULL rather than set,
	// carrying the credentials it was minted (`lease`) -- one that
	// expires and one that does not, since ExpiresAt is the nullable
	// half of that row.
	expires := sub.Add(time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "c3d4-1", TaskID: "c3d4", Sandbox: "grain-c3d4-1", Attempt: 1, StartedAt: sub,
		Leases: []model.Lease{
			{Capability: "githubtoken", Resource: "owner/payments-api",
				MintedBy: model.CredentialRef{Name: "github-app"}, IssuedAt: sub, ExpiresAt: &expires},
			{Capability: "memory", Resource: "", MintedBy: model.CredentialRef{Name: ""}, IssuedAt: sub},
		},
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	// What a finished run recorded about its own tool use
	// (task_run_tool, task_run_check_wait, task_run_wait_credit,
	// task_run_ci_habit): four tables nothing else here writes, and the only ones whose rows are
	// entirely numbers, where a value that came back as text rather than
	// as an integer would still read the same in a diff.
	if err := store.RecordRunTelemetry(ctx, "a1b2-1", model.RunTelemetry{
		Tools: []model.RunToolUse{
			{Tool: "run_command", Calls: 12, Errored: 1, TimedOut: 0, ResultBytes: 98765, MaxResultBytes: 43210},
			{Tool: awkward, Calls: 1},
		},
		CheckWaits: []model.RunCheckWait{
			{RunID: "a1b2-1", Seq: 1, Verdict: "failed", Waited: 90 * time.Second, PushesBefore: 1},
			{RunID: "a1b2-1", Seq: 2, Verdict: "passed", Waited: 4 * time.Minute, PushesBefore: 2},
		},
		WaitCredit: &model.RunWaitCredit{RunID: "a1b2-1", Blocked: 6 * time.Minute, Credited: 5 * time.Minute},
		CIHabit:    &model.RunCIHabit{NarrowTestBeforeFixPush: true, PushedAfterRed: true, Selective: true},
	}); err != nil {
		t.Fatalf("recording telemetry: %v", err)
	}
	if err := store.RecordRunReportCheck(ctx, model.RunReportCheck{
		RunID: "a1b2-1", Claims: 5, Uncited: 1, Unread: 2, Mismatched: 3,
	}); err != nil {
		t.Fatalf("recording report check: %v", err)
	}
	// A file the live run attached by path (task_run_staged_attachment):
	// a blob, keyed by the path exactly as the run wrote it.
	if live, err := store.StageAttachment(ctx, "c3d4", model.StagedAttachment{
		Path: awkward, Filename: "shot.png", ContentType: "image/png", Content: blob, CreatedAt: sub,
	}); err != nil || !live {
		t.Fatalf("staging an attachment: live=%v, %v", live, err)
	}
	// How far the daemon has read a run host's spool
	// (task_run_spool_cursor): an offset and the time it last moved.
	if err := store.SetSpoolCursor(ctx, "a1b2-1", 987654, sub); err != nil {
		t.Fatalf("advancing a spool cursor: %v", err)
	}
	// A live k8s-granule sandbox's reverse-channel token hash and expiry
	// (sandbox_access).
	if err := store.SetSandboxAccess(ctx, "c3d4-1",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		sub.Add(time.Hour)); err != nil {
		t.Fatalf("recording sandbox access credential: %v", err)
	}
	// A running CI step's exec id and output cursor
	// (ci_step_attachment).
	if err := store.PutCIStepAttachment(ctx, model.CIStepAttachment{
		RunID: "c3d4-run", JobID: "tests.yml/go", StepID: "a1-s0", Sandbox: "c3d4-1",
		ExecID: "exec-7-" + awkward, Cursor: 987654, UpdatedAt: sub,
	}); err != nil {
		t.Fatalf("recording a CI step attachment: %v", err)
	}
	// What a run said when it failed for a reason grain could not name
	// (task_run_unmatched_failure) -- the one table here whose whole
	// content is a provider's own words, so the awkward string is the
	// point of it rather than decoration: this is the row most likely to
	// carry a quote, a brace or a non-ASCII character out of somebody
	// else's error body and back in again.
	if err := store.RecordUnmatchedFailure(ctx, model.UnmatchedFailure{
		RunID: "a1b2-1", TaskID: "a1b2", Framework: "claude", At: sub,
		Text: awkward + " api error: 529 {\"type\":\"overloaded\"}", Suspect: true,
	}); err != nil {
		t.Fatalf("recording an unmatched failure: %v", err)
	}
	// How many of a run's tool calls the cluster's exec tunnel failed
	// (task_run_exec_failure).
	if err := store.RecordExecTransportFailure(ctx, model.ExecTransportFailure{
		RunID: "a1b2-1", TaskID: "a1b2", At: sub, Calls: 3,
	}); err != nil {
		t.Fatalf("recording exec-tunnel failures: %v", err)
	}
	// What a run's capability grants did for it
	// (task_run_capability_audit): a capability server's own JSON
	// record, verbatim -- argv out of a sandbox, so the awkward string
	// again -- and grain's note beside it, the column left empty.
	if err := store.AppendRunCapabilityAudit(ctx, "a1b2-1", []model.CapabilityAuditRecord{
		{Grant: "host-script#host-script", AttemptID: "e1-a1b2-1-0", At: sub,
			Record: `{"time":"2026-01-01T00:00:00Z","argv":["` + awkward + `"]}`},
		{Grant: "host-script#host-script", AttemptID: "e1-a1b2-1-0", At: sub, Note: "the capability server left out 1 further record(s)"},
	}); err != nil {
		t.Fatalf("recording a capability audit: %v", err)
	}
	// A run of grain's own CI (ci_run, ci_job, ci_step): the three tables
	// whose interesting column is a step's output, which is raw bytes
	// out of somebody else's build and exactly the thing a round trip
	// through JSON has to bring back unchanged. One job that ran and one
	// that was skipped, so the nullable timestamps are covered set and
	// unset, and a step whose exit code is the -1 that means it never
	// produced one.
	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "3f2a1c", Key: "ci1:abc", Repo: "owner/payments-api", Ref: "grain/task-1",
		Commit: "9f8e7d6c5b4a39281706", Event: "push", RequestedBy: "a1b2",
		StartedAt: sub, FinishedAt: sub.Add(3 * time.Minute), Conclusion: "failure",
		Jobs: []model.CIJob{
			{
				Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "unit",
				Name: awkward, Key: "ci1:job", Sandbox: "grain-ci-3f2a1c-unit",
				StartedAt: sub, FinishedAt: sub.Add(2 * time.Minute), Conclusion: "failure",
				Steps: []model.CIStep{
					{Name: "go test", Conclusion: "failure", ExitCode: 1,
						Output: awkward + "\n--- FAIL: TestSomething", StartedAt: sub,
						FinishedAt: sub.Add(2 * time.Minute)},
					{Name: "", Conclusion: "skipped", ExitCode: -1},
				},
			},
			{
				Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "publish",
				Name: "publish", Key: "ci1:job2", Cached: true, Conclusion: "skipped",
				Error: "a job it needs did not succeed", Activity: awkward, ActivityAt: sub,
			},
		},
	}); err != nil {
		t.Fatalf("recording a CI run: %v", err)
	}

	if err := store.PutCIStepOutput(ctx, "3f2a1c", 0, 0, awkward+"\n--- FAIL: TestSomething\nchunk"); err != nil {
		t.Fatalf("recording a CI step log chunk: %v", err)
	}

	// What sandboxes have cost this deployment (sandbox_usage): both
	// kinds, and both halves of one -- a sandbox counted as started and
	// one counted as finished -- so the row carries a duration and the
	// dates at both ends rather than only the zeroes an untouched
	// counter has.
	if err := store.RecordSandboxStart(ctx, model.SandboxKindAgent, true, sub); err != nil {
		t.Fatalf("counting an agent sandbox: %v", err)
	}
	if err := store.RecordSandboxFinish(ctx, model.SandboxKindAgent, 97*time.Minute, sub.Add(97*time.Minute)); err != nil {
		t.Fatalf("counting a finished agent sandbox: %v", err)
	}
	// And one that was asked for and never came up, which is the counter
	// whose value is the difference between two columns rather than a
	// column of its own.
	if err := store.RecordSandboxStart(ctx, model.SandboxKindCI, false, sub); err != nil {
		t.Fatalf("counting a CI sandbox that never started: %v", err)
	}

	// An observation, which is what a reconcile cycle stamps.
	at := sub.Add(2 * time.Minute)
	if err := store.ObserveField(ctx, "a1b2", at, func(o *model.Observation) {
		o.PrOpenedAt = &at
	}); err != nil {
		t.Fatalf("observing: %v", err)
	}

	// The settings half.
	//
	// The fields this deployment declares (custom_field), which the task
	// above already carries values for: one enum holding a set, with
	// options and a default and a position, and one that says the least a
	// declaration can say, so the columns added with a default are
	// covered set and unset.
	for _, field := range []model.CustomField{
		{Name: "customer", Label: awkward, Type: model.CustomFieldEnum,
			Options: []string{"acme", awkward}, Description: awkward,
			Default: "acme", Position: 3, Multiple: true, ShowOnRow: true},
		{Name: "due-date", Type: model.CustomFieldDate},
	} {
		if err := store.PutCustomField(ctx, field); err != nil {
			t.Fatalf("declaring custom field %s: %v", field.Name, err)
		}
	}
	for _, set := range []model.APIScopeSet{
		{Name: "ops/read", Description: awkward, Scopes: []string{"ci:read", "tasks:*"}, Version: 2},
		{Name: "ops/triage", Includes: []string{"grain/ci-read", "grain/task-admin", "ops/read"},
			Deny: []string{"tasks:admin"}, Repos: []string{"owner/payments-api"}, RequiresPerson: true, Deprecated: true},
	} {
		if err := store.PutAPIScopeSet(ctx, set); err != nil {
			t.Fatalf("declaring api scope set %s: %v", set.Name, err)
		}
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: awkward, Body: awkward,
		Reads: []model.RepoRef{{Owner: "owner", Name: "payments-api"}},
		// A template's own write targets, capabilities and field values,
		// carried onto every task it files.
		Writes: []model.WriteTarget{
			{Repo: model.RepoRef{Owner: "owner", Name: "docs"}, Base: "main"},
			{Repo: model.RepoRef{Owner: "owner", Name: "infra"}},
		},
		Grants: []model.Grant{{Capability: "githubtoken", Via: model.GrantByLabel}},
		// The inputs it asks for when somebody instantiates it, one of
		// each kind (model.TemplateField): a text box and a selector.
		Fields: []model.TemplateField{
			{Name: "dependency", Label: awkward, Required: true},
			{Name: "environment", Options: []string{"staging", awkward}, Default: "staging"},
		},
		CustomFields: map[string][]string{"customer": {"acme", awkward}},
		Interactive:  true,
		CreatedAt:    sub,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	// A second template, so the qualification plan below has two items
	// and therefore a dependency between them to record.
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-2", Name: "smoke", Title: "Smoke test", Body: "Poke it.",
		CreatedAt: sub,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := store.PutSuite(ctx, model.Suite{
		ID: "suite-1", Name: awkward, Mode: model.SuiteCount, Count: 3,
		Items:     []model.SuiteItem{{TemplateID: "tpl-1"}, {TemplateID: "tpl-1"}},
		AutoMerge: true, CreatedAt: sub,
	}); err != nil {
		t.Fatalf("putting a suite: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "owner", Name: "payments-api"},
		DefaultCapabilities: []string{"githubtoken"},
		CICapabilities:      []string{"gcp-key"},
		PromptExtension:     awkward,
		SetupCommand:        "",
	}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                         model.RepoRef{Owner: "owner", Name: "billing"},
		CloneURL:                     "https://github.com/owner/billing.git",
		DefaultCapabilities:          []string{"githubtoken"},
		SetupCommand:                 "make test",
		CIMode:                       model.CIModeGrain,
		GitCredential:                "billing-token",
		RepairTemplateID:             "repair-tpl",
		RequireBranchUpToDate:        true,
		WatchDefaultBranch:           true,
		MergeOnGreen:                 true,
		MergeRecheck:                 model.MergeRecheckEnforce,
		BaseRepairBypassesHumanGates: true,
		ReleaseSecrets:               []string{"SECRET_1"},
	}); err != nil {
		t.Fatalf("putting repo config with a git credential: %v", err)
	}
	if err := store.PutGCPKey(ctx, model.GCPKey{
		Name:                "deploy",
		ProjectID:           "test-gcp-project",
		ServiceAccountEmail: "deployer@test-gcp-project.iam.gserviceaccount.com",
		ServiceAccountCount: 2,
		MinterCredential:    "custom-minter",
		MaxKeyAge:           12 * time.Hour,
		Description:         awkward,
	}); err != nil {
		t.Fatalf("putting gcp key: %v", err)
	}
	// A repository that declares its own templates, suites and schedules
	// (model.RepoDeclaration, grain/task-60): the switch on the repo
	// config, the provenance rows, and grain's record of the poll that
	// wrote them. Written through SyncRepoDeclarations rather than with
	// raw inserts so the fixture is a shape the daemon can actually
	// produce -- which for this table means the template row and its
	// provenance row agreeing about the id.
	declaring := model.RepoRef{Owner: "owner", Name: "billing"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: declaring, Declarations: true}); err != nil {
		t.Fatalf("putting a declaring repo's config: %v", err)
	}
	declaredID := model.RepoDeclaredID(model.DeclaredTemplate, declaring, "nightly")
	declaredTarget := declaring
	if _, err := store.SyncRepoDeclarations(ctx, declaring, "0f1e2d3c4b5a69788796a5b4c3d2e1f001234567",
		model.RepoDeclarations{Templates: []model.DeclaredTemplateFile{{
			Path: model.TemplateDeclarationDir + "/nightly.yml",
			Template: model.Template{
				ID: declaredID, Name: "Nightly sweep", Title: awkward, Body: "Sweep it.",
				Target: &declaredTarget,
			},
			// And its push set (grain/task-2536), in repo_declaration_push.
			Push: model.RefPushSet{
				{Pattern: "refs/heads/candidate", Force: true},
				{Pattern: "refs/tags/v*"},
			},
		}},
			// And a path trigger (grain/task-1712), whose rule is stored
			// as JSON in repo_path_trigger.
			Triggers: []model.PathTrigger{{
				Name: "design", Path: model.TriggerDeclarationDir + "/design.yml",
				On: model.TriggerOnMerge, Paths: []string{"docs/design/**"}, Ignore: []string{awkward},
				Template: declaredID, Per: model.TriggerPerFolder,
			}},
			// And merge rules (grain/task-1659), stored as JSON in
			// repo_merge_rules.
			MergeRules: &model.MergeRules{Path: model.MergeRulesPath, Rules: []model.MergeRule{{
				ID: "docs", Description: awkward, Paths: []string{"docs/**"},
				MergePolicy: model.MergePolicy{CI: &model.CIRule{Grain: []string{"lint"}}},
			}}},
		}, sub, time.UTC); err != nil {
		t.Fatalf("syncing a repository's declarations: %v", err)
	}
	if err := store.PutPathTriggerCursor(ctx, declaring, "main",
		"0f1e2d3c4b5a69788796a5b4c3d2e1f001234567", sub); err != nil {
		t.Fatalf("recording a path trigger cursor: %v", err)
	}
	// The grain CI workflows this deployment declares for a repo it does
	// not control (model.RepoWorkflow). Two of them, since the table's
	// key is (repo, path) and a repository with one workflow would not
	// show the path doing any work; the text is a real workflow rather
	// than the awkward string above, because the column holds a file that
	// ci.Parse has already accepted on the way in and a round-trip that
	// mangled it would be a run of the wrong jobs.
	if err := store.PutRepoWorkflows(ctx, model.RepoRef{Owner: "owner", Name: "payments-api"},
		[]model.RepoWorkflow{
			{
				Repo: model.RepoRef{Owner: "owner", Name: "payments-api"},
				Path: ".grain/workflows/lint.yml",
				Text: "name: lint\non: push\njobs:\n  lint:\n    steps:\n      - run: make lint\n",
			},
			{
				Repo: model.RepoRef{Owner: "owner", Name: "payments-api"},
				Path: ".grain/workflows/tests.yml",
				Text: "name: tests\non:\n  push:\n    branches: ['*']\njobs:\n  tests:\n    steps:\n" +
					"      - name: unit\n        run: make test\n",
			},
		}); err != nil {
		t.Fatalf("putting repo workflows: %v", err)
	}
	// Two schedules, one of each recurrence shape, so the columns that
	// are NULL for one kind and set for the other are covered both ways
	// -- and one of each overlap policy, since 'skip' is the column's own
	// default and a row that only ever held the default would not show a
	// value going missing.
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sch-1", Title: awkward, Body: "", Target: model.RepoRef{Owner: "owner", Name: "payments-api"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
		Overlap:    model.OverlapAllow,
		// What a firing carries besides its own content: read-only
		// repositories, extra write targets, capabilities and field
		// values, one child table each.
		Reads: []model.RepoRef{{Owner: "owner", Name: "docs"}},
		Writes: []model.WriteTarget{
			{Repo: model.RepoRef{Owner: "owner", Name: "docs"}, Base: "release-2026-09"},
			{Repo: model.RepoRef{Owner: "owner", Name: "infra"}},
		},
		Grants:       []model.Grant{{Capability: "githubtoken", Via: model.GrantByLabel}},
		CustomFields: map[string][]string{"customer": {"acme", awkward}},
		Enabled:      true, NextRunAt: sub, CreatedAt: sub,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}
	last := sub.Add(-time.Hour)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sch-2", Title: "weekly", Base: "release",
		Target:     model.RepoRef{Owner: "owner", Name: "payments-api"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: 90, Weekday: time.Tuesday},
		Overlap:    model.OverlapSkip,
		Enabled:    false, NextRunAt: sub, LastRunAt: &last, CreatedAt: sub,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}
	// Three saved dashboards (grain/task-48, grain/task-88,
	// grain/task-83), one of each kind that has child rows of its own: a
	// Kanban board's columns, a panels board's panels and a report
	// board's sections. Their ids come from the store's own allocator,
	// which is what puts a row in board_sequence.
	kanbanID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: kanbanID, Name: awkward, Folder: "Reports", Kind: model.BoardKanban,
		Description: awkward, Query: "q=ci&sort=created:desc&f=state:any:running",
		Columns: []model.BoardColumn{
			{Name: awkward, States: []model.State{model.StateQueued, model.StateRunning}},
			{Name: "Done", States: []model.State{model.StateCompleted, model.StateClosed}},
		},
		OrderKey: 1.5,
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	panelsID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: panelsID, Name: "Monday morning", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{
			{Kind: model.PanelGantt, Title: awkward, Query: "f=repo:any:owner/payments-api",
				Range: "7d", Width: model.PanelFull, OrderKey: 10},
			// A panel that says the least one can: the board's own
			// question, the kind's own default range and width.
			{Kind: model.PanelStates},
		},
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	reportID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: reportID, Name: "How payments is going", Kind: model.BoardReport,
		Query: "f=repo:any:owner/payments-api&f=created:within:30",
		Sections: []model.BoardSection{
			{Kind: model.SectionThroughput, Title: awkward, OrderKey: 10},
			// A section that says the least one can: its own name for a
			// heading, and its place taken from where it sits.
			{Kind: model.SectionLatency},
		},
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	// What grain has learned about working here (model.Memory): the
	// deployment-wide row, whose repo key is two empty strings, and one
	// about a repo.
	//
	// One of each author (model.Memory.UpdatedBy): the whole use of that
	// column is telling an operator's own correction from what last
	// night's memory agent wrote, and a dump that carried one kind and
	// not the other would restore a deployment that had lost the
	// distinction while looking entirely healthy.
	for _, m := range []model.Memory{
		{Text: awkward, UpdatedAt: sub,
			UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"}},
		{Repo: &model.RepoRef{Owner: "owner", Name: "payments-web"}, Text: "Payments web memory.",
			UpdatedAt: sub, UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: awkward}},
		{Repo: &model.RepoRef{Owner: "owner", Name: "payments-api"}, Text: "The tests need a docker daemon.",
			UpdatedAt: sub, UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: awkward}},
	} {
		if err := store.PutMemory(ctx, m); err != nil {
			t.Fatalf("putting a memory: %v", err)
		}
	}
	// And the long form of it (model.MemoryDocument), in both scopes:
	// the note itself never reaches a prompt, so the dump is the only
	// place a restore can get it back from.
	for _, d := range []model.MemoryDocument{
		{Name: "state-repo-shape", Summary: awkward, Text: awkward, UpdatedAt: sub,
			UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: awkward}},
		{Repo: &model.RepoRef{Owner: "owner", Name: "payments-web"}, Name: "guidelines", Summary: "guidelines",
			Text: "Guidelines.", UpdatedAt: sub, UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: awkward}},
		{Repo: &model.RepoRef{Owner: "owner", Name: "payments-api"}, Name: "flaky-e2e",
			Summary: "why the third retry fails", Text: "The suite reuses the VM.", UpdatedAt: sub,
			UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"}},
	} {
		if err := store.PutMemoryDocument(ctx, d); err != nil {
			t.Fatalf("putting a memory document: %v", err)
		}
	}
	// A week of this deployment's own account of itself
	// (model.ReleaseNote): prose written by an agent, exported as
	// markdown beside the memory above, and -- like the memory -- in no
	// other place a restore could get it back from.
	if err := store.PutReleaseNote(ctx, model.ReleaseNote{
		Period: "2026-W09", Title: awkward, Text: awkward, CreatedAt: sub, UpdatedAt: sub,
		UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "a1b2"},
	}); err != nil {
		t.Fatalf("putting a release note: %v", err)
	}
	// The qualification plan for a repo (qualification_config,
	// qualification_item and the dependency between two items), which the
	// run below is created from.
	if err := store.PutQualificationPlan(ctx, model.QualificationPlan{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Configured: true,
		RequireApproval: true, AutoPromote: false,
		Items: []model.QualificationItem{
			{TemplateID: "tpl-1", Repeat: 2},
			{TemplateID: "tpl-2", Repeat: 1, DependsOn: []string{"tpl-1"}},
		},
	}); err != nil {
		t.Fatalf("putting a qualification plan: %v", err)
	}
	if err := store.PutStandingSandbox(ctx, model.StandingSandbox{
		Name: "standing-1", Image: "docker.io/repo/granule-app:v1", Command: awkward, Dir: "/app",
		CPUs: 2, MemoryMB: 4096, DiskGB: 20, NetworkAccess: model.NetworkOpen, Enabled: true,
		Env: map[string]string{"APP_ENV": "staging", "DEBUG": awkward},
		Secrets: []model.StandingSandboxSecret{
			{SandboxName: "standing-1", SecretName: "gcp/sa-key", FilePath: "/etc/secrets/gcp.json"},
			{SandboxName: "standing-1", SecretName: "db/password", EnvVar: "DB_PASSWORD"},
		},
	}); err != nil {
		t.Fatalf("putting a standing sandbox: %v", err)
	}
	if err := store.PutPersistentSandbox(ctx, model.PersistentSandbox{
		Name: "staging-1", Repo: "acme/widgets", Ref: "main", AutoUpdate: true, Command: awkward, Dir: "web",
		Env: map[string]string{"APP_ENV": "staging"}, Capabilities: []string{"gcp-key"},
		CPUs: 2, MemoryMB: 4096, DiskGB: 20, NetworkAccess: model.NetworkOpen,
		SandboxPort: 8080, HostPort: 4280, UISubpath: "/staging/staging-1", StripPrefix: true, Enabled: true,
	}); err != nil {
		t.Fatalf("putting a persistent sandbox: %v", err)
	}
	// Rows that carry a REAL sort key -- suite_item's order_key above and
	// task's own -- are already written by the calls above; grain_config
	// is the deployment's own row and the one every settings import
	// replaces.
	cfg := model.Config{}
	if stored, err := store.GetConfig(ctx); err != nil {
		t.Fatalf("reading the config: %v", err)
	} else if stored != nil {
		cfg = *stored
	}
	cfg.PromptExtension = awkward
	cfg.MaxWorkers = 7
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	// Grain's own record of what it did, the half of the schema that is
	// not settings (staterepo.SettingsTables, and notSettings in
	// tables_internal_test.go). Every one of these is written by the
	// daemon rather than proposed in a pull request, and every one of
	// them is in the dump a restore is made of.
	//
	// The pipeline gate (model.PipelinePause): paused by a person, with
	// no task exempted, which is the shape an operator's own pause takes.
	if err := store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: sub, Reason: awkward,
		By: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
	}); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	// And the upgrade that gate is sometimes held for
	// (model.ScheduledUpgrade): one branch, waiting for this deployment
	// to go idle.
	if err := store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch: "release-2026-09", RequestedAt: sub,
		By: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
	}); err != nil {
		t.Fatalf("scheduling an upgrade: %v", err)
	}
	// And the last day the daily automatic upgrade fired
	// (model.AutoUpgradeRun).
	if err := store.SetAutoUpgradeRun(ctx, model.AutoUpgradeRun{
		Day: "2026-09-04", Branch: "latest", FiredAt: sub,
	}); err != nil {
		t.Fatalf("recording an automatic upgrade: %v", err)
	}
	// A usage-limit pause in force (model.StoredUsageLimitPause), with
	// labels and an escalation so every column carries something.
	if err := store.PutUsageLimitPause(ctx, "connection|agy-1|gemini-3-pro", model.UsageLimitPause{
		Connection: "agy-1", Model: "gemini-3-pro", Labels: []string{"auto"}, Team: model.DefaultTeamID,
		Framework: "antigravity", Until: sub.Add(13 * time.Hour), Reason: "RESOURCE_EXHAUSTED",
		Escalated: "the provider's refusal is account-wide",
	}); err != nil {
		t.Fatalf("recording a usage-limit pause: %v", err)
	}
	// What grain CI last made of a branch things merge into
	// (model.BaseBranchCI): a failure, with every optional column filled
	// -- a finish time, a detail, the merge that started it, the repair
	// it earned and the second run that confirmed it -- since a row that
	// is still running would leave most of the columns NULL or empty and
	// prove nothing about them.
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Branch: "main",
		RunID: "run-9", Commit: "0123456789abcdef0123456789abcdef01234567",
		StartedAt: sub, FinishedAt: &sub, Conclusion: model.BaseBranchCIFailure,
		Detail: awkward, MergedTaskID: "a1b2", RepairTaskID: "c3d4",
		Confirmation: model.BaseCIConfirmed, ConfirmRunID: "run-10",
		ConfirmJobs:     []string{".grain/workflows/tests.yml:go", ".grain/workflows/tests.yml:ui-e2e"},
		ConfirmAttempts: 1, ConfirmDetail: awkward,
	}); err != nil {
		t.Fatalf("recording base branch CI: %v", err)
	}
	// And what grain last dispatched on that branch after a merge
	// (model.BaseBranchDispatch): a finished dispatch, so the optional
	// columns -- the finish time, the conclusion and its detail -- carry
	// something, and with two jobs on it so the delimited column holds
	// more than one element on the round trip.
	if err := store.PutBaseBranchDispatch(ctx, model.BaseBranchDispatch{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Branch: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Jobs:   []string{".grain/workflows/image.yml:guest-cache", ".grain/workflows/image.yml:base-cache"},
		RunID:  "run-13", StartedAt: sub, FinishedAt: &sub,
		Conclusion: "success", Detail: awkward,
	}); err != nil {
		t.Fatalf("recording a base branch dispatch: %v", err)
	}
	// And the release a merge into that branch set off
	// (model.BaseBranchRelease): a finished release, so the optional
	// columns -- the finish time, the conclusion and its detail -- carry
	// something on the round trip.
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Branch: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		RunID:  "run-14", StartedAt: sub, FinishedAt: &sub,
		Conclusion: "failure", Detail: awkward,
	}); err != nil {
		t.Fatalf("recording a base branch release: %v", err)
	}
	// One firing of a schedule a workflow's own cron declares
	// (model.ScheduledWorkflowRun): a finished run whose review has been
	// filed, so that every optional column carries something -- the
	// attempt and finish times, the branch and workflow resolved when the
	// run was started, and the task that reads it.
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID: "repo:schedule:owner/payments-api:workflow.nightly", Stem: "nightly",
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Branch: "main",
		Workflow: ".grain/workflows/nightly.yml", RunID: "run-12",
		FiredAt: sub, AttemptedAt: &sub, FinishedAt: &sub, Attempts: 2,
		Conclusion: "failure", Detail: awkward, ReviewTaskID: "e5f6",
	}); err != nil {
		t.Fatalf("recording a scheduled workflow run: %v", err)
	}
	// One GitHub Actions job that failed on that repository
	// (model.GitHubCIJob), with its log tail read, so every optional
	// column -- times, step, URLs, output and tail -- carries something.
	if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo: "owner/payments-api", CheckRunID: 4242, App: "github-actions", Ref: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567", Workflow: "ci", RunID: 77, RunAttempt: 1,
		JobName: "unit", Conclusion: "failure", StartedAt: &sub, CompletedAt: &sub,
		FailingStep: "go test", RunURL: "https://github.com/owner/payments-api/actions/runs/77",
		JobURL: "https://github.com/owner/payments-api/actions/runs/77/job/4242", Output: awkward,
		FailureTail: awkward, TailSource: "log", TailFetched: true, Via: "webhook",
		RecordedAt: sub, UpdatedAt: sub,
	}); err != nil {
		t.Fatalf("recording a GitHub CI job: %v", err)
	}
	// The speculative merge in flight for that same branch
	// (model.MergeBatch): a finished, failing batch with every optional
	// column filled, for the reason above -- and with several members, so
	// the JSON column carries more than one element on the round trip.
	if err := store.PutMergeBatch(ctx, model.MergeBatch{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Branch: "main",
		Ref:        model.MergeBatchRef("eu", "main"),
		BaseCommit: "0123456789abcdef0123456789abcdef01234567",
		Commit:     "89abcdef0123456789abcdef0123456789abcdef",
		Members: []model.MergeBatchMember{
			{TaskID: "a1b2", Number: 41, HeadRef: "grain/task-a1b2", HeadSHA: "1111111111111111111111111111111111111111"},
			{TaskID: "c3d4", Number: 42, HeadRef: "grain/task-c3d4", HeadSHA: "2222222222222222222222222222222222222222"},
		},
		RunID: "run-11", StartedAt: sub, FinishedAt: &sub,
		Conclusion: model.MergeBatchFailure, Detail: awkward, Splits: 1, Queued: 5,
	}); err != nil {
		t.Fatalf("recording a merge batch: %v", err)
	}
	// And one batch that has already ended (model.MergeBatchRecord): what
	// the same machinery keeps once the row above is deleted, which is the
	// only lasting record that a batch ever happened.
	if err := store.RecordMergeBatch(ctx, model.MergeBatchRecord{
		Repo:   model.RepoRef{Owner: "owner", Name: "payments-api"},
		Branch: "main", Commit: "89abcdef0123456789abcdef0123456789abcdef", RunID: "run-12",
		Size: 3, Queued: 5, Splits: 1, Outcome: model.MergeBatchCulprit, Merged: 0, PostMerge: true,
		Detail: awkward, StartedAt: sub, FinishedAt: sub, Reason: model.AbandonBaseMoved,
	}); err != nil {
		t.Fatalf("recording a finished merge batch: %v", err)
	}
	// And one red stretch of a base branch and what it held
	// (model.BaseHoldPass), finished so every column is set.
	if _, err := store.PutBaseHoldPass(ctx, model.BaseHoldPass{
		Repo:   model.RepoRef{Owner: "owner", Name: "payments-api"},
		Branch: "main", RepairTaskID: "4", StartedAt: sub, SampledAt: sub, FinishedAt: &sub,
		Held: 1, Peak: 2, HeldTasks: []string{"1", "2"}, PRSeconds: 5400,
	}); err != nil {
		t.Fatalf("recording a base branch hold: %v", err)
	}
	// And one wait that has already ended (model.MergeGatherRecord): what
	// the queue's gather window leaves behind once the hold itself, which
	// lives in the daemon's memory, is gone.
	if err := store.RecordMergeGather(ctx, model.MergeGatherRecord{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Number: 43,
		Waiting: "c3d4", Window: 5 * time.Minute, Outcome: model.MergeGatherExpired,
		ArmedAt: sub, EndedAt: sub.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("recording an ended gather hold: %v", err)
	}
	// And what the overlap re-check said about one merge
	// (model.MergeRecheckRecord).
	if err := store.RecordMergeRecheck(ctx, model.MergeRecheckRecord{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Branch: "main", Number: 44,
		TaskID: "t-44", Mode: model.MergeRecheckObserve, Head: "e5f6", Fork: "a1b2", Tip: "c3d4",
		Triggered: true, Clauses: model.MergeRecheckClauseSamePackage, Detail: "pkg/ui",
		BaseVerdict: model.MergeRecheckBaseGreen, DecidedAt: sub,
	}); err != nil {
		t.Fatalf("recording a merge re-check decision: %v", err)
	}
	// And one merge an operator forced (model.ForceMergeRecord): the
	// record left once the override itself -- a stamp on the task's own
	// observation -- has been spent by the merge it authorised.
	if err := store.RecordForceMerge(ctx, model.ForceMergeRecord{
		TaskID: "c3d4", Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, Number: 43,
		Branch: "main", Commit: "89abcdef0123456789abcdef0123456789abcdef",
		Reason: awkward, By: "ada", Skipped: awkward,
		RequestedAt: sub, MergedAt: sub.Add(time.Minute),
	}); err != nil {
		t.Fatalf("recording a forced merge: %v", err)
	}
	// And one thing the queue had to do over again
	// (model.MergeQueueContentionEvent): a base merged into a stale head.
	if err := store.RecordMergeQueueContention(ctx, model.MergeQueueContentionEvent{
		Repo: model.RepoRef{Owner: "owner", Name: "payments-api"}, TaskID: "c3d4",
		Kind: model.MergeQueueRebase, Reason: awkward, At: sub,
	}); err != nil {
		t.Fatalf("recording a merge queue rebase: %v", err)
	}
	// And one skipped firing of a schedule (model.ScheduleSkip), fired late
	// so the nullable resolved_at column carries a value.
	if _, err := store.RecordScheduleSkip(ctx, model.ScheduleSkip{
		ScheduleID: "sched-skip", Occurrence: sub, At: sub, Reason: model.SuppressedByOpenTask,
		TaskID: "c3d4", TaskState: model.StateAwaitingSubmit, Detail: awkward,
		Outcome: model.ScheduleSkipFired, ResolvedAt: &sub, FiredTaskID: "d4e5", Folded: 1, NotedTaskID: "c3d4",
	}); err != nil {
		t.Fatalf("recording a skipped firing: %v", err)
	}
	repo := model.RepoRef{Owner: "owner", Name: "payments-api"}
	// A branch grain was asked to create, a release, and the candidate
	// cut from it -- the release chain, each row a status the reconciler
	// walks.
	if _, err := store.CreateBranch(ctx, repo, "release-2026-09", sub); err != nil {
		t.Fatalf("creating a branch: %v", err)
	}
	release, err := store.CreateRelease(ctx, repo, "2026.09", sub)
	if err != nil {
		t.Fatalf("creating a release: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, release.ID); err != nil {
		t.Fatalf("provisioning the release: %v", err)
	}
	// The candidate CreateRelease cut with it -- read back rather than
	// cut again, since a release starts with one.
	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("reading the release's own candidate: %v", err)
	}
	candidate := *current
	// The qualification run that candidate gets, and the tasks it filed
	// (qualification_run, qualification_task).
	plan, err := store.GetQualificationPlan(ctx, repo)
	if err != nil || plan == nil {
		t.Fatalf("reading the qualification plan back: %v", err)
	}
	if _, err := store.CreateQualificationRun(ctx, candidate, *plan, sub); err != nil {
		t.Fatalf("creating a qualification run: %v", err)
	}
	// A suite run and its first pass (suite_run, suite_run_item,
	// suite_run_task), which also allocates task ids and so writes
	// task_sequence.
	suite, err := store.GetSuite(ctx, "suite-1")
	if err != nil || suite == nil {
		t.Fatalf("reading the suite back: %v", err)
	}
	if _, err := store.CreateSuiteRun(ctx, *suite, repo, "main", sub); err != nil {
		t.Fatalf("creating a suite run: %v", err)
	}
	// The remaining id allocators, each its own table: a number handed
	// out is a row, and a dump that lost them would hand the same id out
	// twice after a restore.
	for _, allocate := range []func(context.Context) (string, error){
		store.NewTaskID, store.NewScheduleID, store.NewTemplateID, store.NewSuiteID, store.NewExecutorTokenID,
	} {
		if _, err := allocate(ctx); err != nil {
			t.Fatalf("allocating an id: %v", err)
		}
	}

	if _, err := store.CreateExecutorToken(ctx, model.ExecutorToken{
		ID: "gxt-1", Name: awkward, TokenHash: "abcdef", Prefix: "gxt_1234", CreatedAt: sub,
	}); err != nil {
		t.Fatalf("creating an executor token: %v", err)
	}
	if _, err := store.CreateExternalAgentToken(ctx, model.ExternalAgentToken{
		Name: "ops-laptop", TokenHash: "fedcba9876543210", Prefix: "gma_1234",
		ScopeSet: "grain/task-admin", Scopes: []string{"tasks:read"}, Repos: []string{"owner/payments-api"},
		RateLimit: 60, MaxConcurrent: 8, CreatedAt: sub, ExpiresAt: &sub, CreatedBy: "owner",
	}); err != nil {
		t.Fatalf("creating an external agent token: %v", err)
	}

	// A handful of tables nothing above reaches, written straight through
	// SQL: they exist in the dump, and a value that does not survive them
	// is as lost as one that does not survive a task.
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO `task_tag` (`task_id`, `tag`) VALUES (?, ?)", []any{"a1b2", awkward}},
		{"INSERT INTO `work_origin_comment` (`origin`, `comment_id`, `body`) VALUES (?, ?, ?)",
			[]any{"owner/docs#7", 12345, awkward}},
		{"INSERT INTO `task_link` (`task_id`, `kind`, `target`, `blocks`) VALUES (?, ?, ?, ?)",
			[]any{"a1b2", "depends_on", "c3d4", 1}},
		{"INSERT INTO `task_read` (`task_id`, `owner`, `name`) VALUES (?, ?, ?)",
			[]any{"a1b2", "owner", "docs"}},
		{"INSERT INTO `task_grant` (`task_id`, `capability`, `via`, `folder`) VALUES (?, ?, ?, ?)",
			[]any{"a1b2", "githubtoken", "", ""}},
		{"INSERT INTO `task_pause` (`task_id`, `paused_at`, `paused_by_kind`, `paused_by_id`, `from_state`, `immediate`, `run_id`) VALUES (?, ?, ?, ?, ?, ?, ?)",
			[]any{"a1b2", time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC), "human", awkward, "running", 1, "run-1"}},
		{"INSERT INTO `secret_resolution_audit` (`timestamp`, `task_id`, `run_id`, `secret_name`, `key`, `principal`) VALUES (?, ?, ?, ?, ?, ?)",
			[]any{time.Now().UTC(), "a1b2", "run-1", "sec", "key", "admin"}},
		{"INSERT INTO `credential_access_audit` (`timestamp`, `task_id`, `run_id`, `sandbox`, `kind`, `resource`, `action`, `credential`, `outcome`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			[]any{time.Now().UTC(), "a1b2", "run-1", "sandbox-1", "git", "owner/docs", "git-upload-pack", "team-default", "forwarded: 200"}},
		{"INSERT INTO `notification` (`subject`, `family`, `severity`, `title`, `body`, `task_id`, `raised_by`, `raised_at`, `last_raised_at`, `raise_count`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			[]any{"pipeline.base-red:owner/docs@main", "pipeline.base-red", "warning", awkward, awkward, "a1b2", "automation:test", time.Now().UTC(), time.Now().UTC(), 2}},
		{"INSERT INTO `task_notification_subscription` (`task_id`, `family`) VALUES (?, ?)",
			[]any{"a1b2", "pipeline.*"}},
		{"INSERT INTO `task_goal` (`task_id`, `kind`, `spec`, `on_met`, `declared_by`, `declared_at`, `evidence`, `progress_at`, `max_resumes`, `max_stall`, `on_limit`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			[]any{"a1b2", "base-green", `{"repo":"owner/docs","branch":"main"}`, "close", "automation:task-goals", time.Now().UTC(), awkward, time.Now().UTC(), 6, 43200, "hold"}},
		{"INSERT INTO `tuning_recommendation` (`target_parameter`, `proposed_value`, `cooldown_expiry`, `adopted`, `task_id`, `created_at`) VALUES (?, ?, ?, ?, ?, ?)",
			[]any{"merge_batch_size", "4", "2026-10-12T00:00:00Z", 0, "a1b2", "2026-10-10T12:00:00Z"}},
	} {
		if _, err := db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("writing %s: %v", stmt.sql, err)
		}
	}
}

// tables_internal_test.go checks that the export writes a file for every
// table the schema has. This is the converse: that it writes a file for
// nothing else. A view exported as a table would be imported back as one
// -- pkg/model derives task_state as a view precisely so that nothing
// writes it -- and a file left behind by a table a later build dropped
// would restore rows nothing wrote.
func TestTheDumpHoldsNoFileThatNamesNoTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	fill(t, ctx, store, db)
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	known := map[string]bool{}
	for _, table := range realTables(t, ctx, db) {
		known[table+".json"] = true
	}
	entries, err := os.ReadDir(filepath.Join(dir, staterepo.TablesDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the dump is empty, so nothing here is checking anything")
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("the dump holds %s, which names no table this build has", e.Name())
		}
	}
	// And a stale file really is removed rather than merely not written:
	// a table a later build drops must not leave its rows behind for the
	// build after that to import.
	stale := filepath.Join(dir, staterepo.TablesDir, "task_template.json")
	if err := os.WriteFile(stale, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("re-exporting: %v", err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the export left behind a file naming a table this build does not have")
	}
}
