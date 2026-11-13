// Package feeds is a fake of private package feeds for tests and the
// Compose "fakes" profile: an npm registry and a NuGet v3 feed with absolute
// URLs in their documents. It records each request with its credential.
package feeds

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Package is one npm package version of the fake registry.
type Package struct {
	Name    string
	Version string
	tgz     []byte
}

// Request is one recorded call.
type Request struct {
	Method string
	Path   string
	Auth   string
	At     time.Time
}

// Fake is a running fake feed server. Base is its URL as clients write it.
type Fake struct {
	mu       sync.Mutex
	requests []Request
	// Credential is the expected value of the Authorization header.
	Credential string
	npm        map[string]*Package
	nupkg      []byte
}

// New returns a feed with the npm package @acme/money 1.0.0 and the NuGet
// package Acme.Money 1.0.0.
func New(credential string) *Fake {
	f := &Fake{Credential: credential, npm: map[string]*Package{}}
	f.npm["@acme/money"] = &Package{Name: "@acme/money", Version: "1.0.0", tgz: npmTarball("@acme/money", "1.0.0")}
	f.nupkg = nugetPackage("Acme.Money", "1.0.0")
	return f
}

func npmTarball(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"package/package.json": fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js"}`, name, version),
		"package/index.js":     "exports.cents = (units) => units * 100;\n",
	}
	for _, n := range []string{"package/package.json", "package/index.js"} {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		_, _ = tw.Write([]byte(files[n]))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func nugetPackage(id, version string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(id + ".nuspec")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><package><metadata><id>%s</id><version>%s</version><authors>acme</authors><description>Money helpers</description></metadata></package>`, id, version)
	_ = zw.Close()
	return buf.Bytes()
}

// Integrity returns the npm integrity value of a package tarball.
func (f *Fake) Integrity(name string) string {
	p := f.npm[name]
	s := sha512.Sum512(p.tgz)
	return "sha512-" + base64.StdEncoding.EncodeToString(s[:])
}

// Requests returns the recorded requests.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Count returns how many requests had a path with the suffix.
func (f *Fake) Count(suffix string) int {
	n := 0
	for _, r := range f.Requests() {
		if strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}

// Reset forgets the recorded requests.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
}

func base(r *http.Request) string { return "http://" + r.Host }

// Handler serves /npm/ and /nuget/. A request without the credential gets 401.
func (f *Fake) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), At: time.Now()})
		f.mu.Unlock()
		if f.Credential != "" && r.Header.Get("Authorization") != f.Credential {
			w.Header().Set("WWW-Authenticate", `Basic realm="acme feed"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read only", http.StatusMethodNotAllowed)
			return
		}
		switch {
		case r.URL.Path == "/npm/" || r.URL.Path == "/npm":
			writeJSON(w, map[string]any{"db_name": "acme"})
		case strings.HasPrefix(r.URL.Path, "/npm/"):
			f.npmRoute(w, r)
		case r.URL.Path == "/nuget/v3/index.json":
			b := base(r)
			writeJSON(w, map[string]any{"version": "3.0.0", "resources": []any{
				map[string]any{"@id": b + "/nuget/v3/flatcontainer/", "@type": "PackageBaseAddress/3.0.0"},
				map[string]any{"@id": b + "/nuget/v3/registration/", "@type": "RegistrationsBaseUrl/3.6.0"},
				map[string]any{"@id": b + "/nuget/v3/query", "@type": "SearchQueryService/3.5.0"},
			}})
		case r.URL.Path == "/nuget/v3/flatcontainer/acme.money/index.json":
			writeJSON(w, map[string]any{"versions": []string{"1.0.0"}})
		case r.URL.Path == "/nuget/v3/flatcontainer/acme.money/1.0.0/acme.money.1.0.0.nupkg":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(f.nupkg)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *Fake) npmRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/npm/")
	name, file, isTarball := strings.Cut(rest, "/-/")
	name = strings.ReplaceAll(name, "%2f", "/")
	name = strings.ReplaceAll(name, "%2F", "/")
	f.mu.Lock()
	p := f.npm[name]
	f.mu.Unlock()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	short := name[strings.LastIndex(name, "/")+1:]
	if isTarball {
		if file != fmt.Sprintf("%s-%s.tgz", short, p.Version) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(p.tgz)
		return
	}
	sum := sha1.Sum(p.tgz)
	tarball := fmt.Sprintf("%s/npm/%s/-/%s-%s.tgz", base(r), name, short, p.Version)
	writeJSON(w, map[string]any{
		"name": name, "dist-tags": map[string]string{"latest": p.Version},
		"versions": map[string]any{p.Version: map[string]any{
			"name": name, "version": p.Version, "main": "index.js",
			"dist": map[string]any{"tarball": tarball, "shasum": hex.EncodeToString(sum[:]), "integrity": f.Integrity(name)},
		}},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
