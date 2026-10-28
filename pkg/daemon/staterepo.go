package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// stateRepoDir is the working tree, under -data-dir beside the store it
// materialises. Both are state a redeploy must not lose, which is what
// -data-dir means.
func StateRepoDir(dataDir string) string { return filepath.Join(dataDir, "state-repo") }

// retiredTeamStateReposDir is where a multi-team build kept each team's
// grain/config-<team> repository. Nothing reads or writes it any more:
// grain/config is the only configuration repository.
func retiredTeamStateReposDir(dataDir string) string {
	return filepath.Join(dataDir, "team-state-repos")
}

// RetiredTeamStateReposNotice is what a start says, once, about a
// team-state-repos directory an older build left in the data directory,
// or "" when there is none. The directory is left where it is rather than
// deleted: it is an operator's data, and the only copy of whatever a team
// once merged there, so removing it is theirs to decide. A path there
// that cannot be read is reported the same way and never stops a start.
func RetiredTeamStateReposNotice(dataDir string) string {
	dir := retiredTeamStateReposDir(dataDir)
	if _, err := os.Lstat(dir); err != nil {
		return ""
	}
	return fmt.Sprintf("%s holds per-team configuration repositories (grain/config-<team>) from an older build; "+
		"nothing reads them any more, since grain/config is the only configuration repository, "+
		"and the directory can be deleted once nothing in it is wanted", dir)
}

// stateRepoReformatStamp is how a schema change tells the next start
// that the dump in the repository is the one it just moved a store aside
// over, and is to be replaced rather than refused.
//
// It sits in the data directory rather than in the working tree or the
// store, because it has to outlive both: the step that writes it
// (scripts/setup.sh's reformat_store_if_schema_changed) moves the store
// aside and archives the working tree in the same breath, and a note
// left in either would go with them. The file's contents are for whoever
// finds it -- nothing reads them -- and its presence is the whole of the
// instruction. staterepo.Reformat's own file has the argument for why
// this is asked rather than inferred.
func StateRepoReformatStamp(dataDir string) string {
	return filepath.Join(dataDir, "state-repo.reformat")
}

// secretsDir is the private half of the layout: the key, the GitHub
// credential ladder, the sandbox token file -- everything that must stay
// on this host and out of the repository.
func SecretsDir(dataDir string) string { return filepath.Join(dataDir, "secrets") }

// agentSessionsDir is where a framework whose CLI keeps its
// conversations in a home grain builds keeps them between one dispatch
// of a task and the next (pkg/agent/sessionstore, and
// buildCodexFramework/buildAntigravityFramework, which are the only
// callers). "" -- a process with no data directory -- is a deployment
// with no store, and every run of it gets the private per-run home those
// packages have always built.
//
// Beside the transcripts under -data-dir/state rather than in the state
// repository, and deliberately: what is in there is the whole content of
// an agent's thinking, tool calls and tool results, which is the same
// class of thing as a transcript and belongs on the deployment's own
// disk rather than in a repository a push copies off the host. It is
// also the bulky half of this feature -- pkg/agent/sessionstore is what
// bounds it -- and nothing outside this host can resume any of it, the
// tokens naming it being ids in a CLI's own store on this machine.
func agentSessionsDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "state", "agent-sessions")
}

// secretsConfig places the two halves of pkg/secrets, both under
// -data-dir/secrets: the private key, which has always been there, and
// the encrypted file, which has not.
//
// The encrypted file used to sit inside the state repository, so that a
// push carried a copy of it off the host. That stopped being safe the
// moment the state repository became somewhere agents are dispatched to
// work (grain/task-186): the git proxy authorizes per repository and
// streams a packfile it does not parse, so a sandbox that may clone the
// state repository gets every object in it -- the ciphertext included,
// and a file deleted three commits ago just as readily as one in the
// tree. Ciphertext an agent can carry off is still ciphertext an agent
// can carry off.
//
// Nothing is lost by moving it. The private key was never in the
// repository and is copied nowhere, so the off-host ciphertext could
// never be decrypted from a clone anyway: a restore always needed
// -data-dir/secrets, and now that directory is the whole of what a
// restore needs.
//
// Named once, here, because four call sites -- the daemon, the UI it
// serves, `grain secrets` and the controller -- have to agree on both
// paths for a key pasted in one to be the key another reads.
func SecretsConfig(dataDir string) secrets.Config {
	return secrets.Config{
		File:    filepath.Join(SecretsDir(dataDir), secrets.DefaultFileName),
		KeyFile: filepath.Join(SecretsDir(dataDir), secrets.DefaultKeyFileName),
	}
}

