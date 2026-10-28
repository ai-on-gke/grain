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
	"time"

	// The IANA zone database, compiled into every binary that links this
	// package. Without it LoadLocation below answers whichever zones the
	// host filesystem happens to carry under /usr/share/zoneinfo, which
	// is a thing a container image can be built without. Roughly half a
	// megabyte of binary, paid once, for a clock that reads the same
	// everywhere grain runs.
	_ "time/tzdata"
)

// DefaultTimeZone is the universal default time zone across Grain: UTC.
const DefaultTimeZone = "UTC"

// TimeZoneOrDefault is name, or DefaultTimeZone ("UTC") when name is empty.
func TimeZoneOrDefault(name string) string {
	if name == "" {
		return DefaultTimeZone
	}
	return name
}

// LoadLocation resolves a zone name to the *time.Location wall-clock
// computation in grain is done against -- Recurrence.Next chief among them,
// which is what decides when a daily, weekly or monthly schedule actually fires.
//
// An empty name or "UTC" resolves to time.UTC.
// Unlike time.LoadLocation it cannot fail; an unknown name falls back to time.UTC.
func LoadLocation(name string) *time.Location {
	if name == "" || name == "UTC" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

// ValidTimeZone reports whether name is an IANA zone this build can
// actually resolve.
//
// "" and "UTC" are valid. "Local", which time.LoadLocation accepts, is not --
// it names whatever zone the daemon's host or TZ environment variable happens
// to be set to, which is non-deterministic.
func ValidTimeZone(name string) bool {
	if name == "" || name == "UTC" {
		return true
	}
	if name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
