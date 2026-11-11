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

//go:build ignore

// forge_download_probe exercises §9.1's host-git forge_download placeholder
// confinement seam (553689d1) over a real child MCP server process and
// verifies #772's dedicated edition release repository bare vX.Y.Z tag handling.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"
)

func main() {
	serveHostGit := flag.Bool("serve-host-git", false, "run as child host-git MCP server")
	flag.Parse()

	gitproxytest.Install()
	if *serveHostGit {
		srv, err := protocol.NewCapabilityServer(gitproxy.HostGitEntry)
		if err != nil {
			log.Fatal(err)
		}
		if err := gitproxy.ServeHostGit(srv); err != nil {
			log.Fatal(err)
		}
		if err := srv.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx := context.Background()
	tmp, err := os.MkdirTemp("", "forge-download-probe-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	// Start a local HTTP upstream serving a release tarball payload; fake
	// forgecli in PATH forwards `forgecli api --include` requests to it.
	payload := []byte("real-streamed-oci-tarball-bytes-for-forge-download-confinement-check\n")
	wantSum := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(wantSum[:])

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		log.Fatal(err)
	}
	forgeCLI := filepath.Join(binDir, gitproxytest.Command)
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "auth" ] && [ "${2:-}" = "token" ]; then
  echo "forgecli-test-token"
  exit 0
fi
if [ "${1:-}" = "api" ]; then
  curl -isS "%s/asset"
  exit 0
fi
echo "unexpected args: $*" >&2
exit 1
`, upstream.URL)
	if err := os.WriteFile(forgeCLI, []byte(script), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.Setenv("PATH", binDir+":"+os.Getenv("PATH")); err != nil {
		log.Fatal(err)
	}

	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	client, err := protocol.Dial(ctx, protocol.ServerConfig{
		Command: exe,
		Args:    []string{"-serve-host-git"},
	}, protocol.DialOptions{Stderr: os.Stderr})
	if err != nil {
		log.Fatalf("dialing child host-git MCP server: %v", err)
	}
	defer client.Close()

	callDownload := func(dest string) (map[string]any, error) {
		raw, err := client.CallTool(ctx, "forge_download", map[string]any{
			"source":   gitproxytest.Source,
			"host":     gitproxytest.Host,
			"api_base": "https://" + gitproxytest.Host + "/api/v3",
			"url":      "https://" + gitproxytest.Host + "/api/v3/repos/acme/grain/releases/assets/42",
			"dest":     dest,
			"header": map[string]any{
				"Accept": []string{"application/octet-stream"},
			},
		})
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			return nil, fmt.Errorf("unmarshaling %q: %w", string(raw), uerr)
		}
		if e, _ := out["error"].(string); e != "" {
			return out, fmt.Errorf("%s", e)
		}
		return out, nil
	}

	stageDir := filepath.Join(tmp, "staging")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		log.Fatal(err)
	}

	fmt.Println("=== 1. forge_download MCP confinement checks (553689d1) ===")

	// (a) Wrong filename prefix.
	badPrefix := filepath.Join(stageDir, "arbitrary-shell-rc")
	if err := os.WriteFile(badPrefix, nil, 0o600); err != nil {
		log.Fatal(err)
	}
	if _, err := callDownload(badPrefix); err == nil || !strings.Contains(err.Error(), "grain-release-asset-") {
		log.Fatalf("expected prefix refusal, got: %v", err)
	}
	fmt.Println("PASS (a) refused non-grain-release-asset-* filename")

	// (b) Non-existent placeholder (must not create file).
	missing := filepath.Join(stageDir, "grain-release-asset-missing.tar.gz")
	if _, err := callDownload(missing); err == nil {
		log.Fatal("expected refusal on missing placeholder")
	}
	if _, statErr := os.Lstat(missing); !os.IsNotExist(statErr) {
		log.Fatalf("missing placeholder was created: %v", statErr)
	}
	fmt.Println("PASS (b) refused missing placeholder without creating file")

	// (c) Symlinked placeholder.
	victim := filepath.Join(stageDir, "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		log.Fatal(err)
	}
	symFile := filepath.Join(stageDir, "grain-release-asset-sym.tar.gz")
	if err := os.Symlink(victim, symFile); err != nil {
		log.Fatal(err)
	}
	if _, err := callDownload(symFile); err == nil {
		log.Fatal("expected refusal on symlink placeholder")
	}
	if b, _ := os.ReadFile(victim); len(b) != 0 {
		log.Fatalf("symlink victim modified: %q", string(b))
	}
	fmt.Println("PASS (c) refused symlink placeholder without touching target")

	// (d) Symlinked parent directory.
	realSub := filepath.Join(stageDir, "real-sub")
	if err := os.MkdirAll(realSub, 0o700); err != nil {
		log.Fatal(err)
	}
	inSub := filepath.Join(realSub, "grain-release-asset-sub.tar.gz")
	if err := os.WriteFile(inSub, nil, 0o600); err != nil {
		log.Fatal(err)
	}
	symDir := filepath.Join(stageDir, "sym-dir")
	if err := os.Symlink(realSub, symDir); err != nil {
		log.Fatal(err)
	}
	if _, err := callDownload(filepath.Join(symDir, "grain-release-asset-sub.tar.gz")); err == nil || !strings.Contains(err.Error(), "symlink") {
		log.Fatalf("expected symlink directory refusal, got: %v", err)
	}
	fmt.Println("PASS (d) refused symlinked parent directory")

	// (e) Non-empty file.
	nonEmpty := filepath.Join(stageDir, "grain-release-asset-nonempty.tar.gz")
	if err := os.WriteFile(nonEmpty, []byte("existing"), 0o600); err != nil {
		log.Fatal(err)
	}
	if _, err := callDownload(nonEmpty); err == nil {
		log.Fatal("expected refusal on non-empty placeholder")
	}
	fmt.Println("PASS (e) refused non-empty placeholder")

	// (f) Hardlinked placeholder (nlink == 2).
	hlOrig := filepath.Join(stageDir, "grain-release-asset-hl1.tar.gz")
	hlTwin := filepath.Join(stageDir, "grain-release-asset-hl2.tar.gz")
	if err := os.WriteFile(hlOrig, nil, 0o600); err != nil {
		log.Fatal(err)
	}
	if err := os.Link(hlOrig, hlTwin); err != nil {
		log.Fatal(err)
	}
	if _, err := callDownload(hlOrig); err == nil {
		log.Fatal("expected refusal on multi-hardlink placeholder")
	}
	fmt.Println("PASS (f) refused multi-hardlink placeholder")

	// (g) Valid empty 0600 placeholder created by daemon.
	valid := filepath.Join(stageDir, "grain-release-asset-valid.tar.gz")
	if err := os.WriteFile(valid, nil, 0o600); err != nil {
		log.Fatal(err)
	}
	res, err := callDownload(valid)
	if err != nil {
		log.Fatalf("valid placeholder download failed: %v", err)
	}
	gotBytes, err := os.ReadFile(valid)
	if err != nil || string(gotBytes) != string(payload) {
		log.Fatalf("downloaded bytes mismatch: %q", string(gotBytes))
	}
	if fmt.Sprint(res["sha256"]) != wantHex {
		log.Fatalf("sha256 = %v, want %s", res["sha256"], wantHex)
	}
	fmt.Printf("PASS (g) streamed %d bytes into daemon placeholder (sha256=%s)\n", len(gotBytes), wantHex)

	fmt.Println("=== 2. Dedicated edition release repo bare vX.Y.Z tags vs shared repo edition suffixes (#772) ===")
	installtracktest.Register()
	edTrack := installtracktest.Track
	const edName = "google"
	savedGet := version.Get
	baseInfo := savedGet()
	version.RegisterEdition(edName)
	version.Get = func() version.Info {
		info := baseInfo
		info.Edition = edName
		return info
	}
	defer func() {
		version.RegisterEdition("")
		version.Get = savedGet
	}()

	edForge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		manifest := `{"schema":1,"repository":"acme/grain-enterprise","commit":"3200320032003200320032003200320032003200","version":"v3.2.0","createdAt":"2026-11-07T22:00:00Z","images":{"grain":{"asset":"grain.tar.gz","sha256":"a151d6b55f5857a09dee62f445d5daf31b6206f68b413a18db56566836ea5d17","imageId":"sha256:5f3c42265ff80e04aa98945204482d02d762a180719addf7837511eecb268741","ref":"` + installtracktest.Repository + `:v3.2.0"}}}`
		switch {
		case strings.Contains(r.URL.Path, "/repos/acme/grain-enterprise/releases"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "tag_name": "v3.2.0", "assets": []map[string]any{{"id": 10, "name": "images.json", "size": len(manifest)}}},
				{"id": 2, "tag_name": "v3.1.0", "assets": []map[string]any{{"id": 11, "name": "images.json", "size": len(manifest)}}},
			})
		case strings.Contains(r.URL.Path, "/repos/ai-on-gke/grain/releases"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 3, "tag_name": "v3.2.0", "assets": []map[string]any{{"id": 12, "name": "images.json", "size": len(manifest)}}},
				{"id": 4, "tag_name": "v2.9.0-" + edName, "assets": []map[string]any{{"id": 13, "name": "images.json", "size": len(manifest)}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer edForge.Close()

	clientFor := func(_ context.Context, _, _, _ string) (*upgrade.ReleaseHostClient, error) {
		return &upgrade.ReleaseHostClient{Client: edForge.Client(), APIBase: edForge.URL}, nil
	}

	dedUpg := upgrade.New(upgrade.Config{
		Edition: edName,
		Image: &upgrade.ImageConfig{
			Repository: installtracktest.Repository,
			Track:      edTrack,
			Source:     upgrade.ImageSourceReleaseAssets,
			ReleaseAssets: upgrade.ReleaseAssetsConfig{
				Repository:    "depot.code.forge.test/acme/grain-enterprise",
				ClientForHost: clientFor,
			},
		},
		AllowedBranches: []string{"latest", "v*"},
	})
	var dedVersions []string
	for _, tg := range dedUpg.Targets(ctx).Targets {
		if tg.Kind == upgrade.TargetVersion {
			dedVersions = append(dedVersions, tg.Name)
		}
	}
	if !slices.Equal(dedVersions, []string{"v3.2.0", "v3.1.0"}) {
		log.Fatalf("dedicated edition repo versions = %v, want [v3.2.0 v3.1.0]", dedVersions)
	}
	fmt.Printf("PASS dedicated edition repo accepts bare vX.Y.Z tags: %v\n", dedVersions)

	sharedUpg := upgrade.New(upgrade.Config{
		Edition: edName,
		Image: &upgrade.ImageConfig{
			Repository: installtracktest.Repository,
			Track:      edTrack,
			Source:     upgrade.ImageSourceReleaseAssets,
			ReleaseAssets: upgrade.ReleaseAssetsConfig{
				Repository:    "github.com/ai-on-gke/grain",
				ClientForHost: clientFor,
			},
		},
		AllowedBranches: []string{"latest", "v*"},
	})
	var sharedVersions []string
	for _, tg := range sharedUpg.Targets(ctx).Targets {
		if tg.Kind == upgrade.TargetVersion {
			sharedVersions = append(sharedVersions, tg.Name)
		}
	}
	wantShared := []string{"v2.9.0-" + edName}
	if !slices.Equal(sharedVersions, wantShared) {
		log.Fatalf("shared repo versions = %v, want %v (bare core v3.2.0 excluded)", sharedVersions, wantShared)
	}
	fmt.Printf("PASS shared repo keeps edition suffix filtering: %v\n", sharedVersions)
}