// openSecrets opens this deployment's secret store, reporting once if it
// had to mint the key -- the one thing about a fresh install that is
// urgent, since a key nobody has copied anywhere is a key one disk
// failure away from taking every secret with it.
//
// It is also where an installation written by a build that kept the
// encrypted file in the state repository catches up: every path that
// reads or writes a secret comes through here, so the file is moved
// once, by whichever of them runs first, rather than by a migration step
// an operator has to know to run.
func OpenSecrets(dataDir string) *secrets.Store {
	moveSecretsOutOfStateRepo(dataDir)
	store := secrets.Open(SecretsConfig(dataDir))
	if store.KeyCreated() {
		log.Printf("grain: generated a new secrets key at %s -- back this file up: "+
			"without it, %s cannot be decrypted by anyone, grain included",
			store.KeyFile(), store.File())
	}
	// A restored secrets file may be sealed to another host's key and is
	// unreadable until the operator imports that key. Said once at startup, where it
	// is a line about the deployment, rather than only later as a run
	// that could not resolve a credential.
	if err := store.Check(); err != nil {
		log.Printf("grain: this host cannot read %s: %v", store.File(), err)
		if recipient, rerr := store.FileRecipient(); rerr == nil && recipient != "" {
			log.Printf("grain: it is encrypted to %s -- install that key with "+
				"`grain state key import` or the Settings pane's State tab", recipient)
		}
	}
	return store
}

// moveSecretsOutOfStateRepo lifts an encrypted secrets file an earlier
// build left inside the state repository out to where secretsConfig now
// looks for it.
//
// A rename, not a copy: a copy would leave the ciphertext in the working
// tree for the next export to commit again. The next commit records its
// removal from the tip.
//
// Failures are logged rather than returned. Every caller is on its way to
// open the store, and retaining the only ciphertext copy is safer than
// refusing to start.
func moveSecretsOutOfStateRepo(dataDir string) {
	from := filepath.Join(StateRepoDir(dataDir), secrets.DefaultFileName)
	if _, err := os.Stat(from); err != nil {
		return
	}
	to := SecretsConfig(dataDir).File
	if _, err := os.Stat(to); err == nil {
		// Both exist: the one outside the repository is the one grain
		// reads, and guessing which is newer is not a guess worth making
		// with secrets. Said out loud so an operator can look.
		log.Printf("grain: %s still holds an encrypted secrets file, and %s exists too -- "+
			"grain reads the second and has left the first alone; delete it once you are sure", from, to)
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		log.Printf("grain: preparing %s: %v", filepath.Dir(to), err)
		return
	}
	if err := os.Rename(from, to); err != nil {
		log.Printf("grain: moving %s out of the state repository to %s: %v", from, to, err)
		return
	}
	log.Printf("grain: moved the encrypted secrets file out of the state repository, from %s to %s -- "+
		"it is beside the private key now, and %s is what a backup has to include",
		from, to, SecretsDir(dataDir))
}

// forbiddenRepos is the set of external repositories this deployment's
// git proxy refuses to every sandbox, whatever a task's scope says
// (gitproxy.ModelAuthorizer.Forbidden).
//
// The config repository is controller-local and is served through its
// grain/config alias, so it has no external repository to refuse. The
// secrets repository is always refused: its sole purpose is to hold the
// ciphertext.
func ForbiddenRepos(_ context.Context, dataDir string) ([]model.RepoRef, error) {
	return SecretsRepoRefs(dataDir), nil
}

// secretsRepoRefs is this deployment's secrets repository as the proxy
// names repositories, or nothing when none is configured or the URL
// names no owner/repo a sandbox could ask for.
//
// A settings file that will not read adds nothing rather than failing
// the whole question: the state repository half of forbiddenRepos is
// what the fail-closed rule is about, and a secrets repository grain
// cannot name is also one it has not cloned and is not pushing to.
func SecretsRepoRefs(dataDir string) []model.RepoRef {
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		log.Printf("grain: cannot read %s to tell the git proxy which repository holds this "+
			"deployment's secrets: %v", secretsrepo.SettingsFileName, err)
		return nil
	}
	owner, name, ok := repoFromRemote(settings.Remote)
	if !ok {
		return nil
	}
	return []model.RepoRef{{Owner: owner, Name: name}}
}

