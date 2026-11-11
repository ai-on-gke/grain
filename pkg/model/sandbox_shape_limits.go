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
	"fmt"
	"sort"
	"strings"
)

// The deployment-wide ceiling on a task's own sandbox shape
// (Task.SandboxCPUs/SandboxMemoryMB/SandboxDiskGB) when
// SandboxShapeLimits.Max leaves a dimension at zero: generous enough for a
// build-heavy run, small enough that one task cannot ask a host for a
// machine nobody meant to hand out.
const (
	DefaultMaxSandboxCPUs     = 32
	DefaultMaxSandboxMemoryMB = 128 * 1024
	DefaultMaxSandboxDiskGB   = 1024
)

// Grain's own default sandbox shape, the one a VM is built at when
// neither the task nor the deployment (Config.SandboxCPUs and its two
// siblings) says otherwise. It mirrors pkg/granule's DefaultCPUs,
// DefaultMemoryMB and DefaultDiskGB, which model cannot import.
const (
	BuiltinSandboxCPUs     = 2
	BuiltinSandboxMemoryMB = 8192
	BuiltinSandboxDiskGB   = 30
)

// SandboxShape is one sandbox size: vCPUs, memory in MiB and root disk in
// GiB. Zero in a dimension means "not stated": no override on a task, no
// bound of its own in a ceiling.
type SandboxShape struct {
	CPUs     int `json:"cpus,omitempty"`
	MemoryMB int `json:"memoryMb,omitempty"`
	DiskGB   int `json:"diskGb,omitempty"`
}

// IsZero reports whether no dimension is stated.
func (s SandboxShape) IsZero() bool { return s == SandboxShape{} }

// TaskSandboxShape is the shape task asks for itself.
func TaskSandboxShape(task Task) SandboxShape {
	return SandboxShape{CPUs: task.SandboxCPUs, MemoryMB: task.SandboxMemoryMB, DiskGB: task.SandboxDiskGB}
}

// Above lists, as "sandboxCpus 64 (ceiling 32)", each dimension s states
// that is larger than ceiling's bound for it. A dimension ceiling leaves
// at zero bounds nothing, and a dimension s leaves at zero asks for
// nothing beyond the default, so neither is ever above.
func (s SandboxShape) Above(ceiling SandboxShape) []string {
	var out []string
	check := func(name string, v, limit int) {
		if v > 0 && limit > 0 && v > limit {
			out = append(out, fmt.Sprintf("%s %d (ceiling %d)", name, v, limit))
		}
	}
	check("sandboxCpus", s.CPUs, ceiling.CPUs)
	check("sandboxMemoryMb", s.MemoryMB, ceiling.MemoryMB)
	check("sandboxDiskGb", s.DiskGB, ceiling.DiskGB)
	return out
}

// SandboxShapeLimits is grain_config.sandbox_shape_limits: the most a task
// may ask for as its own sandbox shape. Max bounds every caller, an
// unlimited role (grain/operator) included -- raise it here to file a
// bigger task. Roles bounds callers holding a limited role further: a
// role named there may not ask for more than its entry, and a role not
// named there is held to Max alone. Neither is clamped: a request above
// either is refused with what it asked for and what it may have.
type SandboxShapeLimits struct {
	// Max is the deployment-wide ceiling. A dimension left at zero is
	// DefaultMaxSandboxCPUs, DefaultMaxSandboxMemoryMB or
	// DefaultMaxSandboxDiskGB.
	Max SandboxShape `json:"max"`
	// Roles is a per-role ceiling, keyed by the role's scope set name
	// (grain/contributor). A dimension left at zero is Max's.
	Roles map[string]SandboxShape `json:"roles,omitempty"`
}

// ResolvedMax is Max with grain's defaults filled into the dimensions it
// leaves at zero.
func (l SandboxShapeLimits) ResolvedMax() SandboxShape {
	out := l.Max
	if out.CPUs <= 0 {
		out.CPUs = DefaultMaxSandboxCPUs
	}
	if out.MemoryMB <= 0 {
		out.MemoryMB = DefaultMaxSandboxMemoryMB
	}
	if out.DiskGB <= 0 {
		out.DiskGB = DefaultMaxSandboxDiskGB
	}
	return out
}

