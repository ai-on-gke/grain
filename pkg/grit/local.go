package grit

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/attest"
	"github.com/bwsalmon/grain/pkg/gritapi"
	"github.com/bwsalmon/grain/pkg/pkgcache"
	"github.com/bwsalmon/grain/pkg/treeid"
	"github.com/bwsalmon/grain/pkg/version"
)

func safeJoin(root, rel string) (string, error) {
	cleanRoot := filepath.Clean(root)
	target := filepath.Join(cleanRoot, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(cleanRoot, target)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return target, nil
}

// JobExecEnv describes the materialized workspace and environment handed to a
// JobRunner for one job execution.
type JobExecEnv struct {
	RunID          string
	Repo           string
	Ref            string
	Commit         string
	Tree           treeid.ID
	Event          string
	TrustedRelease bool
	JobID          string
	Attempt        int
	WorkDir        string
	Env            []string
	SecretValues   []string
}

// JobExecResult is the outcome of a single job execution.
type JobExecResult struct {
	Conclusion string // "success", "failure", "cancelled", "errored"
	Cached     bool
	Log        []byte
	Tests      *attest.Tests
	Artifacts  map[string][]byte
	Fault      *gritapi.FaultSummary
}

// JobRunner executes a single job inside its materialized workspace.
type JobRunner func(ctx context.Context, env JobExecEnv) (JobExecResult, error)

// LocalConfig configures an in-process grit execution instance.
type LocalConfig struct {
	DataDir       string
	Store         *Store
	Executor      string // default "local"
	Concurrency   int    // default 8
	Signer        attest.Signer
	PublicKeys    []gritapi.PublicKeyInfo
	PkgCacheAdmin pkgcache.Admin
	JobRunner     JobRunner
}

// Local is the in-process grit implementation satisfying `gritapi.Backend`
// and backing `grit serve`.
type Local struct {
	store     *Store
	ownsStore bool
	executor  string
	limit     int
	signer    attest.Signer
	pubKeys   []gritapi.PublicKeyInfo
	pkgAdmin  pkgcache.Admin
	jobRunner JobRunner

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	runningJobs int
	queuedJobs  int
	inFlight    map[string]context.CancelFunc
}

// NewLocal constructs and starts a Local instance, resuming any incomplete
// runs stored in DataDir/Store.
func NewLocal(cfg LocalConfig) (*Local, error) {
	store := cfg.Store
	ownsStore := false
	if store == nil {
		if cfg.DataDir == "" {
			return nil, errors.New("grit: LocalConfig requires DataDir or Store")
		}
		var err error
		store, err = OpenStore(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		ownsStore = true
	}
	execName := cfg.Executor
	if execName == "" {
		execName = "local"
	}
	limit := cfg.Concurrency
	if limit <= 0 {
		limit = 8
	}
	runner := cfg.JobRunner
	if runner == nil {
		runner = defaultLocalJobRunner
	}
	var pubKeys []gritapi.PublicKeyInfo
	if len(cfg.PublicKeys) > 0 {
		pubKeys = append(pubKeys, cfg.PublicKeys...)
	} else if cfg.Signer != nil {
		pubKeys = append(pubKeys, gritapi.PublicKeyInfo{
			KeyID:  cfg.Signer.KeyID(),
			Status: attest.KeyActive,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	l := &Local{
		store:     store,
		ownsStore: ownsStore,
		executor:  execName,
		limit:     limit,
		signer:    cfg.Signer,
		pubKeys:   pubKeys,
		pkgAdmin:  cfg.PkgCacheAdmin,
		jobRunner: runner,
		ctx:       ctx,
		cancel:    cancel,
		inFlight:  make(map[string]context.CancelFunc),
	}
	if err := l.resumeIncompleteRuns(); err != nil {
		cancel()
		if ownsStore {
			_ = store.Close()
		}
		return nil, err
	}
	return l, nil
}

// Store returns the underlying durable Store.
func (l *Local) Store() *Store {
	return l.store
}

// Close stops background execution, waits for in-flight goroutines to exit,
// and closes the store if Local opened it.
func (l *Local) Close() error {
	l.cancel()
	l.wg.Wait()
	if l.ownsStore {
		return l.store.Close()
	}
	return nil
}

func (l *Local) resumeIncompleteRuns() error {
	runs, err := l.store.IncompleteRuns()
	if err != nil {
		return err
	}
	for _, r := range runs {
		runCopy := r
		runCtx, runCancel := context.WithCancel(l.ctx)
		l.mu.Lock()
		l.inFlight[runCopy.ID] = runCancel
		l.mu.Unlock()
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.driveRun(runCtx, runCopy, nil)
		}()
	}
	return nil
}

// Info implements gritapi.Backend.
func (l *Local) Info(ctx context.Context) (gritapi.Info, error) {
	l.mu.Lock()
	running := l.runningJobs
	queued := l.queuedJobs
	l.mu.Unlock()

	ver := version.Release
	if ver == "" {
		ver = "v1"
	}
	return gritapi.Info{
		Version:               ver,
		Epoch:                 l.store.Epoch(),
		Executor:              l.executor,
		PackageCache:          l.pkgAdmin != nil,
		AttestationPublicKeys: l.pubKeys,
		Capacity: gritapi.Capacity{
			Running: running,
			Queued:  queued,
			Limit:   l.limit,
		},
	}, nil
}

// NegotiateTrees implements gritapi.Backend.
func (l *Local) NegotiateTrees(ctx context.Context, sha256s []string) ([]string, error) {
	var missing []string
	for _, h := range sha256s {
		h = strings.TrimSpace(strings.ToLower(h))
		if h == "" {
			continue
		}
		ok, err := l.store.HasTree(h)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, h)
		}
	}
	return missing, nil
}

// UploadTree verifies that ustarArchive hashes to sha256Hex and persists it in
// the tree store.
func (l *Local) UploadTree(ctx context.Context, sha256Hex string, ustarArchive []byte) error {
	want := strings.TrimSpace(strings.ToLower(sha256Hex))
	sum := sha256.Sum256(ustarArchive)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return &gritapi.APIError{
			StatusCode: 400,
			Code:       "tree_digest_mismatch",
			Message:    fmt.Sprintf("uploaded archive sha256 %s does not match %s", got, want),
		}
	}
	return l.store.PutTree(want, ustarArchive)
}

// StartRun implements gritapi.Backend.
func (l *Local) StartRun(ctx context.Context, req gritapi.StartRun) (gritapi.RunStatus, error) {
	if req.Repo == "" || req.Commit == "" {
		return gritapi.RunStatus{}, &gritapi.APIError{
			StatusCode: 400,
			Code:       "invalid_request",
			Message:    "repo and commit are required",
		}
	}
	if req.RequestID == "" {
		req.RequestID = gritapi.ComputeRequestID(req)
	}
	if req.Secrets != nil {
		if err := validateSecrets(req.Secrets); err != nil {
			return gritapi.RunStatus{}, err
		}
	}

	// Check if requestId already exists (idempotent submission).
	l.store.mu.Lock()
	existing, exists, err := l.store.getRunByKeyLocked("request_id", req.RequestID)
	l.store.mu.Unlock()
	if err != nil {
		return gritapi.RunStatus{}, err
	}
	if exists {
		return existing, nil
	}

	if req.Tree.SHA256 != "" {
		hasTree, err := l.store.HasTree(strings.ToLower(req.Tree.SHA256))
		if err != nil {
			return gritapi.RunStatus{}, err
		}
		if !hasTree {
			return gritapi.RunStatus{}, &gritapi.APIError{
				StatusCode: 428,
				Code:       "tree_missing",
				SHA256:     strings.ToLower(req.Tree.SHA256),
				Message:    fmt.Sprintf("tree %s is not in grit's tree store", req.Tree.SHA256),
			}
		}
	}

	jobNames := req.Jobs
	if len(jobNames) == 0 {
		jobNames = []string{"default"}
	}
	jobs := make([]gritapi.JobStatus, len(jobNames))
	for i, name := range jobNames {
		jobs[i] = gritapi.JobStatus{
			ID:      name,
			Name:    name,
			Status:  "queued",
			Attempt: 1,
		}
	}

	var idBytes [6]byte
	_, _ = rand.Read(idBytes[:])
	runID := "r-" + hex.EncodeToString(idBytes[:])
	now := time.Now().UTC()
	run := gritapi.RunStatus{
		ID:             runID,
		RequestID:      req.RequestID,
		Repo:           req.Repo,
		Ref:            req.Ref,
		Commit:         req.Commit,
		Tree:           req.Tree,
		Event:          req.Event,
		TrustedRelease: req.TrustedRelease,
		Status:         "queued",
		Jobs:           jobs,
		CreatedAt:      now,
	}

	saved, created, err := l.store.CreateOrGetRun(run)
	if err != nil {
		return gritapi.RunStatus{}, err
	}
	if !created {
		return saved, nil
	}

	_, _ = l.store.AppendEvent(gritapi.Event{
		At:        now,
		RunID:     saved.ID,
		RequestID: saved.RequestID,
		Repo:      saved.Repo,
		Kind:      gritapi.EventRunQueued,
	})
	_, _ = l.store.AppendEvent(gritapi.Event{
		At:        now,
		RunID:     saved.ID,
		RequestID: saved.RequestID,
		Repo:      saved.Repo,
		Kind:      gritapi.EventRunPlanned,
		Jobs:      saved.Jobs,
	})

	runCtx, runCancel := context.WithCancel(l.ctx)
	l.mu.Lock()
	l.inFlight[saved.ID] = runCancel
	l.queuedJobs += len(saved.Jobs)
	l.mu.Unlock()

	secretsCopy := cloneSecrets(req.Secrets)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.driveRun(runCtx, saved, secretsCopy)
	}()

	return saved, nil
}

