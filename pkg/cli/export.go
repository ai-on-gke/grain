// grain export creates a standalone, verified archive bundle of a Grain deployment
// containing the SQLite database (checkpointing WAL) or database configuration,
// the controller state repository (state-repo), and all credentials and secrets.
//
// An optional symmetric passphrase or key encrypts the bundle with AES-256-GCM.
package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"golang.org/x/crypto/pbkdf2"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/postgres"
	grainsqlite "github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

const exportUsage = `usage: grain export -data-dir DIR [flags]

Create a verified migration bundle containing the database, state repository,
and credentials/secrets from a Grain deployment.

Flags:
  -data-dir DIR           root directory of the grain installation (required; defaults to $GRAIN_DATA_DIR)
  -out, -output, -file F  path to write the migration bundle (default: grain-export-<timestamp>.tar.gz or .enc)

Encryption (AES-256-GCM, optional; the bundle holds every secret, so encrypt it):
` + bundleSecretFlagsUsage

const (
	EncryptedBundleMagic = "GRAIN_MIGRATE_ENC_V1"
	PBKDF2Iterations     = 100000
	SaltLength           = 16
	NonceLength          = 12
	KeyLength            = 32
	ManifestFileName     = "manifest.json"
	// ExportFormatVersion 2 added team state repositories
	// (state/teams/<team>.bundle, listed in ExportManifest.TeamStateRepos).
	// This build writes none, since grain/config is the only configuration
	// repository, and ignores any an older build's bundle carries; a
	// version 1 bundle has none and still imports.
	ExportFormatVersion = 2
)

// supportedExportFormatVersions are the bundle formats grain import reads.
var supportedExportFormatVersions = []int{1, ExportFormatVersion}

type FileManifest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

type ExportManifest struct {
	FormatVersion   int       `json:"formatVersion"`
	CreatedAt       time.Time `json:"createdAt"`
	GrainVersion    string    `json:"grainVersion,omitempty"`
	SchemaVersion   int       `json:"schemaVersion"`
	DatabaseType    string    `json:"databaseType"` // "sqlite" or "postgres"
	StateRepoBranch string    `json:"stateRepoBranch,omitempty"`
	StateRepoHead   string    `json:"stateRepoHead,omitempty"`
	Encrypted       bool      `json:"encrypted"`
	// TeamStateRepos are the per-team configuration repositories
	// (grain/config-<team>) an older, multi-team build's bundle carried.
	// This build never writes it, and import reads it only to say it is
	// ignoring them.
	TeamStateRepos []RetiredTeamStateRepo `json:"teamStateRepos,omitempty"`
	Files          []FileManifest         `json:"files"`
}

// RetiredTeamStateRepo is the part of an older bundle's entry for a team
// configuration repository that import reports when it ignores it.
type RetiredTeamStateRepo struct {
	Team   string `json:"team"`
	Bundle string `json:"bundle"`
}

func exportCmd(args []string) {
	if err := runExport(context.Background(), args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grain export: "+err.Error())
		os.Exit(1)
	}
}

