package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestTLSCerts generates a fresh two-tier CA → leaf cert chain and
// writes the LEAF cert + key to disk, returning the PEM-encoded CA cert
// separately for the client's RootCAs pool. This mirrors a real
// deployment shape (leaf cert presented by the server, trusted via CA
// anchor at the client) rather than re-using a single self-signed cert
// as both root and leaf. Leaf cert has KeyUsageDigitalSignature +
// ExtKeyUsageServerAuth (no CertSign, no IsCA) — strict TLS stacks
// accept it without complaint.
//
// All certs are 1h-valid with a 1-minute clock-skew tolerance.
func writeTestTLSCerts(t *testing.T, dir string) (certPath, keyPath string, caCertPEM []byte) {
	t.Helper()

	// 1. Self-signed CA (KeyUsageCertSign + IsCA are correct HERE — this
	//    cert is the trust anchor, not an end-entity cert).
	caPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mcp-gateway-test-ca"},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPriv.PublicKey, caPriv)
	require.NoError(t, err)
	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	// 2. Server leaf cert signed by CA. Standard end-entity key usage only.
	leafPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "mcp-gateway-test-leaf"},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafPriv.PublicKey, caPriv)
	require.NoError(t, err)
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	leafKeyDER, err := x509.MarshalECPrivateKey(leafPriv)
	require.NoError(t, err)
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, leafPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, leafKeyPEM, 0o600))
	return certPath, keyPath, caCertPEM
}

// newTLSTestServer constructs a Server wired to empty lifecycle/proxy
// (no backend servers needed) with the provided GatewaySettings override
// and optional auth. HTTPPort=0 so net.Listen picks a random free port.
func newTLSTestServer(t *testing.T, gw models.GatewaySettings, authCfg AuthConfig) *Server {
	t.Helper()
	cfg := &models.Config{
		Servers: make(map[string]*models.ServerConfig),
	}
	cfg.ApplyDefaults()
	cfg.Gateway = gw
	// HTTPPort=0 ⇒ ListenAndServe binds a random free port via net.Listen
	// on "host:0"; tests read the bound address via srv.Addr() after
	// waitForListener returns.

	lm := lifecycle.NewManager(cfg, "test", testLogger())
	p := proxy.New(cfg, lm, "test", testLogger())
	return NewServer(lm, p, nil, cfg, "", testLogger(), authCfg, "test")
}

// waitForListener waits for srv.Addr() to report a bound listener.
// Uses require.Eventually so the poll cadence/timeout are standard and
// the failure message is consistent with the rest of the suite. 5s
// deadline tolerates slow Windows CI; 20ms tick keeps the happy path
// fast on local runs.
func waitForListener(t *testing.T, srv *Server) net.Addr {
	t.Helper()
	var addr net.Addr
	require.Eventually(t, func() bool {
		addr = srv.Addr()
		return addr != nil
	}, 5*time.Second, 20*time.Millisecond, "server did not bind listener within deadline")
	return addr
}

// TestServer_TLSSelfSigned_SuccessPath (T15B.1) exercises the ServeTLS
// branch in ListenAndServe — previously uncovered by the default
// `go test ./...` path. Generates a self-signed cert + key in
// t.TempDir(), configures GatewaySettings.TLSCertPath / TLSKeyPath,
// starts the server in a goroutine, waits for the listener to bind,
// probes with an https.Client pinned to the self-signed cert:
//   - 200 on the public /api/v1/health endpoint
//   - 401 on an authed route when no Bearer is presented
func TestServer_TLSSelfSigned_SuccessPath(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, caCertPEM := writeTestTLSCerts(t, dir)

	authCfg := AuthConfig{Enabled: true, Token: strings.Repeat("a", 43)}
	srv := newTLSTestServer(t, models.GatewaySettings{
		HTTPPort:     0, // random free port
		BindAddress:  "127.0.0.1",
		TLSCertPath:  certPath,
		TLSKeyPath:   keyPath,
		Transports:   []string{"http"},
	}, authCfg)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				t.Logf("server returned: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Log("server did not shut down within 5s")
		}
	})

	addr := waitForListener(t, srv)

	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(caCertPEM), "failed to parse test CA cert PEM")
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: certPool, MinVersion: tls.VersionTLS12},
		},
	}

	baseURL := "https://" + addr.String()

	// Public endpoint — no Bearer required, 200.
	resp, err := client.Get(baseURL + "/api/v1/health")
	require.NoError(t, err, "TLS GET /api/v1/health failed")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "health should be 200 over TLS")

	// Authed endpoint without Bearer — 401 proves auth middleware runs on TLS path.
	resp2, err := client.Get(baseURL + "/api/v1/servers")
	require.NoError(t, err, "TLS GET /api/v1/servers failed")
	_ = resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode, "authed route without Bearer must be 401 on TLS")
}

