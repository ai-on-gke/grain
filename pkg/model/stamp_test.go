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

package model

import (
	"errors"
	"fmt"
	"testing"
)

// SQLite's own wording, captured from a real embedded engine by
// pkg/model/sqlite's TestSQLiteReportsABusyDatabase, which asserts the
// engine still says this. The two fail together if a driver upgrade
// rewords it.
const sqliteBusy = "database is locked (5) (SQLITE_BUSY)"

func TestIsSerializationFailureRecognisesALostRace(t *testing.T) {
	for name, err := range map[string]error{
		"sqlite":              errors.New(sqliteBusy),
		"wrapped by a caller": fmt.Errorf("writing task 7: %w", errors.New(sqliteBusy)),
		"bare SQLITE_BUSY":    errors.New("SQLITE_BUSY"),
	} {
		t.Run(name, func(t *testing.T) {
			if !isSerializationFailure(err) {
				t.Fatalf("%v was not recognised, so write would surface it instead of retrying", err)
			}
		})
	}
}

func TestIsSerializationFailureLeavesEverythingElseAlone(t *testing.T) {
	for name, err := range map[string]error{
		"nil":              nil,
		"connection error": errors.New("connection refused"),
		"unknown column":   errors.New("no such column: nope"),
		"caller's own":     errors.New("updating task 7: no such task"),
	} {
		t.Run(name, func(t *testing.T) {
			if isSerializationFailure(err) {
				t.Fatalf("%v was misread as a lost race, so write would retry it pointlessly", err)
			}
		})
	}
}
