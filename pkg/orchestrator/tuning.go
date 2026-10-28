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

package orchestrator

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ValidateTuningProposal verifies that a tuning proposal targets a recognized grain_config parameter.
func ValidateTuningProposal(param string) error {
	return model.ValidateTuningParameter(param)
}

// ExtractTuningProposal extracts the target parameter and proposed value from proposal fields or text.
func ExtractTuningProposal(p map[string]any, title string, body string) (param string, val string) {
	// 1. Direct fields on the propose_task payload
	if v, ok := p["target_parameter"].(string); ok && strings.TrimSpace(v) != "" {
		param = strings.TrimSpace(v)
	} else if v, ok := p["parameter"].(string); ok && strings.TrimSpace(v) != "" {
		param = strings.TrimSpace(v)
	}

	if v, ok := p["proposed_value"].(string); ok && strings.TrimSpace(v) != "" {
		val = strings.TrimSpace(v)
	} else if v, ok := p["value"].(string); ok && strings.TrimSpace(v) != "" {
		val = strings.TrimSpace(v)
	}

	// 2. In template_fields
	if tf, ok := p["template_fields"].(map[string]any); ok {
		if param == "" {
			if v, ok := tf["target_parameter"].(string); ok && strings.TrimSpace(v) != "" {
				param = strings.TrimSpace(v)
			} else if v, ok := tf["parameter"].(string); ok && strings.TrimSpace(v) != "" {
				param = strings.TrimSpace(v)
			}
		}
		if val == "" {
			if v, ok := tf["proposed_value"].(string); ok && strings.TrimSpace(v) != "" {
				val = strings.TrimSpace(v)
			} else if v, ok := tf["value"].(string); ok && strings.TrimSpace(v) != "" {
				val = strings.TrimSpace(v)
			}
		}
	} else if tfStr, ok := p["template_fields"].(map[string]string); ok {
		if param == "" {
			if v, ok := tfStr["target_parameter"]; ok && strings.TrimSpace(v) != "" {
				param = strings.TrimSpace(v)
			} else if v, ok := tfStr["parameter"]; ok && strings.TrimSpace(v) != "" {
				param = strings.TrimSpace(v)
			}
		}
		if val == "" {
			if v, ok := tfStr["proposed_value"]; ok && strings.TrimSpace(v) != "" {
				val = strings.TrimSpace(v)
			} else if v, ok := tfStr["value"]; ok && strings.TrimSpace(v) != "" {
				val = strings.TrimSpace(v)
			}
		}
	}

	// 3. In custom_fields
	if cf, ok := p["custom_fields"].(map[string]any); ok {
		if param == "" {
			if v, ok := cf["target_parameter"].(string); ok && strings.TrimSpace(v) != "" {
				param = strings.TrimSpace(v)
			} else if v, ok := cf["parameter"].(string); ok && strings.TrimSpace(v) != "" {
				param = strings.TrimSpace(v)
			}
		}
	}

	// 4. Fallback to title and body matching
	if param == "" && title != "" {
		patterns := []*regexp.Regexp{
			regexp.MustCompile(`(?i)(?:tune|tune\s+parameter|change|set)\s+([a-zA-Z0-9_-]+)(?:\s+(?:to|=|:)\s*([a-zA-Z0-9_.-]+))?`),
			regexp.MustCompile(`(?i)(?:parameter|param)[:=]\s*([a-zA-Z0-9_-]+)`),
		}
		for _, re := range patterns {
			matches := re.FindStringSubmatch(title)
			if len(matches) > 1 && matches[1] != "" {
				candidate := matches[1]
				if model.IsRecognizedTuningParameter(candidate) || strings.Contains(strings.ToLower(title), "tune") {
					param = candidate
					if val == "" && len(matches) > 2 {
						val = matches[2]
					}
					break
				}
			}
		}
	}

	return model.CanonicalizeTuningParameter(param), strings.TrimSpace(val)
}

// RecordTuningRecommendation records a tuning recommendation for a tuning task.
func RecordTuningRecommendation(ctx context.Context, store *model.Store, taskID string, param string, value string, cooldown time.Duration, now time.Time) (int64, error) {
	if store == nil {
		return 0, fmt.Errorf("store is nil")
	}
	if err := ValidateTuningProposal(param); err != nil {
		return 0, err
	}
	if cooldown <= 0 {
		cooldown = time.Duration(model.InfraReviewCooldownDays) * 24 * time.Hour
	}
	rec := model.TuningRecommendation{
		TargetParameter: model.CanonicalizeTuningParameter(param),
		ProposedValue:   value,
		CooldownExpiry:  now.Add(cooldown),
		Adopted:         false,
		TaskID:          taskID,
		CreatedAt:       now,
	}
	return store.RecordTuningRecommendation(ctx, rec)
}

// RetrieveTuningRecommendations lists tuning recommendations from the store.
func RetrieveTuningRecommendations(ctx context.Context, store *model.Store, param string, limit int) ([]model.TuningRecommendation, error) {
	if store == nil {
		return nil, fmt.Errorf("store is nil")
	}
	return store.ListTuningRecommendations(ctx, param, limit)
}

// RetrieveLatestTuningRecommendation retrieves the most recent recommendation for a parameter.
func RetrieveLatestTuningRecommendation(ctx context.Context, store *model.Store, param string) (*model.TuningRecommendation, error) {
	if store == nil {
		return nil, fmt.Errorf("store is nil")
	}
	return store.LatestTuningRecommendationForParameter(ctx, param)
}

// MarkTuningRecommendationAdopted marks a recommendation as adopted.
func MarkTuningRecommendationAdopted(ctx context.Context, store *model.Store, id int64, adopted bool) error {
	if store == nil {
		return fmt.Errorf("store is nil")
	}
	return store.UpdateTuningRecommendationAdopted(ctx, id, adopted)
}

// CheckAndMarkTuningAdoption marks any tuning recommendation associated with task as adopted.
func CheckAndMarkTuningAdoption(ctx context.Context, store *model.Store, task model.Task) error {
	if store == nil {
		return nil
	}
	recs, err := store.ListTuningRecommendations(ctx, "", 50)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if !r.Adopted && (r.TaskID == task.ID || strings.Contains(task.Title, r.TargetParameter)) {
			_ = store.UpdateTuningRecommendationAdopted(ctx, r.ID, true)
		}
	}
	return nil
}