// checkStateRepoBaseline refuses a data dir whose state repository holds
// a dump stamped from before the single-tenant baseline, in the startup
// guard's words (model.CheckSchemaBaseline), and only reads the stamp.
//
// It runs before the store is opened, because the store is not always
// where a pre-reset data dir says what it is. One a pre-reset build ran
// on PostgreSQL, with -postgres-url and no db-config.json, has no store
// under it at all: started without the URL, this build would create a
// fresh SQLite store there and only then fail to load the dump, in
// staterepo's generic ErrSchemaTooOld, leaving behind a store/ the
// pre-reset build would find if it ran on the dir again (task 4815).
//
// A dump with no stamp, or none at all, is not refused here: that is a
// data dir nothing has written yet. Nor is one with an upgrade note
// beside it, whose store-and-dump pair openStoreForSchemaUpgrade and
// loadStateRepo judge together, the guard included.
func checkStateRepoBaseline(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	if _, ok := readSchemaUpgrade(dataDir); ok {
		return nil
	}
	dir := StateRepoDir(dataDir)
	if !staterepo.HasDump(dir) {
		return nil
	}
	found, err := staterepo.ReadSchemaVersion(dir)
	if err != nil {
		// The load says so, in its own words, once the store is open.
		return nil
	}
	return model.CheckSchemaBaseline(found, model.DataDir)
}

// OpenStateRepo prepares the controller-local config repository and wires
// its optional external Git mirror from <data-dir>/state-repo.json.
func OpenStateRepo(ctx context.Context, dataDir string) (*staterepo.Repo, error) {
	settings, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	// Before staterepo.Open's first git command, which is the one that
	// would die on a reftable repository in git's own words.
	if err := checkRefStorage(StateRepoDir(dataDir)); err != nil {
		return nil, err
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir:     StateRepoDir(dataDir),
		DataDir: dataDir,
		Remote:  settings.Remote,
		Branch:  settings.Branch,
		Token:   RepoToken(dataDir, settings.Remote, settings.TokenFile),
		// grain's own commits, attributed to grain: a reviewer of a
		// settings diff should be able to tell at a glance which changes
		// the daemon exported and which a human or an agent proposed.
		AuthorName:  "grain",
		AuthorEmail: "grain@" + hostnameOrLocalhost(),
		// Native grain/config changes are checked by the sandbox image;
		// the repository itself no longer installs a forge workflow.
		CheckImage: checkImage(settings),
		NoWorkflow: true,
		// Reset notices are retained locally because there is no external
		// pull request on which to post them.
		AnnounceResets: stateResetNotice{}.announce,
		// System-schedule edits use the same local notice path.
		AnnounceSystemScheduleEdits: stateResetNotice{}.announceSystemScheduleEdits,
		// Which build this is, for the export commit that follows an
		// upgrade: staterepo records it beside the working tree and names
		// both builds in the message the next time they differ, so the
		// history of a state repository says when the deployment writing
		// it changed rather than only that its dump did.
		Build: buildIdentity(),
	})
	if err != nil {
		return nil, err
	}
	if err := staterepo.EnsureIgnored(repo.Dir()); err != nil {
		return nil, err
	}
	return repo, nil
}

func loadStateRepo(ctx context.Context, dataDir string, repo *staterepo.Repo, db *sql.DB, migrated bool) error {
	if settings, err := staterepo.LoadSettings(dataDir); err == nil && strings.TrimSpace(settings.Remote) != "" {
		return staterepo.LoadExternal(ctx, repo, db, model.SchemaVersion)
	}
	err := staterepo.Load(ctx, repo, db, model.SchemaVersion)
	if recoverDivergedStateRepo(ctx, repo, err) {
		err = staterepo.Load(ctx, repo, db, model.SchemaVersion)
	}
	if migrated {
		if migrateStateRepo(ctx, repo, db, err) {
			err = staterepo.Load(ctx, repo, db, model.SchemaVersion)
		}
		return err
	}
	if reformatStateRepo(ctx, dataDir, repo, db, err) {
		err = staterepo.Load(ctx, repo, db, model.SchemaVersion)
	}
	return err
}

func fatalStateRepoLoad(err error) bool {
	return err != nil && !errors.Is(err, staterepo.ErrUnreachable)
}

