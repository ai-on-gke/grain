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

package cli

import (
	"errors"
	"flag"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/model"
)

// taskPriority is a -priority value, read as the task API reads one
// (model.ParseTaskPriority): -priority 1e3 is 1000 here as it is in a
// POST, rather than a flag.Int parse error.
type taskPriority int

func (p *taskPriority) String() string { return strconv.Itoa(int(*p)) }

func (p *taskPriority) Set(s string) error {
	n, err := model.ParseTaskPriority(s)
	if err != nil {
		return errors.New("not a whole number")
	}
	*p = taskPriority(n)
	return nil
}

// priorityFlag defines -priority on fs, defaulting to
// model.DefaultTaskPriority.
func priorityFlag(fs *flag.FlagSet, usage string) *taskPriority {
	p := taskPriority(model.DefaultTaskPriority)
	fs.Var(&p, "priority", usage)
	return &p
}