func validateSecrets(sp *gritapi.SecretPayload) error {
	for _, entry := range sp.Manifest {
		if entry.Kind != "env" && entry.Kind != "file" {
			return &gritapi.APIError{
				StatusCode: 400,
				Code:       "invalid_secret_manifest",
				Message:    fmt.Sprintf("unsupported secret kind %q", entry.Kind),
			}
		}
		blob, ok := sp.Blobs[entry.SHA256]
		if !ok {
			return &gritapi.APIError{
				StatusCode: 400,
				Code:       "missing_secret_blob",
				Message:    fmt.Sprintf("secret blob %s missing for target %s", entry.SHA256, entry.Target),
			}
		}
		sum := sha256.Sum256(blob)
		if hex.EncodeToString(sum[:]) != strings.ToLower(entry.SHA256) {
			return &gritapi.APIError{
				StatusCode: 400,
				Code:       "secret_digest_mismatch",
				Message:    fmt.Sprintf("secret blob digest mismatch for %s", entry.Target),
			}
		}
	}
	return nil
}

func cloneSecrets(sp *gritapi.SecretPayload) *gritapi.SecretPayload {
	if sp == nil {
		return nil
	}
	out := &gritapi.SecretPayload{
		Manifest: append([]gritapi.SecretManifestEntry(nil), sp.Manifest...),
		Blobs:    make(map[string][]byte, len(sp.Blobs)),
	}
	for k, v := range sp.Blobs {
		out.Blobs[k] = append([]byte(nil), v...)
	}
	return out
}