func runExport(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, exportUsage) }

	dataDir := fs.String("data-dir", dataDirDefault(), "root data directory")
	outPath := fs.String("out", "", "output bundle path")
	fs.StringVar(outPath, "output", "", "output bundle path (alias)")
	fs.StringVar(outPath, "file", "", "output bundle path (alias)")
	secretFlags := addBundleSecretFlags(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *outPath == "" && fs.NArg() > 0 {
		*outPath = fs.Arg(0)
	}

	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}

	pass, key, err := secretFlags.resolve(os.Stderr)
	if err != nil {
		return err
	}

	// Create temporary staging directory
	stageDir, err := os.MkdirTemp("", "grain-export-*")
	if err != nil {
		return fmt.Errorf("creating temporary staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	if err := os.Chmod(stageDir, 0o700); err != nil {
		return fmt.Errorf("protecting staging directory: %w", err)
	}

	// 1. Snapshot database (checkpointing WAL or saving db-config.json)
	dbType, err := snapshotExportDatabase(ctx, *dataDir, stageDir)
	if err != nil {
		return fmt.Errorf("exporting database: %w", err)
	}

	// 2. Bundle state repository
	headSHA, branchName, err := bundleStateRepo(ctx, *dataDir, stageDir)
	if err != nil {
		return fmt.Errorf("exporting state repository: %w", err)
	}

	// 3. Stage credentials and secrets
	if err := stageSecrets(*dataDir, stageDir); err != nil {
		return fmt.Errorf("exporting secrets: %w", err)
	}

	// 4. Generate integrity manifest
	isEncrypted := pass != "" || key != nil
	manifest, err := generateManifest(stageDir, dbType, headSHA, branchName, isEncrypted)
	if err != nil {
		return fmt.Errorf("generating integrity manifest: %w", err)
	}

	// 5. Package into tar.gz
	tarGzBytes, err := createTarGz(stageDir)
	if err != nil {
		return fmt.Errorf("creating archive bundle: %w", err)
	}

	// 6. Encrypt if requested
	finalBytes := tarGzBytes
	if isEncrypted {
		finalBytes, err = encryptBundle(tarGzBytes, pass, key)
		if err != nil {
			return fmt.Errorf("encrypting bundle: %w", err)
		}
	}

	// 7. Write to output file
	targetFile := *outPath
	if targetFile == "" {
		ext := ".tar.gz"
		if isEncrypted {
			ext = ".enc"
		}
		targetFile = fmt.Sprintf("grain-export-%s%s", time.Now().UTC().Format("20060102T150405Z"), ext)
	}

	if err := os.WriteFile(targetFile, finalBytes, 0o600); err != nil {
		return fmt.Errorf("writing output file %s: %w", targetFile, err)
	}

	fmt.Fprintf(out, "exported %d files (%d bytes) to %s (database: %s, state repo HEAD: %s)\n",
		len(manifest.Files), len(finalBytes), targetFile, dbType, headSHA)
	return nil
}

func snapshotExportDatabase(ctx context.Context, dataDir, stageDir string) (string, error) {
	dbType := "sqlite"
	dbConfigPath := filepath.Join(dataDir, postgres.DatabaseConfigFile)
	if cfg, _ := postgres.ReadDatabaseConfig(dataDir); cfg != nil && cfg.Driver != "" {
		dbType = cfg.Driver
		// The bundle gets the file as SaveDatabaseConfig writes it now,
		// with the password in the secret store stageSecrets copies
		// beside it, never one an older build left in clear text.
		if cfg.HasPlaintextPassword() {
			if _, err := postgres.MigrateDatabaseConfigPassword(dataDir, daemon.OpenSecrets(dataDir)); err != nil {
				return "", fmt.Errorf("moving the database password out of %s before exporting it: %w", postgres.DatabaseConfigFile, err)
			}
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "database"), 0o700); err != nil {
			return "", err
		}
		data, err := os.ReadFile(dbConfigPath)
		if err == nil {
			if err := os.WriteFile(filepath.Join(stageDir, "database", postgres.DatabaseConfigFile), data, 0o600); err != nil {
				return "", fmt.Errorf("copying db-config.json: %w", err)
			}
		}
	}

	sqlitePath := filepath.Join(dataDir, "store", "grain.db")
	if _, err := os.Stat(sqlitePath); err == nil {
		if err := os.MkdirAll(filepath.Join(stageDir, "database"), 0o700); err != nil {
			return "", err
		}
		destFile := filepath.Join(stageDir, "database", "grain.db")

		// Open SQLite db, flush WAL and vacuum snapshot
		db, err := grainsqlite.Open(grainsqlite.DefaultConfig(filepath.Join(dataDir, "store")))
		if err == nil {
			_, _ = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
			quoted := "'" + strings.ReplaceAll(destFile, "'", "''") + "'"
			_, vacErr := db.ExecContext(ctx, "VACUUM INTO "+quoted)
			db.Close()
			if vacErr != nil {
				if err := copyFile(sqlitePath, destFile, 0o600); err != nil {
					return "", fmt.Errorf("snapshotting sqlite database: %w", err)
				}
			}
		} else {
			if err := copyFile(sqlitePath, destFile, 0o600); err != nil {
				return "", fmt.Errorf("copying sqlite database: %w", err)
			}
		}
		if dbType == "" {
			dbType = "sqlite"
		}
	}

	return dbType, nil
}

