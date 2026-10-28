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

// grain import extracts a verified migration bundle into a target data directory,
// restores the database, state repository, team state repositories, and secrets
// with proper permissions,
// applies any needed schema migrations, and verifies credential ladder and state repo consistency.
package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"golang.org/x/crypto/pbkdf2"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const importUsage = `usage: grain import -data-dir DIR -in FILE [flags]

Extract and restore a migration bundle into the target Grain data directory,
running store migrations and verifying state repository and secrets consistency.

Flags:
  -data-dir DIR           root directory of the grain installation (required; defaults to $GRAIN_DATA_DIR)
  -in, -input, -file F    path to migration bundle archive
  -force                  overwrite an existing database, secrets, or state
                          repositories in the target data directory

Decryption (for a bundle exported with a passphrase or key):
` + bundleSecretFlagsUsage

func importCmd(args []string) {
	if err := runImport(context.Background(), args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grain import: "+err.Error())
		os.Exit(1)
	}
}

func runImport(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, importUsage) }

	dataDir := fs.String("data-dir", dataDirDefault(), "root data directory")
	inPath := fs.String("in", "", "input bundle path")
	fs.StringVar(inPath, "input", "", "input bundle path (alias)")
	fs.StringVar(inPath, "file", "", "input bundle path (alias)")
	secretFlags := addBundleSecretFlags(fs)
	force := fs.Bool("force", false, "overwrite existing data in target directory")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *inPath == "" && fs.NArg() > 0 {
		*inPath = fs.Arg(0)
	}

	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}
	if *inPath == "" {
		fs.Usage()
		return errors.New("-in bundle file is required")
	}

	pass, key, err := secretFlags.resolve(os.Stderr)
	if err != nil {
		return err
	}

	// 1. Read input bundle file
	rawBundle, err := os.ReadFile(*inPath)
	if err != nil {
		return fmt.Errorf("reading migration bundle %s: %w", *inPath, err)
	}

	// 2. Decrypt bundle if encrypted
	decryptedBytes, err := decryptBundle(rawBundle, pass, key)
	if err != nil {
		return fmt.Errorf("decrypting migration bundle: %w", err)
	}

	// 3. Staging extraction directory
	stageDir, err := os.MkdirTemp("", "grain-import-*")
	if err != nil {
		return fmt.Errorf("creating temporary staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	if err := os.Chmod(stageDir, 0o700); err != nil {
		return fmt.Errorf("protecting staging directory: %w", err)
	}

	// Extract tar.gz into stageDir
	if err := extractTarGz(decryptedBytes, stageDir); err != nil {
		return fmt.Errorf("extracting migration bundle: %w", err)
	}

	// 4. Verify integrity manifest
	manifest, err := verifyManifest(stageDir)
	if err != nil {
		return fmt.Errorf("verifying migration bundle integrity: %w", err)
	}

	// 5. Pre-check target directory for existing data
	if !*force {
		if existing := existingImportTargets(*dataDir); len(existing) > 0 {
			return fmt.Errorf("target data directory %s already contains %s; pass -force to overwrite", *dataDir, strings.Join(existing, ", "))
		}
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("creating target data directory %s: %w", *dataDir, err)
	}

	// 6. Restore database
	if err := restoreDatabase(stageDir, *dataDir); err != nil {
		return fmt.Errorf("restoring database: %w", err)
	}

	// 7. Restore secrets directory
	if err := restoreSecrets(stageDir, *dataDir); err != nil {
		return fmt.Errorf("restoring secrets: %w", err)
	}

	// 8. Restore state repository
	if err := restoreStateRepo(ctx, stageDir, *dataDir, manifest.StateRepoBranch, *force); err != nil {
		return fmt.Errorf("restoring state repository: %w", err)
	}

	// 8b. An older, multi-team build's bundle may carry per-team
	// configuration repositories (grain/config-<team>). There is one
	// configuration repository now, grain/config, so they are left in
	// the stage and said so rather than refused.
	if note := retiredTeamStateReposNote(manifest.TeamStateRepos); note != "" {
		fmt.Fprintln(out, note)
	}

	// 9. Run store migrations & verifications, against the store the
	// daemon on this data directory will run on: a Postgres bundle
	// restores db-config.json and no SQLite store, and opening the
	// embedded one here would create an empty one at this build's schema
	// and report that instead of the database's.
	driver, dsn, err := daemon.ResolveDatabase(*dataDir)
	if err != nil {
		return fmt.Errorf("resolving the database this data directory runs on: %w", err)
	}
	if manifest.DatabaseType != "sqlite" && driver != "" && driver != "sqlite" {
		schemaStatus, err := checkImportedPostgresSchema(ctx, *dataDir, driver, dsn)
		if err != nil {
			return err
		}
		return verifyImport(ctx, out, *dataDir, manifest, schemaStatus)
	}
	schemaStatus := fmt.Sprintf("%d", model.SchemaVersion)
	_, db, err := daemon.OpenStore("", "", *dataDir)
	switch {
	case errors.Is(err, model.ErrSchemaTooOld):
		from, dir, perr := prepareImportedSchemaUpgrade(ctx, *dataDir)
		if perr != nil {
			return fmt.Errorf("running store migrations: %w", perr)
		}
		schemaStatus = fmt.Sprintf("%d, migrates to %d when grain next starts on %s (prepared in %s)",
			from, model.SchemaVersion, *dataDir, dir)
	case err != nil:
		return fmt.Errorf("running store migrations: %w", err)
	default:
		defer db.Close()
	}

	return verifyImport(ctx, out, *dataDir, manifest, schemaStatus)
}

