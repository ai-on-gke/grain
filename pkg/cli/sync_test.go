package cli

// sync_test.go exercises cmdSync's "settings" path end to end against a
// real embedded SQLite store -- the same "nothing here is a fake standing
// in for the store" discipline cmd/grain's own tests are held to. The "gcp" path is pkg/gcpsetup's own, covered network-
// free by gcpsetup_test.go; syncGCP itself is exercised for its
// validation only, the same boundary setup_test.go draws for
// cmdSetupGCP.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func writeSyncConfig(t *testing.T, dir string, cfg syncConfig) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sync.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// syncTestServer is an embedded store behind the same UI server a daemon
// runs, and the HTTP client `grain sync` reaches it with.
func syncTestServer(t *testing.T) (string, *ui.HTTPClient) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(
		ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()}), store))
	t.Cleanup(srv.Close)
	return srv.URL, accessCodeClient(srv.URL)
}

func TestCmdSyncRequiresConfig(t *testing.T) {
	t.Parallel()
	if err := cmdSync(context.Background(), nil); err == nil {
		t.Fatal("expected an error with no -config")
	}
}

func TestCmdSyncRejectsAConfigWithNeitherSection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeSyncConfig(t, dir, syncConfig{})
	err := cmdSync(context.Background(), []string{"-config", path})
	if err == nil || !strings.Contains(err.Error(), "nothing to sync") {
		t.Fatalf("cmdSync() = %v, want a \"nothing to sync\" error", err)
	}
}

func TestCmdSyncGCPSectionRequiresProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeSyncConfig(t, dir, syncConfig{GCP: &syncGCPConfig{}})
	err := cmdSync(context.Background(), []string{"-config", path})
	if err == nil || !strings.Contains(err.Error(), "gcp.project") {
		t.Fatalf("cmdSync() = %v, want a gcp.project error", err)
	}
}

func TestCmdSyncAppliesSettingsAgainstAnEmbeddedStore(t *testing.T) {
	useAccessCodeFile(t)
	pollInterval := "45s"
	maxWorkers := 2
	githubHost := "github.example"

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()}), store))
	defer srv.Close()

	path := writeSyncConfig(t, dir, syncConfig{Settings: settingsRequest(pollInterval, maxWorkers, githubHost)})

	if err := cmdSync(context.Background(), []string{"-config", path, "-server", srv.URL}); err != nil {
		t.Fatalf("cmdSync: %v", err)
	}

	// A second sync against the same, now-already-applied settings must
	// succeed too -- the "safe to re-run, nothing to do if nothing
	// changed" bar this file's own doc comment sets.
	if err := cmdSync(context.Background(), []string{"-config", path, "-server", srv.URL}); err != nil {
		t.Fatalf("cmdSync (second run): %v", err)
	}

	settings, err := accessCodeClient(srv.URL).GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PollInterval != pollInterval || settings.GitHubHost != githubHost {
		t.Fatalf("settings = %+v, want poll interval %q, github host %q",
			settings, pollInterval, githubHost)
	}
	if settings.MaxMergesPerRepo != 2 {
		t.Fatalf("settings.MaxMergesPerRepo = %v, want 2", settings.MaxMergesPerRepo)
	}
}

