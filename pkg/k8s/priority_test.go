package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"text/template"
)

func TestManifestCarriesAPriorityClassOnlyWhenOneIsNamed(t *testing.T) {
	shape := PodShape{CPUs: 1, MemoryMB: 1, DiskGB: 1}

	pod := decodeManifest(t, Client{}, "p", shape, PodOptions{Image: "example/sandbox", PriorityClassName: "grain-ci"})
	spec := pod["spec"].(map[string]any)
	if got := spec["priorityClassName"]; got != "grain-ci" {
		t.Errorf("spec.priorityClassName = %v, want grain-ci", got)
	}

	pod = decodeManifest(t, Client{}, "p", shape, PodOptions{Image: "example/sandbox"})
	spec = pod["spec"].(map[string]any)
	if got, ok := spec["priorityClassName"]; ok {
		t.Errorf("spec.priorityClassName = %v with none named; want the key absent", got)
	}
}

func TestMissingPriorityClassReadsTheAdmissionPluginsRefusal(t *testing.T) {
	refused := &commandError{
		argv:   []string{"kubectl", "apply"},
		stderr: `Error from server (Forbidden): error when creating "STDIN": pods "p" is forbidden: no PriorityClass with name grain-ci was found`,
		err:    &exec.ExitError{},
	}
	if !missingPriorityClass(refused) {
		t.Errorf("missingPriorityClass(%v) = false, want true", refused)
	}
	if missingPriorityClass(&commandError{stderr: "Error from server (Forbidden): exceeded quota", err: errors.New("exit 1")}) {
		t.Error("missingPriorityClass read a quota refusal as a missing class")
	}
	if missingPriorityClass(errors.New("no PriorityClass with name x")) {
		t.Error("missingPriorityClass read something that is not kubectl's own failure")
	}
}

func TestPodDisruptionFormatReadsATrueDisruptionTarget(t *testing.T) {
	render := func(pod string) string {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal([]byte(pod), &obj); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := template.Must(template.New("d").Parse(podDisruptionFormat)).Execute(&out, obj); err != nil {
			t.Fatalf("rendering podDisruptionFormat: %v", err)
		}
		return out.String()
	}
	preempted := `{"status": {"phase": "Running", "conditions": [
		{"type": "Ready", "status": "True"},
		{"type": "DisruptionTarget", "status": "True", "reason": "PreemptionByScheduler",
		 "message": "grain/grain-granule-792-2: preempting to accommodate a higher priority pod"}]}}`
	if got, want := render(preempted), "PreemptionByScheduler: grain/grain-granule-792-2: preempting to accommodate a higher priority pod"; got != want {
		t.Errorf("rendered %q, want %q", got, want)
	}
	cleared := `{"status": {"phase": "Running", "conditions": [
		{"type": "DisruptionTarget", "status": "False", "reason": "PreemptionByScheduler"}]}}`
	if got := render(cleared); got != "" {
		t.Errorf("a False DisruptionTarget rendered %q, want nothing", got)
	}
}

func TestDisruptedReportsAPreemptedPodAndAVanishedOne(t *testing.T) {
	kubectl, _ := fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: "PreemptionByScheduler: preempting to accommodate a higher priority pod"}})
	how, disrupted, err := Client{Kubectl: kubectl}.Disrupted(context.Background(), "p")
	if err != nil || !disrupted {
		t.Fatalf("Disrupted = %q, %v, %v; want disrupted", how, disrupted, err)
	}
	if !strings.Contains(how, "PreemptionByScheduler") {
		t.Errorf("how = %q, want the condition's own reason in it", how)
	}

	kubectl, _ = fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {code: 1}})
	how, disrupted, err = Client{Kubectl: kubectl}.Disrupted(context.Background(), "p")
	if err != nil || !disrupted {
		t.Fatalf("Disrupted on a missing pod = %q, %v, %v; want disrupted", how, disrupted, err)
	}

	kubectl, _ = fakeKubectl(t, map[string]struct {
		stdout string
		code   int
	}{"get": {stdout: ""}})
	if how, disrupted, err := (Client{Kubectl: kubectl}).Disrupted(context.Background(), "p"); err != nil || disrupted {
		t.Fatalf("Disrupted on an undisturbed pod = %q, %v, %v; want not disrupted", how, disrupted, err)
	}
}
