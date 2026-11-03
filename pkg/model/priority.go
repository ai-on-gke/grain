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
	"math"
	"strconv"
	"strings"
)

// ErrPriorityNotWhole is ParseTaskPriority's refusal.
var ErrPriorityNotWhole = errors.New("not a whole number")

// ParseTaskPriority reads a task priority written as a JSON number, the
// one reading every way of setting one shares: POST and PATCH
// /api/tasks, `grain create -priority`, `grain update -priority` and the
// sandbox's `grain task propose --priority`. Plain digits are read as
// they are; otherwise any JSON number that is whole and within 2^53,
// where a float64 still holds it exactly -- so 1000, 1000.0 and 1e3 are
// all 1000. 1.5, "8" (quoted), 0x10, digits past int and 1e20 are
// refused. The caller checks the 1-or-more bound.
func ParseTaskPriority(text string) (int, error) {
	text = strings.TrimSpace(text)
	if n, err := strconv.Atoi(text); err == nil {
		return n, nil
	}
	const exact = 1 << 53
	var x float64
	if json.Unmarshal([]byte(text), &x) != nil || x != math.Trunc(x) || math.Abs(x) > exact {
		return 0, ErrPriorityNotWhole
	}
	return int(x), nil
}
