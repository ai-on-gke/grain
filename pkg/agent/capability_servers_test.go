package agent

import (
	"errors"
	"testing"
)

type remoteTestHost struct{ LocalHost }

func (remoteTestHost) IsLocal() bool { return false }

// stagedLocallyTestHost stages on grain's own machine (IsLocal) but runs
// its CLI somewhere with a loopback of its own, as a run pod does.
type stagedLocallyTestHost struct{ LocalHost }

func (stagedLocallyTestHost) SharesHostLoopback() bool { return false }

type carrierTestHost struct {
	remoteTestHost
	released *int
	err      error
}

func (h carrierTestHost) CarryCapabilityServers(servers []CapabilityMCPServer) ([]CapabilityMCPServer, func(), error) {
	if h.err != nil {
		return nil, nil, h.err
	}
	out := make([]CapabilityMCPServer, len(servers))
	for i, s := range servers {
		s.URL = "http://127.0.0.1:2/carried/mcp"
		out[i] = s
	}
	return out, func() { *h.released++ }, nil
}

func TestCarryCapabilityServers(t *testing.T) {
	servers := []CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:1/x/mcp", Tools: []string{"t"}}}
	if got, release, err := CarryCapabilityServers(LocalHost{}, servers); err != nil || got[0].URL != servers[0].URL {
		t.Errorf("a local CLI was refused or given another URL: %v, %v", got, err)
	} else {
		release()
	}
	if _, release, err := CarryCapabilityServers(remoteTestHost{}, nil); err != nil {
		t.Errorf("a remote CLI with no capability servers was refused: %v", err)
	} else {
		release()
	}
	if _, _, err := CarryCapabilityServers(remoteTestHost{}, servers); err == nil {
		t.Error("a remote CLI was handed loopback proxies it cannot reach")
	}
	if _, _, err := CarryCapabilityServers(stagedLocallyTestHost{}, servers); err == nil {
		t.Error("a CLI that only stages locally was handed loopback proxies it cannot reach")
	}

	released := 0
	got, release, err := CarryCapabilityServers(carrierTestHost{released: &released}, servers)
	if err != nil {
		t.Fatalf("a host that carries capability servers was refused: %v", err)
	}
	if got[0].URL != "http://127.0.0.1:2/carried/mcp" || got[0].Name != "self-debug" || servers[0].URL != "http://127.0.0.1:1/x/mcp" {
		t.Errorf("carried servers = %+v (original %+v), want the carrier's URL under the same name", got, servers)
	}
	release()
	if released != 1 {
		t.Errorf("release reached the carrier %d times, want 1", released)
	}
	refused := errors.New("too old")
	if _, _, err := CarryCapabilityServers(carrierTestHost{released: &released, err: refused}, servers); !errors.Is(err, refused) {
		t.Errorf("a carrier's refusal came back as %v", err)
	}
}