// verifyImport checks that what the import restored loads -- the GitHub
// credentials, the secret store and the state repositories -- and prints
// the import's summary, schemaStatus being what step 9 found the store at.
func verifyImport(ctx context.Context, out io.Writer, dataDir string, manifest *ExportManifest, schemaStatus string) error {
	// Verify the GitHub credential material loads
	ladderStatus := "none"
	githubSecretsDir := filepath.Join(dataDir, "secrets", "github")
	if entries, err := os.ReadDir(githubSecretsDir); err == nil && len(entries) > 0 {
		creds, err := gitproxy.LoadCredentialSet(githubSecretsDir)
		if err != nil {
			return fmt.Errorf("verifying GitHub credentials: %w", err)
		}
		ladderStatus = fmt.Sprintf("loaded %d credentials", len(creds.Names()))
	}

	// Verify secrets encryption store if present
	skippedRelay := false
	for _, entry := range manifest.Files {
		if isBundleRelayFile(entry.Path) {
			skippedRelay = true
		}
	}
	secretsKeyPath := filepath.Join(dataDir, "secrets", secrets.DefaultKeyFileName)
	secretsEncPath := filepath.Join(dataDir, "secrets", secrets.DefaultFileName)
	if _, errK := os.Stat(secretsKeyPath); errK == nil {
		if _, errE := os.Stat(secretsEncPath); errE == nil {
			secStore := secrets.Open(secrets.Config{
				File:    secretsEncPath,
				KeyFile: secretsKeyPath,
			})
			entries, err := secStore.List()
			if err != nil {
				return fmt.Errorf("verifying secrets store: %w", err)
			}
			for _, e := range entries {
				if e.Name == "relay" {
					_ = secStore.DeleteSecret("relay")
					skippedRelay = true
				}
			}
		}
	}
	if skippedRelay {
		fmt.Fprintln(out, "note: skipping relay settings from bundle (the rendezvous relay has been removed; see docs/relay-removed.md)")
	}

	// Verify state repository consistency if present
	repoDir := daemon.StateRepoDir(dataDir)
	stateRepoStatus := "none"
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		_, err := staterepo.Open(ctx, staterepo.Config{Dir: repoDir})
		if err != nil {
			return fmt.Errorf("verifying state repository: %w", err)
		}
		stateRepoStatus = "verified"
	}

	fmt.Fprintf(out, "successfully imported %d files into %s\n", len(manifest.Files), dataDir)
	fmt.Fprintf(out, "  schema version: %s\n", schemaStatus)
	fmt.Fprintf(out, "  database:       %s\n", manifest.DatabaseType)
	fmt.Fprintf(out, "  state repo:     %s\n", stateRepoStatus)
	fmt.Fprintf(out, "  credentials:    %s\n", ladderStatus)
	return nil
}