func zeroSecrets(sp *gritapi.SecretPayload) {
	if sp == nil {
		return
	}
	for k, b := range sp.Blobs {
		for i := range b {
			b[i] = 0
		}
		delete(sp.Blobs, k)
	}
}

func (l *Local) driveRun(ctx context.Context, run gritapi.RunStatus, secrets *gritapi.SecretPayload) {
	run.Jobs = append([]gritapi.JobStatus(nil), run.Jobs...)
	defer func() {
		zeroSecrets(secrets)
		l.mu.Lock()
		delete(l.inFlight, run.ID)
		l.mu.Unlock()
	}()

	if run.StartedAt == nil {
		now := time.Now().UTC()
		run.StartedAt = &now
	}
	run.Status = "running"
	_ = l.store.UpdateRun(run, nil)

	overallConclusion := "success"
	for i := range run.Jobs {
		if run.Jobs[i].Status == "completed" {
			if run.Jobs[i].Conclusion != "success" && overallConclusion == "success" {
				overallConclusion = run.Jobs[i].Conclusion
			}
			continue
		}

		if ctx.Err() != nil {
			// Check whether the cancellation came from CancelRun vs Local.Close() shutdown.
			current, ok, _ := l.store.GetRun(run.ID)
			if ok && current.Status == "completed" && current.Conclusion == "cancelled" {
				return
			}
			// Local is shutting down mid-run; leave the run in `running` state
			// in SQLite so the next Local instance resumes it cleanly.
			return
		}

		l.mu.Lock()
		if l.queuedJobs > 0 {
			l.queuedJobs--
		}
		l.runningJobs++
		l.mu.Unlock()

		jobStart := time.Now().UTC()
		run.Jobs[i].Status = "running"
		run.Jobs[i].StartedAt = &jobStart
		if run.Jobs[i].Attempt <= 0 {
			run.Jobs[i].Attempt = 1
		}
		_ = l.store.UpdateRun(run, nil)
		_, _ = l.store.AppendEvent(gritapi.Event{
			At:        jobStart,
			RunID:     run.ID,
			RequestID: run.RequestID,
			Repo:      run.Repo,
			Kind:      gritapi.EventJobStarted,
			Job:       run.Jobs[i].ID,
			Attempt:   run.Jobs[i].Attempt,
		})

		res, err := l.executeOneJob(ctx, run, run.Jobs[i], secrets)
		l.mu.Lock()
		if l.runningJobs > 0 {
			l.runningJobs--
		}
		l.mu.Unlock()

		if ctx.Err() != nil {
			current, ok, _ := l.store.GetRun(run.ID)
			if ok && current.Status == "completed" && current.Conclusion == "cancelled" {
				return
			}
			return
		}

		jobEnd := time.Now().UTC()
		durMs := jobEnd.Sub(jobStart).Milliseconds()
		if durMs < 0 {
			durMs = 0
		}
		conclusion := res.Conclusion
		if err != nil && conclusion == "" {
			conclusion = "errored"
		}
		if conclusion == "" {
			conclusion = "success"
		}

		_ = l.store.WriteLog(run.ID, run.Jobs[i].ID, res.Log, true)
		for name, data := range res.Artifacts {
			_ = l.store.WriteArtifact(run.ID, run.Jobs[i].ID, name, data)
		}

		run.Jobs[i].Status = "completed"
		run.Jobs[i].Conclusion = conclusion
		run.Jobs[i].Cached = res.Cached
		run.Jobs[i].DurationMs = durMs
		run.Jobs[i].Tests = res.Tests
		run.Jobs[i].CompletedAt = &jobEnd
		_ = l.store.UpdateRun(run, nil)

		if conclusion != "success" && overallConclusion == "success" {
			overallConclusion = conclusion
		}

		_, _ = l.store.AppendEvent(gritapi.Event{
			At:         jobEnd,
			RunID:      run.ID,
			RequestID:  run.RequestID,
			Repo:       run.Repo,
			Kind:       gritapi.EventJobCompleted,
			Job:        run.Jobs[i].ID,
			Attempt:    run.Jobs[i].Attempt,
			Conclusion: conclusion,
			Cached:     res.Cached,
			DurationMs: durMs,
			Tests:      res.Tests,
			Fault:      res.Fault,
		})
	}

	completedAt := time.Now().UTC()
	run.Status = "completed"
	run.Conclusion = overallConclusion
	run.CompletedAt = &completedAt

	var signedEnv *attest.Envelope
	if l.signer != nil {
		env, attSHA, err := l.signRunAttestation(run)
		if err == nil {
			signedEnv = env
			run.AttestationSHA256 = attSHA
		}
	}

	_ = l.store.UpdateRun(run, signedEnv)
	if run.AttestationSHA256 != "" {
		_, _ = l.store.AppendEvent(gritapi.Event{
			At:                completedAt,
			RunID:             run.ID,
			RequestID:         run.RequestID,
			Repo:              run.Repo,
			Kind:              gritapi.EventRunAttested,
			AttestationSHA256: run.AttestationSHA256,
		})
	}
	_, _ = l.store.AppendEvent(gritapi.Event{
		At:                completedAt,
		RunID:             run.ID,
		RequestID:         run.RequestID,
		Repo:              run.Repo,
		Kind:              gritapi.EventRunCompleted,
		Conclusion:        run.Conclusion,
		AttestationSHA256: run.AttestationSHA256,
		Jobs:              run.Jobs,
	})
}

