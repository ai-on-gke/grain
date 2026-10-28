package antigravity

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// agyModelsOutput is what `agy models` printed on 1.1.26, captured whole
// in docs/agy-surface.md -- progress line included, since that line is on
// stdout and the parser has to ignore it.
const agyModelsOutput = `Fetching available models...
gemini-3.8-flash-high	Gemini 3.8 Flash (High)
gemini-3.8-flash-medium	Gemini 3.8 Flash (Medium)
gemini-3.8-flash-low	Gemini 3.8 Flash (Low)
gemini-3.1-pro-high	Gemini 3.1 Pro (High)
gemini-3.1-pro-low	Gemini 3.1 Pro (Low)
`

// agyHelpOutput is the two lines of `agy --help` this package reads: the
// one documenting --effort, and a neighbour that must not be mistaken for
// it.
const agyHelpOutput = `Usage of agy:
  --agent                         Agent for the current CLI session
  --effort                        Reasoning effort for the current CLI session (low|medium|high)
  --model                         Model for the current CLI session
`

// fakeProbe answers each agy subcommand from a canned capture, and
// records the environment it was handed -- the credential half of this
// being as much of the contract as the parsing (see Catalog).
type fakeProbe struct {
	out  map[string]string
	err  map[string]error
	envs map[string][]string
	// settingsAtRun is agy's own settings file, copied out of the HOME
	// this probe was given while the probe is notionally in flight:
	// Catalog deletes that directory as it returns, so a test cannot look
	// afterwards (the same trick antigravity_test.go's recordingRunner
	// plays for a run's HOME).
	settingsAtRun string
	sawSettings   bool
}

func (p *fakeProbe) run(_ context.Context, env []string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	if p.envs == nil {
		p.envs = map[string][]string{}
	}
	p.envs[key] = env
	for _, e := range env {
		home, ok := strings.CutPrefix(e, "HOME=")
		if !ok {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(home, cliSettingsRelPath)); err == nil {
			p.settingsAtRun, p.sawSettings = string(b), true
		}
	}
	return p.out[key], p.err[key]
}

func newFakeProbe() *fakeProbe {
	return &fakeProbe{out: map[string]string{
		"models": agyModelsOutput,
		"--help": agyHelpOutput,
	}, err: map[string]error{}}
}

func TestCatalogReadsModelsAndEfforts(t *testing.T) {
	got, err := catalog(context.Background(), agent.LocalHost{}, newFakeProbe(), "key")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !slices.Equal(got.Efforts, []string{"low", "medium", "high"}) {
		t.Errorf("Efforts = %v, want agy's own --effort vocabulary", got.Efforts)
	}
	want := []Model{
		{ID: "gemini-3.8-flash-high", Label: "Gemini 3.8 Flash (High)", Effort: "high", Family: "gemini-3.8-flash"},
		{ID: "gemini-3.8-flash-medium", Label: "Gemini 3.8 Flash (Medium)", Effort: "medium", Family: "gemini-3.8-flash"},
		{ID: "gemini-3.8-flash-low", Label: "Gemini 3.8 Flash (Low)", Effort: "low", Family: "gemini-3.8-flash"},
		{ID: "gemini-3.1-pro-high", Label: "Gemini 3.1 Pro (High)", Effort: "high", Family: "gemini-3.1-pro"},
		{ID: "gemini-3.1-pro-low", Label: "Gemini 3.1 Pro (Low)", Effort: "low", Family: "gemini-3.1-pro"},
	}
	if !slices.Equal(got.Models, want) {
		t.Errorf("Models = %+v, want %+v", got.Models, want)
	}
}

// The catalog is read with the deployment's own key, in the environment
// and nowhere else -- and with GOOGLE_API_KEY cleared beside it, for the
// same reason Run clears it: agy prefers that variable when both are set,
// so an unrelated key exported on the controller would otherwise decide
// which catalog comes back.
func TestCatalogPassesTheKeyInTheEnvironment(t *testing.T) {
	p := newFakeProbe()
	if _, err := catalog(context.Background(), agent.LocalHost{}, p, "secret-key"); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	env := p.envs["models"]
	if !slices.Contains(env, "GEMINI_API_KEY=secret-key") {
		t.Errorf("env = %v, want GEMINI_API_KEY", env)
	}
	if !slices.Contains(env, "GOOGLE_API_KEY=") {
		t.Errorf("env = %v, want GOOGLE_API_KEY cleared", env)
	}
	if !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "HOME=") }) {
		t.Fatalf("env = %v, want a private HOME", env)
	}
	// That HOME is not the controller's own, and its settings file is the
	// whole point of it: without "modelProvider" agy ignores
	// GEMINI_API_KEY and demands a login instead of listing anything.
	if !p.sawSettings {
		t.Fatal("the probe's HOME held no settings file")
	}
	if !strings.Contains(p.settingsAtRun, "gemini") {
		t.Errorf("settings = %s, want the API-key model provider", p.settingsAtRun)
	}
}

// A deployment with no key of its own means agy to authenticate from its
// ambient session -- Catalog's contract matches Run's, so neither the
// variable nor the setting that reads it is written.
func TestCatalogWithoutAKeySetsNoCredential(t *testing.T) {
	p := newFakeProbe()
	if _, err := catalog(context.Background(), agent.LocalHost{}, p, ""); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, e := range p.envs["models"] {
		if strings.HasPrefix(e, "GEMINI_API_KEY=") {
			t.Errorf("env = %v, want no GEMINI_API_KEY for a deployment that configured none", p.envs["models"])
		}
	}
}

