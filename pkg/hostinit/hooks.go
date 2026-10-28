package hostinit

import (
	"sort"
	"strings"
	"sync"
)

// AllowedHostsHook adds entries to the -ui-allowed-hosts grain init
// writes for a listener bound to every interface, for a machine some
// environment fronts with a web proxy of its own: names a browser puts
// in Host that are not the machine's (machineHostnames). Registering one
// is how a build that knows such an environment teaches the installer
// about it; core grain registers none.
type AllowedHostsHook struct {
	// Name identifies the hook; registering another with the same Name
	// replaces it.
	Name string
	// Hosts is what to add on a machine named names -- none when the
	// environment is not this one -- and what they are for, which the
	// step line says after them ("for this machine's web proxy URL").
	Hosts func(names []string) (hosts []string, purpose string)
}

var (
	allowedHostsHooksMu sync.RWMutex
	allowedHostsHooks   = map[string]AllowedHostsHook{}
)

// RegisterAllowedHosts adds h, replacing any hook with the same Name,
// and returns a function that removes it again (for tests).
func RegisterAllowedHosts(h AllowedHostsHook) (unregister func()) {
	allowedHostsHooksMu.Lock()
	defer allowedHostsHooksMu.Unlock()
	allowedHostsHooks[h.Name] = h
	return func() {
		allowedHostsHooksMu.Lock()
		defer allowedHostsHooksMu.Unlock()
		delete(allowedHostsHooks, h.Name)
	}
}

// hookedHost is one entry a registered AllowedHostsHook adds, and why.
type hookedHost struct{ host, purpose string }

// hookedAllowedHosts is what every registered AllowedHostsHook adds for
// a machine named names, in hook-name order, without repeats.
func hookedAllowedHosts(names []string) []hookedHost {
	allowedHostsHooksMu.RLock()
	hooks := make([]AllowedHostsHook, 0, len(allowedHostsHooks))
	for _, h := range allowedHostsHooks {
		hooks = append(hooks, h)
	}
	allowedHostsHooksMu.RUnlock()
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Name < hooks[j].Name })

	var out []hookedHost
	seen := map[string]bool{}
	for _, h := range hooks {
		if h.Hosts == nil {
			continue
		}
		hosts, purpose := h.Hosts(names)
		for _, host := range hosts {
			if host = strings.TrimSpace(host); host == "" || seen[host] {
				continue
			}
			seen[host] = true
			out = append(out, hookedHost{host: host, purpose: purpose})
		}
	}
	return out
}
