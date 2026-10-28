package daemon

import (
	"context"
	"database/sql"
	"log"
	"time"

	grainbackup "github.com/ai-on-gke/grain/pkg/backup"
)

func BackupService(ctx context.Context, gcsBucket, driveFolder, credentialsFile string) (grainbackup.Service, string, error) {
	if gcsBucket != "" {
		destination, err := grainbackup.NewGoogleCloudStorage(ctx, gcsBucket, credentialsFile)
		if err != nil {
			return grainbackup.Service{}, "", err
		}
		return grainbackup.Service{Destination: destination}, "GCS", nil
	}
	destination, err := grainbackup.NewGoogleDrive(ctx, driveFolder, credentialsFile)
	if err != nil {
		return grainbackup.Service{}, "", err
	}
	return grainbackup.Service{Destination: destination}, "Google Drive", nil
}

// startScheduledBackups starts no goroutine when backups are disabled. A
// configured scheduler waits one full interval before its first run: startup is
// already busy restoring and reconciling state, and an operator who wants a
// backup immediately has the manual command above.
func startScheduledBackups(ctx context.Context, db *sql.DB, cfg Config) <-chan struct{} {
	done := make(chan struct{})
	if cfg.BackupInterval == 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		RunBackupLoop(ctx, cfg.BackupInterval, func(runCtx context.Context) {
			// Construct the client per run so a credential file repaired or
			// rotated under a live daemon is picked up at the next interval.
			// A broken backup target is logged and retried; it must not take
			// the task UI or reconciler down with it.
			service, destination, err := BackupService(runCtx, cfg.BackupGCSBucket, cfg.BackupGoogleDriveFolder, cfg.BackupGoogleCredentialsFile)
			if err != nil {
				log.Printf("grain backup: scheduled backup failed: %v", err)
				return
			}
			result, err := service.Run(runCtx, db, StateRepoDir(cfg.DataDir))
			if err != nil {
				log.Printf("grain backup: scheduled backup failed: %v", err)
				return
			}
			log.Printf("grain backup: scheduled backup uploaded %s to %s (%d bytes, config %s)",
				result.Name, destination, result.Size, shortCommit(result.ConfigHead))
		})
	}()
	return done
}

func RunBackupLoop(ctx context.Context, interval time.Duration, run func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A tick and a cancellation that are both ready are chosen
			// between at random, so the tick that arrived while the last
			// backup was running can be picked after the deployment has
			// already been told to stop. Nothing should start a backup
			// with a context that is already done -- it would be
			// cancelled halfway through at best -- and on a loaded host
			// that window is wide enough to hit (the CI failure this was
			// noticed as, grain/task-196).
			if ctx.Err() != nil {
				return
			}
			run(ctx)
		}
	}
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
