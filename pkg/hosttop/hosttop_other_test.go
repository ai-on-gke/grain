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

//go:build !linux && !darwin

package hosttop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hosttop"
)

// On a platform hosttop has no invocation for, Read refuses rather than
// guessing at flags -- and refuses without running anything, so the answer
// is the same whether or not something named `top` happens to be on PATH.
func TestReadReportsAnUnsupportedPlatform(t *testing.T) {
	_, err := hosttop.Read(context.Background(), 0)
	if err == nil {
		t.Fatal("Read returned no error on a platform with no top invocation")
	}
	if !strings.Contains(err.Error(), "only available on Linux and macOS") {
		t.Errorf("Read error = %v, want one naming the platforms it supports", err)
	}
}
