package runhost

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
)

func testKube() Kubernetes {
	return Kubernetes{
		Client:         k8s.Client{Namespace: "grain"},
		Volumes:        []KubeVolume{{Claim: "grain-data", MountPath: "/data"}, {Claim: "grain-creds", MountPath: "/data/creds"}},
		ServiceAccount: "grain",
		DaemonSelector: map[string]string{"app.kubernetes.io/name": "grain", "app.kubernetes.io/component": KubeDaemonComponent},
		Security:       k8s.PodSecurity{Pod: k8s.SecurityContext{"runAsUser": 1000}},
		Env:            map[string]string{"HOME": "/data/creds"},
	}
}

func testSpec() Spec {
	deadline := time.Date(2026, 10, 13, 6, 0, 0, 0, time.UTC)
	return Spec{
		Name:  "abc-runhost-run-r1-123",
		Image: "ghcr.io/bwsalmon/grain/grain:main",
		Labels: Labels{Deployment: "abc", Task: "grain/task-7", Run: "r1", Generation: "v1.2.3+dirty",
			Protocol: Protocol, Deadline: deadline, Spool: "/data/runhosts/spool/run-r1-123"},
		Mounts: []Mount{
			{Path: "/data/runhosts/spool"}, {Path: "/data/runhosts/staging"},
			{Path: "/data/creds"}, {Path: "/data/creds/codex", ReadOnly: true},
		},
		Command: []string{"/usr/local/bin/grain", "runhost", "exec", "-spool", "/data/runhosts/spool/run-r1-123"},
	}
}