// TestServer_NonLoopbackNoTLS_RefusesToStart (T15B.2) pins the
// deliberate refusal wording at server.go:322: when the server binds
// non-loopback with Bearer auth enabled and no TLS configured, it must
// refuse to start. This keeps the guard intact across refactors — the
// wording is the grep target.
func TestServer_NonLoopbackNoTLS_RefusesToStart(t *testing.T) {
	authCfg := AuthConfig{Enabled: true, Token: strings.Repeat("b", 43)}
	srv := newTLSTestServer(t, models.GatewaySettings{
		HTTPPort:    0,
		BindAddress: "0.0.0.0",
		AllowRemote: true,
	}, authCfg)

	err := srv.ListenAndServe(context.Background())
	require.Error(t, err, "non-loopback + auth + no TLS must refuse to start")

	msg := err.Error()
	// Deliberate wording — future refactors must keep these substrings.
	assert.Contains(t, msg, "non-loopback", "error must name the binding reason")
	assert.Contains(t, msg, "Bearer auth is enabled", "error must name the auth condition")
	assert.Contains(t, msg, "refusing to start without TLS", "error must name the refusal")
	assert.Contains(t, msg, "gateway.tls_cert_path", "error must name tls_cert_path config key")
	assert.Contains(t, msg, "gateway.tls_key_path", "error must name tls_key_path config key")
}

// TestServer_HalfConfiguredTLS_RefusesToStart (T15B.3) exercises the
// new half-configured TLS guard. Before v1.5.0, setting exactly one of
// TLSCertPath / TLSKeyPath silently dropped back to plain HTTP — an
// operator who misconfigured one path saw no warning and assumed TLS.
// The guard now refuses to start and names BOTH paths in the error, so
// operators can see exactly what is missing. Wording is deliberate and
// quoted verbatim in CHANGELOG; future refactors must not reword it
// silently.
func TestServer_HalfConfiguredTLS_RefusesToStart(t *testing.T) {
	tests := []struct {
		name        string
		certPath    string
		keyPath     string
		containsAll []string
	}{
		{
			name:     "cert-only",
			certPath: "/tmp/cert.pem",
			keyPath:  "",
			containsAll: []string{
				"TLS is half-configured",
				"gateway.tls_cert_path is set",
				"gateway.tls_key_path is empty",
				"both must be set to enable TLS",
				"both must be empty for plain HTTP",
			},
		},
		{
			name:     "key-only",
			certPath: "",
			keyPath:  "/tmp/key.pem",
			containsAll: []string{
				"TLS is half-configured",
				"gateway.tls_key_path is set",
				"gateway.tls_cert_path is empty",
				"both must be set to enable TLS",
				"both must be empty for plain HTTP",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTLSTestServer(t, models.GatewaySettings{
				HTTPPort:    0,
				BindAddress: "127.0.0.1",
				TLSCertPath: tc.certPath,
				TLSKeyPath:  tc.keyPath,
			}, AuthConfig{})

			err := srv.ListenAndServe(context.Background())
			require.Error(t, err, "half-configured TLS must refuse to start")

			msg := err.Error()
			for _, want := range tc.containsAll {
				assert.Contains(t, msg, want, "error message missing required substring: %q", want)
			}
		})
	}
}
