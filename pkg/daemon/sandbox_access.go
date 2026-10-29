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

package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// SandboxAccessService is the Service deploy/kubernetes/grain-granule.yaml
// puts in front of the acceptor, and so the default dial name.
const SandboxAccessService = "grain-access"

// sandboxAccessDial is where wrappers dial the acceptor.
func sandboxAccessDial(cfg Config, namespace string) string {
	if cfg.SandboxAccessDial != "" {
		return cfg.SandboxAccessDial
	}
	return net.JoinHostPort(SandboxAccessService+"."+namespace+".svc", strconv.Itoa(sandboxaccess.DefaultPort))
}

// sandboxAccessControllerIPs are the acceptor's addresses as a guest
// would see them, for granule's in-pod drop: the grain-access Service's
// ClusterIP (which Kubernetes hands every pod started after the Service
// as GRAIN_ACCESS_SERVICE_HOST) and this pod's own IP (GRAIN_POD_IP).
func sandboxAccessControllerIPs(getenv func(string) string) []string {
	var ips []string
	for _, name := range []string{"GRAIN_ACCESS_SERVICE_HOST", "GRAIN_POD_IP"} {
		if ip := net.ParseIP(getenv(name)); ip != nil {
			ips = append(ips, ip.String())
		}
	}
	return ips
}

// startSandboxAccess loads (or creates) the deployment CA, restores stored
// sandbox-access credentials from accessStore, starts the acceptor on
// cfg.SandboxAccessAddr, and returns the access config K8sGranuleSandboxes
// mints credentials through. live is consulted on every admission; it is a
// pointer because the backend it reads is built after this.
func startSandboxAccess(ctx context.Context, cfg Config, accessStore orchestrator.SandboxAccessStore, deployment, namespace string, live *func(string) bool) (*orchestrator.K8sGranuleAccess, error) {
	var store sandboxaccess.CAStore = sandboxaccess.FileCAStore{Path: filepath.Join(cfg.DataDir, "sandbox-access-ca.pem")}
	if cfg.SandboxAccessCASecret != "" {
		store = sandboxaccess.SecretManagerStore{Secret: cfg.SandboxAccessCASecret}
	}
	if deployment == "" {
		deployment = "grain"
	}
	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ca, err := sandboxaccess.LoadOrCreateCA(loadCtx, store, deployment, time.Now())
	cancel()
	if err != nil {
		return nil, fmt.Errorf("sandbox access: loading the CA: %w", err)
	}
	dial := sandboxAccessDial(cfg, namespace)
	dialHost, _, err := net.SplitHostPort(dial)
	if err != nil {
		return nil, fmt.Errorf("sandbox access: -sandbox-access-dial %q: %w", dial, err)
	}
	controllerIPs := sandboxAccessControllerIPs(os.Getenv)
	names := append([]string{
		dialHost,
		SandboxAccessService,
		SandboxAccessService + "." + namespace + ".svc",
		SandboxAccessService + "." + namespace + ".svc.cluster.local",
	}, controllerIPs...)
	startupReadoptable := map[string]bool{}
	acceptor, err := sandboxaccess.Listen(sandboxaccess.Config{
		Deployment:  deployment,
		CA:          ca,
		ServerNames: names,
		Live: func(sandbox string) bool {
			if *live != nil {
				return (*live)(sandbox)
			}
			return startupReadoptable[sandbox]
		},
	}, cfg.SandboxAccessAddr)
	if err != nil {
		return nil, err
	}
	if accessStore != nil {
		creds, err := accessStore.SandboxAccessCredentials(ctx)
		if err != nil {
			_ = acceptor.Close()
			return nil, fmt.Errorf("sandbox access: loading stored credentials: %w", err)
		}
		now := time.Now()
		for _, c := range creds {
			if !c.NotAfter.After(now) {
				_ = accessStore.DeleteSandboxAccess(ctx, c.Sandbox)
				continue
			}
			if err := acceptor.Register(c.Sandbox, c.TokenHash, c.NotAfter); err != nil {
				_ = acceptor.Close()
				return nil, fmt.Errorf("sandbox access: registering stored credential for %q: %w", c.Sandbox, err)
			}
			startupReadoptable[c.Sandbox] = true
		}
	}
	go func() {
		if err := acceptor.Serve(ctx); err != nil {
			log.Printf("grain daemon: sandbox access acceptor stopped: %v", err)
		}
	}()
	log.Printf("grain daemon: sandbox access acceptor on %s; wrappers dial %s, guest traffic to %v dropped in-pod",
		acceptor.Addr(), dial, controllerIPs)
	return &orchestrator.K8sGranuleAccess{Acceptor: acceptor, Store: accessStore, Dial: dial, ControllerIPs: controllerIPs}, nil
}