// RoleCeiling is the most a caller holding roles (a limited session's
// scope set names) may ask for: for each role its own entry in Roles,
// with Max standing in for a dimension the entry leaves at zero or for a
// role with no entry, never above Max -- and of those, the largest, since
// holding a second role widens what a session may do rather than
// narrowing it. With no roles at all it is ResolvedMax.
func (l SandboxShapeLimits) RoleCeiling(roles []string) SandboxShape {
	maxShape := l.ResolvedMax()
	if len(roles) == 0 {
		return maxShape
	}
	var out SandboxShape
	for _, role := range roles {
		c := l.Roles[strings.TrimSpace(role)]
		pick := func(v, limit int) int {
			if v <= 0 || v > limit {
				return limit
			}
			return v
		}
		out.CPUs = max(out.CPUs, pick(c.CPUs, maxShape.CPUs))
		out.MemoryMB = max(out.MemoryMB, pick(c.MemoryMB, maxShape.MemoryMB))
		out.DiskGB = max(out.DiskGB, pick(c.DiskGB, maxShape.DiskGB))
	}
	return out
}

// Validate refuses a negative dimension anywhere and an unnamed role.
func (l SandboxShapeLimits) Validate() error {
	check := func(where string, s SandboxShape) error {
		if s.CPUs < 0 || s.MemoryMB < 0 || s.DiskGB < 0 {
			return fmt.Errorf("sandbox shape limits: %s cannot be negative (0 is the default)", where)
		}
		return nil
	}
	if err := check("max", l.Max); err != nil {
		return err
	}
	names := make([]string, 0, len(l.Roles))
	for name := range l.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("sandbox shape limits: a role ceiling needs a role name")
		}
		if err := check("role "+name, l.Roles[name]); err != nil {
			return err
		}
	}
	return nil
}

// EncodeSandboxShapeLimits is grain_config.sandbox_shape_limits' text: ""
// for no limits of the deployment's own, JSON otherwise.
func EncodeSandboxShapeLimits(l SandboxShapeLimits) (string, error) {
	if l.Max.IsZero() && len(l.Roles) == 0 {
		return "", nil
	}
	b, err := json.Marshal(l)
	if err != nil {
		return "", fmt.Errorf("encoding sandbox_shape_limits: %w", err)
	}
	return string(b), nil
}

// DecodeSandboxShapeLimits reads EncodeSandboxShapeLimits' text back.
func DecodeSandboxShapeLimits(text string) (SandboxShapeLimits, error) {
	var l SandboxShapeLimits
	if strings.TrimSpace(text) == "" {
		return l, nil
	}
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		return SandboxShapeLimits{}, fmt.Errorf("decoding sandbox_shape_limits: %w", err)
	}
	return l, nil
}

// EffectiveSandboxShape is the shape task actually runs at in each
// dimension: its own override, else grain's own default (Builtin*). A
// deployment-wide default larger than grain's own is not seen here, so a
// comparison against it errs toward calling a request larger than it is.
func EffectiveSandboxShape(task Task) SandboxShape {
	out := SandboxShape{CPUs: BuiltinSandboxCPUs, MemoryMB: BuiltinSandboxMemoryMB, DiskGB: BuiltinSandboxDiskGB}
	if task.SandboxCPUs > 0 {
		out.CPUs = task.SandboxCPUs
	}
	if task.SandboxMemoryMB > 0 {
		out.MemoryMB = task.SandboxMemoryMB
	}
	if task.SandboxDiskGB > 0 {
		out.DiskGB = task.SandboxDiskGB
	}
	return out
}

// ProposalSandboxShapeAbove is why child, a proposal parent filed, asks
// for a larger sandbox than parent itself runs in, "" when it does not:
// an agent may not hand its follow-up a bigger machine than its own
// without a person -- an unlimited role -- approving it.
func ProposalSandboxShapeAbove(child, parent Task) string {
	above := TaskSandboxShape(child).Above(EffectiveSandboxShape(parent))
	if len(above) == 0 {
		return ""
	}
	return fmt.Sprintf("it asks for a larger sandbox than task %s that proposed it runs in (%s)",
		strings.TrimSpace(parent.ID), strings.Join(above, ", "))
}