// checkImportedPostgresSchema reads the schema of the Postgres database
// a data directory restored from a Postgres bundle runs on (through the
// db-config.json the bundle restored, or the environment), for the
// import's summary. The bundle carries the database's connection and not
// its rows, so there is nothing of its own to migrate: a database at an
// older schema is refused naming `grain state prepare-upgrade`, which
// exports it once the daemon still serving it has been stopped, rather
// than having its rows exported from here while it may be taking writes.
func checkImportedPostgresSchema(ctx context.Context, dataDir, driver, dsn string) (string, error) {
	if driver != "postgres" {
		return "", fmt.Errorf("running store migrations: this data directory's daemon runs on %q, which this build does not support", driver)
	}
	found, err := daemon.PostgresStoreSchema(ctx, dsn, dataDir)
	if err != nil {
		return "", fmt.Errorf("reading the schema of the postgres database this data directory runs on: %w", err)
	}
	switch {
	case found == 0:
		return fmt.Sprintf("none yet, the postgres database is initialised at %d when grain next starts on %s",
			model.SchemaVersion, dataDir), nil
	case found < model.SchemaVersion:
		return "", fmt.Errorf("the files are restored into %s, but the postgres database it runs on is at schema %d "+
			"and this build knows %d -- stop any daemon still running on that database, run "+
			"`grain state -data-dir %s prepare-upgrade`, then start grain", dataDir, found, model.SchemaVersion, dataDir)
	case found > model.SchemaVersion:
		return "", fmt.Errorf("the files are restored into %s, but the postgres database it runs on is at schema %d, "+
			"newer than this build's %d -- run a build that knows it", dataDir, found, model.SchemaVersion)
	}
	return fmt.Sprintf("%d", found), nil
}

// prepareImportedSchemaUpgrade carries a bundle exported by the build
// before this one -- a store stamped with an older schema -- the way
// `grain state prepare-upgrade` carries a store left behind by setup.sh:
// it writes the upgrade note (daemon.PrepareStoreSchemaUpgrade) and
// rehearses importing it at this build's schema. The next start then
// moves the restored store aside, rebuilds it from the note's dump
// (staterepo.MigrateDump, ImportMigrated) and rewrites the state
// repository, the one migration route that does both halves.
//
// Migrating here instead would be a second copy of that route, and the
// state repository half of it needs the daemon's own load. A rehearsal
// that fails removes the note, so nothing is armed for a start that
// would only fail the same way.
//
// The store this command restored is SQLite (restoreDatabase), so a
// data directory whose daemon resolves to another database is refused
// rather than having that database exported in its place. (A Postgres
// bundle restores no store and is checked by checkImportedPostgresSchema
// instead.)
func prepareImportedSchemaUpgrade(ctx context.Context, dataDir string) (int, string, error) {
	if driver, _, err := daemon.ResolveDatabase(dataDir); err != nil {
		return 0, "", err
	} else if driver != "" && driver != "sqlite" {
		return 0, "", fmt.Errorf("the imported store is at an older schema than this build's %d, and this "+
			"data directory's daemon runs on %s rather than the SQLite store the bundle restored -- run "+
			"`grain state -data-dir %s prepare-upgrade` against the database it runs on, then start grain",
			model.SchemaVersion, driver, dataDir)
	}
	from, dir, err := daemon.PrepareStoreSchemaUpgrade(ctx, dataDir, "grain import")
	if err != nil {
		return from, "", fmt.Errorf("the imported store is at schema %d and this build knows %d, and its "+
			"rows could not be exported for the migration: %w", from, model.SchemaVersion, err)
	}
	if dir == "" {
		// Only a store the open above refused as too old gets here, so
		// a "current" answer is a store that changed under this command.
		return from, "", fmt.Errorf("the imported store reads as schema %d, which this build refused to open", from)
	}
	if err := rehearseSchemaUpgrade(ctx, dir); err != nil {
		daemon.DiscardSchemaUpgrade(dataDir)
		return from, "", fmt.Errorf("this build could not take the rows exported from the imported store at "+
			"schema %d, so nothing is armed: %w", from, err)
	}
	return from, dir, nil
}