func (l *Local) signRunAttestation(run gritapi.RunStatus) (*attest.Envelope, string, error) {
	started := run.CreatedAt
	if run.StartedAt != nil {
		started = *run.StartedAt
	}
	completed := time.Now().UTC()
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	attJobs := make([]attest.Job, len(run.Jobs))
	for i, j := range run.Jobs {
		attJobs[i] = attest.Job{
			ID: j.ID,
			Result: attest.Result{
				Conclusion: j.Conclusion,
				Attempts:   j.Attempt,
			},
			Tests: j.Tests,
		}
	}
	ver := version.Release
	if ver == "" {
		ver = "v1"
	}
	stmt := attest.NewStatement(
		attest.Tree{
			SHA256:  run.Tree.SHA256,
			GitTree: run.Tree.GitTree,
		},
		run.Commit,
		attest.RunPredicate{
			Grit: attest.GritInfo{
				Version:  ver,
				Instance: l.store.Epoch(),
			},
			Request: attest.RequestInfo{
				RequestID:   run.RequestID,
				Run:         run.ID,
				Repo:        run.Repo,
				Ref:         run.Ref,
				Event:       run.Event,
				SubmittedAt: run.CreatedAt,
			},
			Jobs: attJobs,
			Timing: attest.Timing{
				QueuedAt:   &run.CreatedAt,
				StartedAt:  &started,
				FinishedAt: &completed,
			},
			IssuedAt: completed,
		},
	)
	env, err := attest.Sign(context.Background(), l.signer, stmt)
	if err != nil {
		return nil, "", err
	}
	dig, err := attest.Digest(env)
	if err != nil {
		return nil, "", err
	}
	return env, strings.TrimPrefix(dig, "sha256:"), nil
}

