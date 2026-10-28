// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Granulefile and Granulefile.guest-base compile this program directly into the sandbox guest image.
func TestGranulefileCarriesGrainsOwnForwarder(t *testing.T) {
	for _, name := range []string{"Granulefile", "Granulefile.guest-base"} {
		granulefile, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		content := string(granulefile)
		if !strings.Contains(content, "cmd/grain-connect/main.go") {
			t.Fatalf("%s does not reference cmd/grain-connect/main.go", name)
		}
		if !strings.Contains(content, "/usr/local/bin/grain-connect") {
			t.Fatalf("%s does not build /usr/local/bin/grain-connect", name)
		}
	}
}

// The forwarder as a connection: an HTTP request written to its stdin
// reaches a server on the socket, and the response comes back on its
// stdout. Which is the whole of what the transport needs of it.
func TestForwarderCarriesAnHTTPExchangeOverAUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "lifecycle.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, `{"protocol":"1","ready":true,"definition":%q}`, req.URL.Path)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	// stdin and stdout as two pipes, which is exactly what an exec gives.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run([]string{socket}, inR, outW); outW.Close() }()

	request := "GET /v1/health HTTP/1.1\r\nHost: lifecycle\r\nConnection: close\r\n\r\n"
	if _, err := inW.WriteString(request); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(outR)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading the response back off stdout: %v", err)
	}
	defer response.Body.Close()
	body := make([]byte, 256)
	n, _ := response.Body.Read(body)
	if !strings.Contains(string(body[:n]), `"/v1/health"`) {
		t.Errorf("body = %q", body[:n])
	}

	inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the forwarder did not finish after stdin closed")
	}
}

func TestForwarderNeedsExactlyOneAddress(t *testing.T) {
	for _, args := range [][]string{{}, {"", ""}, {"  "}} {
		if err := run(args, strings.NewReader(""), os.Stdout); err == nil {
			t.Errorf("%q: accepted", args)
		}
	}
}

func TestForwarderReportsASocketNothingIsListeningOn(t *testing.T) {
	for _, addr := range []string{
		filepath.Join(t.TempDir(), "absent.sock"),
		"./absent.sock",
		"absent.sock",
	} {
		err := run([]string{addr}, strings.NewReader(""), os.Stdout)
		if err == nil || !strings.Contains(err.Error(), "dialing unix") {
			t.Fatalf("addr=%s error = %v, want dialing unix error", addr, err)
		}
	}
}
