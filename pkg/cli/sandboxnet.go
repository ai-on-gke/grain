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

// sandboxnet.go is where the services a sandbox has to reach get
// published: the git proxy, the three model proxies, the package cache
// and the Actions cache.
//
// All six are the same shape -- bind an ephemeral port, hand a sandbox a
// URL naming it -- and until this file existed each of them wrote that
// shape out for itself. Four copies of:
//
//	bindHost := "127.0.0.1"
//	if advertise != "" {
//		bindHost = "0.0.0.0"
//	}
//	ln, _ := net.Listen("tcp", bindHost+":0")
//	url := "http://" + ln.Addr().String()
//	if advertise != "" {
//		url = fmt.Sprintf("http://%s:%d", advertise, ln.Addr().(*net.TCPAddr).Port)
//	}
//
// which is four chances to get it wrong, and it had already gone wrong
// once: three of them asked sandboxAdvertiseHost(cfg), which answers for
// whichever backend this deployment runs, while startModelProxy read
// cfg.granuleGitProxyHost directly. Nothing ever copies the container or
// Kubernetes flag into that field, so under -container-sandboxes or
// -k8s-sandboxes the model proxies bound loopback and advertised
// loopback while the git proxy and both caches were correctly on the
// address sandboxes can route to -- a deployment where an agent could
// clone but not reach a model.
//
// That bug is not interesting on its own. What is interesting is that it
// was invisible: nothing named the set of services a sandbox must reach,
// so nothing could notice one of them answering a different question.
// Fabric is that name. A service published through it cannot disagree
// with the others about where sandboxes are, because it is not asked --
// and Endpoints makes the set something a caller can enumerate rather
// than something spread across four files, which is what a second
// implementation (a sandbox host that is not this machine, reached over
// pkg/tunnel) needs in order to publish all of them without being edited
// every time one is added.
//
// The package cache's *interception* half is deliberately not here. Its
// two listeners are on fixed ports (443 and 80) at an address a
// sandbox's DNS has been pointed at, so the routing key is a hostname
// and not a URL, and packageCacheDNSAddrs/packageCacheTransparentAddrs
// already derive the pair from one value for the same
// don't-let-them-drift reason this file exists. Publishing an address
// rather than a URL is a second method on Fabric when something needs
// it, not a thing to bend Serve into.
package cli

import "flag"

// DefaultSandboxPortBase is where that block starts unless
// -sandbox-port-base says otherwise.
//
// Three constraints picked it, and a replacement has to meet all three:
//
//   - Below the ephemeral range. Linux hands out 32768-60999 by default
//     (/proc/sys/net/ipv4/ip_local_port_range), and a block inside it
//     collides with this daemon's own outbound connections -- rarely, and
//     therefore at the worst possible moment.
//   - Aligned to SandboxPortBlockSize, so the block is maskable as well
//     as rangeable. 9728 is 0x2600.
//   - Clear of what grain already uses: 4242 for the UI (80 and 8420 on
//     older installs), 8422 for the executor listener, 12000-12255 for
//     -granule-base-port's NAT forwards, and 443/80/53 for the package
//     cache's interception and resolver -- which are fixed by their protocols and so are not in
//     this block at all (see Fabric.Host).
const DefaultSandboxPortBase = 9728

// k8sSandboxServiceFlag registers -k8s-sandbox-service, the Kubernetes
// half of what the port block is for: fixed ports keep a sandbox's URLs
// valid across a restart, and a Service in front of them keeps the
// address valid across a new daemon pod (daemon.K8sSandboxServiceHost).
func k8sSandboxServiceFlag(fs *flag.FlagSet) *string {
	return fs.String("k8s-sandbox-service", "",
		"name of a ClusterIP Service in -k8s-namespace that selects this daemon's pod and forwards the git "+
			"proxy's and model proxies' ports from the -sandbox-port-base block (so it needs that block). When "+
			"set, sandbox pods are handed the Service's cluster IP for those, in place of -k8s-git-proxy-host, "+
			"so a sandbox that outlives this pod -- a run the next daemon pod adopts after a rollout -- can "+
			"still push and still reach its model. The daemon resolves it at startup and refuses to start if "+
			"it cannot. deploy/kubernetes/grain.yaml names its grain-sandbox Service. Only used with "+
			"-k8s-sandboxes or -k8s-granule-sandboxes (bwsalmon/agents#320: seeds the stored configuration on first use; ignored once one exists -- see loadConfig)")
}
