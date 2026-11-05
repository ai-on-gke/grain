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

// storeswitch.go notices the store being switched underneath a running
// daemon. On 2026-11-04 `grain upgrade-postgres` rewrote db-config.json
// to select PostgreSQL while the daemon kept running, and writing, on
// SQLite for another twelve hours; the next start opened the stale copy.
// A daemon now watches what its data directory selects and, the moment
// that is no longer the store it opened, says so at error level, raises
// a critical deployment alert (GET /api/config's storeSwitch, `grain
// health`) and dispatches nothing more until it is restarted.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// storeSelectionInterval is how often the selection is read again.
var storeSelectionInterval = 10 * time.Second

// storeSwitched is set, once and for the life of the process, when the
// data directory starts selecting a store other than the one opened.
var storeSwitched atomic.Pointer[ui.StoreSwitch]

// CurrentStoreSwitch is the standing alert, or nil.
func CurrentStoreSwitch() *ui.StoreSwitch { return storeSwitched.Load() }

// storeDispatchHalted is orchestrator.Deps.DispatchHalted.
func storeDispatchHalted() string {
	if s := storeSwitched.Load(); s != nil {
		return s.Message
	}
	return ""
}

func sanitizedDSN(dsn string) string {
	return postgres.DefaultConfig(dsn).SanitizedDSN()
}

// storeSelection is the store a start on dataDir would open now, the way
// ResolveDatabase finds it, named by StoreDescription. It reads
// db-config.json directly rather than through LoadDatabaseConfig, which
// touches the secret store: only the driver and where it points matter
// here, never the password.
func storeSelection(dataDir string) (string, error) {
	driver, dsn := os.Getenv("GRAIN_DB_DRIVER"), os.Getenv("GRAIN_DB_DSN")
	if dsn == "" {
		if u := os.Getenv("GRAIN_POSTGRES_URL"); u != "" {
			driver, dsn = "postgres", u
		} else if u := os.Getenv("DATABASE_URL"); u != "" {
			driver, dsn = "postgres", u
		}
	}
	if driver == "" {
		saved, err := postgres.ReadDatabaseConfig(dataDir)
		if err != nil {
			return "", err
		}
		if saved != nil && saved.Driver != "" {
			driver = saved.Driver
			if dsn == "" {
				dsn = saved.DSN
			}
		}
	}
	return StoreDescription(driver, dsn, dataDir), nil
}

// checkStoreSelection compares the selection now with opened, the one
// taken when the store was opened, and raises the alert on a change. It
// reports whether the alert stands.
func checkStoreSelection(dataDir, opened string, now time.Time) bool {
	if storeSwitched.Load() != nil {
		return true
	}
	selected, err := storeSelection(dataDir)
	if err != nil || selected == opened {
		return false
	}
	msg := fmt.Sprintf("the store this daemon writes to is no longer the one %s selects: it opened %s, and a restart would open %s. "+
		"Something copied, migrated or moved the store while this daemon was running, and every write since the copy is in this store only. "+
		"No more tasks are dispatched until the daemon is restarted. Stop it (%s), bring the new store up to date from this one "+
		"(or put %s back), check it is not behind %s, and start it again (docs/database.md)",
		filepath.Join(dataDir, postgres.DatabaseConfigFile), opened, selected, StopDaemonCommand,
		postgres.DatabaseConfigFile, StoreHighWaterFile)
	if storeSwitched.CompareAndSwap(nil, &ui.StoreSwitch{Since: now.UTC(), Opened: opened, Selected: selected, Message: msg}) {
		log.Printf("grain daemon: ERROR: %s", msg)
	}
	return true
}

// watchStoreSelection runs checkStoreSelection until ctx ends.
func watchStoreSelection(ctx context.Context, dataDir, opened string) {
	if dataDir == "" {
		return
	}
	t := time.NewTicker(storeSelectionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			checkStoreSelection(dataDir, opened, now)
		}
	}
}