func bundleStateRepo(ctx context.Context, dataDir, stageDir string) (headSHA string, branchName string, err error) {
	repoDir := daemon.StateRepoDir(dataDir)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		if _, err := os.Stat(filepath.Join(dataDir, "state.git")); err == nil {
			repoDir = filepath.Join(dataDir, "state.git")
		} else {
			return "", "", nil
		}
	}

	headOut, err := execGit(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", nil
	}
	headSHA = strings.TrimSpace(headOut)

	branchOut, err := execGit(ctx, repoDir, "symbolic-ref", "--short", "HEAD")
	if err == nil {
		branchName = strings.TrimSpace(branchOut)
	}
	if branchName == "" {
		branchName = staterepo.DefaultBranch
	}

	if err := os.MkdirAll(filepath.Join(stageDir, "state"), 0o700); err != nil {
		return "", "", err
	}
	bundleDest := filepath.Join(stageDir, "state", "state-repo.bundle")
	_, err = execGit(ctx, repoDir, "bundle", "create", "--end-of-options", bundleDest, "--all")
	if err != nil {
		return "", "", fmt.Errorf("creating state repo bundle: %w", err)
	}
	return headSHA, branchName, nil
}

func stageSecrets(dataDir, stageDir string) error {
	secDir := daemon.SecretsDir(dataDir)
	if _, err := os.Stat(secDir); os.IsNotExist(err) {
		return nil
	}

	return filepath.Walk(secDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(stageDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		return copyFile(path, target, mode)
	})
}

func generateManifest(stageDir, dbType, stateHead, stateBranch string, encrypted bool) (*ExportManifest, error) {
	manifest := &ExportManifest{
		FormatVersion:   ExportFormatVersion,
		CreatedAt:       time.Now().UTC(),
		GrainVersion:    fmt.Sprintf("v%d", model.SchemaVersion),
		SchemaVersion:   model.SchemaVersion,
		DatabaseType:    dbType,
		StateRepoBranch: stateBranch,
		StateRepoHead:   stateHead,
		Encrypted:       encrypted,
		Files:           []FileManifest{},
	}

	err := filepath.Walk(stageDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stageDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ManifestFileName {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, FileManifest{
			Path:   rel,
			Size:   info.Size(),
			Mode:   uint32(info.Mode().Perm()),
			SHA256: hex.EncodeToString(hash[:]),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Path < manifest.Files[j].Path
	})

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ManifestFileName), manifestJSON, 0o600); err != nil {
		return nil, fmt.Errorf("writing manifest: %w", err)
	}

	return manifest, nil
}

func createTarGz(stageDir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.Walk(stageDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stageDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if info.IsDir() {
			header.Mode = 0o700
		} else {
			header.Mode = int64(info.Mode().Perm())
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encryptBundle encrypts with rawKey, a decoded 32-byte key, or else with
// a key derived from passphrase; with neither it returns plaintext as is.
func encryptBundle(plaintext []byte, passphrase string, rawKey []byte) ([]byte, error) {
	if passphrase == "" && rawKey == nil {
		return plaintext, nil
	}
	salt := make([]byte, SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}
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
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(EncryptedBundleMagic))

	var buf bytes.Buffer
	buf.WriteString(EncryptedBundleMagic)
	buf.Write(salt)
	buf.Write(nonce)
	buf.Write(ciphertext)
	return buf.Bytes(), nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func execGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
