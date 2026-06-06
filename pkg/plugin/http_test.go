package plugin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCA generates a throwaway CA cert, writes it as PEM to a temp file, and
// returns the path plus the parsed cert. Fully in-process, so the CA-pool tests do
// not depend on any OS cert bundle (the runtime is a Linux container that may have a
// minimal or absent system bundle).
func writeTestCA(t *testing.T) (path string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "goclaw-test-proxy-ca"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(1<<31-1, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "proxy-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, cert
}

func TestHTTPClientTrustsProxyCAWhenSet(t *testing.T) {
	caPath, caCert := writeTestCA(t)
	t.Setenv("SSL_CERT_FILE", caPath)

	c := HTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("RootCAs is nil; want a pool containing the proxy CA")
	}
	// The pool must trust the proxy CA: a leaf signed by it verifies against the pool.
	if !poolTrustsCA(t, tr.TLSClientConfig.RootCAs, caCert) {
		t.Error("pool does not trust the proxy CA")
	}
	if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2", tr.TLSClientConfig.MinVersion)
	}
}

func TestHTTPClientSystemRootsWhenCAUnset(t *testing.T) {
	// Empty SSL_CERT_FILE is treated as unset by proxyCAPool (t.Setenv restores any
	// real value after the test).
	t.Setenv("SSL_CERT_FILE", "")

	c := HTTPClient()
	tr := c.Transport.(*http.Transport)
	if tr.TLSClientConfig != nil {
		t.Errorf("TLSClientConfig = %+v, want nil (system roots)", tr.TLSClientConfig)
	}
}

func TestHTTPClientUnreadableCAFallsBackToSystemRoots(t *testing.T) {
	// SSL_CERT_FILE points at a nonexistent file: leave RootCAs nil rather than fail.
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "does-not-exist.pem"))
	c := HTTPClient()
	tr := c.Transport.(*http.Transport)
	if tr.TLSClientConfig != nil {
		t.Errorf("TLSClientConfig = %+v, want nil when CA file is unreadable", tr.TLSClientConfig)
	}
}

func TestHTTPClientTimeoutDefaultAndHonored(t *testing.T) {
	if got := HTTPClient().Timeout; got != defaultHTTPTimeout {
		t.Errorf("HTTPClient().Timeout = %v, want %v", got, defaultHTTPTimeout)
	}
	if got := HTTPClientTimeout(0).Timeout; got != defaultHTTPTimeout {
		t.Errorf("HTTPClientTimeout(0).Timeout = %v, want default %v", got, defaultHTTPTimeout)
	}
	if got := HTTPClientTimeout(-5 * time.Second).Timeout; got != defaultHTTPTimeout {
		t.Errorf("negative timeout = %v, want default %v", got, defaultHTTPTimeout)
	}
	want := 7 * time.Second
	if got := HTTPClientTimeout(want).Timeout; got != want {
		t.Errorf("HTTPClientTimeout(%v).Timeout = %v, want it honored", want, got)
	}
}

// TestHTTPClientHonorsProxyEnv proves the Transport's Proxy func is wired so an https
// request routes through HTTPS_PROXY. It asserts on the real default (HTTPClient uses
// http.ProxyFromEnvironment) by setting the env, AND separately proves the wiring is
// deterministic via the injectable builder (avoiding ProxyFromEnvironment's
// process-global env cache, which can make the env-based path order-sensitive).
func TestHTTPClientHonorsProxyEnv(t *testing.T) {
	const proxy = "http://host.docker.internal:18080"

	// Deterministic path: inject a proxy func and confirm the Transport uses it.
	fixed, _ := url.Parse(proxy)
	tr := newProxyTransport(func(*http.Request) (*url.URL, error) { return fixed, nil })
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1/x", nil)
	got, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy func error: %v", err)
	}
	if got == nil || got.String() != proxy {
		t.Fatalf("Transport routed to %v, want %s", got, proxy)
	}

	// Real default path: HTTPClient wires http.ProxyFromEnvironment. With HTTPS_PROXY
	// set, an https request should resolve to the proxy. (ProxyFromEnvironment caches
	// env once per process; this assertion is best-effort and skipped if the cache was
	// already primed without our value.)
	t.Setenv("HTTPS_PROXY", proxy)
	t.Setenv("NO_PROXY", "host.docker.internal,localhost,127.0.0.1")
	c := HTTPClient()
	ctr := c.Transport.(*http.Transport)
	u, err := ctr.Proxy(req)
	if err != nil {
		t.Fatalf("default Proxy func error: %v", err)
	}
	if u == nil || u.String() != proxy {
		t.Skipf("ProxyFromEnvironment returned %v (env cache primed before this test); the injected-func assertion above already proves the wiring", u)
	}
}

// poolTrustsCA reports whether pool verifies a leaf chained to caCert. We build a
// minimal leaf signed by a fresh CA matching caCert's key is overkill; instead we
// verify caCert itself against the pool (a CA cert verifies as its own chain root when
// the pool contains it).
func poolTrustsCA(t *testing.T, pool *x509.CertPool, caCert *x509.Certificate) bool {
	t.Helper()
	opts := x509.VerifyOptions{Roots: pool}
	_, err := caCert.Verify(opts)
	return err == nil
}
