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

import "flag"

// githubWebhookFlags registers -github-webhook-url and -github-webhook-addr
// for receiving verified GitHub App webhook events (pkg/ghwebhook,
// cmd/github-webhook).
func githubWebhookFlags(fs *flag.FlagSet) (url, addr *string) {
	url = fs.String("github-webhook-url", "",
		"base URL of this deployment's github-webhook gateway service (e.g. http://grain-webhook:8080), "+
			"signed with GITHUB_WEBHOOK_RELAY_SECRET; empty disables remote webhook long-polling")
	addr = fs.String("github-webhook-addr", "",
		"local address (e.g. :4244) to listen on directly for signed GitHub App webhooks using "+
			"GITHUB_WEBHOOK_SECRET or a GitHub App credential's webhook_secret; mutually exclusive with -github-webhook-url")
	return url, addr
}
