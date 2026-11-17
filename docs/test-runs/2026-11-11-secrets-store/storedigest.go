//go:build ignore

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

// storedigest DATA_DIR SECRET/KEY... prints a short sha256 of each stored
// value (whitespace-trimmed), never the value, so a walkthrough can show
// the store holds the bytes a file held. It opens the store the way
// `grain secrets` does, through the key file's KeySource (a KMS-wrapped
// key reaches GRAIN_KMS_ENDPOINT).
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

func main() {
	store := secrets.Open(daemon.SecretsConfig(os.Args[1]))
	for _, ref := range os.Args[2:] {
		v, err := store.Resolve(context.Background(), ref)
		if err != nil {
			fmt.Printf("%s: error: %v\n", ref, err)
			continue
		}
		fmt.Printf("%s %x\n", ref, sha256.Sum256([]byte(strings.TrimSpace(v))))
	}
}