func recoverDivergedStateRepo(ctx context.Context, repo *staterepo.Repo, err error) bool {
	if !errors.Is(err, staterepo.ErrDiverged) {
		return false
	}
	recovered, rerr := repo.RecoverDiverged(ctx)
	if rerr != nil {
		log.Printf("grain: the state repository has diverged from its remote and is not syncing: %v", rerr)
		return false
	}
	if !recovered {
		log.Printf("grain: the state repository has diverged from its remote and is not syncing: %v", err)
		return false
	}
	log.Printf("grain: the state repository had diverged from its remote -- every local commit was grain's "+
		"own export, so %s has been reset onto origin/%s and the database will be exported over it again",
		repo.Dir(), repo.Branch())
	return true
}

// migrateStateRepo is reformatStateRepo's counterpart for the start that
// has just carried this deployment's rows across a schema change: the
// repository holds those same rows at the schema they came from, and the
// database now holds them at the schema this build knows.
//
// It reports whether the caller should load again, which it does exactly
// when the repository has been rewritten.
//
// It asks for no note of its own. The note is the one openStoreForSchemaUpgrade
// acted on to rebuild the store, and this runs in the same start, off the
// same authorisation -- which is why that note is not removed until this
// has had its turn. And the case reformatStateRepo has to be careful
// about cannot arise here: a restore, where the dump is the only copy of
// a deployment's state and writing over it would delete what somebody
// came for. This database *is* that dump, imported and re-exported.
func migrateStateRepo(ctx context.Context, repo *staterepo.Repo, db *sql.DB, err error) bool {
	if !errors.Is(err, staterepo.ErrSchemaTooOld) && !errors.Is(err, staterepo.ErrSchemaTooNew) {
		return false
	}
	committed, merr := staterepo.Migrated(ctx, repo, db, model.SchemaVersion)
	if !committed && merr == nil {
		log.Printf("grain: the state repository's dump has been rewritten at schema %d from the database it "+
			"was imported into, but produced no changes to commit", model.SchemaVersion)
		return true
	}
	if !committed {
		log.Printf("grain: this deployment's store was migrated to schema %d and its state repository "+
			"could not be brought with it: %v. Nothing here is lost -- the repository still holds the "+
			"dump the store was rebuilt from -- and the next start tries again",
			model.SchemaVersion, merr)
		return false
	}
	if merr != nil {
		log.Printf("grain: the config repository was migrated onto schema %d but its export reported: %v", model.SchemaVersion, merr)
		return true
	}
	log.Printf("grain: the config repository's dump was rewritten at schema %d from the database it was imported into; the prior dump is the preceding commit in %s", model.SchemaVersion, repo.Dir())
	return true
}

// reformatStateRepo answers the other question a failed load can raise:
// the repository holds a dump at a schema this build does not know, and
// is that the schema change this host was just put through?
//
// It reports whether the caller should load again, which it does exactly
// when the repository has been reformatted onto this build's schema.
//
// A schema bump leaves stateRepoReformatStamp when it archives the old
// store and repository. This consumes that one-shot authorization after
// committing an export at the new schema. A mismatch without the stamp
// remains fatal so grain never overwrites a dump unexpectedly.
func reformatStateRepo(ctx context.Context, dataDir string, repo *staterepo.Repo, db *sql.DB, err error) bool {
	if !errors.Is(err, staterepo.ErrSchemaTooOld) && !errors.Is(err, staterepo.ErrSchemaTooNew) {
		return false
	}
	stamp := StateRepoReformatStamp(dataDir)
	if _, serr := os.Stat(stamp); serr != nil {
		log.Printf("grain: the state repository holds a dump this build cannot read (%v). Nothing here says "+
			"this host's schema was changed under it, so grain will not write over that dump: if this is "+
			"a repository you are restoring, run the build that wrote it; if this deployment's schema did "+
			"change, `grain state reformat` (or re-running scripts/setup.sh) says so and the next start "+
			"replaces the dump with this deployment's database", err)
		return false
	}
	committed, rerr := staterepo.Reformat(ctx, repo, db, model.SchemaVersion)
	if !committed {
		log.Printf("grain: %s asked for the state repository to be reformatted onto schema %d and that "+
			"did not happen: %v. The stamp is left in place, so the next start tries again",
			stamp, model.SchemaVersion, rerr)
		return false
	}
	// Remove the one-shot authorization as soon as the reformat commits so
	// it cannot authorize a later schema change.
	if uerr := os.Remove(stamp); uerr != nil {
		log.Printf("grain: the state repository has been reformatted but %s is still there (%v) -- remove "+
			"it by hand, or the next schema change will reformat this repository without being asked",
			stamp, uerr)
	}
	if rerr != nil {
		log.Printf("grain: the config repository was reformatted onto schema %d but its export reported: %v", model.SchemaVersion, rerr)
		return true
	}
	log.Printf("grain: this host's schema changed to %d, so the config repository's incompatible dump was replaced from the database; the prior dump is the preceding commit in %s", model.SchemaVersion, repo.Dir())
	return true
}

