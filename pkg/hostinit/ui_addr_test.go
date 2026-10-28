package hostinit

import (
	"os"
	"strings"
	"testing"
)

// settleUIAddr is what grain init writes as the unit's -ui-addr and
// -ui-allowed-hosts, and where its steps then reach the daemon.
func TestSettleUIAddr(t *testing.T) {
	defaults, _ := DefaultAllowedHosts(machineHostnames())
	names := strings.Join(defaults, ",")
	for _, c := range []struct {
		name      string
		unit      string // "" for no installed unit
		given     string
		extra     []string
		wantArgs  []string
		wantDial  string
		wantInfo  string
		wantHosts bool
		notMove   bool
	}{
		{
			name:      "fresh install",
			wantArgs:  []string{"-ui-addr", "0.0.0.0:4242", "-ui-allowed-hosts", names},
			wantDial:  "127.0.0.1:4242",
			wantInfo:  "the default for a fresh install",
			wantHosts: true,
		},
		{
			name:     "re-run keeps the installed address and hosts",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d -ui-addr 127.0.0.1:9000 -ui-allowed-hosts grain.example\n",
			wantArgs: []string{"-ui-addr", "127.0.0.1:9000", "-ui-allowed-hosts", "grain.example"},
			wantDial: "127.0.0.1:9000",
			wantInfo: "kept from the installed",
		},
		{
			// A re-run of a fresh install is not told to move to where it already is.
			name:     "re-run keeps an installed unit already on the default",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d -ui-addr 0.0.0.0:4242 -ui-allowed-hosts h\n",
			wantArgs: []string{"-ui-addr", "0.0.0.0:4242", "-ui-allowed-hosts", "h"},
			wantDial: "127.0.0.1:4242",
			wantInfo: "kept from the installed",
			notMove:  true,
		},
		{
			name:     "re-run over a unit an older grain init wrote, naming none",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d\n",
			wantArgs: []string{"-ui-addr", LegacyUIAddr},
			wantDial: LegacyUIAddr,
			wantInfo: "names no -ui-addr",
		},
		{
			name:     "an explicit -ui-addr wins over the installed one",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d -ui-addr 127.0.0.1:9000\n",
			given:    "127.0.0.1:4242",
			wantArgs: []string{"-ui-addr", "127.0.0.1:4242"},
			wantDial: "127.0.0.1:4242",
			wantInfo: "as given",
		},
		{
			name:     "-daemon-arg -ui-addr is left where it is",
			extra:    []string{"-ui-addr=127.0.0.1:7000"},
			wantDial: "127.0.0.1:7000",
			wantInfo: "from -daemon-arg -ui-addr",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := withTempUnitPath(t)
			if c.unit != "" {
				if err := os.WriteFile(path, []byte(c.unit), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			opts := Options{RepoRoot: t.TempDir(), DataDir: t.TempDir(), UIAddr: c.given, ExtraDaemonArgs: c.extra}.withDefaults()
			var r Result
			got := settleUIAddr(opts, &r)
			if c.wantHosts && names == "" {
				c.wantArgs = c.wantArgs[:2]
			}
			if strings.Join(got.uiDaemonArgs, " ") != strings.Join(c.wantArgs, " ") {
				t.Errorf("unit gets %q, want %q", got.uiDaemonArgs, c.wantArgs)
			}
			if got.UIAddr != c.wantDial {
				t.Errorf("the steps dial %q, want %q", got.UIAddr, c.wantDial)
			}
			if len(r.Steps) != 1 || !strings.Contains(r.Steps[0].Detail, c.wantInfo) {
				t.Errorf("reported %+v, want one step saying %q", r.Steps, c.wantInfo)
			}
			if move := "to move to the new default"; c.notMove && len(r.Steps) == 1 && strings.Contains(r.Steps[0].Detail, move) {
				t.Errorf("reported %q for a unit already on the default", r.Steps[0].Detail)
			}
		})
	}
}

// A fresh install's report says the exposure out loud.
func TestSettleUIAddrSaysTheExposure(t *testing.T) {
	withTempUnitPath(t)
	var r Result
	settleUIAddr(Options{RepoRoot: t.TempDir(), DataDir: t.TempDir()}.withDefaults(), &r)
	for _, want := range []string{"http://<this host>:4242/", "every caller presents the access code", "ssh -L 4242:localhost:4242"} {
		if !strings.Contains(r.Steps[0].Detail, want) {
			t.Errorf("the report does not say %q: %s", want, r.Steps[0].Detail)
		}
	}
}

// With an AllowedHostsHook registered -- here a made-up web proxy, the
// way google/hostinit registers a Cloudtop's -- the default list carries
// what it adds after this machine's own names, and the step says why;
// without one, or off the machine it is for, it is the names alone. A
// re-run over a unit an earlier grain init gave the names alone adds the
// hook's, and an explicit -daemon-arg -ui-allowed-hosts is still left as
// it is.
func TestSettleUIAddrAddsWhatAnAllowedHostsHookAdds(t *testing.T) {
	prev := machineHostnames
	t.Cleanup(func() { machineHostnames = prev })

	if got, why := DefaultAllowedHosts([]string{"me.wp.example", "me"}); strings.Join(got, ",") != "me.wp.example,me" || strings.Contains(why, "proxy") {
		t.Errorf("with no hook the default is %q (%s), want the machine's own names alone", got, why)
	}
	t.Cleanup(RegisterAllowedHosts(AllowedHostsHook{
		Name: "webproxy-test",
		Hosts: func(names []string) ([]string, string) {
			for _, n := range names {
				if strings.HasSuffix(n, ".wp.example") {
					return []string{"*.proxy.wp.example"}, "for this machine's web proxy URL"
				}
			}
			return nil, ""
		},
	}))
	if got, why := DefaultAllowedHosts([]string{"build-7.us-central1.internal", "build-7"}); strings.Join(got, ",") != "build-7.us-central1.internal,build-7" || strings.Contains(why, "proxy") {
		t.Errorf("off the hook's machines the default is %q (%s), want the machine's own names alone", got, why)
	}
	if got, _ := DefaultAllowedHosts(nil); got != nil {
		t.Errorf("with no hostname the default is %q, want none", got)
	}

	machineHostnames = func() []string { return []string{"me.wp.example", "me"} }
	const want = "me.wp.example,me,*.proxy.wp.example"
	for _, c := range []struct {
		name     string
		unit     string
		extra    []string
		wantArgs []string
		wantInfo string
	}{
		{
			name:     "fresh install",
			wantArgs: []string{"-ui-addr", "0.0.0.0:4242", "-ui-allowed-hosts", want},
			wantInfo: "plus *.proxy.wp.example for this machine's web proxy URL",
		},
		{
			name:     "re-run over the machine's own names",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d -ui-addr 0.0.0.0:4242 -ui-allowed-hosts me.wp.example,me\n",
			wantArgs: []string{"-ui-addr", "0.0.0.0:4242", "-ui-allowed-hosts", want},
			wantInfo: "adding *.proxy.wp.example for this machine's web proxy URL to the installed unit's own names",
		},
		{
			name:     "re-run over a list someone chose",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d -ui-addr 0.0.0.0:4242 -ui-allowed-hosts me,grain.example\n",
			wantArgs: []string{"-ui-addr", "0.0.0.0:4242", "-ui-allowed-hosts", "me,grain.example"},
			wantInfo: "kept from the installed unit",
		},
		{
			name:     "an explicit -daemon-arg -ui-allowed-hosts",
			unit:     "[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir /d -ui-addr 0.0.0.0:4242 -ui-allowed-hosts me.wp.example,me\n",
			extra:    []string{"-ui-allowed-hosts=me"},
			wantArgs: []string{"-ui-addr", "0.0.0.0:4242"},
			wantInfo: "from -daemon-arg -ui-allowed-hosts",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := withTempUnitPath(t)
			if c.unit != "" {
				if err := os.WriteFile(path, []byte(c.unit), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var r Result
			got := settleUIAddr(Options{RepoRoot: t.TempDir(), DataDir: t.TempDir(), ExtraDaemonArgs: c.extra}.withDefaults(), &r)
			if strings.Join(got.uiDaemonArgs, " ") != strings.Join(c.wantArgs, " ") {
				t.Errorf("unit gets %q, want %q", got.uiDaemonArgs, c.wantArgs)
			}
			if len(r.Steps) != 1 || !strings.Contains(r.Steps[0].Detail, c.wantInfo) {
				t.Errorf("reported %+v, want one step saying %q", r.Steps, c.wantInfo)
			}
		})
	}
}
