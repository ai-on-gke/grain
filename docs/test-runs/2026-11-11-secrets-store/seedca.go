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

// seedca writes <data-dir>/sandbox-access-ca.pem the way a 61668e58
// daemon's FileCAStore does on a Kubernetes deployment's first start
// (sandboxaccess.LoadOrCreateCA), for a host-sandbox walkthrough whose
// daemon never starts sandbox access itself, and makes the
// release-approval key the way `grain release approval-key` does
// (releaseapproval.LoadSigner). Run it from a 61668e58 checkout.
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

func main() {
	dataDir := os.Args[1]
	store := sandboxaccess.FileCAStore{Path: filepath.Join(dataDir, "sandbox-access-ca.pem")}
	if _, err := sandboxaccess.LoadOrCreateCA(context.Background(), store, "grain", time.Now()); err != nil {
		log.Fatal(err)
	}
	if _, err := releaseapproval.LoadSigner(dataDir); err != nil {
		log.Fatal(err)
	}
}