func (l *Local) executeOneJob(ctx context.Context, run gritapi.RunStatus, job gritapi.JobStatus, secrets *gritapi.SecretPayload) (JobExecResult, error) {
	workDir, err := os.MkdirTemp("", "grit-job-*")
	if err != nil {
		return JobExecResult{Conclusion: "errored"}, err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	if run.Tree.SHA256 != "" {
		archive, err := l.store.ReadTree(strings.ToLower(run.Tree.SHA256))
		if err != nil {
			return JobExecResult{Conclusion: "errored"}, err
		}
		if err := extractUstarArchive(archive, workDir); err != nil {
			return JobExecResult{Conclusion: "errored"}, err
		}
	}

	var extraEnv []string
	var secretValues []string
	if secrets != nil {
		for _, m := range secrets.Manifest {
			blob := secrets.Blobs[m.SHA256]
			if len(blob) == 0 {
				continue
			}
			secretValues = append(secretValues, string(blob))
			switch m.Kind {
			case "env":
				extraEnv = append(extraEnv, m.Target+"="+string(blob))
			case "file":
				targetPath, err := safeJoin(workDir, strings.TrimLeft(m.Target, "/"))
				if err != nil {
					return JobExecResult{Conclusion: "errored"}, err
				}
				if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
					return JobExecResult{Conclusion: "errored"}, err
				}
				if err := os.WriteFile(targetPath, blob, 0o600); err != nil {
					return JobExecResult{Conclusion: "errored"}, err
				}
			}
		}
	}

	res, err := l.jobRunner(ctx, JobExecEnv{
		RunID:          run.ID,
		Repo:           run.Repo,
		Ref:            run.Ref,
		Commit:         run.Commit,
		Tree:           run.Tree,
		Event:          run.Event,
		TrustedRelease: run.TrustedRelease,
		JobID:          job.ID,
		Attempt:        job.Attempt,
		WorkDir:        workDir,
		Env:            extraEnv,
		SecretValues:   secretValues,
	})
	if len(res.Log) > 0 && len(secretValues) > 0 {
		s := string(res.Log)
		for _, sec := range secretValues {
			if sec != "" {
				s = strings.ReplaceAll(s, sec, "[REDACTED]")
			}
		}
		res.Log = []byte(s)
	}
	return res, err
}

