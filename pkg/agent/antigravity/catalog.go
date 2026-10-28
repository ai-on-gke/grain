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
	"fmt"
	"github.com/ai-on-gke/grain/pkg/authfix"

	"github.com/ai-on-gke/grain/pkg/agent"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// This file is the other half of what this package knows about agy: not
// how to drive a run, but what the installed binary says it can run at
// all. `grain settings -gemini-model` and the Settings pane behind it
// used to be a free-text field over a vocabulary only agy knows -- a
// deployment that typed a name agy has never heard of learned so on the
// next dispatch, as a run that failed before it started ("--model
// gemini-3.1-pro requires --effort" being the friendliest of them). The
// catalog is a fact about the binary on this host, it changes when that
// binary is upgraded, and asking it is one subprocess -- so the UI asks
// rather than making an operator remember (grain/task-365).
//
// It is read as evidence rather than as an API, the same reading
// docs/agy-surface.md's own preamble asks for: agy prints a human
// listing, not JSON (1.1.26's `agy models --help` offers no
// --output-format, whatever its changelog says arrived later), so
// everything below is parsing text a release could reword. Every failure
// path therefore ends somewhere a human can still type a model name by
// hand: Catalog returns an error rather than a guess, the API reports
// that error beside an empty list, and the field it feeds stays writable.

// Model is one entry of agy's own model catalog -- `agy models`' own id
// and human label, plus the two things grain reads out of the id.
type Model struct {
	// ID is the name --model takes, e.g. "gemini-3.1-pro-high".
	ID string
	// Label is agy's own display name for it, e.g. "Gemini 3.1 Pro
	// (High)". Empty for a listing that printed an id and nothing else.
	Label string
	// Effort is the reasoning effort the id carries as a suffix ("high"
	// for the example above), or "" for a name that carries none. agy's
	// catalog spells effort into the name rather than leaving it to
	// --effort, and refuses either half on its own -- see DefaultEffort.
	Effort string
	// Family is ID with that suffix removed ("gemini-3.1-pro"), which is
	// what makes the several efforts of one model group together on a
	// picker. Equal to ID when Effort is empty.
	Family string
}

// ModelCatalog is what one installed agy answers about itself: the models
// it will accept for --model, and the reasoning-effort vocabulary its own
// --effort flag documents.
//
// Efforts is that vocabulary as the binary states it rather than as
// Efforts() hard-codes it -- the wider of the two answers a picker can
// give, and the one a future agy that added or renamed a word would be
// read through. Which efforts a *given* model has is a narrower question
// that neither answers, and the ids do: gemini-3.1-pro is listed high and
// low and never medium, which is exactly the pairing agy refuses at the
// start of a run.
type ModelCatalog struct {
	Models  []Model
	Efforts []string
}

// Efforts() is what a catalog falls back to when agy's own --help no
// longer spells the vocabulary out: the same three words this package
// already validates a stored effort against, which is what 1.1.26
// documents and what every model name in its catalog is suffixed with. A
// guess only in the sense that it is not this binary's own answer, and it
// never overrides one that parsed.

// Catalog asks the agy binary at agyPath what models it can run.
//
// apiKey is the deployment's own Gemini API key, passed exactly as Run
// passes it -- in the environment, never in argv, with GOOGLE_API_KEY
// cleared alongside so an unrelated key exported on the controller cannot
// become the one this answer is billed against. It is needed because
// `agy models` fetches the catalog rather than reciting a compiled-in
// list ("Fetching available models..."), and an unauthenticated agy
// answers with a login demand instead. An empty key is not an error here
// for the same reason it is not one in Run: a deployment may mean agy to
// authenticate from its own ambient session.
//
// The subprocess runs under a private HOME holding nothing but the
// settings file that makes agy read GEMINI_API_KEY at all -- the same
// reasoning as writeAgyHome, minus the per-run MCP config and hooks a
// listing has no use for. It is emphatically not the controller's own
// ~/.gemini: reading a catalog must not touch, and must not depend on,
// whatever session a human left there.
//
// Bound it with the context. `agy models` is a network call and this is
// reached from an HTTP handler.
func Catalog(ctx context.Context, agyPath, apiKey string) (ModelCatalog, error) {
	return catalog(ctx, agent.LocalHost{}, execProbe{agyPath: agyPath}, apiKey)
}

