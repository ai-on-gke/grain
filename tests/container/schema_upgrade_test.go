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

package container

// The upgrade that changes this deployment's database schema, run
// against real images (grain/task-90, and grain/task-59 for the
// mechanism itself).
//
// Everything else about a schema-changing upgrade is already covered a
// layer at a time: pkg/upgrade/schema_test.go pins the argv with docker
// stubbed out, and pkg/cli/daemon_schema_migration_test.go runs the
// migration itself against a real store and a real state repository.
// What neither can reach is the pair of couplings that only exist
// because the daemon is in a container and the build it is rehearsing
// against is in another one:
//
//	the mount. `docker run --volume <dir>:<dir>:ro` (pkg/upgrade's
//	imageRunner.rehearse) works only because scripts/setup.sh
//	bind-mounts $GRAIN_DATA_DIR at its own path, so a path the daemon
//	names inside its container is a path the host's docker daemon can
//	find. Nothing fails when that stops being true: the rehearsal simply
//	starts failing every schema-changing upgrade, which reads exactly
//	like "the new build cannot take these rows"
//
//	the entrypoint. `grain state check -migrate <dir>` has to run as the
//	image's own ENTRYPOINT (Dockerfile), against a dump written by a
//	*different* build
//
// So this file runs the whole of it: a deployment with rows in it, an
// image at another schema pulled out of the throwaway registry, the
// rehearsal in a container of that image over a directory of this host's
// filesystem, the cut-over, and then the deployment brought back up on
// the new image to see whether the rows arrived.
//
// Three tests, because "the new build can take these rows" has one happy
// answer and two unhappy ones, and a deployment meets all three:
//
//	the migration itself, onto a build whose schema change is one the
//	importer absorbs -- a nullable column added to `task`, which is the
//	commonest shape a bump actually takes. The rows have to arrive
//	(grain/task-119)
//
//	the refusal from the floor: a build whose single-tenant baseline
//	(model.BaselineSchemaVersion) is above the dump this deployment
//	exported, which is the startup guard refusing a store from before
//	the reset before a single row is read
//
//	the refusal from the import: a change the importer cannot absorb and
//	dumpFixups has no entry for -- a NOT NULL column with no default,
//	which fails on the INSERT of a row that has nothing to put in it.
//	Distinct from the one above and worth its own test, because it is
//	the failure that arrives *through* a successful-looking migration
//	plan rather than in front of it (grain/task-119)
//
// The second image is this checkout's own source with a constant moved
// and, where the test is about the rows rather than about the version
// comparison, a column added to the `task` table.
// `go build -overlay` is what makes that cheap -- no copy of the
// tree, nothing written into it -- and the result is dropped over
// /usr/local/bin/grain in a layer on top of the image under test, so the
// two builds differ in exactly the thing being tested and in nothing
// else. It does mean these tests are only as true as the checkout and
// GRAIN_TEST_IMAGE being the same commit, which is how CI runs them
// (.github/workflows/build-artifacts.yml builds the image and then
// drives this suite from the checkout it was built from). What they do
// not assume is agreement about the schema number itself: the image
// under test is *asked* which schema it knows, and the second image is
// patched to one past that answer, rather than either being read out of
// pkg/model here.
//
// One thing that build is not: `make build`. It runs `go build` alone,
// so the binary it produces carries pkg/ui/static as the checkout has it
// -- the tracked placeholder, not a Vite build -- and the deployment
// that comes up on it serves the API and no frontend. That is all these
// tests ask of it, and paying for `npm ci && npm run build` here would
// buy a UI nothing looks at.

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A schema change carried across by the Upgrade button, with nothing
// stubbed.
//
// The deployment has a task in it, the build it is upgrading to knows a
// schema one ahead of its own, and what has to survive is the task. In
// order: the running build exports its rows, the *new* image imports
// them at its own schema in a container that touches nothing, the ref
// file moves, and the container that comes up next -- the new image,
// over the same data directory -- finds the store it cannot open, the
// note beside it, and rebuilds itself out of the export.
func TestASchemaChangingUpgradeCarriesTheDeploymentsRowsOntoTheNewImage(t *testing.T) {
	requireImage(t)
	requireGoToolchain(t)
	requireGit(t)
	repo := registry(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	refFile := filepath.Join(root, "data", "image.env")
	restart := filepath.Join(control, "restart")
	writeRef(t, refFile, repo+":v-one")

	// The build the deployment is on, asked the way the upgrade asks it.
	from := imageSchemaVersion(t, image)
	to := from + 1
	// A bump with an actual column in it, of the shape the importer
	// absorbs: this is the difference between proving that rows cross a
	// schema change and proving that a number does (taskColumnEdit).
	next := buildImageWithEdits(t, "next-schema",
		schemaVersionEdit(to), taskColumnEdit("`e2e_note` TEXT NULL"))

	d := upgradeDaemon(t, root, repo, refFile, restart)
	// More than one row, and more than one table: a comment lands in a
	// table of its own, so a migration that carried tasks and dropped
	// everything hanging off them would still satisfy a test that only
	// asked after a task.
	filed := createTask(t, d, "a task filed before the schema changed")
	alsoFiled := []task{
		createTask(t, d, "a second task, so one lost row is visible"),
		createTask(t, d, "a third, filed the moment before the upgrade"),
	}
	status, body := d.api(t, http.MethodPost, "/api/tasks/"+filed.ID+"/comments",
		map[string]string{"body": "a comment that has to survive the schema change"})
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("commenting: %d %s", status, body)
	}

	final := upgradeTo(t, d, "next-schema")
	if final.Phase != "ok" {
		t.Fatalf("the upgrade onto a build at schema %d ended %q: %s\n%s", to, final.Phase, final.Detail, d.logs())
	}
	// The Detail line is what the pane shows an operator, and a schema
	// change is the one thing about an upgrade they cannot see coming.
	if want := fmt.Sprintf("migrating this deployment from schema %d to %d", from, to); !strings.Contains(final.Detail, want) {
		t.Errorf("the upgrade does not say it migrated the deployment (%q): %s", want, final.Detail)
	}
	// Pinned to the digest the pull fetched, as every image-mode upgrade
	// is (TestAnUpgradePullsABranchTagAndRepointsTheDeployment).
	pinned := next + "@" + digestOf(t, repo, next)
	if got := readRef(t, refFile); got != "GRAIN_IMAGE="+pinned {
		t.Fatalf("the ref file reads %q, want it pinned to %s", got, pinned)
	}

	// The mount the whole rehearsal rests on, in the words the daemon
	// logged as it ran it: the export handed to a container of the new
	// image at the path it has out here, read-only, with `state check
	// -migrate` as the image's own entrypoint. Asserted rather than
	// inferred from the upgrade having succeeded, because an upgrade
	// that skipped the rehearsal entirely would succeed too -- and this
	// is the line that stops working if a deployment ever stops
	// bind-mounting its data directory at its own path.
	dump := filepath.Join(root, "data", "schema-upgrade")
	logs := d.logs()
	for _, want := range []string{
		"--volume " + dump + ":" + dump + ":ro",
		"state check -migrate " + dump,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("the daemon never ran the migration rehearsal with %q:\n%s", want, logs)
		}
	}

	// The export the outgoing build made for the incoming one, still on
	// the host between the two: this is the directory the rehearsal was
	// handed, and the one the new build's first start reads.
	note := filepath.Join(dump, "upgrade.json")
	raw, err := os.ReadFile(note)
	if err != nil {
		t.Fatalf("the upgrade left no note for the build it cut over to: %v", err)
	}
	for _, want := range []string{`"from": ` + strconv.Itoa(from), `"to": ` + strconv.Itoa(to)} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s does not say %s:\n%s", note, want, raw)
		}
	}

	// And now the restart the upgrade asked for, which on a real host is
	// systemd bringing the unit back up on whatever the ref file names.
	waitFor(t, "the restart request the upgrade ends with", 60*time.Second, func() error {
		_, err := os.Stat(restart)
		return err
	})
	d.stop()

	// Started from what the ref file names rather than from the tag this
	// test pushed: that line is the whole cut-over, and a restart that
	// came up on anything else would not be the one systemd performs.
	back := start(t, root, options{image: refFileImage(t, refFile)})

	status, body = back.api(t, http.MethodGet, "/api/tasks/"+filed.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("the task this deployment was running on is gone after the upgrade: %d %s\n%s",
			status, body, back.logs())
	}
	var fetched task
	decode(t, body, &fetched)
	if fetched.Title != "a task filed before the schema changed" {
		t.Errorf("the task came back as %q", fetched.Title)
	}
	var kept bool
	for _, comment := range fetched.Comments {
		if strings.Contains(comment.Body, "has to survive the schema change") {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the comment on that task did not come across: %s", body)
	}

	_, body = back.api(t, http.MethodGet, "/api/tasks", nil)
	var listed []task
	decode(t, body, &listed)
	for _, want := range alsoFiled {
		if !slicesContainsID(listed, want.ID) {
			t.Errorf("%s (%q) did not survive the migration: %s", want.ID, want.Title, body)
		}
	}

	// The store the new build could not open is moved aside, never
	// deleted: it is the only copy of anything the migration could not
	// carry, or was written in the seconds between the export and the
	// restart.
	archived, err := filepath.Glob(filepath.Join(root, "data", fmt.Sprintf("store.schema%d-*", from)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 {
		t.Errorf("the store written at schema %d was not archived beside the new one: %v", from, archived)
	}
	// The state repository holds the same rows at the same old schema, so
	// it gets the same treatment one step later (migrateStateRepo in
	// pkg/cli/staterepo.go) -- and the commit it leaves has to be the
	// one saying the rows came across rather than the one saying they
	// were thrown away. The two diffs are identical, which is exactly why
	// the messages differ, and this is the only place that difference is
	// ever checked against a real repository written by a real upgrade.
	subjects := gitSubjects(t, filepath.Join(root, "data", "state-repo"))
	migrated := fmt.Sprintf("Migrate grain state to schema %d", to)
	reformatted := fmt.Sprintf("Reformat grain state for schema %d", to)
	if !slices.Contains(subjects, migrated) {
		t.Errorf("the state repository has no %q commit:\n%s", migrated, strings.Join(subjects, "\n"))
	}
	if slices.Contains(subjects, reformatted) {
		t.Errorf("the state repository was reformatted rather than migrated -- the rows in it are gone:\n%s",
			strings.Join(subjects, "\n"))
	}

	// And the note is spent -- one left lying about would migrate some
	// later restart onto rows that stopped describing this deployment
	// hours ago.
	if _, err := os.Stat(filepath.Dir(note)); !os.IsNotExist(err) {
		t.Errorf("%s is still there after being acted on (%v)", filepath.Dir(note), err)
	}
}

// A rehearsal that fails must leave the deployment exactly where it was.
//
// This is the whole point of running the import in the new image *before*
// the ref file is touched: a build that cannot take this deployment's
// rows is not a build to hand the deployment to, and the failure has to
// look like nothing happening rather than like a deployment that will not
// come up. The refusal here is a real one out of pkg/staterepo -- the
// image is built with its baseline above the schema this deployment's
// dump is written at, which is what a deployment from before the
// single-tenant reset meets when it is upgraded onto a build after it.
func TestAnUpgradeToABuildThatRefusesTheseRowsChangesNothing(t *testing.T) {
	requireImage(t)
	requireGoToolchain(t)
	repo := registry(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	refFile := filepath.Join(root, "data", "image.env")
	restart := filepath.Join(control, "restart")
	writeRef(t, refFile, repo+":v-one")

	from := imageSchemaVersion(t, image)
	to := from + 1
	// The baseline sits below SchemaVersion once a schema change has
	// landed after the reset, so both move: a build whose baseline passed
	// this deployment's schema while its SchemaVersion stood still would
	// read as no schema change at all, and the upgrade would never ask
	// it to import anything.
	buildImageWithEdits(t, "unmigratable-schema", schemaVersionEdit(to), baselineEdit(to))

	d := upgradeDaemon(t, root, repo, refFile, restart)
	filed := createTask(t, d, "a task the new build will not take")

	final := upgradeTo(t, d, "unmigratable-schema")
	if final.Phase != "failed" {
		t.Fatalf("an upgrade to a build that refuses these rows ended %q: %s\n%s",
			final.Phase, final.Detail, d.logs())
	}
	// A refusal, or a container that never finished? Asked before the
	// wording is, because they are the same Phase and the loop below
	// would report the second as the first (see failIfTheRunNeverFinished).
	failIfTheRunNeverFinished(t, d, final)
	// Which failure it was, in the words the pane shows: not "the image
	// is broken" but "that build could not take this deployment's rows".
	for _, want := range []string{"could not take this deployment's rows", "single-tenant baseline"} {
		if !strings.Contains(final.Detail, want) {
			t.Errorf("the failure does not say %q: %s", want, final.Detail)
		}
	}

	// Nothing moved. The ref file is the deployment, and this one still
	// names the build that is serving.
	if got := readRef(t, refFile); got != "GRAIN_IMAGE="+repo+":v-one" {
		t.Errorf("a refused upgrade rewrote the ref file to %q", got)
	}
	if _, err := os.Stat(restart); err == nil {
		t.Error("a refused upgrade still asked for a restart")
	}
	// Including the note: one left armed behind a failed upgrade is a
	// migration waiting to happen at some unrelated later restart.
	if _, err := os.Stat(filepath.Join(root, "data", "schema-upgrade")); !os.IsNotExist(err) {
		t.Errorf("the export made for the refused build is still armed (%v)", err)
	}

	// And the deployment is still serving, on the build that wrote those
	// rows -- which is the only build that can.
	status, body := d.api(t, http.MethodGet, "/api/tasks/"+filed.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("the deployment is not still serving its rows: %d %s", status, body)
	}
}

// The other refusal: a schema change the importer itself cannot absorb.
//
// The test above is refused before a row is read -- Migratable saying the
// dump is older than this build will look back. This one is refused in
// the middle of reading them: a new NOT NULL column with no default and
// no dumpFixups entry, so every INSERT built out of a dump that has no
// value for it fails on the constraint. That is the case dumpFixups
// exists for and the case nobody has written one for yet, and the
// deployment's only defence is that the rehearsal runs before the ref
// file moves.
//
// Both refusals have to look identical from the outside -- nothing
// happened, the old build is still serving -- and the assertion that
// they failed at the *schema* step rather than somewhere convenient is
// what makes that mean anything: an upgrade that fell over on the pull
// would leave a deployment that looks exactly this untouched.
func TestASchemaChangeTheImporterCannotAbsorbChangesNothing(t *testing.T) {
	requireImage(t)
	requireGoToolchain(t)
	repo := registry(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	refFile := filepath.Join(root, "data", "image.env")
	restart := filepath.Join(control, "restart")
	writeRef(t, refFile, repo+":v-one")

	from := imageSchemaVersion(t, image)
	to := from + 1
	buildImageWithEdits(t, "unimportable-schema",
		schemaVersionEdit(to), taskColumnEdit("`e2e_required` TEXT NOT NULL"))

	d := upgradeDaemon(t, root, repo, refFile, restart)
	// A row in the table the new column is on. An empty table imports
	// perfectly happily whatever its columns say, so a deployment with
	// nothing in it would prove nothing here.
	filed := createTask(t, d, "a task the new build cannot import")

	final := upgradeTo(t, d, "unimportable-schema")
	if final.Phase != "failed" {
		t.Fatalf("an upgrade to a build that cannot import these rows ended %q: %s\n%s",
			final.Phase, final.Detail, d.logs())
	}
	// The same question this upgrade's twin above asks, and for the same
	// reason: a `docker run` killed on its bound fails at the health
	// check, which leaves the schema step "pending" and the check below
	// reporting "something else failed this upgrade" about a step that
	// was never reached.
	failIfTheRunNeverFinished(t, d, final)
	state, detail, ok := final.step("schema")
	if !ok {
		t.Fatalf("the upgrade ran no schema step at all: %s", final.Detail)
	}
	if state != "failed" {
		t.Fatalf("the schema step ended %q, so something else failed this upgrade: %s\n%s",
			state, final.Detail, detail)
	}
	// And refused where this test means it to be refused: in the import,
	// on a row, rather than by Migratable before one was read. Without
	// this the test would go on passing if the refusal moved -- if
	// somebody wrote the dumpFixup this column has none of, or raised
	// the baseline -- while no longer covering the case it is
	// named for. staterepo's own wording, not SQLite's, so the assertion
	// is about grain rather than about a driver's error text.
	for _, want := range []string{"could not take this deployment's rows", "inserting task row"} {
		if !strings.Contains(final.Detail, want) {
			t.Errorf("the failure does not say %q: %s", want, final.Detail)
		}
	}

	// Nothing moved, in the same three places the test above checks: the
	// ref file, the restart request, and the note.
	if got := readRef(t, refFile); got != "GRAIN_IMAGE="+repo+":v-one" {
		t.Errorf("a refused upgrade rewrote the ref file to %q", got)
	}
	if _, err := os.Stat(restart); err == nil {
		t.Error("a refused upgrade still asked for a restart")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "schema-upgrade")); !os.IsNotExist(err) {
		t.Errorf("the export made for the refused build is still armed (%v)", err)
	}
	// And the store is untouched: an upgrade that got as far as archiving
	// it would be one this deployment could not come back up from.
	archived, err := filepath.Glob(filepath.Join(root, "data", fmt.Sprintf("store.schema%d-*", from)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 0 {
		t.Errorf("a refused upgrade archived the store anyway: %v", archived)
	}

	status, body := d.api(t, http.MethodGet, "/api/tasks/"+filed.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("the deployment is not still serving its rows: %d %s", status, body)
	}
	if final.Running.Image != repo+":v-one" {
		t.Errorf("the deployment reports running %q, want the build it started on", final.Running.Image)
	}
}

// --- telling a bound apart from a refusal --------------------------------

// failIfTheRunNeverFinished ends the test here, in words that say what
// actually happened, when the upgrade it is handed died on one of
// pkg/upgrade's bounds rather than on the refusal the caller is about to
// assert the wording of.
//
// The two tests above exist to check *which* refusal an upgrade
// reported, so both of them go on from a failed upgrade to assert on the
// text of the failure. That is right when the new build refused these
// rows and wrong when the upgrade never got that far: a `docker run` of
// the image that was killed on its bound is also Phase "failed", with a
// Detail that says nothing either assertion is looking for -- so the
// tests reported it as "the failure does not say \"migrates from\"" and
// as "the schema step ended \"pending\", so something else failed this
// upgrade". Both read as the upgrade-refusal logic being broken. It was
// not; it never ran, and two release runs had to be compared side by
// side before anybody could see that (grain/task-341).
//
// So the bound is reported as itself: still a failure, because a
// deployment that cannot health-check an image it has just pulled is a
// real problem, but a failure naming the bound and what the bound
// covers, which is a container's whole life and not the subcommand
// inside it.
func failIfTheRunNeverFinished(t *testing.T, d *Daemon, s upgradeStatus) {
	t.Helper()
	step, detail, ok := s.ranOutOfTime()
	if !ok {
		return
	}
	what, where := "one of this upgrade's container runs", "it"
	if step != "" {
		what = fmt.Sprintf("the %q step", step)
		where = "its `docker run` of the image this upgrade had just pulled"
	}
	t.Fatalf("%s never completed, so this upgrade never reached the refusal this test "+
		"is about: %s was killed on its bound. Nothing refused these rows and there is no wording "+
		"here to assert on -- this is not the upgrade logic failing.\n"+
		"The bound is pkg/upgrade/image.go's imageRunTimeout (every `docker run` of the newly "+
		"pulled image) or schemaCheckTimeout (the migration rehearsal), and what it covers is a "+
		"whole container's life: docker creating one over the layers of a multi-gigabyte image, "+
		"running it, and deleting its writable layer for `--rm`. The subcommand inside it answers "+
		"in milliseconds. A host busy building images is what expires it, and this suite runs on "+
		"one -- .grain/workflows/release.yml builds three and then drives these tests.\n"+
		"the upgrade's failure, in full: %s\n%s", what, where, detail, d.logs())
}

// The two failures this suite must not confuse, read the way
// failIfTheRunNeverFinished reads them -- from an upgradeStatus and
// nothing else, since that is all these tests can see of an upgrade.
//
// Fixtures rather than a real slow image: making a `docker run` overrun
// a two-minute bound on purpose would cost this suite two minutes and a
// contrived image, and what is under test here is the reading, not the
// bound. pkg/upgrade/image_test.go's
// TestAnImageThatAnswersAndThenHangsFailsTheHealthCheckOnItsBound is
// where the bound itself is exercised.
func TestATimedOutContainerRunIsNotReadAsARefusedUpgrade(t *testing.T) {
	timedOut := upgradeStatus{
		Phase: "failed",
		Detail: "health check failed on the pulled image, leaving this deployment on the image it " +
			"already runs: docker run --rm localhost:41227/grain:unimportable-schema schema-version: " +
			"it ran out of time after 2m0s and was killed (signal: killed): 18",
	}
	timedOut.Steps = append(timedOut.Steps, upgradeStep{Name: "pull image", State: "ok"},
		upgradeStep{Name: "health check", State: "failed"}, upgradeStep{Name: "schema", State: "pending"})

	// The wording before grain/task-341, which a deployment still on an
	// older image produces: the same reading has to survive it, since the
	// image under test is not always this checkout's build of it.
	older := timedOut
	older.Detail = strings.Replace(timedOut.Detail, "ran out of time after 2m0s", "ran out of time", 1)

	// And the other half of the pair: a question asked for its answer,
	// which pkg/upgrade/output.go's askError words differently.
	asked := timedOut
	asked.Detail = "asking the build being upgraded to which schema it knows: docker run --rm " +
		"localhost:41227/grain:unimportable-schema schema-version: no answer within 2m0s, so it " +
		"was killed (signal: killed)"

	for _, s := range []upgradeStatus{timedOut, older, asked} {
		gotStep, gotDetail, ok := s.ranOutOfTime()
		if !ok {
			t.Errorf("a run killed on its bound was not read as one: %s", s.Detail)
			continue
		}
		if gotStep != "health check" {
			t.Errorf("the bound expired at %q, want the step that actually failed", gotStep)
		}
		if gotDetail != s.Detail {
			t.Errorf("detail = %q, want the failure in full", gotDetail)
		}
	}

	// A real refusal is not one, and this is the assertion that matters:
	// a reading that called *this* a timeout would hide the failure these
	// tests exist to catch.
	refused := upgradeStatus{
		Phase: "failed",
		Detail: "the build being upgraded to could not take this deployment's rows: importing: " +
			"inserting task row: NOT NULL constraint failed: task.e2e_required",
	}
	refused.Steps = append(refused.Steps, upgradeStep{Name: "pull image", State: "ok"},
		upgradeStep{Name: "schema", State: "failed"})
	if _, _, ok := refused.ranOutOfTime(); ok {
		t.Errorf("a build refusing these rows was read as a bound expiring: %s", refused.Detail)
	}
}

// --- building an image at another schema --------------------------------

// requireGoToolchain gates the two tests above on the one thing the rest
// of this suite does not need: a Go toolchain to build the second
// image's binary with. repoRoot is the other half of that gate, the
// source to build.
//
// Skipped rather than failed, like every other gate here -- though both
// halves are ordinarily present by construction, since running this
// suite at all means compiling it out of this checkout.
func requireGoToolchain(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain, so no second image at another schema can be built")
	}
}

// requireGit gates the one assertion here that reads the state
// repository as a repository rather than through the daemon: which
// commit a migration left in it.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git, so the state repository's own history cannot be read")
	}
}

// gitSubjects is every commit subject in a repository, newest first.
func gitSubjects(t *testing.T, dir string) []string {
	t.Helper()
	log := exec.Command("git", "log", "--format=%s")
	log.Dir = dir
	out, err := log.CombinedOutput()
	if err != nil {
		t.Fatalf("git log in %s: %v\n%s", dir, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// sourceEdit is one substitution made to this checkout's source on the
// way into a test image.
//
// Matched by pattern rather than by literal text, because the number
// these two patterns match is exactly the thing that changes when
// somebody bumps the schema, and a literal would have to be edited in
// step with every such bump. A pattern that matches anything other than
// once fails the test loudly (buildImageWithEdits), so a declaration
// that moves or changes shape is still reported here rather than
// quietly leaving the tests building an image identical to the one they
// are upgrading from.
type sourceEdit struct {
	// path is relative to the repository root.
	path    string
	pattern *regexp.Regexp
	with    string
	// added turns this from a substitution into a whole file the build
	// sees and the checkout does not, with `with` as its contents.
	// `go build -overlay` adds a file to a package as readily as it
	// replaces one, and a fixture that has to *add* something to a
	// declaration -- a column to a table -- is better off writing a file
	// of its own than teaching a regexp to edit Go source that
	// concatenates backtick-quoted identifiers.
	added bool
}

// schemaVersionEdit moves model.SchemaVersion, which is the whole of what
// makes an upgrade a schema-changing one: it is what `grain
// schema-version` prints, what pkg/upgrade compares against the running
// build's, and what the store a build creates is stamped with.
func schemaVersionEdit(version int) sourceEdit {
	return sourceEdit{
		path:    filepath.Join("pkg", "model", "schema.go"),
		pattern: regexp.MustCompile(`(?m)^const SchemaVersion = \w+$`),
		with:    fmt.Sprintf("const SchemaVersion = %d", version),
	}
}

// baselineEdit raises model.BaselineSchemaVersion to version, which
// makes the resulting build refuse a store or a dump stamped with
// anything older -- including the one the deployment under test exports
// for it -- as one from before the single-tenant reset.
func baselineEdit(version int) sourceEdit {
	return sourceEdit{
		path:    filepath.Join("pkg", "model", "schema.go"),
		pattern: regexp.MustCompile(`(?m)^const BaselineSchemaVersion = \d+$`),
		with:    fmt.Sprintf("const BaselineSchemaVersion = %d", version),
	}
}

// taskColumnEdit adds one column to the `task` table, which is what
// makes a bumped constant an actual schema change: without it the two
// builds' databases are identical and only the number they are stamped
// with differs, so the import proves the version comparison and nothing
// about whether rows can cross.
//
// Which column is the whole difference between the two answers a
// rehearsal can give. `TEXT NULL` is a change staterepo.Import already
// absorbs -- a column the dump has no value for is simply not named in
// the INSERT -- and `TEXT NOT NULL` with no default is one it cannot,
// since that same INSERT then fails on a constraint. Neither has a
// dumpFixups entry, because neither version exists.
//
// It is applied at init time, over the DDL string, rather than as an
// edit to schema.go: that file builds its statements out of Go source
// concatenating backtick-quoted identifiers, and a fixture patching
// *that* would break whenever somebody reformatted a table. This one
// makes the same change to the same statement and says so out loud when
// its anchor stops matching.
func taskColumnEdit(column string) sourceEdit {
	return sourceEdit{
		path:  filepath.Join("pkg", "model", "zz_container_e2e_column.go"),
		added: true,
		with: `package model

import "strings"

const e2eTaskTable = "CREATE TABLE IF NOT EXISTS ` + "`task`" + ` ("
const e2eTaskColumn = ` + strconv.Quote(column) + `

func init() {
	for i, ddl := range Tables {
		if !strings.HasPrefix(ddl, e2eTaskTable) {
			continue
		}
		patched := strings.Replace(ddl, "  PRIMARY KEY (", "  "+e2eTaskColumn+",\n  PRIMARY KEY (", 1)
		if patched == ddl {
			panic("container e2e fixture: the task table has no PRIMARY KEY line to add a column before")
		}
		Tables[i] = patched
		return
	}
	panic("container e2e fixture: no task table to add a column to")
}
`,
	}
}

// buildImageWithEdits builds an image that is the one under test with a
// `grain` built from this checkout's source, edits applied, laid over it
// -- and pushes it to the throwaway registry under tag, so that the
// deployment reaches it exactly as it reaches any other published branch
// (an ordinary `docker pull` of repository:tag).
//
// Only the binary is replaced. Everything else the deployment runs -- the
// entrypoint, tini, git, the docker CLI -- stays whatever the image under
// test carries, because those are not what is being varied and an image
// assembled some other way would not be the image a deployment runs.
func buildImageWithEdits(t *testing.T, tag string, edits ...sourceEdit) string {
	t.Helper()
	root := repoRoot(t)
	staging := t.TempDir()

	// -overlay rather than a copy of the tree or an edit in place: the
	// build sees the changed files and the checkout never does, so a
	// failed run leaves nothing behind to explain.
	overlay := map[string]string{}
	for i, edit := range edits {
		original := filepath.Join(root, edit.path)
		if edit.added {
			// Refused rather than written over: a file that has appeared
			// in the checkout under this name means the fixture is now
			// deleting somebody's code, which is not what any caller
			// here is asking for.
			if _, err := os.Stat(original); err == nil {
				t.Fatalf("%s exists in the checkout, and this test adds it -- rename the file the "+
					"fixture builds with", edit.path)
			}
			staged := filepath.Join(staging, fmt.Sprintf("add-%d-%s", i, filepath.Base(edit.path)))
			if err := os.WriteFile(staged, []byte(edit.with), 0o644); err != nil {
				t.Fatalf("writing the added %s: %v", edit.path, err)
			}
			overlay[original] = staged
			continue
		}
		// A second edit to the same file patches what the first left,
		// rather than the checkout's copy over the top of it.
		from := original
		if staged, ok := overlay[original]; ok {
			from = staged
		}
		source, err := os.ReadFile(from)
		if err != nil {
			t.Fatalf("reading %s to patch it: %v", edit.path, err)
		}
		if found := edit.pattern.FindAllString(string(source), -1); len(found) != 1 {
			t.Fatalf("%s matches %s %d times, want exactly one -- this test patches a declaration that "+
				"has moved or changed shape", edit.path, edit.pattern, len(found))
		}
		patched := filepath.Join(staging, fmt.Sprintf("edit-%d-%s", i, filepath.Base(edit.path)))
		if err := os.WriteFile(patched, edit.pattern.ReplaceAll(source, []byte(edit.with)), 0o644); err != nil {
			t.Fatalf("writing the patched %s: %v", edit.path, err)
		}
		overlay[original] = patched
	}
	overlayFile := filepath.Join(staging, "overlay.json")
	if err := os.WriteFile(overlayFile, []byte(overlayJSON(overlay)), 0o644); err != nil {
		t.Fatalf("writing the build overlay: %v", err)
	}

	// CGO_ENABLED=0 for the reason the Makefile gives: a genuinely static
	// binary, with no libc coupling to the machine that built it -- which
	// here is the runner rather than the image's own build stage.
	// -buildvcs=false because the commit stamp is not what is being
	// tested and reading it needs a .git this process can vouch for.
	build := exec.Command("go", "build", "-buildvcs=false",
		"-overlay", overlayFile, "-o", filepath.Join(staging, "grain"), "./cmd/grain")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building a grain at another schema: %v\n%s", err, out)
	}

	dockerfile := fmt.Sprintf("FROM %s\nCOPY grain /usr/local/bin/grain\n", image)
	if err := os.WriteFile(filepath.Join(staging, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("writing the Dockerfile for %s: %v", tag, err)
	}
	local := "grain-e2e-" + tag + ":" + randomSuffix(t, 8)
	docker(t, "build", "--tag", local, staging)
	t.Cleanup(func() { _, _ = tryDocker("rmi", "--force", local) })

	ref := registry(t) + ":" + tag
	docker(t, "tag", local, ref)
	docker(t, "push", ref)
	return ref
}

// overlayJSON is the file `go build -overlay` reads: which real path is
// answered by which replacement file.
func overlayJSON(replace map[string]string) string {
	var pairs []string
	for from, to := range replace {
		pairs = append(pairs, fmt.Sprintf("%q: %q", from, to))
	}
	return `{"Replace": {` + strings.Join(pairs, ", ") + `}}`
}

// repoRoot is the checkout this test was built from, which is where `go
// build` has to run and where the source being patched lives.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locating the checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("no checkout to build a second image from (%v)", err)
	}
	return root
}

// imageSchemaVersion asks an image which schema it knows, by running the
// subcommand an upgrade's own health check runs in it.
func imageSchemaVersion(t *testing.T, ref string) int {
	t.Helper()
	out := docker(t, "run", "--rm", ref, "schema-version")
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	version, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("%s answered %q when asked which schema it knows", ref, out)
	}
	return version
}

// refFileImage is the image the deployment would come up on next: the ref
// file's own line, read the way grain-daemon.service reads it.
func refFileImage(t *testing.T, path string) string {
	t.Helper()
	ref, ok := strings.CutPrefix(readRef(t, path), "GRAIN_IMAGE=")
	if !ok {
		t.Fatalf("%s names no image", path)
	}
	return ref
}
