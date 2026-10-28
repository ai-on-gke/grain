package ui

import (
	"net/http"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

// GitAmbientSource is one registered ambient source as the connection
// editor offers it (gitproxy.AmbientSourceUI): the name a connection
// stores, the tool it runs, the hosts it is auto's answer for, whether
// it carries those hosts' traffic itself, and the label, help and host
// presets the registration gave it. gh and git credential fill are core
// grain's own and the editor knows them without asking.
type GitAmbientSource struct {
	Name            string                   `json:"name"`
	Command         string                   `json:"command"`
	Label           string                   `json:"label"`
	Hosts           []string                 `json:"hosts,omitempty"`
	ForgeViaCommand bool                     `json:"forgeViaCommand,omitempty"`
	Help            string                   `json:"help,omitempty"`
	Presets         []GitAmbientSourcePreset `json:"presets,omitempty"`
}

// GitAmbientSourcePreset is a host a GitAmbientSource fills a new
// connection in for.
type GitAmbientSourcePreset struct {
	Host  string `json:"host"`
	Label string `json:"label"`
}

// GitAmbientSources is every registered ambient source, in auto's order,
// for GET /api/git-ambient-sources. A source registered without UI is
// labelled with its command.
func GitAmbientSources() []GitAmbientSource {
	out := []GitAmbientSource{}
	for _, src := range gitproxy.RegisteredAmbientSources() {
		v := GitAmbientSource{
			Name: src.Name, Command: src.Command, Label: src.Command,
			Hosts: src.Hosts, ForgeViaCommand: src.ForgeViaCommand != nil,
		}
		if src.UI != nil {
			if src.UI.Label != "" {
				v.Label = src.UI.Label
			}
			v.Help = src.UI.Help
			for _, p := range src.UI.Presets {
				v.Presets = append(v.Presets, GitAmbientSourcePreset{Host: p.Host, Label: p.Label})
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleListGitAmbientSources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, GitAmbientSources())
}