func extractUstarArchive(archive []byte, destDir string) error {
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func defaultLocalJobRunner(ctx context.Context, env JobExecEnv) (JobExecResult, error) {
	scriptPath := filepath.Join(env.WorkDir, ".grain", "grit-job.sh")
	if _, err := os.Stat(scriptPath); err == nil {
		cmd := exec.CommandContext(ctx, "bash", scriptPath, env.JobID)
		cmd.Dir = env.WorkDir
		cmd.Env = append([]string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + env.WorkDir,
			"GRIT_RUN_ID=" + env.RunID,
			"GRIT_JOB_ID=" + env.JobID,
			"GRIT_REPO=" + env.Repo,
			"GRIT_REF=" + env.Ref,
			"GRIT_COMMIT=" + env.Commit,
		}, env.Env...)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			return JobExecResult{
				Conclusion: "failure",
				Log:        out,
				Fault: &gritapi.FaultSummary{
					Kind:    "step_failed",
					Step:    env.JobID,
					Message: runErr.Error(),
				},
			}, nil
		}
		return JobExecResult{
			Conclusion: "success",
			Log:        out,
		}, nil
	}
	return JobExecResult{
		Conclusion: "success",
		Log:        []byte(fmt.Sprintf("job %s completed on %s@%s\n", env.JobID, env.Repo, env.Commit)),
	}, nil
}

// GetRun implements gritapi.Backend.
func (l *Local) GetRun(ctx context.Context, id string) (gritapi.RunStatus, error) {
	run, ok, err := l.store.GetRun(id)
	if err != nil {
		return gritapi.RunStatus{}, err
	}
	if !ok {
		return gritapi.RunStatus{}, &gritapi.APIError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    fmt.Sprintf("run %q not found", id),
		}
	}
	return run, nil
}

// CancelRun implements gritapi.Backend.
func (l *Local) CancelRun(ctx context.Context, id string) (gritapi.RunStatus, error) {
	run, ok, err := l.store.GetRun(id)
	if err != nil {
		return gritapi.RunStatus{}, err
	}
	if !ok {
		return gritapi.RunStatus{}, &gritapi.APIError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    fmt.Sprintf("run %q not found", id),
		}
	}
	if run.Status == "completed" {
		return run, nil
	}

	now := time.Now().UTC()
	for i := range run.Jobs {
		if run.Jobs[i].Status != "completed" {
			run.Jobs[i].Status = "completed"
			run.Jobs[i].Conclusion = "cancelled"
			run.Jobs[i].CompletedAt = &now
		}
	}
	run.Status = "completed"
	run.Conclusion = "cancelled"
	run.CompletedAt = &now
	_ = l.store.UpdateRun(run, nil)

	l.mu.Lock()
	cancelFn := l.inFlight[id]
	delete(l.inFlight, id)
	l.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}

	_, _ = l.store.AppendEvent(gritapi.Event{
		At:         now,
		RunID:      run.ID,
		RequestID:  run.RequestID,
		Repo:       run.Repo,
		Kind:       gritapi.EventRunCompleted,
		Conclusion: "cancelled",
		Jobs:       run.Jobs,
	})
	return run, nil
}

// RetriggerRun implements gritapi.Backend.
func (l *Local) RetriggerRun(ctx context.Context, id string, req gritapi.RetriggerRequest) (gritapi.RunStatus, error) {
	run, ok, err := l.store.GetRun(id)
	if err != nil {
		return gritapi.RunStatus{}, err
	}
	if !ok {
		return gritapi.RunStatus{}, &gritapi.APIError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    fmt.Sprintf("run %q not found", id),
		}
	}
	wantSet := make(map[string]bool, len(req.Jobs))
	for _, j := range req.Jobs {
		wantSet[j] = true
	}
	for i := range run.Jobs {
		shouldRerun := false
		if len(wantSet) > 0 {
			shouldRerun = wantSet[run.Jobs[i].ID] || wantSet["*"]
		} else {
			shouldRerun = run.Jobs[i].Conclusion != "success"
		}
		if shouldRerun {
			run.Jobs[i].Status = "queued"
			run.Jobs[i].Conclusion = ""
			run.Jobs[i].Attempt++
			run.Jobs[i].CompletedAt = nil
		}
	}
	run.Status = "running"
	run.Conclusion = ""
	run.CompletedAt = nil
	if err := l.store.UpdateRun(run, nil); err != nil {
		return gritapi.RunStatus{}, err
	}

	runCtx, runCancel := context.WithCancel(l.ctx)
	l.mu.Lock()
	l.inFlight[run.ID] = runCancel
	l.mu.Unlock()

	secretsCopy := cloneSecrets(req.Secrets)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.driveRun(runCtx, run, secretsCopy)
	}()
	return run, nil
}

