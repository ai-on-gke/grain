package intree

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/bwsalmon/grain/pkg/hostscriptcli"
)

// hostScriptHelperEnv makes this test binary serve the host-script server
// over stdio, with its data directory in hostScriptHelperData, the way
// hostd starts `grain capability host-script` for a run: host entry.
// grainHelperEnv makes it the sandbox's grain instead, answering
// `grain host-script` the way cmd/grain-sandbox does.
const (
	hostScriptHelperEnv  = "GRAIN_INTREE_TEST_HOST_SCRIPT"
	hostScriptHelperData = "GRAIN_INTREE_TEST_HOST_SCRIPT_DATA"
	grainHelperEnv       = "GRAIN_INTREE_TEST_GRAIN"
)

// testMainHelpers are the other helper modes of this test binary, by the
// variable that selects each, registered by files a build tag may leave
// out (gchat_hostd_test.go's). Each serves and exits.
var testMainHelpers = map[string]func(){}

func TestMain(m *testing.M) {
	for env, serve := range testMainHelpers {
		if os.Getenv(env) == "1" {
			serve()
		}
	}
	if os.Getenv(grainHelperEnv) == "1" {
		if len(os.Args) < 2 || os.Args[1] != "host-script" {
			fmt.Fprintln(os.Stderr, "grain: only host-script")
			os.Exit(2)
		}
		os.Exit(hostscriptcli.Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	if os.Getenv(hostScriptHelperEnv) == "1" {
		s, err := NewHostScript(HostScriptConfig{Listen: "127.0.0.1:0", DataDir: os.Getenv(hostScriptHelperData)})
		if err == nil {
			err = s.Serve(context.Background(), os.Stdin, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
