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

package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// graphqlServer is a forge that serves GraphQL at graphqlAt only, and
// REST under restPrefix, recording every path it was asked for. Anything
// else is a 401 "Bad credentials", the way a GitHub Enterprise Server
// answered a GraphQL post that went under /api/v3.
func graphqlServer(t *testing.T, graphqlAt, restPrefix string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST" && r.URL.Path == graphqlAt:
			w.WriteHeader(200)
			w.Write([]byte(`{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`))
		case r.Method == "GET" && r.URL.Path == restPrefix+"/repos/experimental/grain":
			w.WriteHeader(200)
			w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func TestRealTransportGraphQLPath(t *testing.T) {
	for _, c := range []struct {
		name string
		t    *RealTransport
		want string
	}{
		{"github.com", &RealTransport{Host: "api.github.com"}, "/graphql"},
		{"enterprise server", &RealTransport{Host: "ghe.example", PathPrefix: "/api/v3"}, "/api/graphql"},
		{"a prefix that is not /v3", &RealTransport{Host: "ghe.example", PathPrefix: "/api"}, "/api/graphql"},
		{"explicit", &RealTransport{Host: "ghe.example", PathPrefix: "/api/v3", GraphQLPath: "/gql"}, "/gql"},
	} {
		if got := c.t.graphqlPath(); got != c.want {
			t.Errorf("%s: graphqlPath() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGraphQLGoesToSlashGraphQLWithNoPrefix(t *testing.T) {
	server, seen := graphqlServer(t, "/graphql", "")
	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"),
		ledger: &callerLedger{logf: func(string, ...any) {}}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.ResolveReviewThread("experimental", "grain", "PRRT_1"); err != nil {
		t.Fatalf("ResolveReviewThread: %v (seen %q)", err, *seen)
	}
	if len(*seen) != 1 || (*seen)[0] != "POST /graphql" {
		t.Fatalf("seen %q, want one POST /graphql", *seen)
	}
}

func TestResolveReviewThreadOnAnEnterpriseServerPostsToAPIGraphQL(t *testing.T) {
	server, seen := graphqlServer(t, "/api/graphql", "/api/v3")
	l := &callerLedger{logf: func(string, ...any) {}}
	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), PathPrefix: "/api/v3", ledger: l}
	client := NewClient(transport, StaticToken{strPtr("t")}).As("review")
	if err := client.ResolveReviewThread("experimental", "grain", "PRRT_1"); err != nil {
		t.Fatalf("ResolveReviewThread: %v (seen %q)", err, *seen)
	}
	if len(*seen) != 1 || (*seen)[0] != "POST /api/graphql" {
		t.Fatalf("seen %q, want one POST /api/graphql", *seen)
	}
	// Charged to GraphQL, not to "write".
	if k := l.callerStats()["review"].Kinds["graphql"]; k.Sent != 1 {
		t.Fatalf("graphql kind = %+v, want one sent (all: %+v)", k, l.callerStats())
	}
}

func TestGraphQLRefusalNamesTheEndpointAndThatRESTAnswers(t *testing.T) {
	// GraphQL is served nowhere this transport posts to, while REST on
	// the same connection answers: the error says so rather than leaving
	// it at "Bad credentials".
	server, _ := graphqlServer(t, "/nowhere", "/api/v3")
	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), PathPrefix: "/api/v3",
		ledger: &callerLedger{logf: func(string, ...any) {}}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	err := client.ResolveReviewThread("experimental", "grain", "PRRT_1")
	if err == nil {
		t.Fatal("ResolveReviewThread succeeded against a server with no GraphQL")
	}
	msg := Message(err)
	for _, want := range []string{"401", "Bad credentials", "GraphQL endpoint", server.URL + "/api/graphql",
		"REST API on the same connection answers for experimental/grain"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Message = %q, want it to say %q", msg, want)
		}
	}
	if !strings.Contains(Readable(err).Error(), "GraphQL endpoint") {
		t.Errorf("Readable dropped the note: %q", Readable(err).Error())
	}
}

func TestGraphQLRefusalWithoutAWorkingRESTSaysOnlyWhichEndpoint(t *testing.T) {
	transport := NewFakeTransport(
		ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)},
		ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)},
	)
	client := NewClient(transport, nil)
	err := client.ResolveReviewThread("o", "r", "PRRT_1")
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want a 404 IsNotFound still answers for", err)
	}
	msg := Message(err)
	if !strings.Contains(msg, "the GraphQL endpoint refused the request") || strings.Contains(msg, "REST API") {
		t.Errorf("Message = %q", msg)
	}
	if transport.Calls[0].Path != "/graphql" {
		t.Errorf("client handed the transport %q, want /graphql", transport.Calls[0].Path)
	}
}