// GetAttestation implements gritapi.Backend.
func (l *Local) GetAttestation(ctx context.Context, id string) (*attest.Envelope, error) {
	env, ok, err := l.store.GetAttestation(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &gritapi.APIError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    fmt.Sprintf("attestation for run %q not found", id),
		}
	}
	return env, nil
}

// GetLog implements gritapi.Backend.
func (l *Local) GetLog(ctx context.Context, runID, jobID string, offset, limit int64) ([]byte, int64, bool, error) {
	data, next, comp, ok, err := l.store.ReadLog(runID, jobID, offset, limit)
	if err != nil {
		return nil, offset, false, err
	}
	if !ok {
		return nil, offset, false, &gritapi.APIError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    fmt.Sprintf("log for run %s job %s not found", runID, jobID),
		}
	}
	return data, next, comp, nil
}

// GetArtifact implements gritapi.Backend.
func (l *Local) GetArtifact(ctx context.Context, runID, jobID, name string) ([]byte, error) {
	data, ok, err := l.store.ReadArtifact(runID, jobID, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &gritapi.APIError{
			StatusCode: 404,
			Code:       "not_found",
			Message:    fmt.Sprintf("artifact %s for run %s job %s not found", name, runID, jobID),
		}
	}
	return data, nil
}

// Events implements gritapi.Backend.
func (l *Local) Events(ctx context.Context, after int64, wait time.Duration, limit int) (gritapi.EventBatch, error) {
	return l.EventsFiltered(ctx, after, wait, limit, nil)
}

// EventsFiltered returns events after seq matching allowRepo, waiting up to
// wait if none are immediately available.
func (l *Local) EventsFiltered(ctx context.Context, after int64, wait time.Duration, limit int, allowRepo func(string) bool) (gritapi.EventBatch, error) {
	deadline := time.Now().Add(wait)
	for {
		ch := l.store.WaitChannel()
		batch, err := l.store.EventsAfter(after, limit, allowRepo)
		if err != nil {
			return gritapi.EventBatch{}, err
		}
		if len(batch.Events) > 0 || wait <= 0 {
			return batch, nil
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return batch, nil
		}
		timer := time.NewTimer(rem)
		select {
		case <-ctx.Done():
			timer.Stop()
			return batch, nil
		case <-l.ctx.Done():
			timer.Stop()
			return batch, nil
		case <-ch:
			timer.Stop()
		case <-timer.C:
			return batch, nil
		}
	}
}

// PkgStats implements gritapi.Backend.
func (l *Local) PkgStats(ctx context.Context) (pkgcache.V1Stats, error) {
	if l.pkgAdmin == nil {
		return pkgcache.V1Stats{Enabled: false, Cache: []pkgcache.KindStats{}}, nil
	}
	st, err := l.pkgAdmin.Stats()
	if err != nil {
		return pkgcache.V1Stats{}, err
	}
	return pkgcache.V1Stats{
		Enabled: true,
		Entries: l.pkgAdmin.Entries(),
		Cache:   st,
	}, nil
}

// PkgFlush implements gritapi.Backend.
func (l *Local) PkgFlush(ctx context.Context, kind string) (pkgcache.V1Flush, error) {
	if l.pkgAdmin == nil {
		return pkgcache.V1Flush{Kind: kind, Cache: []pkgcache.KindStats{}}, nil
	}
	removed, bytesRemoved, err := l.pkgAdmin.Flush(kind)
	if err != nil {
		return pkgcache.V1Flush{}, err
	}
	st, err := l.pkgAdmin.Stats()
	if err != nil {
		return pkgcache.V1Flush{}, err
	}
	return pkgcache.V1Flush{
		Kind:    kind,
		Entries: removed,
		Bytes:   bytesRemoved,
		Cache:   st,
	}, nil
}