// decryptBundle decrypts an encrypted bundle with rawKey, a decoded 32-byte
// key, or else with a key derived from passphrase. A bundle that is not
// encrypted is returned as is.
func decryptBundle(data []byte, passphrase string, rawKey []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(EncryptedBundleMagic)) {
		return data, nil
	}
	if passphrase == "" && rawKey == nil {
		return nil, errors.New("bundle is encrypted; passphrase or key is required")
	}
	headerLen := len(EncryptedBundleMagic) + SaltLength + NonceLength
	if len(data) < headerLen {
		return nil, errors.New("corrupted encrypted bundle: header too short")
	}

	salt := data[len(EncryptedBundleMagic) : len(EncryptedBundleMagic)+SaltLength]
	nonce := data[len(EncryptedBundleMagic)+SaltLength : headerLen]
	ciphertext := data[headerLen:]

	key := rawKey
	if key == nil {
		key = pbkdf2.Key([]byte(passphrase), salt, PBKDF2Iterations, KeyLength, sha256.New)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(EncryptedBundleMagic))
	if err != nil {
		return nil, fmt.Errorf("authentication failed (incorrect passphrase/key or corrupted bundle): %w", err)
	}
	return plaintext, nil
}

func extractTarGz(tarGzData []byte, destDir string) error {
	gr, err := gzip.NewReader(bytes.NewReader(tarGzData))
	if err != nil {
		return fmt.Errorf("reading gzip header: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		cleanName := filepath.FromSlash(header.Name)
		if !filepath.IsLocal(cleanName) {
			return fmt.Errorf("invalid archive entry path: %s", header.Name)
		}

		targetPath := filepath.Join(destDir, cleanName)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o700); err != nil {
				return fmt.Errorf("creating directory %s: %w", targetPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
				return fmt.Errorf("creating parent directory for %s: %w", targetPath, err)
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, ownerOnlyMode(os.FileMode(header.Mode)))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", targetPath, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("writing file %s: %w", targetPath, err)
			}
			outFile.Close()
		}
	}
	return nil
}

