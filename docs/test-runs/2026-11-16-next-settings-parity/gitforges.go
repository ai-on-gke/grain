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

//go:build ignore

// gitforges runs a local authenticated HTTPS smart-git server and a local SSH
// git server over bare repositories under -root, generating a self-signed CA +
// server TLS certificate and Ed25519 SSH host/client keys in -cert-dir.
package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type auditEvent struct {
	At      string `json:"at"`
	Proto   string `json:"proto"`
	AuthOK  bool   `json:"authOk"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	Command string `json:"command,omitempty"`
	Note    string `json:"note,omitempty"`
}

var logMu sync.Mutex

func writeAudit(logPath string, ev auditEvent) {
	if logPath == "" {
		return
	}
	ev.At = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(ev)
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func generateTLS(certDir string) (tls.Certificate, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Walkthrough Git CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(filepath.Join(certDir, "ca.pem"), caPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caTmpl, &srvKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	srvCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER})
	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	srvKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: srvKeyDER})
	if err := os.WriteFile(filepath.Join(certDir, "server.crt"), srvCertPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(filepath.Join(certDir, "server.key"), srvKeyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(srvCertPEM, srvKeyPEM)
}

func generateSSHKeys(certDir, sshAddr string) (ssh.Signer, ssh.PublicKey, error) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		return nil, nil, err
	}
	h, port, err := net.SplitHostPort(sshAddr)
	if err != nil {
		return nil, nil, err
	}
	khHost := h
	if port != "22" {
		khHost = fmt.Sprintf("[%s]:%s", h, port)
	}
	khLine := khHost + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))) + "\n"
	if err := os.WriteFile(filepath.Join(certDir, "known_hosts"), []byte(khLine), 0o644); err != nil {
		return nil, nil, err
	}
	khPort22 := h + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))) + "\n"
	_ = os.WriteFile(filepath.Join(certDir, "known_hosts_port22"), []byte(khPort22), 0o644)

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(certDir, "ssh_client_key"), pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, nil, err
	}
	sshClientPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(certDir, "ssh_client_key.pub"), ssh.MarshalAuthorizedKey(sshClientPub), 0o644); err != nil {
		return nil, nil, err
	}
	return hostSigner, sshClientPub, nil
}

func execSSHGit(ch ssh.Channel, command string, env []string, root, logPath string) {
	defer ch.Close()
	service, quoted, _ := strings.Cut(command, " ")
	repoPath := strings.Trim(quoted, "'")
	repoPath = strings.TrimPrefix(repoPath, "/")
	writeAudit(logPath, auditEvent{Proto: "ssh", AuthOK: true, Command: command, Path: repoPath})
	status := uint32(0)
	if (service != "git-upload-pack" && service != "git-receive-pack") || strings.Contains(repoPath, "..") {
		fmt.Fprintf(ch.Stderr(), "unsupported command %q\n", command)
		status = 1
	} else {
		full := filepath.Join(root, repoPath)
		cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), full)
		cmd.Env = append(os.Environ(), env...)
		stdin, _ := cmd.StdinPipe()
		cmd.Stdout = ch
		cmd.Stderr = ch.Stderr()
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(ch.Stderr(), err)
			status = 1
		} else {
			go func() {
				_, _ = io.Copy(stdin, ch)
				_ = stdin.Close()
			}()
			if err := cmd.Wait(); err != nil {
				status = 128
			}
		}
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}

func main() {
	httpsAddr := flag.String("https-addr", "127.0.0.1:4294", "HTTPS smart-git listen address")
	sshAddr := flag.String("ssh-addr", "127.0.0.1:4295", "SSH git listen address")
	root := flag.String("root", "", "directory containing <owner>/<repo>.git bare repos")
	certDir := flag.String("cert-dir", "", "directory to write ca.pem, ssh_client_key, known_hosts")
	httpsToken := flag.String("https-token", "walk_https_secret_pat_99", "required HTTP Basic Auth password/token")
	logPath := flag.String("log", "", "optional JSONL request audit log")
	flag.Parse()

	if *root == "" || *certDir == "" {
		log.Fatal("-root and -cert-dir are required")
	}
	if err := os.MkdirAll(*certDir, 0o755); err != nil {
		log.Fatal(err)
	}

	tlsCert, err := generateTLS(*certDir)
	if err != nil {
		log.Fatalf("generateTLS: %v", err)
	}
	hostSigner, clientPub, err := generateSSHKeys(*certDir, *sshAddr)
	if err != nil {
		log.Fatalf("generateSSHKeys: %v", err)
	}

	// Start SSH git server.
	sshCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientPub.Marshal()) {
				return nil, nil
			}
			writeAudit(*logPath, auditEvent{Proto: "ssh", AuthOK: false, Note: "unknown public key"})
			return nil, fmt.Errorf("unknown public key")
		},
	}
	sshCfg.AddHostKey(hostSigner)
	sshLn, err := net.Listen("tcp", *sshAddr)
	if err != nil {
		log.Fatalf("listen ssh %s: %v", *sshAddr, err)
	}
	go func() {
		for {
			conn, err := sshLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, chans, reqs, err := ssh.NewServerConn(c, sshCfg)
				if err != nil {
					_ = c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						_ = newCh.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					go func() {
						var env []string
						for req := range chReqs {
							switch req.Type {
							case "env":
								var kv struct{ Name, Value string }
								if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" {
									env = append(env, "GIT_PROTOCOL="+kv.Value)
								}
								_ = req.Reply(true, nil)
							case "exec":
								var p struct{ Command string }
								if ssh.Unmarshal(req.Payload, &p) != nil {
									_ = req.Reply(false, nil)
									continue
								}
								_ = req.Reply(true, nil)
								go execSSHGit(ch, p.Command, env, *root, *logPath)
							default:
								_ = req.Reply(false, nil)
							}
						}
					}()
				}
			}(conn)
		}
	}()

	// Start authenticated HTTPS smart-git server.
	gitBin, err := exec.LookPath("git")
	if err != nil {
		log.Fatalf("git not in PATH: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		user, pass, ok := r.BasicAuth()
		authOK := ok && (pass == *httpsToken || user == *httpsToken)
		if !authOK {
			writeAudit(*logPath, auditEvent{Proto: "https", AuthOK: false, Method: r.Method, Path: r.URL.Path})
			w.Header().Set("WWW-Authenticate", `Basic realm="walk-https-git"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		writeAudit(*logPath, auditEvent{Proto: "https", AuthOK: true, Method: r.Method, Path: r.URL.Path})
		cgiHandler := &cgi.Handler{
			Path: gitBin,
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + *root,
				"GIT_HTTP_EXPORT_ALL=1",
				"REMOTE_USER=" + user,
			},
		}
		cgiHandler.ServeHTTP(w, r)
	})

	httpsLn, err := tls.Listen("tcp", *httpsAddr, &tls.Config{Certificates: []tls.Certificate{tlsCert}})
	if err != nil {
		log.Fatalf("listen https %s: %v", *httpsAddr, err)
	}
	log.Printf("gitforges listening: https=%s ssh=%s root=%s", *httpsAddr, *sshAddr, *root)
	if err := http.Serve(httpsLn, handler); err != nil {
		log.Fatal(err)
	}
}
