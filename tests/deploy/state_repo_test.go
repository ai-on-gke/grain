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

package deploy

// The deployment path for the state repository (pkg/staterepo) and the
// secrets key it carries: a Terraform variable, an instance-metadata
// attribute, a `cfg` read, an environment variable and a file write, in
// five different files, none of which can see the other four. Exactly
// what this package exists to check -- a variable declared and never
// passed on, or an attribute pushed under a name nothing reads back,
// fails silently and looks like a working deploy.

import (
	"strings"
	"testing"
)

func TestTheConfigRepositoryHasNoDeployTimeMirror(t *testing.T) {
	files := []string{
		read(t, "deploy", "terraform", "gcp", "variables.tf"),
		read(t, "deploy", "terraform", "gcp", "instance.tf"),
		read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"),
		setupCode(t),
	}
	for _, removed := range []string{"state_repo_url", "state_repo_branch", "GRAIN_STATE_REPO_URL", "GRAIN_STATE_REPO_BRANCH"} {
		for _, file := range files {
			if strings.Contains(file, removed) {
				t.Errorf("deployment still contains removed config mirror setting %q", removed)
			}
		}
	}
	contains(t, setupCode(t), "grain/config (controller local)")
}

// The other repository a deployment is pointed at, and the one whose
// absence is silent: with no secrets repository configured, the
// encrypted file exists on one disk and everything looks fine until the
// disk does not. Same five files, same failure mode -- a variable
// declared and never passed on reads as a working deploy.
func TestTheSecretsRepositoryIsConfiguredByTheDeployToo(t *testing.T) {
	variables := read(t, "deploy", "terraform", "gcp", "variables.tf")
	for _, v := range []string{`variable "secrets_repo_url"`, `variable "secrets_repo_branch"`} {
		contains(t, variables, v)
	}

	instance := read(t, "deploy", "terraform", "gcp", "instance.tf")
	contains(t, instance, "secrets_repo_url          = var.secrets_repo_url")
	contains(t, instance, "secrets_repo_branch       = var.secrets_repo_branch")

	deploy := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))
	contains(t, deploy, `SECRETS_REPO_URL="$(cfg secrets_repo_url)"`)
	contains(t, deploy, `SECRETS_REPO_BRANCH="$(cfg secrets_repo_branch)"`)
	contains(t, deploy, `GRAIN_SECRETS_REPO_URL="$SECRETS_REPO_URL"`)
	contains(t, deploy, `GRAIN_SECRETS_REPO_BRANCH="$SECRETS_REPO_BRANCH"`)

	setup := setupCode(t)
	contains(t, setup, "secrets-repo.json")
	contains(t, setup, `[ -n "$GRAIN_SECRETS_REPO_URL" ] || return 0`)
}

// The secrets key is seeded the way GRAIN_GITHUB_TOKEN and the minter
// key already are -- a Secret Manager version pushed after the apply,
// read back with the host's own token, and written once.
func TestTheSecretsKeyIsSeededTheWayEveryOtherCredentialIs(t *testing.T) {
	push := stripComments(read(t, "deploy", "terraform", "gcp", "deploy", "push-secrets.sh"))
	contains(t, push, `push_secret "secrets-key" "${GRAIN_SECRETS_KEY:-}"`)

	// Without this a deployment moving off instance metadata would lose
	// the key its predecessor was given to the apply that drops the
	// attribute, leaving the next rebuild seeding nothing.
	contains(t, read(t, "deploy", "terraform", "gcp", "deploy", "migrate-secrets.sh"),
		`"grain-secrets-key=secrets-key"`)

	deploy := stripComments(read(t, "deploy", "terraform", "gcp", "files", "deploy.sh"))
	contains(t, deploy, `SECRETS_KEY="$(secret secrets-key)"`)
	contains(t, deploy, `GRAIN_SECRETS_KEY="$SECRETS_KEY"`)

	// seed_secret, not a bare write: a key already on this host is the
	// key that host's own secrets file was encrypted to, so it always
	// wins over whatever a deploy carries.
	contains(t, setupCode(t),
		`seed_secret "$GRAIN_DATA_DIR/secrets/secrets.key" "$GRAIN_SECRETS_KEY"`)
}

// Ordering is the whole of this: both of these steps have to happen
// before anything runs the `grain` CLI against the store.
//
// Opening a store with no key mints one (pkg/secrets.Open), so a host
// rebuilt with a key to seed would otherwise end up holding one it
// generated a moment earlier -- and the encrypted secrets file lives
// inside the state repository's working tree, so the tree has to be the
// cloned one before seed_gcp_minter_key writes this deploy's freshly
// rotated minter key into it.
func TestTheSecretsKeyAndTheStateRepositoryComeBeforeAnythingOpensTheStore(t *testing.T) {
	code := setupCode(t)
	// "\n  configure_state_repo\n" is the call rather than the definition,
	// which a shell file necessarily carries above it.
	before(t, code,
		`seed_secret "$GRAIN_DATA_DIR/secrets/secrets.key"`, "\n  configure_state_repo\n",
		"the secrets key must be seeded before the state repository is opened")
	before(t, code,
		"\n  configure_state_repo\n", "seed_gcp_minter_key\n",
		"the state repository must be opened before a secret is written into its working tree")
	before(t, code,
		`seed_secret "$GRAIN_DATA_DIR/secrets/secrets.key"`, "seed_gcp_minter_key\n",
		"the secrets key must be seeded before `grain secrets` mints one of its own")
}