// An agy whose --help no longer documents the vocabulary still lists
// models: the fallback names the suffixes, and a listing an operator can
// pick from is worth more than a probe that fails whole over a reworded
// usage line.
func TestCatalogFallsBackToKnownEfforts(t *testing.T) {
	p := newFakeProbe()
	p.out["--help"] = "Usage of agy:\n  --effort  Reasoning effort\n"
	got, err := catalog(context.Background(), agent.LocalHost{}, p, "key")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !slices.Equal(got.Efforts, Efforts()) {
		t.Errorf("Efforts = %v, want the fallback %v", got.Efforts, Efforts())
	}
	if got.Models[0].Effort != "high" {
		t.Errorf("Models[0] = %+v, want an effort read off the name", got.Models[0])
	}
}

// The failures a caller has to be able to tell an operator about, since
// the field this feeds stays typable in exactly these two cases.
func TestCatalogReportsAgyRefusing(t *testing.T) {
	p := newFakeProbe()
	p.err["models"] = errors.New("authentication required. Run 'agy' to log in, then retry")
	if _, err := catalog(context.Background(), agent.LocalHost{}, p, "key"); err == nil {
		t.Fatal("catalog: want an error when agy refuses the listing")
	} else if !strings.Contains(err.Error(), "authentication required") {
		t.Errorf("err = %v, want agy's own refusal in it", err)
	}
}

func TestCatalogReportsAnUnreadableListing(t *testing.T) {
	p := newFakeProbe()
	p.out["models"] = "Fetching available models...\nno models are available for this account\n"
	if _, err := catalog(context.Background(), agent.LocalHost{}, p, "key"); err == nil {
		t.Fatal("catalog: want an error when nothing in the listing looks like a model")
	}
}

func TestParseModelsSkipsWhatIsNotAModel(t *testing.T) {
	// A label-less listing is read as ids, so dropping the display
	// column would not cost a deployment its picker.
	got := parseModels("Fetching available models...\n\ngemini-3.1-pro-high\nWarning: something happened\n", Efforts())
	want := []Model{{ID: "gemini-3.1-pro-high", Effort: "high", Family: "gemini-3.1-pro"}}
	if !slices.Equal(got, want) {
		t.Errorf("parseModels = %+v, want %+v", got, want)
	}
}

type fakeCatalogHost struct {
	agent.LocalHost
	out           map[string]string
	err           map[string]error
	paths         []string
	args          [][]string
	envs          map[string][]string
	settingsAtRun string
	homes         []string
	// onRun, when set, is called with each command while what CatalogOn
	// staged for it is still there.
	onRun func(c agent.Command)
}

func (h *fakeCatalogHost) Run(_ context.Context, c agent.Command, _ io.Writer) (string, error) {
	key := strings.Join(c.Args, " ")
	h.paths = append(h.paths, c.Path)
	h.args = append(h.args, c.Args)
	if h.envs == nil {
		h.envs = map[string][]string{}
	}
	h.envs[key] = c.Env
	if h.onRun != nil {
		h.onRun(c)
	}
	for _, e := range c.Env {
		if home, ok := strings.CutPrefix(e, "HOME="); ok {
			h.homes = append(h.homes, home)
			if b, err := os.ReadFile(filepath.Join(home, cliSettingsRelPath)); err == nil {
				h.settingsAtRun = string(b)
			}
		}
	}
	return h.out[key], h.err[key]
}

func TestCatalogOnStagesAndRunsThroughTheHost(t *testing.T) {
	host := &fakeCatalogHost{
		out: map[string]string{
			"--help": agyHelpOutput,
			"models": agyModelsOutput,
		},
		err: map[string]error{},
	}
	got, err := CatalogOn(context.Background(), host, "/opt/agy/bin/agy", "")
	if err != nil {
		t.Fatalf("CatalogOn: %v", err)
	}
	if len(got.Models) != 5 || got.Models[0].ID != "gemini-3.8-flash-high" {
		t.Fatalf("Models = %+v, want the host's model listing", got.Models)
	}
	if !slices.Equal(got.Efforts, []string{"low", "medium", "high"}) {
		t.Errorf("Efforts = %v, want agy's --effort vocabulary", got.Efforts)
	}
	if !slices.Equal(host.paths, []string{"/opt/agy/bin/agy", "/opt/agy/bin/agy"}) {
		t.Errorf("paths = %q, want the host binary path on both probe calls", host.paths)
	}
	if host.settingsAtRun == "" {
		t.Fatal("the host probe's HOME held no settings file while running")
	}
	for _, home := range host.homes {
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("temporary HOME %s was not cleaned up after CatalogOn: err = %v", home, err)
		}
	}

	// When the host binary exits non-zero with a login refusal on stdout,
	// CatalogOn preserves that output in the error just as Catalog does.
	refusing := &fakeCatalogHost{
		out: map[string]string{
			"--help": agyHelpOutput,
			"models": "Fetching available models...\nError: Please sign in to view available models.\n",
		},
		err: map[string]error{
			"models": errors.New("exit status 1"),
		},
	}
	_, err = CatalogOn(context.Background(), refusing, "/usr/local/bin/agy", "")
	if err == nil || !strings.Contains(err.Error(), "Please sign in to view available models") {
		t.Fatalf("CatalogOn error = %v, want the CLI's stdout included in the error", err)
	}
}

// The real binary, when this host has one: the parsers above are written
// against a capture, and this is the one test that would notice a release
// rewording the listing they were captured from. Skipped everywhere agy
// is not installed, which includes CI -- see tests/e2e for the live
// suites that do have a credential.
func TestLiveCatalogReadsTheInstalledAgy(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		t.Skip("no GEMINI_API_KEY: the catalog is a fetch, not a compiled-in list")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		t.Skip("no agy on this host")
	}
	got, err := Catalog(context.Background(), agyPath, key)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(got.Models) == 0 {
		t.Fatal("Catalog returned no models")
	}
}