// A single-tenant daemon trusts no loopback caller, so `grain sync` run
// from a shell that never read /etc/profile.d/grain.sh -- the one
// google/scripts/cloudtop-install.sh seeds its first model from -- has to find
// the access code through cli.env's GRAIN_DATA_DIR, as every other CLI
// command does (task 4496). It used to send nothing and be refused 401.
func TestCmdSyncPresentsTheLocalAPITokenFromCLIEnvsDataDir(t *testing.T) {
	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	srv := httptest.NewServer(ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: code,
	}, store))
	t.Cleanup(srv.Close)

	dataDir := t.TempDir()
	if err := os.WriteFile(ui.LocalAPITokenPath(dataDir), []byte(code+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{ui.LocalAPITokenFileEnv, "GRAIN_TOKEN", "GRAIN_AUTH_TOKEN", "GRAIN_ID_TOKEN", "GRAIN_TUNNEL_TOKEN"} {
		t.Setenv(env, "")
	}
	path := writeSyncConfig(t, t.TempDir(), syncConfig{Settings: settingsRequest("45s", 2, "github.example")})

	// With no cli.env either, nothing names the token, and the daemon
	// says so -- the failure this test is here to keep fixed.
	t.Setenv(cliEnvFileEnvVar, "")
	t.Setenv(dataDirEnvVar, "")
	if err := cmdSync(context.Background(), []string{"-config", path, "-server", srv.URL, "-dry-run"}); err == nil {
		t.Fatal("cmdSync succeeded against a single-tenant daemon with no access code anywhere; the test proves nothing")
	}

	writeCLIEnv(t, "GRAIN_DATA_DIR="+dataDir+"\n")
	if err := cmdSync(context.Background(), []string{"-config", path, "-server", srv.URL}); err != nil {
		t.Fatalf("cmdSync with cli.env naming the data directory: %v", err)
	}
	settings, err := (&ui.HTTPClient{BaseURL: srv.URL, Token: code}).GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.PollInterval != "45s" {
		t.Errorf("settings.PollInterval = %q, want the synced 45s", settings.PollInterval)
	}
}

// seedSettings saves a settings row against server, so that a test can
// then ask what a config file would change about one -- a plan against
// a deployment nothing has ever configured is a different question (the
// first save's own required fields), covered separately below.
func seedSettings(t *testing.T, server string, req ui.UpdateSettingsRequest) ui.Settings {
	t.Helper()
	settings, err := accessCodeClient(server).UpdateSettings(context.Background(), req)
	if err != nil {
		t.Fatalf("seeding settings: %v", err)
	}
	return settings
}

// The "settings" half of a config repo's pull-request check
// (grain/task-241): what the file would change about the settings, said
// before anybody merges it, with the settings themselves untouched.
func TestCmdSyncDryRunPlansSettingsWithoutWriting(t *testing.T) {
	useAccessCodeFile(t)
	server, _ := syncTestServer(t)
	seedSettings(t, server, *settingsRequest("60s", 1, "github.example"))
	// Auto-merge is on by default (model.DefaultConfig), so a plan that
	// would turn it on has to start from a deployment that turned it
	// off -- which is also the deployment where turning it back on by
	// accident matters.
	off := false
	seedSettings(t, server, ui.UpdateSettingsRequest{AutoMergeByDefault: &off})

	dir := t.TempDir()
	workers := 4
	autoMerge := true
	path := writeSyncConfig(t, dir, syncConfig{Settings: &ui.UpdateSettingsRequest{
		MaxMergesPerRepo: &workers, AutoMergeByDefault: &autoMerge,
	}})

	out := captureStdout(t, func() {
		if err := cmdSync(context.Background(), []string{"-config", path, "-server", server, "-dry-run"}); err != nil {
			t.Fatalf("cmdSync: %v", err)
		}
	})
	if !strings.Contains(out, "settings -- what would change:") {
		t.Errorf("a dry run did not head the settings plan as a plan; it printed:\n%s", out)
	}
	if !strings.Contains(out, `max merges per repo: "1" -> "4"`) {
		t.Errorf("a dry run did not report the setting it would change; it printed:\n%s", out)
	}

	settings, err := accessCodeClient(server).GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.MaxMergesPerRepo != 1 {
		t.Fatalf("a dry run applied the settings section: max merges per repo = %d, want the stored 1",
			settings.MaxMergesPerRepo)
	}
	if settings.AutoMergeByDefault {
		t.Fatal("a dry run turned auto-merge on -- the change this plan exists to show rather than make")
	}
	if !strings.Contains(out, `auto merge by default: "false" -> "true"`) {
		t.Errorf("a dry run did not report the auto-merge it would turn on; it printed:\n%s", out)
	}
}

// The reason the plan is the daemon's own (PlanSettings) rather than a
// comparison worked out here between the file's values and GetSettings':
// every value in a request is the daemon's to normalize, and a file
// stating one in another spelling of the same setting changes nothing.
// A client-side diff would report both of these as changes, on every
// run, for ever -- a plan nobody could read.
func TestCmdSyncDryRunPlansWhatTheDaemonWouldStoreNotWhatTheFileSays(t *testing.T) {
	useAccessCodeFile(t)
	server, _ := syncTestServer(t)
	seedSettings(t, server, *settingsRequest("60s", 1, "github.example"))

	dir := t.TempDir()
	pollInterval := "1m"
	path := writeSyncConfig(t, dir, syncConfig{Settings: &ui.UpdateSettingsRequest{
		PollInterval: &pollInterval,
	}})

	out := captureStdout(t, func() {
		if err := cmdSync(context.Background(), []string{"-config", path, "-server", server, "-dry-run"}); err != nil {
			t.Fatalf("cmdSync: %v", err)
		}
	})
	if !strings.Contains(out, "settings: already up to date, nothing changed") {
		t.Errorf("a dry run reported a change the daemon would not make; it printed:\n%s", out)
	}
}

// A plan is also where a value the daemon would refuse is found out
// about -- while the pull request that carries it is still open, rather
// than by the sync that applies it half a file later.
func TestCmdSyncDryRunRefusesASettingTheDaemonWouldRefuse(t *testing.T) {
	useAccessCodeFile(t)
	server, _ := syncTestServer(t)
	seedSettings(t, server, *settingsRequest("60s", 1, "github.example"))

	dir := t.TempDir()
	badPoll := "not-a-duration"
	path := writeSyncConfig(t, dir, syncConfig{Settings: &ui.UpdateSettingsRequest{PollInterval: &badPoll}})

	err := cmdSync(context.Background(), []string{"-config", path, "-server", server, "-dry-run"})
	if err == nil || !strings.Contains(err.Error(), "pollInterval") {
		t.Fatalf("cmdSync() = %v, want a dry run refusing the invalid poll interval", err)
	}
}

// A daemon too old to have the plan route answers 404, and a dry run
// says so as the deployment's own limitation -- rather than as the "not
// found" a config file naming something that does not exist would get.
// It changed nothing either way, which is what the route being a route
// of its own buys (ui.Server.handlePlanSettings).
func TestCmdSyncDryRunExplainsADaemonWithNoPlanRoute(t *testing.T) {
	t.Parallel()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/settings/plan" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such API path: POST /api/settings/plan"}`))
			return
		}
		writeJSONForTest(w, ui.Settings{Configured: true, PollInterval: "60s", MaxMergesPerRepo: 1})
	}))
	defer old.Close()

	dir := t.TempDir()
	workers := 4
	path := writeSyncConfig(t, dir, syncConfig{Settings: &ui.UpdateSettingsRequest{MaxMergesPerRepo: &workers}})

	err := cmdSync(context.Background(), []string{"-config", path, "-server", old.URL, "-dry-run"})
	if err == nil || !strings.Contains(err.Error(), "no plan mode for settings") {
		t.Fatalf("cmdSync() = %v, want an error naming the daemon's missing plan mode", err)
	}
}

func writeJSONForTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// settingsRequest builds a syncConfig's own settings section with every
// field the first-ever UpdateSettings call requires
// (ui.Client.UpdateSettings's own doc comment) filled in.
func settingsRequest(pollInterval string, maxWorkers int, githubHost string) *ui.UpdateSettingsRequest {
	return &ui.UpdateSettingsRequest{
		PollInterval:     &pollInterval,
		MaxMergesPerRepo: &maxWorkers,
		GitHubHost:       &githubHost,
	}
}

// printSettingsDiff is the whole of what a sync run reports, and its one
// failure mode is silence: a setting the config file really changed, with
// no row in settingsDiffRows, prints as "nothing changed" against a
// deployment that just changed. That has happened twice -- the sandbox VM
// shape, then seven settings at once -- and both times the table was a
// hand-written list nobody was reminded to add to.
//
// This is the reminder, and it is derived rather than written down:
// ui.UpdateSettingsRequest's own JSON tags are the definition of what a
// config file can set (syncSettings hands UpdateSettings the whole
// request), so every one of them must be a row here or a deliberate,
// reasoned entry in settingsDiffExceptions.
func TestSettingsDiffRowsCoverEveryUpdatableSetting(t *testing.T) {
	t.Parallel()
	rows := map[string]string{}
	for _, row := range settingsDiffRows {
		if other, dup := rows[row.field]; dup {
			t.Errorf("settingsDiffRows has two rows for %q (%q and %q); one setting is one line of output",
				row.field, other, row.name)
		}
		rows[row.field] = row.name
	}

	settable := map[string]bool{}
	for _, field := range updateSettingsFields() {
		settable[field] = true
		if _, ok := rows[field]; ok {
			continue
		}
		if why, excused := settingsDiffExceptions[field]; excused {
			if strings.TrimSpace(why) == "" {
				t.Errorf("settingsDiffExceptions excuses %q with no reason", field)
			}
			continue
		}
		t.Errorf("ui.UpdateSettingsRequest has a %q field with no settingsDiffRows row: "+
			"a config file setting it changes the deployment, and `grain sync` prints "+
			"\"already up to date, nothing changed\" -- add a row, or a settingsDiffExceptions "+
			"entry saying why it cannot be diffed", field)
	}

	// The other direction, so the table cannot drift into naming settings
	// that no longer exist: a row (or an exception) for a field
	// UpdateSettingsRequest does not have reports on nothing, and reads as
	// coverage it isn't.
	for field := range rows {
		if !settable[field] {
			t.Errorf("settingsDiffRows has a row for %q, which is not a ui.UpdateSettingsRequest field -- "+
				"no config file can set it, so the row diffs something no sync ever changes", field)
		}
	}
	for field := range settingsDiffExceptions {
		if !settable[field] {
			t.Errorf("settingsDiffExceptions excuses %q, which is not a ui.UpdateSettingsRequest field -- "+
				"a stale exception excuses nothing and hides the next real one", field)
		}
	}
}

// updateSettingsFields is every setting a "settings" section can carry,
// by the JSON name it is spelled with -- ui.UpdateSettingsRequest's own
// tags, which is where cmdSync unmarshals that section into.
func updateSettingsFields() []string {
	t := reflect.TypeOf(ui.UpdateSettingsRequest{})
	fields := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields = append(fields, name)
	}
	return fields
}

// Having a row is half of it; the row has to actually report the change.
// Every setting is moved off its zero value at once (settingsWithEveryFieldChanged)
// rather than by a hand-written pair of ui.Settings, so this cannot go
// stale the way the list it replaced did: a row reading a field nothing
// changed, or reading nothing at all, fails here.
func TestPrintSettingsDiffReportsEveryRowItHas(t *testing.T) {
	before := ui.Settings{}
	after := settingsWithEveryFieldChanged()

	out := captureStdout(t, func() { printSettingsDiff(before, after, false) })

	for _, row := range settingsDiffRows {
		if !strings.Contains(out, "  "+row.name+": ") {
			t.Errorf("printSettingsDiff did not report %q (%s) as changed; it printed:\n%s",
				row.name, row.field, out)
		}
	}
}

// settingsWithEveryFieldChanged is a settings row with every field moved
// off its zero value, so that a diff against the zero ui.Settings has to
// report every row there is.
func settingsWithEveryFieldChanged() ui.Settings {
	var s ui.Settings
	v := reflect.ValueOf(&s).Elem()
	for i := range v.NumField() {
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			f.SetString("changed")
		case reflect.Int:
			f.SetInt(7)
		case reflect.Float64:
			// The budget's two overcommit ratios, and the one kind of
			// setting here whose zero value is a value somebody can
			// choose rather than an unset one.
			f.SetFloat(1.5)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			// Only the []string ones matter here -- they are what the two
			// list rows join -- and a slice of anything else (the
			// capability statuses) is left alone rather than filled with a
			// zero element that would say nothing.
			if f.Type().Elem().Kind() == reflect.String {
				f.Set(reflect.ValueOf([]string{"one", "two"}))
			}
		}
		s.AgentBackends = []model.AgentBackend{{ID: "backend-1", Framework: model.AgentFrameworkClaude}}
	}
	// The one struct-valued setting: the overcommit mode and its bounds.
	s.SandboxOvercommit = model.OvercommitSettings{Mode: model.OvercommitAuto}
	return s
}

// The lists read as lists, not as Go slices: this is a workflow log, and
// `["acme/widgets" "acme/gadgets"]` is not how anything else here names a
// set.
func TestPrintSettingsDiffJoinsListSettings(t *testing.T) {
	before := ui.Settings{DefaultCapabilities: []string{"self-debug"}}
	after := ui.Settings{DefaultCapabilities: []string{"self-debug", "gcp-key"}}

	out := captureStdout(t, func() { printSettingsDiff(before, after, false) })

	if !strings.Contains(out, `"self-debug" -> "self-debug, gcp-key"`) {
		t.Errorf("default capabilities are not reported as a joined list; printed:\n%s", out)
	}
}

// The other half of the two list fields above: they are compared as the
// joined strings they are printed as, so two equal lists must not read as
// a change. A sync run that reported one on every tick would be as
// useless as one that reported none.
func TestPrintSettingsDiffSaysNothingChangedWhenNothingDid(t *testing.T) {
	settings := ui.Settings{
		EnvironmentName: "staging", PollInterval: "30s", MaxMergesPerRepo: 1,
	}
	out := captureStdout(t, func() { printSettingsDiff(settings, settings, false) })
	if !strings.Contains(out, "already up to date, nothing changed") {
		t.Errorf("printSettingsDiff printed %q, want the no-op line", out)
	}
	if strings.Contains(out, "settings changed:") {
		t.Errorf("printSettingsDiff reported a change where there was none: %q", out)
	}
}