// "You have not backed this up" belongs where an operator is already
// looking, which is the readiness summary at the end of a run.
func TestTheReadinessSummaryNamesTheSecretsKeyAndTheStateRepository(t *testing.T) {
	readiness := body(t, setupText(t), "report_readiness() {")
	contains(t, readiness, "secrets key:")
	contains(t, readiness, "config repository:")
	if !strings.Contains(readiness, "back up") {
		t.Error("the readiness summary never says to back the secrets key up")
	}
	// Presence asked of the file, because `grain state key show` would
	// mint a key on a host that has none -- a readiness report must not
	// create what it reports on.
	contains(t, readiness, `[ -s "$GRAIN_DATA_DIR/secrets/secrets.key" ]`)

	// And the operator's own handle on the same thing, for a host that is
	// long past its deploy.
	contains(t, read(t, "pkg", "cli", "state.go"), "GRAIN_SECRETS_KEY")
}

// A schema bump and the start that follows it agree on one filename, in
// two files that cannot see each other: setup.sh writes the note, and
// cmd/grain reads it and removes it. Nothing else checks that pair, and
// a disagreement is silent in the worst way -- the note is written, the
// daemon never finds it, and the deployment fails to start on every
// restart exactly as it did before any of this existed (grain/task-44).
func TestTheSchemaBumpAndTheDaemonAgreeOnTheReformatNote(t *testing.T) {
	const stamp = "state-repo.reformat"
	// In the function that moves the store aside, rather than anywhere in
	// the script: what makes the note true is the archive beside it.
	reformat := body(t, setupText(t), "reformat_store_if_schema_changed() {")
	contains(t, reformat, `"$GRAIN_DATA_DIR/`+stamp+`"`)
	// Owned by the daemon's account, since the daemon is what deletes it.
	// A root-owned note is one it can never spend, and one that would
	// then authorise every schema change after this one. Written as that
	// account, too, rather than by root and chowned after (#2405).
	contains(t, reformat, `grain_write 0600 "$reformat_stamp"`)

	daemon := read(t, "pkg", "daemon", "staterepo.go")
	contains(t, daemon, `filepath.Join(dataDir, "`+stamp+`")`)
}

// A re-run of setup.sh across a schema change carries the rows the way
// the Upgrade button does, and only resets the store when the operator
// has said so (grain/task-4357). Before this the reset was the only
// answer: every user and task gone, the run exiting 0, and nothing in
// the readiness report about it.
func TestASchemaChangeMigratesBeforeItWillEverResetTheStore(t *testing.T) {
	setup := setupText(t)
	reformat := body(t, setup, "reformat_store_if_schema_changed() {")
	before(t, reformat, "systemctl stop grain-daemon.service", `state -data-dir "$GRAIN_DATA_DIR" prepare-upgrade`,
		"the daemon is stopped before the export, so no row lands after it")
	before(t, reformat, `state -data-dir "$GRAIN_DATA_DIR" prepare-upgrade`, `as_grain mv -T "$store_dir" "$backup"`,
		"the store is moved aside before the migration is even tried")
	before(t, reformat, `"$GRAIN_ALLOW_STORE_RESET" != "1"`, `as_grain mv -T "$store_dir" "$backup"`,
		"the store is moved aside without GRAIN_ALLOW_STORE_RESET being asked")
	contains(t, setup, `GRAIN_ALLOW_STORE_RESET="${GRAIN_ALLOW_STORE_RESET:-0}"`)
	contains(t, body(t, setup, "usage() {"), "GRAIN_ALLOW_STORE_RESET")

	// A reset is the readiness report's business, with the archive named.
	readiness := body(t, setup, "report_readiness() {")
	contains(t, readiness, `"$GRAIN_SCHEMA_CHANGE_REPORT" = "reset"`)
	contains(t, readiness, `!! the store's schema changed`)

	// And setup.sh's word is the one cmd/grain answers to.
	contains(t, read(t, "pkg", "cli", "state.go"), `case "prepare-upgrade":`)
}

// Beside grain hostd, grain_cli runs the extracted binary on the host,
// which needs git in PATH to initialize state-repo. On a minimal host
// with no git installed (such as the clean Debian Trixie e2e host),
// configure_state_repo uses run-image.sh so the deployment image's git
// initializes state-repo as the service account before the daemon starts.
func TestConfigureStateRepoUsesTheImageWhenTheHostLacksGit(t *testing.T) {
	cfgRepo := body(t, setupCode(t), "configure_state_repo() {")
	contains(t, cfgRepo, "! command -v git >/dev/null 2>&1")
	contains(t, cfgRepo, `/usr/local/lib/grain/run-image.sh -- state -data-dir "$GRAIN_DATA_DIR" status`)
}