func verifyManifest(stageDir string) (*ExportManifest, error) {
	manifestPath := filepath.Join(stageDir, ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var manifest ExportManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	if !slices.Contains(supportedExportFormatVersions, manifest.FormatVersion) {
		return nil, fmt.Errorf("unsupported export format version %d (expected one of %v)", manifest.FormatVersion, supportedExportFormatVersions)
	}

	listed := make(map[string]bool, len(manifest.Files))
	for _, entry := range manifest.Files {
		listed[entry.Path] = true
	}
	// Everything restored from the stage is restored by walking it, so a
	// file the manifest does not list would go into the data directory
	// with nothing having checked it. Refuse the bundle instead.
	err = filepath.WalkDir(stageDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stageDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != ManifestFileName && !listed[rel] {
			return fmt.Errorf("integrity error: bundle file %s is not listed in the manifest", rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, entry := range manifest.Files {
		filePath := filepath.Join(stageDir, filepath.FromSlash(entry.Path))
		fileData, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("missing bundle file %s: %w", entry.Path, err)
		}
		if int64(len(fileData)) != entry.Size {
			return nil, fmt.Errorf("integrity error: size mismatch for %s: got %d, want %d", entry.Path, len(fileData), entry.Size)
		}
		hash := sha256.Sum256(fileData)
		hashStr := hex.EncodeToString(hash[:])
		if hashStr != entry.SHA256 {
			return nil, fmt.Errorf("integrity error: checksum mismatch for %s: got %s, want %s", entry.Path, hashStr, entry.SHA256)
		}
	}
	return &manifest, nil
}

// existingImportTargets names what in dataDir an import would overwrite:
// a database, a non-empty secrets directory, and the state repository.
// Without -force, any of them stops the import
// before anything is written.
func existingImportTargets(dataDir string) []string {
	var existing []string
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	nonEmpty := func(dir string) bool {
		entries, err := os.ReadDir(dir)
		return err == nil && len(entries) > 0
	}
	if exists(filepath.Join(dataDir, "store", "grain.db")) || exists(filepath.Join(dataDir, postgres.DatabaseConfigFile)) {
		existing = append(existing, "an existing database")
	}
	if nonEmpty(daemon.SecretsDir(dataDir)) {
		existing = append(existing, "secrets ("+daemon.SecretsDir(dataDir)+")")
	}
	if exists(daemon.StateRepoDir(dataDir)) {
		existing = append(existing, "a state repository ("+daemon.StateRepoDir(dataDir)+")")
	}
	return existing
}

func restoreDatabase(stageDir, dataDir string) error {
	// 1. SQLite database
	srcDB := filepath.Join(stageDir, "database", "grain.db")
	if _, err := os.Stat(srcDB); err == nil {
		storeDir := filepath.Join(dataDir, "store")
		if err := os.MkdirAll(storeDir, 0o700); err != nil {
			return err
		}
		targetDB := filepath.Join(storeDir, "grain.db")
		if err := copyFile(srcDB, targetDB, 0o600); err != nil {
			return err
		}
	}

	// 2. Database config
	srcCfg := filepath.Join(stageDir, "database", postgres.DatabaseConfigFile)
	if _, err := os.Stat(srcCfg); err == nil {
		targetCfg := filepath.Join(dataDir, postgres.DatabaseConfigFile)
		if err := copyFile(srcCfg, targetCfg, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func restoreSecrets(stageDir, dataDir string) error {
	secSrc := filepath.Join(stageDir, "secrets")
	if _, err := os.Stat(secSrc); os.IsNotExist(err) {
		return nil
	}

	secDst := daemon.SecretsDir(dataDir)
	if err := os.MkdirAll(secDst, 0o700); err != nil {
		return err
	}

	return filepath.Walk(secSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(secSrc, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isBundleRelayFile(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(secDst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target, ownerOnlyMode(info.Mode()))
	})
}

func isBundleRelayFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch filepath.Base(rel) {
	case "relay-settings.json", "relay-settings-audit.jsonl", "relay-status.json":
		return true
	}
	return rel == "secrets/relay" || strings.HasPrefix(rel, "secrets/relay/") ||
		rel == "relay" || strings.HasPrefix(rel, "relay/")
}

// ownerOnlyMode is the permission a file extracted or restored from a
// bundle is written with: its owner bits and nothing else, and 0600 when
// it has none. Everything a bundle carries is either the store or a
// credential, and the bundle itself is an input this command did not
// write -- a group- or world-readable mode in its tar headers, or a
// setuid bit, must not carry over into the data directory.
func ownerOnlyMode(mode os.FileMode) os.FileMode {
	if perm := mode.Perm() & 0o700; perm != 0 {
		return perm
	}
	return 0o600
}

func restoreStateRepo(ctx context.Context, stageDir, dataDir, branch string, force bool) error {
	bundlePath := filepath.Join(stageDir, "state", "state-repo.bundle")
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		return nil
	}

	targetRepo := daemon.StateRepoDir(dataDir)
	if _, err := os.Stat(targetRepo); err == nil {
		if force {
			if err := os.RemoveAll(targetRepo); err != nil {
				return fmt.Errorf("removing existing state repository: %w", err)
			}
		} else {
			return fmt.Errorf("state repository already exists at %s", targetRepo)
		}
	}

	if err := os.MkdirAll(filepath.Dir(targetRepo), 0o700); err != nil {
		return err
	}

	if err := cloneStateBundle(ctx, bundlePath, targetRepo, "", false); err != nil {
		return fmt.Errorf("cloning state repo from bundle: %w", err)
	}

	if branch != "" && branch != staterepo.DefaultBranch {
		_, _ = execGit(ctx, targetRepo, "switch", "--end-of-options", branch)
	}
	return nil
}

// cloneStateBundle clones bundlePath into target with every branch the
// bundle carries as a local branch, the way it was on the exporting host,
// and no remote: a plain clone keeps only the checked-out branch locally
// and the rest as origin's, which go with origin when it is removed.
func cloneStateBundle(ctx context.Context, bundlePath, target, branch string, bare bool) error {
	args := []string{"clone"}
	if bare {
		args = append(args, "--bare")
	}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", bundlePath, target)
	if _, err := execGit(ctx, "", args...); err != nil {
		return err
	}
	if !bare {
		if _, err := execGit(ctx, target, "fetch", "--update-head-ok", "--", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
			return fmt.Errorf("copying the bundle's branches: %w", err)
		}
	}
	_, _ = execGit(ctx, target, "remote", "remove", "origin")
	return nil
}

// retiredTeamStateReposNote is what import says about the per-team
// configuration repositories an older bundle lists, or "" for a bundle
// that lists none.
func retiredTeamStateReposNote(repos []RetiredTeamStateRepo) string {
	if len(repos) == 0 {
		return ""
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		// Quoted: the names are the bundle's, and are printed as data.
		names = append(names, fmt.Sprintf("%q", "grain/config-"+r.Team))
	}
	return fmt.Sprintf("note: ignoring %d team configuration %s this bundle carries (%s): "+
		"grain/config is the only configuration repository, and nothing was restored for them",
		len(repos), plural(len(repos), "repository", "repositories"), strings.Join(names, ", "))
}