// RepoToken is the token for either of the two repositories grain
// pushes to on its own account: the state repository above, and the
// secrets repository (secretsrepo.go), which is configured the same way
// -- its own token file, or the deployment's git connection for it --
// because it is the same question with a different URL in it.
func RepoToken(dataDir, remote, tokenFile string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if tokenFile != "" {
			data, err := os.ReadFile(tokenFile)
			if err != nil {
				return "", fmt.Errorf("reading %s: %w", tokenFile, err)
			}
			return strings.TrimSpace(string(data)), nil
		}
		owner, repo, ok := repoFromRemote(remote)
		if !ok {
			return "", nil
		}
		if !ladderMaySendTo(remote) {
			return "", fmt.Errorf("%s is not an https URL on github.com, and this deployment's GitHub "+
				"credentials are only ever sent to github.com: name a token for this repository "+
				"explicitly (`grain secrets repo -token-file`, or the State pane's token field) "+
				"if it really lives on another host", remote)
		}
		store := repoTokenStoreFor(dataDir)
		if store == nil {
			return "", fmt.Errorf("this deployment's git connections are not open yet, so nothing names a "+
				"credential for %s/%s: name a token for this repository explicitly (`grain secrets repo "+
				"-token-file`, or the State pane's token field) if it has to be reached before the daemon starts",
				owner, repo)
		}
		name, err := store.GitConnectionOn(ctx, "", owner, repo)
		if err != nil {
			return "", fmt.Errorf("no git connection covers %s/%s: %w", owner, repo, err)
		}
		credentials, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
		if err != nil {
			return "", err
		}
		cred, ok := credentials.GetForRepo(name, owner, repo)
		if !ok || cred.Token == nil {
			return "", fmt.Errorf("the git connection for %s/%s names credential %q, which has no token on this deployment", owner, repo, name)
		}
		return *cred.Token, nil
	}
}

// repoTokenStores is, per data directory, the store RepoToken reads the
// deployment's git connections from once the daemon has opened it
// (SetRepoTokenStore). The two repositories it answers for are synced
// before the store opens as well as after, so it cannot be handed one up
// front; until it is, only an explicit token file answers. The
// credentials.json ladder is not asked (docs/single-tenant-reset.md,
// stage 1).
var repoTokenStores sync.Map // filepath.Clean(dataDir) -> *model.Store

// SetRepoTokenStore is where RepoToken finds the git connections of the
// deployment in dataDir from now on. The answer forgets it again, for
// the daemon to call as its store closes.
func SetRepoTokenStore(dataDir string, store *model.Store) (release func()) {
	key := filepath.Clean(dataDir)
	repoTokenStores.Store(key, store)
	return func() { repoTokenStores.CompareAndDelete(key, store) }
}

func repoTokenStoreFor(dataDir string) *model.Store {
	if v, ok := repoTokenStores.Load(filepath.Clean(dataDir)); ok {
		return v.(*model.Store)
	}
	return nil
}

// ladderMaySendTo is whether the GitHub credential ladder may be asked
// for a token to send to remote at all.
//
// The ladder is keyed by owner/repo and says nothing about where its
// tokens may go, so without this a remote of
// http://anywhere.example/owner/repo matched a "*" pattern as readily as
// https://github.com/owner/repo did, and the deployment's GitHub token
// went out as Basic auth to whichever host the settings named (#2372).
// Those tokens are github.com's, so github.com over TLS is the only
// place they are sent; a repository on any other host -- GitHub
// Enterprise included -- is pushed with a token named for it explicitly.
func ladderMaySendTo(remote string) bool {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "github.com") && (u.Port() == "" || u.Port() == "443")
}

