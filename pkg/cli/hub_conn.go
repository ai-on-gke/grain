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
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hubenv"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// hubHTTPClient and hubOIDCToken are seams for unit tests so tests
// never touch the network or host gcloud/ADC credentials.
var (
	hubHTTPClient *http.Client
	hubOIDCToken  func(ctx context.Context, hubURL, audience string) (string, error)
)

func resolveHubCLIBearer(ctx context.Context, hubURL, audience, flagToken string) (string, error) {
	if tok := firstNonEmptyTrimmed(flagToken, hubenv.Get(daemon.HubTokenEnv)); tok != "" {
		return tok, nil
	}
	if hubOIDCToken != nil {
		return hubOIDCToken(ctx, hubURL, audience)
	}
	rc := ui.NewHTTPClient(hubURL)
	rc.Audience = firstNonEmptyTrimmed(audience, hubURL)
	return rc.GoogleIDToken(ctx)
}

func newHubAPIClient(c *ui.HTTPClient, hubURL, bearer string) (*api.Client, error) {
	hc := hubHTTPClient
	if hc == nil && c != nil && c.HTTP != nil {
		hc = c.HTTP
	}
	cfg := api.ClientConfig{
		BaseURL:    hubURL,
		HTTPClient: hc,
	}
	if strings.HasPrefix(bearer, api.RegistrationTokenPrefix) {
		cfg.RegistrationToken = bearer
	} else {
		cfg.BearerToken = bearer
	}
	return api.NewClient(cfg)
}

// hubTarget records which Grain Hub URL a `grain hub` subcommand dialled and
// where that URL came from, so transport failures name the hub and its
// flag/env var rather than falling through to explainDaemonError's daemon
// message.
type hubTarget struct {
	url    string
	source string
}

type hubTargetKey struct{}

func withHubTarget(ctx context.Context, t *hubTarget) context.Context {
	return context.WithValue(ctx, hubTargetKey{}, t)
}

func recordHubTarget(ctx context.Context, t hubTarget) {
	if dst, ok := ctx.Value(hubTargetKey{}).(*hubTarget); ok && dst != nil {
		*dst = t
	}
}

// resolveHubURL returns the hub base URL from -hub-url, $GRAIN_HUB_URL (or its
// deprecated $GRAIN_REGISTRY_URL alias), or the daemon's stored hub settings,
// along with a human-readable source label.
func resolveHubURL(flagVal, daemonVal string) (url, source string) {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v, "-hub-url"
	}
	envSource := "$" + daemon.HubURLEnv
	if os.Getenv(daemon.HubURLEnv) == "" {
		envSource = "$" + hubenv.Former(daemon.HubURLEnv)
	}
	if v := strings.TrimSpace(hubenv.Get(daemon.HubURLEnv)); v != "" {
		return v, envSource
	}
	if v := strings.TrimSpace(daemonVal); v != "" {
		return v, "daemon settings"
	}
	return "", ""
}

// explainHubError classifies a transport failure talking to a Grain Hub as a
// hub_unreachable cliError (exit 4) naming the hub URL and how to change it
// (-hub-url / $GRAIN_HUB_URL), so runCLI's explainDaemonError does not blame
// the local grain daemon.
func explainHubError(err error, t hubTarget) error {
	if err == nil || t.url == "" {
		return err
	}
	var classified *cliError
	if errors.As(err, &classified) {
		return err
	}
	where := t.url
	if t.source != "" && t.source != "-hub-url" {
		where += " (from " + t.source + ")"
	}
	fix := "Set $" + daemon.HubURLEnv + " or pass -hub-url"

	switch {
	case daemonRefused(err):
		return &cliError{
			exit: exitUnreachable,
			code: "hub_unreachable",
			err:  fmt.Errorf("no grain hub answered at %s. %s (%v)", where, fix, rootCause(err)),
		}
	case daemonTimedOut(err):
		return &cliError{
			exit: exitUnreachable,
			code: "hub_unreachable",
			err:  fmt.Errorf("the grain hub at %s did not answer in time. %s, or pass a longer -timeout for a slow call (%v)", where, fix, rootCause(err)),
		}
	case transportError(err):
		return &cliError{
			exit: exitUnreachable,
			code: "hub_unreachable",
			err:  fmt.Errorf("could not reach the grain hub at %s. %s (%v)", where, fix, rootCause(err)),
		}
	}
	return err
}