// CatalogOn is Catalog run through host (such as agent.HostdHost when agy
// runs on the bare machine through grain hostd): the private HOME is
// staged through host and `agy --help` and `agy models` are started
// through host.Run, so the probe uses the same binary and ambient login
// that a dispatch's turns run under.
func CatalogOn(ctx context.Context, host agent.Host, agyPath, apiKey string) (ModelCatalog, error) {
	if host == nil {
		return Catalog(ctx, agyPath, apiKey)
	}
	return catalog(ctx, host, hostProbe{host: host, agyPath: agyPath}, apiKey)
}

// probe is the seam Catalog's own subprocesses go through, so the
// parsing below can be tested against captured agy output without a
// binary or a live credential -- the same reason Framework has runner.
type probe interface {
	run(ctx context.Context, env []string, args ...string) (stdout string, err error)
}

type execProbe struct{ agyPath string }

// run returns stdout and stderr concatenated, deliberately: agy prints
// its own progress line ("Fetching available models...") and its usage
// text on whichever stream a given subcommand chose, and the parsers
// below ignore what they cannot read anyway. An exit status still comes
// back as an error, with that combined output in the message, since that
// is the only place agy says why it refused.
func (p execProbe) run(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, p.agyPath, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("running %s %s: %w (output: %s)",
			p.agyPath, strings.Join(args, " "), authfix.Wrap(err, string(out), p.agyPath), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// hostProbe is execProbe for a binary reached through an agent.Host --
// the same seam a run's own CLI is started through, so a catalog is read
// from the binary a dispatch there runs.
type hostProbe struct {
	host    agent.Host
	agyPath string
}

func (p hostProbe) run(ctx context.Context, env []string, args ...string) (string, error) {
	name := p.agyPath
	if name == "" {
		name = "agy"
	}
	out, err := p.host.Run(ctx, agent.Command{Path: name, Args: args, Env: env}, nil)
	if err != nil {
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			return out, fmt.Errorf("running %s %s: %w (output: %s)",
				name, strings.Join(args, " "), authfix.Wrap(err, out, name), trimmed)
		}
		return out, fmt.Errorf("running %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func catalog(ctx context.Context, host agent.Host, p probe, apiKey string) (ModelCatalog, error) {
	home, cleanup, err := writeCatalogHome(ctx, host, providerFor(apiKey))
	if err != nil {
		return ModelCatalog{}, err
	}
	defer cleanup()

	// The same environment a dispatch's agy gets (agyEnv): the private
	// HOME above, the key with GOOGLE_API_KEY cleared beside it, and the
	// updater switched off -- which this listing needs as much as a run
	// does. `agy models` replaces a writable binary in place, measured on
	// 1.1.27 even when it *refuses* the listing for want of a credential
	// (docs/hand-runs/2026-09-17-agy-self-update.md); `agy --help`, the
	// other exec below, does not. Without the switch, an operator opening
	// the model list in Settings is enough to change which agy this
	// deployment runs (selfupdate.go).
	env := agyEnv(home, apiKey)

	// Efforts first, and never fatally: the vocabulary is what names the
	// suffixes on the models, and a --help that stopped documenting it
	// leaves Efforts() rather than failing a listing that is otherwise
	// perfectly good.
	efforts := Efforts()
	if help, err := p.run(ctx, env, "--help"); err == nil || help != "" {
		if parsed := parseEfforts(help); len(parsed) > 0 {
			efforts = parsed
		}
	}

	out, err := p.run(ctx, env, "models")
	if err != nil {
		return ModelCatalog{}, fmt.Errorf("antigravity: listing agy's models: %w", err)
	}
	models := parseModels(out, efforts)
	if len(models) == 0 {
		// A zero exit and nothing this parser recognised means agy's
		// listing changed shape, which is worth saying plainly: the
		// caller's fallback is a field an operator types into, and
		// "agy printed no model names" is what tells them to.
		return ModelCatalog{}, fmt.Errorf("antigravity: agy models printed no model names (output: %s)",
			strings.TrimSpace(out))
	}
	return ModelCatalog{Models: models, Efforts: efforts}, nil
}

// writeCatalogHome is writeAgyHome's smaller sibling: the private HOME a
// catalog probe runs under, holding only the settings file. See Catalog.
//
// Staged through the Host the probe runs on, so the settings file
// exists beside the binary that reads it. It is emphatically not the
// controller's own ~/.gemini -- reading a catalog must not touch, and
// must not depend on, whatever session a human left there.
func writeCatalogHome(ctx context.Context, host agent.Host, provider string) (home string, cleanup func(), err error) {
	settings, err := settingsJSON(provider)
	if err != nil {
		return "", nil, fmt.Errorf("antigravity: building agy's settings: %w", err)
	}
	// No store and no task: a model listing has no conversation to keep
	// and no task to keep one for, so it gets the temp home every run
	// got before the store existed (openAgyHome).
	home, _, cleanup, err = writeHomeFiles(ctx, host, nil, "", "", map[string][]byte{cliSettingsRelPath: settings})
	return home, cleanup, err
}

// modelIDPattern is what an id has to look like to be taken for one: a
// bare token, since every line agy prints that is not a model ("Fetching
// available models...", a login demand, a blank line) carries spaces or
// punctuation this refuses.
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// parseModels reads `agy models`' listing: one model per line, id and
// display label separated by a tab.
//
// A line with no tab is still read as a model when the whole of it looks
// like an id, so a future agy that drops the label column keeps working;
// anything else on the line -- progress, warnings, a usage message --
// fails modelIDPattern and is skipped. Order is agy's own (newest family
// first, highest effort first within a family), which is the order a
// picker should offer them in, so nothing here sorts.
func parseModels(out string, efforts []string) []Model {
	var models []Model
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(strings.TrimSuffix(line, "\r"), " \t")
		if line == "" {
			continue
		}
		id, label, _ := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if !modelIDPattern.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		effort, family := splitEffort(id, efforts)
		models = append(models, Model{
			ID:     id,
			Label:  strings.TrimSpace(label),
			Effort: effort,
			Family: family,
		})
	}
	return models
}

// splitEffort reads the effort agy spelled into a model name, against the
// vocabulary that same binary documents rather than a list hard-coded
// here -- "gemini-3.1-pro-high" is a family and an effort only because
// "high" is one of the words --effort takes.
func splitEffort(id string, efforts []string) (effort, family string) {
	for _, e := range efforts {
		if suffix := "-" + e; strings.HasSuffix(id, suffix) {
			return e, strings.TrimSuffix(id, suffix)
		}
	}
	return "", id
}

// effortFlagPattern picks the vocabulary out of agy's own usage line for
// --effort: "Reasoning effort for the current CLI session
// (low|medium|high)". The alternation in parentheses is the whole of what
// is read -- a flag documented without one leaves the fallback in place.
var effortFlagPattern = regexp.MustCompile(`\(([a-z]+(?:\|[a-z]+)+)\)`)

// parseEfforts reads that vocabulary out of `agy --help`.
func parseEfforts(help string) []string {
	for _, line := range strings.Split(help, "\n") {
		if !strings.Contains(line, "--effort") {
			continue
		}
		m := effortFlagPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var efforts []string
		for _, e := range strings.Split(m[1], "|") {
			if e = strings.TrimSpace(e); e != "" {
				efforts = append(efforts, e)
			}
		}
		if len(efforts) > 0 {
			return efforts
		}
	}
	return nil
}
