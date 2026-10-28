package guestimage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeDockerDeclaring writes a stand-in docker that answers `image
// inspect -f {{...ExposedPorts...}}` with declares, and returns its
// path. A non-empty fail makes it exit non-zero instead, which is what
// an image that is not on this machine looks like.
func fakeDockerDeclaring(t *testing.T, declares, fail string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n"
	if fail != "" {
		script += "echo '" + fail + "' >&2\nexit 1\n"
	} else {
		script += "printf '%s '  " + declares + "\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDeclaredPorts: what an image says it wants reachable, in one
// spelling and one order, so that a build writing it into a Dockerfile
// and a launch writing it into a docker run agree about what the image
// said.
func TestDeclaredPorts(t *testing.T) {
	d := fakeDockerDeclaring(t, "53/udp 8080 8080/tcp 9090-9099", "")

	ports, err := DeclaredPorts(context.Background(), d, "guest:latest", nil)
	if err != nil {
		t.Fatalf("DeclaredPorts() error = %v", err)
	}
	// Sorted, and the bare 8080 folded into the 8080/tcp it means:
	// docker's own spelling names the protocol either way.
	want := []string{"53/udp", "8080/tcp", "9090-9099/tcp"}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("DeclaredPorts() = %v, want %v", ports, want)
	}
}

// TestDeclaredPorts_DropsWhatIsNotAPort: the answer is a string out of
// an image somebody else authored, and every caller gives it meaning
// somewhere -- a generated Dockerfile, a "docker run -p". Anything that
// is not a port declaration is dropped, and said out loud, rather than
// carried into either.
func TestDeclaredPorts_DropsWhatIsNotAPort(t *testing.T) {
	d := fakeDockerDeclaring(t, "8080/tcp --privileged", "")
	var report strings.Builder

	ports, err := DeclaredPorts(context.Background(), d, "guest:latest", &report)
	if err != nil {
		t.Fatalf("DeclaredPorts() error = %v", err)
	}
	if want := []string{"8080/tcp"}; !reflect.DeepEqual(ports, want) {
		t.Errorf("DeclaredPorts() = %v, want %v", ports, want)
	}
	if !strings.Contains(report.String(), "--privileged") {
		t.Errorf("report = %q, want it to name what was dropped", report.String())
	}
}

// TestDeclaredPorts_UnreadableImageIsAnError: "this image declares
// nothing" and "nobody could ask" are different answers, and a caller
// about to publish what came back needs to be able to tell them apart.
func TestDeclaredPorts_UnreadableImageIsAnError(t *testing.T) {
	d := fakeDockerDeclaring(t, "", "Error: No such image: guest:latest")

	_, err := DeclaredPorts(context.Background(), d, "guest:latest", nil)
	if err == nil {
		t.Fatal("DeclaredPorts() of an image docker cannot inspect = nil error, want one")
	}
	if !strings.Contains(err.Error(), "guest:latest") || !strings.Contains(err.Error(), "EXPOSE") {
		t.Errorf("error = %v, want it to name the image and the question", err)
	}
}
