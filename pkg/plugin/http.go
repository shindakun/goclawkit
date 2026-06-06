package plugin

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"os"
	"time"
)

// defaultHTTPTimeout is used by HTTPClient and by HTTPClientTimeout when given a
// non-positive duration.
const defaultHTTPTimeout = 30 * time.Second

// HTTPClient returns an *http.Client correctly configured to reach external APIs from
// inside the goclaw sandbox THROUGH the credential proxy, with a 30s timeout. Use
// HTTPClientTimeout to pick a different timeout.
//
// Plugins that call an HTTPS API (e.g. Gmail) should use this and send NO auth header:
// the proxy injects the real credential on the way out, so the plugin never holds a
// token. It honors the container's HTTPS_PROXY/NO_PROXY env and trusts the proxy CA
// (SSL_CERT_FILE).
//
// Do NOT hand-roll a Transport for an external call unless you replicate
// ProxyFromEnvironment AND the proxy CA, or your requests will silently bypass the
// proxy (going out with no injected auth) and fail with opaque 401/TLS errors.
//
// It works in both modes with no branching by the author:
//   - proxy mode: HTTPS_PROXY + SSL_CERT_FILE are set; requests route through the proxy,
//     which terminates TLS with a leaf minted by the proxy CA this client trusts.
//   - direct mode (proxy off / dev): the env is absent; the client uses the system
//     roots and a direct connection, unchanged.
//
// The returned client is safe for concurrent use.
func HTTPClient() *http.Client {
	return HTTPClientTimeout(defaultHTTPTimeout)
}

// HTTPClientTimeout is HTTPClient with an explicit timeout. timeout <= 0 uses the 30s
// default. See HTTPClient for the proxy/CA behavior.
func HTTPClientTimeout(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &http.Client{Timeout: timeout, Transport: newProxyTransport(http.ProxyFromEnvironment)}
}

// newProxyTransport builds the proxy-aware, proxy-CA-trusting Transport. The proxy
// func is a parameter so tests can inject a deterministic one instead of fighting
// ProxyFromEnvironment's process-global env cache.
func newProxyTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	tr := &http.Transport{
		// Proxy from env: HTTPS_PROXY / NO_PROXY. This is the line authors forget, and
		// the whole reason this helper exists.
		Proxy:                 proxy,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if pool, ok := proxyCAPool(); ok {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return tr
}

// proxyCAPool builds a cert pool of the SYSTEM roots PLUS the proxy CA from
// SSL_CERT_FILE, returning ok=false (leave RootCAs nil -> system roots) when
// SSL_CERT_FILE is unset or unreadable.
//
// We start from the system roots and ADD the proxy CA, never replace: a plugin may
// also call hosts with no stored credential, which the proxy blind-tunnels (passes
// through unintercepted), so those still present their REAL public cert and must
// validate against system roots. A proxy-CA-only pool would break them. On a minimal
// container with no system bundle, SystemCertPool may be empty/err; we fall back to a
// fresh pool so the proxy CA is still trusted (the only root that matters in proxy
// mode).
func proxyCAPool() (*x509.CertPool, bool) {
	caFile := os.Getenv("SSL_CERT_FILE")
	if caFile == "" {
		return nil, false
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, false
	}
	pool, perr := x509.SystemCertPool()
	if perr != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, false
	}
	return pool, true
}