// repoFromRemote pulls owner and repo out of a git URL, in either of the
// two forms an operator pastes: an https URL or scp-style ssh. It is
// only used to ask the credential ladder a question, so a URL it does
// not recognise is a "no answer" rather than an error.
func repoFromRemote(remote string) (owner, repo string, ok bool) {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if remote == "" {
		return "", "", false
	}
	path := remote
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		path = u.Path
	} else if at := strings.Index(remote, "@"); at >= 0 {
		if colon := strings.Index(remote[at:], ":"); colon >= 0 {
			path = remote[at+colon+1:]
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}

// remoteHost is the other half of the same URL: the host git talks to,
// in either of the two forms above, lowercased and without the user or
// the port. "" when the URL names none -- a bare path, a local
// repository -- which is a host nothing can be asked about rather than
// an error.
func remoteHost(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	if at := strings.Index(remote, "@"); at >= 0 {
		if colon := strings.Index(remote[at:], ":"); colon >= 0 {
			return strings.ToLower(remote[at+1 : at+colon])
		}
	}
	return ""
}

func hostnameOrLocalhost() string {
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return "localhost"
}

// stateSyncInterval is how often the daemon reconciles its database with
// the repository, in both directions.
//
// A timer rather than a hook on every write: the export is a full dump,
// which is cheap at grain's scale but not free, and batching a burst of
// writes into one commit produces a history a human can actually read --
// one commit per reconcile cycle's worth of change rather than one per
// row. Nothing is lost outbound by the delay, because the database, not
// the repository, is what the daemon reads from while it runs; inbound
// it is the upper bound on how long a merged settings change takes to
// become live, which at half a minute is a good deal less than the
// restart it used to take.
const stateSyncInterval = 30 * time.Second

// stateSyncLoop applies merged local changes, exports, and commits until
// ctx is cancelled, then does it once more so a clean shutdown leaves
// nothing unwritten.
//
// The last one is a syncAll rather than a sync, which today is the same
// call: the two differed when an ordinary tick left grain's own churn
// for an hourly clock to pick up, and the repository has carried no such
// rows since grain/task-395. It stays a syncAll because what a shutdown
// owes the repository is everything, and this is where that is said.
//
// A failure is logged and the loop continues; the next cycle retries the
// local import or export.
// nudge is a second reason to run a cycle immediately. The merge
// queue merging a settings pull request is the one thing that sends on
// it (stateManager.Nudge). A nil channel simply never fires, which is
// what every caller with nothing to nudge it has.
// startStateSync runs stateSyncLoop in the background and returns what
// stops it: the loop's context is cancelled, so it makes its final
// export, and the call returns once it has. Run defers it, and Run can
// return with ctx still live -- any startup error after the loop has
// started, such as a -k8s-sandbox-service that never resolves -- so the
// stop cannot wait on ctx alone: it did, and the daemon hung there
// instead of exiting with the error
// (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F3).
func startStateSync(ctx context.Context, sync, syncAll func(context.Context) (bool, error), nudge <-chan struct{}) (stop func()) {
	loopCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		stateSyncLoop(loopCtx, sync, syncAll, nudge)
	}()
	return func() {
		cancel()
		<-stopped
	}
}

func stateSyncLoop(ctx context.Context, sync, syncAll func(context.Context) (bool, error), nudge <-chan struct{}) {
	ticker := time.NewTicker(stateSyncInterval)
	defer ticker.Stop()
	// Keep a persistent failure to one journal line rather than repeating
	// it every thirty seconds.
	var last string
	for {
		select {
		case <-ctx.Done():
			// Deliberately not ctx: it is already cancelled, and the last
			// sync is the one that must not be skipped.
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateSyncInterval)
			defer cancel()
			if _, err := syncAll(final); err != nil {
				log.Printf("grain: final state sync failed: %v", err)
			}
			return
		case <-ticker.C:
			runStateSync(ctx, sync, &last)
		case <-nudge:
			// The ticker is deliberately not reset here: a nudge is an
			// extra cycle, not a rescheduled one, and a merge landing a
			// second before the tick should not push that tick half a
			// minute out.
			runStateSync(ctx, sync, &last)
		}
	}
}

// runStateSync is one cycle and what the loop says about it: a failure
// is logged once and then only when it changes, which is what keeps a
// persistent condition to one line rather than one every thirty seconds.
// last is the loop's memory, shared by both of the
// reasons a cycle runs.
func runStateSync(ctx context.Context, sync func(context.Context) (bool, error), last *string) {
	switch _, err := sync(ctx); {
	case err == nil:
		*last = ""
	case err.Error() != *last:
		*last = err.Error()
		log.Printf("grain: state sync failed: %v", err)
	}
}
