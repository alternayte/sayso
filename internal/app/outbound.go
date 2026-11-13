package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// HTTPClient returns the client for all outbound HTTP. It uses the proxy
// variables and trusts the extra CA certificates (C-08, REQ-CORE-008).
func HTTPClient(cfg *Config) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pems := []byte(cfg.ExtraCAPEM)
	if cfg.ExtraCAFile != "" {
		b, err := os.ReadFile(cfg.ExtraCAFile)
		if err != nil {
			return nil, fmt.Errorf("SAYSO_EXTRA_CA_FILE: %w", err)
		}
		pems = append(pems, '\n')
		pems = append(pems, b...)
	}
	if len(pems) > 1 && !pool.AppendCertsFromPEM(pems) {
		return nil, fmt.Errorf("extra CA: no certificate found in SAYSO_EXTRA_CA_PEM or SAYSO_EXTRA_CA_FILE")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: t, Timeout: 60 * time.Second, CheckRedirect: keepCredentialsOnHost}, nil
}

// redirectHeaders are the request headers that may follow a redirect to
// another host. Go drops Authorization there, but not x-api-key, api-key or
// a custom feed header, which would hand the credential to the new host.
var redirectHeaders = map[string]bool{"Accept": true, "Accept-Encoding": true, "Accept-Language": true, "User-Agent": true, "Content-Type": true, "Range": true}

func keepCredentialsOnHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		for k := range req.Header {
			if !redirectHeaders[http.CanonicalHeaderKey(k)] {
				req.Header.Del(k)
			}
		}
	}
	return nil
}
