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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// acquireDaemonWriterLock takes the session-level PostgreSQL single-writer
// advisory lock before openStoreForSchemaUpgrade touches the database, so a
// slow-draining old VM cannot write after a replacement VM starts. Returns
// (nil, nil) when running on SQLite.
func acquireDaemonWriterLock(ctx context.Context, cfg Config, onLost func(error)) (*postgres.WriterLock, error) {
	if cfg.DbDriver != "postgres" {
		return nil, nil
	}

	const (
		maxDialRetries = 15
		retryInterval  = 2 * time.Second
	)

	var lastErr error
	for attempt := 1; attempt <= maxDialRetries; attempt++ {
		var pgCfg postgres.Config
		if cfg.DbDSN != "" {
			pgCfg = postgres.DefaultConfig(cfg.DbDSN)
		} else if postgres.IsAvailable() {
			var err error
			pgCfg, _, err = postgres.EnsureRunning(ctx, cfg.DataDir)
			if err != nil {
				lastErr = fmt.Errorf("starting included postgres database: %w", err)
			}
		} else {
			return nil, errors.New("opening the task store: postgres DSN is required")
		}

		if lastErr == nil {
			lock, err := postgres.AcquireWriterLock(ctx, pgCfg, postgres.WriterLockOptions{
				WaitTimeout:       cfg.WriterLockWaitTimeout,
				KeepAliveInterval: cfg.WriterLockKeepAlive,
				ReacquireWindow:   cfg.WriterLockReacquireWindow,
				ApplicationName:   "grain-daemon-writer-lock",
				Logf:              log.Printf,
				OnLost:            onLost,
			})
			if err == nil {
				log.Printf("grain daemon: acquired PostgreSQL single-writer advisory lock (schema=%s, key=%d)",
					lock.Schema(), lock.Key())
				return lock, nil
			}
			// AcquireWriterLock already waited out WaitTimeout on lock contention;
			// do not multiply that wait across connection-readiness retries.
			if errors.Is(err, postgres.ErrWriterLockHeld) || ctx.Err() != nil {
				return nil, fmt.Errorf("acquiring PostgreSQL single-writer lock: %w", err)
			}
			lastErr = err
		}

		if attempt < maxDialRetries {
			log.Printf("grain daemon: PostgreSQL not ready for writer lock yet (%v); retrying in %v (attempt %d/%d)...",
				lastErr, retryInterval, attempt, maxDialRetries)
			timer := time.NewTimer(retryInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			lastErr = nil
		}
	}
	return nil, fmt.Errorf("acquiring PostgreSQL single-writer lock: %w", lastErr)
}
