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

package actions

import (
	"path"
	"strings"
)

// A job with a `container:` of its own runs its steps in an image grain
// never prepared, on the guest's network -- act starts the job container
// with `--network host`, so it reads the guest's /etc/resolv.conf, and
// where this deployment runs its package cache that resolver answers
// deb.debian.org, pypi.org and the rest with the cache's HTTPS listener.
// The guest trusts that listener because grain put the deployment's root
// CA in its store (pkg/pkgcache's CACertPath); debian:bookworm-slim does
// not, and a step fetching https://deb.debian.org/... fails with
// "self-signed certificate in certificate chain".
//
// Turning verification off is not the answer, and neither is the
// workflow's own `container.options`: act ignores --container-options for
// a job that declares `container:`, and grain does not edit what a
// workflow says about its container. What reaches every job container act
// starts is act's tool cache, the named volume act-toolcache mounted at
// /opt/hostedtoolcache. So the guest copies its own trust store into that
// volume before act runs, and the job's steps are given the variables
// the common TLS clients read their roots from, pointing at the copy.
// The CA travels; nothing is trusted that the guest does not already
// trust.
//
// Only where the guest holds grain's root CA: with no package cache the
// container's own store is the right one, and replacing it with the
// guest's would drop any CA the image itself was built to trust.

// GrainCACertPath is where a sandbox holds this deployment's root CA when
// its package cache is on -- pkg/pkgcache's CACertPath, repeated because
// pkg/grit/actions is also `grit act` on a laptop and imports nothing of
// grain's.
const GrainCACertPath = "/usr/local/share/ca-certificates/grain-ca.crt"

// guestCABundle is the guest's trust store, which update-ca-certificates
// built with GrainCACertPath in it.
const guestCABundle = "/etc/ssl/certs/ca-certificates.crt"

// toolCacheVolume and toolCachePath are the volume act mounts into every
// job container and where (act's RunContext.GetBindsAndMounts).
const (
	toolCacheVolume = "act-toolcache"
	toolCachePath   = "/opt/hostedtoolcache"
)

// ContainerCADir is where, inside a job container, the guest's trust
// store is found: a directory of act's tool cache no setup-* action
// installs a tool under.
const ContainerCADir = toolCachePath + "/.grain-ca"

// ContainerCAEnv is every variable a container job's steps may be given,
// in the order they are passed, and the file each points at. All but
// NODE_EXTRA_CA_CERTS replace the client's roots, so they are given the
// guest's whole store, public roots included; Node adds what it is given
// to its own, so it is given grain's root alone.
var ContainerCAEnv = []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"}

var containerCAFile = map[string]string{
	"SSL_CERT_FILE":       path.Join(ContainerCADir, "ca-certificates.crt"),
	"REQUESTS_CA_BUNDLE":  path.Join(ContainerCADir, "ca-certificates.crt"),
	"CURL_CA_BUNDLE":      path.Join(ContainerCADir, "ca-certificates.crt"),
	"NODE_EXTRA_CA_CERTS": path.Join(ContainerCADir, "grain-ca.crt"),
}

// containerCAScript is the part of the act script that puts the guest's
// trust store in act's tool cache and passes caEnv to act, for a job
// whose container would otherwise not trust the package cache. Names
// outside ContainerCAEnv are dropped rather than written into a script.
//
// The same files are put at ContainerCADir on the guest itself, so the
// variables name a real file even when a `container:` whose image is an
// expression comes out empty and act runs the job on the guest instead;
// there they are the guest's own store, which its clients read anyway.
//
// The volume's files belong to root wherever docker keeps them, so the
// copy is made as root: grain's sandboxes give their user passwordless
// sudo, as pkg/orchestrator's package-cache setup relies on too.
func containerCAScript(caEnv []string) string {
	var env strings.Builder
	for _, name := range ContainerCAEnv {
		for _, want := range caEnv {
			if want == name {
				env.WriteString(" --env " + shellWord(name+"="+containerCAFile[name]))
				break
			}
		}
	}
	if env.Len() == 0 {
		return ""
	}
	return "if [ -r " + shellWord(GrainCACertPath) + " ] && [ -r " + shellWord(guestCABundle) + " ]; then\n" +
		"  grain_sudo=\n" +
		"  if [ \"$(id -u)\" != 0 ]; then grain_sudo='sudo -n'; fi\n" +
		"  docker volume create " + toolCacheVolume + " >/dev/null\n" +
		"  grain_toolcache=$(docker volume inspect --format '{{.Mountpoint}}' " + toolCacheVolume + ")\n" +
		"  for grain_ca_dir in \"$grain_toolcache/" + path.Base(ContainerCADir) + "\" " + shellWord(ContainerCADir) + "; do\n" +
		"    $grain_sudo install -D -m 0644 " + shellWord(guestCABundle) + " \"$grain_ca_dir/ca-certificates.crt\"\n" +
		"    $grain_sudo install -D -m 0644 " + shellWord(GrainCACertPath) + " \"$grain_ca_dir/grain-ca.crt\"\n" +
		"  done\n" +
		"  set -- \"$@\"" + env.String() + "\n" +
		"fi\n"
}
