package daemon

import (
	"bytes"
	"strings"
	"testing"
)

// -k8s-pod is a fourth sandbox transport, exclusive with the other three,
// and needs the directory its tools work in, since a pod has no root this
// process could default it from.
func TestMCPServerK8sPodTransportFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no workspace", []string{"-k8s-pod", "grain-sandbox-7-1", "-k8s-namespace", "grain"}, "-k8s-pod needs -workspace"},
		{"with a container too", []string{"-k8s-pod", "grain-sandbox-7-1", "-container", "c", "-workspace", "/home/agent"}, "exactly one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := ServeMCPServerPinned(strings.NewReader(""), &out, tc.args, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mcpserver %q = %v, want an error containing %q", tc.args, err, tc.want)
			}
		})
	}
}
