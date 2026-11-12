package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/doctor"
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/testenv"
)

// privateCA returns a CA and a server certificate for host signed by it.
func privateCA(t *testing.T, host string) (caPEM []byte, cert tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "acme test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, ca, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}
}

// connectProxy is an HTTPS proxy that sends every CONNECT to target.
func connectProxy(t *testing.T, target string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		n.Add(1)
		up, err := net.Dial("tcp", target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = up.Close()
			return
		}
		go func() { _, _ = io.Copy(up, conn); _ = up.Close() }()
		go func() { _, _ = io.Copy(conn, up); _ = conn.Close() }()
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// seedProvider migrates the database and stores one provider with a key.
func seedProvider(t *testing.T, dbURL, name, baseURL, key string) {
	t.Helper()
	cmd := exec.Command(testenv.Binary(t), "migrate")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "SAYSO_DATABASE_URL=" + dbURL}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("migrate: %v %s", err, out)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sec, err := secrets.Open(ctx, pool, mustKey(t), app.NewRedactor(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	id, err := sec.PutValue(ctx, "provider."+name+".key", key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers (name, api_type, base_url, auth_style, secret_id, models) VALUES ($1, 'openai-completions', $2, 'bearer', $3, '{fake-large}')`,
		name, baseURL, id); err != nil {
		t.Fatal(err)
	}
}

func runDoctor(t *testing.T, dbURL string, env ...string) (map[string]doctor.Result, string) {
	t.Helper()
	cmd := exec.Command(testenv.Binary(t), "doctor", "--json")
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "SAYSO_DATABASE_URL=" + dbURL, "SAYSO_SECRET_KEY=" + testenv.SecretKey(), "SAYSO_TEMPORAL_ADDRESS=127.0.0.1:1"}, env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, _ := cmd.Output()
	var rs []doctor.Result
	if err := json.Unmarshal(out, &rs); err != nil {
		t.Fatalf("doctor output: %v: %s %s", err, out, stderr.String())
	}
	got := map[string]doctor.Result{}
	for _, r := range rs {
		got[r.Name] = r
	}
	return got, string(out) + stderr.String()
}

func TestSCN_CORE_007_ProxyAndExtraCA(t *testing.T) {
	const host = "fake-model.acme.test"
	caPEM, cert := privateCA(t, host)
	fm := fakemodel.New()
	tlsSrv := httptest.NewUnstartedServer(fm.Handler())
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	_, port, _ := net.SplitHostPort(tlsSrv.Listener.Addr().String())
	proxy, connects := connectProxy(t, tlsSrv.Listener.Addr().String())

	dbURL := testenv.Postgres(t)
	seedProvider(t, dbURL, "tls-fake", "https://"+host+":"+port+"/v1", "sk-tls-0001")

	got, _ := runDoctor(t, dbURL, "HTTPS_PROXY="+proxy.URL, "SAYSO_EXTRA_CA_PEM="+string(caPEM))
	if r := got["provider tls-fake"]; !r.OK {
		t.Fatalf("with the extra CA: %+v", r)
	}
	if connects.Load() == 0 {
		t.Fatal("the provider call did not go through the proxy")
	}
	if len(fm.Requests()) == 0 {
		t.Fatal("the fake provider received no request")
	}
	before := connects.Load()
	got, _ = runDoctor(t, dbURL, "HTTPS_PROXY="+proxy.URL)
	r := got["provider tls-fake"]
	if r.OK || !strings.Contains(r.Detail, "certificate") {
		t.Fatalf("without the extra CA: %+v", r)
	}
	if connects.Load() == before {
		t.Fatal("the second call did not go through the proxy")
	}
}

func TestSCN_CORE_009_FailedProviderCallRedacted(t *testing.T) {
	const providerKey = "sk-acme-failing-key-55ab"
	// The provider answers 401 and echoes the request headers, as some gateways do.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key","authorization":"`+r.Header.Get("Authorization")+`"}`)
	}))
	defer srv.Close()
	dbURL := testenv.Postgres(t)
	seedProvider(t, dbURL, "failing", srv.URL+"/v1", providerKey)
	got, all := runDoctor(t, dbURL)
	if r := got["provider failing"]; r.OK || !strings.Contains(r.Detail, "401") {
		t.Fatalf("provider check: %+v", r)
	}
	if strings.Contains(all, providerKey) || strings.Contains(all, "Bearer sk-") {
		t.Fatalf("doctor output holds the key or the Authorization value:\n%s", all)
	}
	if !strings.Contains(all, "[redacted:provider.failing.key]") {
		t.Fatalf("doctor output does not show the redaction:\n%s", all)
	}
}
