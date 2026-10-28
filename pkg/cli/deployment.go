package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// The deployment's own connections and keys. Single-tenant grain has one
// owner, the deployment, so these name no team: they call the
// deployment's routes (/api/deployment), which keep what they configure
// on the default team's row until the schema reset moves it to the
// settings row (docs/single-tenant-reset.md, stages 1 and 6).

// cmdGitAmbient adds an ambient git connection to the deployment: the
// installer's way of using the operator's own `gh` login instead of
// requiring GRAIN_GITHUB_TOKEN (google/scripts/cloudtop-install.sh).
func cmdGitAmbient(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain git-ambient", flag.ContinueOnError)
	host := fs.String("host", model.DefaultGitHost, "git host the login is for")
	kind := fs.String("kind", "github", "the host's kind: github (a forge API) or git (clone, fetch and push only)")
	baseURL := fs.String("base-url", "", "the host's scheme://host[:port], when that is not https on the default port")
	proxy := fs.String("proxy", "", "the http(s)://host:port proxy the host is reached through, if any "+
		"(read from the source's own tool when empty, for a registered source that has one)")
	source := fs.String("source", "auto", "which of this host's tools to ask: "+strings.Join(gitproxy.AmbientSourceNames(), ", ")+
		" (auto: a registered source on its own hosts; gh, then git, elsewhere)")
	ifNone := fs.Bool("if-none", false, "do nothing when the deployment already has a git connection on that host")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *ifNone {
		existing, err := c.GetDeployment(ctx)
		if err != nil {
			return err
		}
		if conn, ok := gitConnectionOn(existing, *host); ok {
			if out.json {
				out.encode(existing)
				return nil
			}
			fmt.Printf("This deployment already has a git connection on %s (%s): leaving it as it is.\n", *host, conn)
			return nil
		}
	}
	updated, err := c.AddDeploymentGitConnection(ctx, ui.AddTeamGitConnectionRequest{
		Type: "ambient", Host: *host, Kind: *kind, BaseURL: *baseURL, Proxy: *proxy, Source: *source,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	if *kind == string(model.GitHostGit) {
		fmt.Printf("This deployment now clones and pushes to %s as this host's own git credential helper login (no pull requests: a plain git host).\n",
			*host)
		return nil
	}
	fmt.Printf("This deployment now clones, pushes and calls GitHub as this host's own %s login on %s.\n",
		gitproxy.AmbientCommandOf(*host, *source), *host)
	return nil
}

// gitConnectionOn names the credential the deployment (team, the row
// that holds its connections) already clones and pushes with on host, if it has one: on the default host that includes the
// legacy single credential (HasGitCredential).
func gitConnectionOn(team ui.Team, host string) (string, bool) {
	for _, conn := range team.GitConnections {
		if model.SameGitHost(conn.Host, host, model.DefaultGitHost) {
			return conn.Credential, true
		}
	}
	if model.SameGitHost(host, "", model.DefaultGitHost) && team.HasGitCredential {
		return strings.TrimSpace(team.GitCredential), true
	}
	return "", false
}

// cmdGitConnections lists what the deployment already clones and
// pushes with, so an installer can tell "this host's login failed its
// check" apart from "this deployment has no git at all".
func cmdGitConnections(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain git-connections", flag.ContinueOnError)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	existing, err := c.GetDeployment(ctx)
	if err != nil {
		return err
	}
	conns := append([]model.TeamGitConnection(nil), existing.GitConnections...)
	if _, ok := gitConnectionOn(ui.Team{GitConnections: conns}, model.DefaultGitHost); !ok && existing.HasGitCredential {
		conns = append(conns, model.TeamGitConnection{Credential: strings.TrimSpace(existing.GitCredential), Host: model.DefaultGitHost})
	}
	if out.json {
		out.encode(conns)
		return nil
	}
	for _, conn := range conns {
		host := conn.Host
		if host == "" {
			host = model.DefaultGitHost
		}
		fmt.Printf("%s %s\n", host, conn.Credential)
	}
	return nil
}

func cmdDefaultModel(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain default-model", flag.ContinueOnError)
	ifNone := fs.Bool("if-none", false, "do nothing when the deployment already has a default model")
	ifUnserved := fs.Bool("if-unserved", false,
		"do nothing when the deployment already has a default model one of its enabled connections serves")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	models := model.ParseDefaultModels(strings.Join(rest, ","))
	if len(models) == 0 {
		return errors.New("usage: grain default-model [--if-none | --if-unserved] <model> [<model>...]")
	}
	name := model.JoinDefaultModels(models)
	if *ifNone || *ifUnserved {
		existing, err := c.GetDeployment(ctx)
		if err != nil {
			return err
		}
		keep := existing.DefaultModel != ""
		if keep && !*ifNone {
			// A default no enabled connection serves is one every task
			// left on it waits behind forever.
			settings, err := c.GetSettings(ctx)
			if err != nil {
				return err
			}
			keep = servesModel(settings.AgentBackends, existing.DefaultModel)
		}
		if keep {
			if out.json {
				out.encode(existing)
				return nil
			}
			fmt.Printf("This deployment already runs on %s by default: leaving it as it is.\n", existing.DefaultModel)
			return nil
		}
		if existing.DefaultModel != "" && !out.json {
			fmt.Printf("No enabled connection serves the default model %s: replacing it.\n", existing.DefaultModel)
		}
	}
	updated, err := c.UpdateDeployment(ctx, ui.UpdateDeploymentRequest{DefaultModel: &name})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("This deployment now runs on %s by default.\n", updated.DefaultModel)
	return nil
}

// servesModel reports whether an enabled connection of the deployment
// serves name (or any model in a comma-separated default model list).
func servesModel(backends []model.AgentBackend, name string) bool {
	enabled := model.DeploymentAgentBackends(backends, false)
	for _, m := range model.ParseDefaultModels(name) {
		for _, b := range enabled {
			if b.SupportsModel(m) {
				return true
			}
		}
	}
	return false
}

// cmdBroadcast sends a notice to all running task agents (or, with -all,
// to every active task of the deployment). Running agents poll human
// comments on their task conversation via daemonAddenda and receive the
// notice on their next tool call.
func cmdBroadcast(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain broadcast", flag.ContinueOnError)
	all := fs.Bool("all", false, "deliver to queued and awaiting-reply tasks too, not only running task agents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name, ok := misplacedFlag(fs, fs.Args()); ok {
		return fmt.Errorf("-%s has to come before the message: grain broadcast -%s <message>", name, name)
	}
	msg := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if msg == "" {
		return errors.New("usage: grain broadcast [-all] <message...>")
	}
	res, err := c.BroadcastDeploymentMessage(ctx, ui.BroadcastTeamRequest{
		Message:     msg,
		RunningOnly: !*all,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(res)
		return nil
	}
	if res.DeliveredCount == 0 {
		if *all {
			fmt.Println("No active tasks to notify.")
		} else {
			fmt.Println("No running task agents to notify.")
		}
		return nil
	}
	ids := make([]string, len(res.TaskIDs))
	for i, id := range res.TaskIDs {
		ids[i] = "#" + id
	}
	fmt.Printf("Sent notice to %d task(s): %s\n", res.DeliveredCount, strings.Join(ids, ", "))
	return nil
}
