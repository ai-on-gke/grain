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

package usage

// The weekly horizon (grain/task-5618).
//
// Providers hold a credential to two quotas at once: the short session
// (interval) window a scope's Window describes, and an account-wide
// weekly one ("You've hit your weekly limit · resets Nov 15, 3am
// (UTC)"). A scope reported over its interval alone reads "0 used, all
// of it left" while its weekly quota is spent and dispatch is paused on
// it, so every scope carries the weekly horizon beside the interval:
// what its runs spent over the last WeeklyWindow, the weekly capacity
// grain learned the last time a weekly limit refused it, and the weekly
// pause in force.
//
// A pause is a weekly one when the provider's own words say so
// (PauseHorizon). Its learned capacity is kept in usage_capacity beside
// the interval's, under the scope's model with weeklyCapacitySuffix, so
// learning one never overwrites the other.

import (
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// WeeklyWindow is the account-wide weekly horizon every scope is also
// reported over.
const WeeklyWindow = 7 * 24 * time.Hour

// The horizons a usage-limit pause can have met (Scope.PauseHorizon).
const (
	// HorizonInterval is the short session window (Scope.Window).
	HorizonInterval = "interval"
	// HorizonWeekly is the account-wide WeeklyWindow.
	HorizonWeekly = "weekly"
)

// weeklyLimitPattern is how a provider's refusal says it is the weekly
// quota: Claude Code's "hit your weekly limit" (and "Opus weekly
// limit"), or a 7-day / per-week quota in another provider's words.
var weeklyLimitPattern = regexp.MustCompile(`(?i)\bweekly\b|\b7[- ]day\b|\bper week\b`)

// PauseHorizon is which of a scope's horizons a usage-limit pause says
// it met: HorizonWeekly when the provider's refusal names its weekly
// quota, else HorizonInterval.
func PauseHorizon(p model.UsageLimitPause) string {
	if weeklyLimitPattern.MatchString(p.Reason) {
		return HorizonWeekly
	}
	return HorizonInterval
}

// weeklyCapacitySuffix marks a usage_capacity row holding a scope's
// learned weekly capacity rather than its interval one.
const weeklyCapacitySuffix = "#weekly"

// capacityModel is the usage_capacity model key of scope model m on
// horizon.
func capacityModel(m, horizon string) string {
	if horizon == HorizonWeekly {
		return m + weeklyCapacitySuffix
	}
	return m
}

// splitCapacityModel is a usage_capacity model key's scope model, and
// whether the row is a weekly capacity.
func splitCapacityModel(key string) (string, bool) {
	if m, ok := strings.CutSuffix(key, weeklyCapacitySuffix); ok {
		return m, true
	}
	return key, false
}
