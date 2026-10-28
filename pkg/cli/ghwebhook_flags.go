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
