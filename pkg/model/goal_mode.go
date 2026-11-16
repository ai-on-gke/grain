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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	DefaultGoalMaxPasses        = 8
	MinGoalMaxPasses            = 1
	MaxGoalMaxPasses            = 20
	DefaultGoalWallClockMinutes = 120
	MinGoalWallClockMinutes     = 5
	MaxGoalWallClockMinutes     = 720
)

// GoalModeConfig configures autonomous multi-pass Goal Mode execution for a task.
type GoalModeConfig struct {
	Enabled            bool `json:"enabled"`
	MaxPasses          int  `json:"maxPasses,omitempty"`
	WallClockMinutes   int  `json:"wallClockMinutes,omitempty"`
	MockMissingSecrets bool `json:"mockMissingSecrets,omitempty"`
}

// GoalAssumption records an autonomous engineering or UX decision made when
// resolving ambiguity without human interaction.
type GoalAssumption struct {
	ID           string    `json:"id"`
	Topic        string    `json:"topic"`
	Decision     string    `json:"decision"`
	Rationale    string    `json:"rationale,omitempty"`
	Alternatives string    `json:"alternatives,omitempty"`
	Pass         int       `json:"pass,omitempty"`
	CreatedAt    time.Time `json:"createdAt,omitempty"`
}

// GoalDeliverable records a concrete requirement and the observed verification
// evidence supporting its completion.
type GoalDeliverable struct {
	Requirement string `json:"requirement"`
	Evidence    string `json:"evidence"`
	Verified    bool   `json:"verified"`
}

// GoalScorecard summarizes the multi-pass verification status and deliverables
// of an autonomous Goal Mode task.
type GoalScorecard struct {
	PassesUsed   int               `json:"passesUsed"`
	MaxPasses    int               `json:"maxPasses"`
	Verified     bool              `json:"verified"`
	StoppedAtCap bool              `json:"stoppedAtCap,omitempty"`
	StopReason   string            `json:"stopReason,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	Deliverables []GoalDeliverable `json:"deliverables,omitempty"`
	UpdatedAt    time.Time         `json:"updatedAt,omitempty"`
}

// NormalizeGoalModeConfig applies defaults and clamps bounds when Enabled is true.
// When Enabled is false, it returns a zeroed config with Enabled: false.
func NormalizeGoalModeConfig(cfg GoalModeConfig) GoalModeConfig {
	if !cfg.Enabled {
		return GoalModeConfig{Enabled: false}
	}
	if cfg.MaxPasses == 0 {
		cfg.MaxPasses = DefaultGoalMaxPasses
	} else if cfg.MaxPasses < MinGoalMaxPasses {
		cfg.MaxPasses = MinGoalMaxPasses
	} else if cfg.MaxPasses > MaxGoalMaxPasses {
		cfg.MaxPasses = MaxGoalMaxPasses
	}

	if cfg.WallClockMinutes == 0 {
		cfg.WallClockMinutes = DefaultGoalWallClockMinutes
	} else if cfg.WallClockMinutes < MinGoalWallClockMinutes {
		cfg.WallClockMinutes = MinGoalWallClockMinutes
	} else if cfg.WallClockMinutes > MaxGoalWallClockMinutes {
		cfg.WallClockMinutes = MaxGoalWallClockMinutes
	}
	return cfg
}

// GoalModeOf extracts and normalizes the task's GoalModeConfig from its links.
// It returns (cfg, true) only when a LinkGoalMode link exists and has Enabled == true.
func GoalModeOf(t Task) (GoalModeConfig, bool) {
	for i := len(t.Links) - 1; i >= 0; i-- {
		l := t.Links[i]
		if l.Kind != LinkGoalMode {
			continue
		}
		var cfg GoalModeConfig
		if err := json.Unmarshal([]byte(l.Target), &cfg); err != nil {
			continue
		}
		if !cfg.Enabled {
			return GoalModeConfig{}, false
		}
		return NormalizeGoalModeConfig(cfg), true
	}
	return GoalModeConfig{}, false
}

// SetGoalMode sets or clears LinkGoalMode on t. When cfg is nil or !cfg.Enabled,
// any existing LinkGoalMode link is removed. Otherwise the normalized config is
// marshaled as compact JSON and stored in a single LinkGoalMode link.
func SetGoalMode(t *Task, cfg *GoalModeConfig) error {
	if t == nil {
		return errors.New("model: nil task")
	}
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		return l.Kind == LinkGoalMode
	})
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	normalized := NormalizeGoalModeConfig(*cfg)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("model: marshaling goal mode config: %w", err)
	}
	t.Links = append(t.Links, Link{
		Kind:   LinkGoalMode,
		Target: string(raw),
	})
	return nil
}

// GoalAssumptionsOf returns all valid GoalAssumption records linked on t in order.
func GoalAssumptionsOf(t Task) []GoalAssumption {
	var out []GoalAssumption
	for _, l := range t.Links {
		if l.Kind != LinkGoalAssumption {
			continue
		}
		var a GoalAssumption
		if err := json.Unmarshal([]byte(l.Target), &a); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// AppendGoalAssumption validates and appends a GoalAssumption link onto t.
// If an assumption with the same non-empty ID or identical (Topic, Decision)
// is already present on t, it is not duplicated.
func AppendGoalAssumption(t *Task, a GoalAssumption) error {
	if t == nil {
		return errors.New("model: nil task")
	}
	a.ID = strings.TrimSpace(a.ID)
	a.Topic = strings.TrimSpace(a.Topic)
	a.Decision = strings.TrimSpace(a.Decision)
	a.Rationale = strings.TrimSpace(a.Rationale)
	a.Alternatives = strings.TrimSpace(a.Alternatives)
	if a.Topic == "" {
		return errors.New("model: goal assumption topic is required")
	}
	if a.Decision == "" {
		return errors.New("model: goal assumption decision is required")
	}
	existing := GoalAssumptionsOf(*t)
	for _, prev := range existing {
		if (a.ID != "" && prev.ID == a.ID) ||
			(strings.EqualFold(prev.Topic, a.Topic) && strings.EqualFold(prev.Decision, a.Decision)) {
			return nil
		}
	}
	if a.ID == "" {
		a.ID = fmt.Sprintf("assumption-%d", len(existing)+1)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("model: marshaling goal assumption: %w", err)
	}
	t.Links = append(t.Links, Link{
		Kind:   LinkGoalAssumption,
		Target: string(raw),
	})
	return nil
}

// GoalScorecardOf returns the latest GoalScorecard linked on t, if present.
func GoalScorecardOf(t Task) (*GoalScorecard, bool) {
	for i := len(t.Links) - 1; i >= 0; i-- {
		l := t.Links[i]
		if l.Kind != LinkGoalScorecard {
			continue
		}
		var s GoalScorecard
		if err := json.Unmarshal([]byte(l.Target), &s); err != nil {
			continue
		}
		return &s, true
	}
	return nil, false
}

// SetGoalScorecard replaces any prior LinkGoalScorecard on t with the compact
// JSON representation of s.
func SetGoalScorecard(t *Task, s GoalScorecard) error {
	if t == nil {
		return errors.New("model: nil task")
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("model: marshaling goal scorecard: %w", err)
	}
	t.Links = slices.DeleteFunc(t.Links, func(l Link) bool {
		return l.Kind == LinkGoalScorecard
	})
	t.Links = append(t.Links, Link{
		Kind:   LinkGoalScorecard,
		Target: string(raw),
	})
	return nil
}