// The pod is given the directories and not the volumes around them: the
// data claim also holds the store and the secrets.
func TestKubeManifestMountsSubPathsAtTheSamePaths(t *testing.T) {
	raw, err := testKube().Manifest(testSpec())
	if err != nil {
		t.Fatal(err)
	}
	var pod struct {
		Metadata struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			RestartPolicy string `json:"restartPolicy"`
			Grace         int    `json:"terminationGracePeriodSeconds"`
			SA            string `json:"serviceAccountName"`
			Volumes       []struct {
				Name string `json:"name"`
				PVC  struct {
					ClaimName string `json:"claimName"`
				} `json:"persistentVolumeClaim"`
			} `json:"volumes"`
			Containers []struct {
				Mounts []struct {
					Name      string `json:"name"`
					MountPath string `json:"mountPath"`
					SubPath   string `json:"subPath"`
					ReadOnly  bool   `json:"readOnly"`
				} `json:"volumeMounts"`
			} `json:"containers"`
			Affinity struct {
				PodAffinity struct {
					Required []struct {
						TopologyKey   string `json:"topologyKey"`
						LabelSelector struct {
							MatchLabels map[string]string `json:"matchLabels"`
						} `json:"labelSelector"`
					} `json:"requiredDuringSchedulingIgnoredDuringExecution"`
				} `json:"podAffinity"`
			} `json:"affinity"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.RestartPolicy != "Never" || pod.Spec.Grace != DefaultKubeGracePeriod || pod.Spec.SA != "grain" {
		t.Fatalf("spec = %+v", pod.Spec)
	}
	claims := map[string]string{}
	for _, v := range pod.Spec.Volumes {
		claims[v.Name] = v.PVC.ClaimName
	}
	type m struct {
		claim, path, sub string
		ro               bool
	}
	var got []m
	for _, vm := range pod.Spec.Containers[0].Mounts {
		got = append(got, m{claims[vm.Name], vm.MountPath, vm.SubPath, vm.ReadOnly})
	}
	want := []m{
		{"grain-data", "/data/runhosts/spool", "runhosts/spool", false},
		{"grain-data", "/data/runhosts/staging", "runhosts/staging", false},
		{"grain-creds", "/data/creds", "", false},
		{"grain-creds", "/data/creds/codex", "codex", true},
	}
	if len(got) != len(want) {
		t.Fatalf("mounts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mount %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(pod.Spec.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want each claim once", pod.Spec.Volumes)
	}
	aff := pod.Spec.Affinity.PodAffinity.Required
	if len(aff) != 1 || aff[0].TopologyKey != "kubernetes.io/hostname" ||
		aff[0].LabelSelector.MatchLabels["app.kubernetes.io/component"] != KubeDaemonComponent {
		t.Fatalf("podAffinity = %+v, want the daemon's node", aff)
	}
	l := pod.Metadata.Labels
	if l[k8s.SandboxLabel] != KubeComponent || l[LabelTask] != "grain_task-7" || l[LabelGeneration] != "v1.2.3_dirty" ||
		l[LabelDeadline] != strconv.FormatInt(testSpec().Labels.Deadline.Unix(), 10) || l[LabelProtocol] != "1" {
		t.Fatalf("labels = %v", l)
	}
	back, err := ParseLabels(pod.Metadata.Annotations)
	if err != nil || back != testSpec().Labels {
		t.Fatalf("annotations read back as %+v (%v), want %+v", back, err, testSpec().Labels)
	}
}

func TestKubeManifestRefusesADirectoryOnNoVolume(t *testing.T) {
	spec := testSpec()
	spec.Mounts = append(spec.Mounts, Mount{Path: "/etc/grain"})
	if _, err := testKube().Manifest(spec); err == nil || !strings.Contains(err.Error(), "/etc/grain") {
		t.Fatalf("Manifest = %v, want a refusal naming /etc/grain", err)
	}
	spec.Mounts = []Mount{{Path: "/database"}}
	if _, err := testKube().Manifest(spec); err == nil {
		t.Fatal("/database is not under /data, but was accepted")
	}
}

func TestKubeManifestSpreadsWithoutADaemonSelector(t *testing.T) {
	k := testKube()
	k.DaemonSelector = nil
	raw, err := k.Manifest(testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "podAffinity") {
		t.Fatalf("a ReadWriteMany run pod was pinned: %s", raw)
	}
}

func TestKubeManifestLabelsTheInstance(t *testing.T) {
	labels := func(k Kubernetes) map[string]string {
		raw, err := k.Manifest(testSpec())
		if err != nil {
			t.Fatal(err)
		}
		var pod struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatal(err)
		}
		return pod.Metadata.Labels
	}
	k := testKube()
	k.Client.Instance = "a1b2c3d4"
	if got := labels(k)[k8s.InstanceLabel]; got != "a1b2c3d4" {
		t.Errorf("%s = %q, want a1b2c3d4", k8s.InstanceLabel, got)
	}
	if got, ok := labels(testKube())[k8s.InstanceLabel]; ok {
		t.Errorf("%s = %q with no instance configured, want none", k8s.InstanceLabel, got)
	}

	// List keeps selecting on the permanent installation id alone: a
	// run pod labelled under a deployment id since edited in Settings is
	// still this daemon's to adopt.
	var selector string
	k.list = func(_ context.Context, s string) ([]k8s.PodSummary, error) { selector = s; return nil, nil }
	if _, err := k.List(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(selector, k8s.InstanceLabel) {
		t.Errorf("selector = %q, want it not to select on %s", selector, k8s.InstanceLabel)
	}
}

func TestKubeListReadsAnnotations(t *testing.T) {
	spec := testSpec()
	k := testKube()
	var selector string
	k.list = func(_ context.Context, s string) ([]k8s.PodSummary, error) {
		selector = s
		other := spec.Labels
		other.Deployment = "someone-else"
		return []k8s.PodSummary{
			{Name: "a", Annotations: spec.Labels.Map(), Phase: k8s.PhaseRunning},
			{Name: "b", Annotations: spec.Labels.Map(), Phase: k8s.PhaseSucceeded},
			{Name: "c", Annotations: spec.Labels.Map(), Phase: k8s.PhaseRunning, Deleting: true},
			{Name: "d", Annotations: spec.Labels.Map(), Phase: k8s.PhasePending},
			{Name: "e", Annotations: other.Map(), Phase: k8s.PhaseRunning},
			{Name: "f", Annotations: map[string]string{}, Phase: k8s.PhaseRunning},
		}, nil
	}
	infos, err := k.List(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selector, "app.kubernetes.io/component=runhost") || !strings.Contains(selector, LabelDeployment+"=abc") {
		t.Fatalf("selector = %q", selector)
	}
	got := map[string]bool{}
	for _, i := range infos {
		got[i.Name] = i.Running
		if i.Labels != spec.Labels {
			t.Fatalf("%s labels = %+v", i.Name, i.Labels)
		}
	}
	want := map[string]bool{"a": true, "b": false, "c": false, "d": true}
	if len(got) != len(want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	for n, r := range want {
		if got[n] != r {
			t.Fatalf("%s running = %v, want %v", n, got[n], r)
		}
	}
}

func TestCheckClaim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		claim   k8s.ClaimSummary
		rwx     bool
		refused bool
		warned  bool
	}{
		{"rwo default", k8s.ClaimSummary{StorageClass: "standard-rwo", AccessModes: []string{"ReadWriteOnce"}}, false, false, false},
		{"rwop", k8s.ClaimSummary{AccessModes: []string{"ReadWriteOncePod"}}, false, true, false},
		{"rwo asked rwx", k8s.ClaimSummary{AccessModes: []string{"ReadWriteOnce"}}, true, true, false},
		{"filestore", k8s.ClaimSummary{StorageClass: "standard-rwx", Provisioner: "filestore.csi.storage.gke.io",
			AccessModes: []string{"ReadWriteMany"}}, true, false, false},
		{"gcsfuse provisioner", k8s.ClaimSummary{Provisioner: "gcsfuse.csi.storage.gke.io",
			AccessModes: []string{"ReadWriteMany"}}, true, true, false},
		{"gcsfuse class, rwo mode", k8s.ClaimSummary{StorageClass: "gcsfuse-bucket",
			AccessModes: []string{"ReadWriteMany"}}, false, true, false},
		{"static rwx", k8s.ClaimSummary{AccessModes: []string{"ReadWriteMany"}}, true, false, true},
		{"rox", k8s.ClaimSummary{AccessModes: []string{"ReadOnlyMany"}}, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warning, err := CheckClaim(tc.claim, "c", tc.rwx)
			if (err != nil) != tc.refused || (warning != "") != tc.warned {
				t.Fatalf("CheckClaim = %q, %v; want refused %v, warned %v", warning, err, tc.refused, tc.warned)
			}
		})
	}
}

func TestPodName(t *testing.T) {
	if got := podName("ABC-runhost-run-R_1-123"); got != "abc-runhost-run-r-1-123" {
		t.Fatalf("podName = %q", got)
	}
}
