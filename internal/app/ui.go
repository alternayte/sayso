package app

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/ui"
)

// uiHandler serves the single-page app. Unknown paths get index.html so the
// router can render them. /setup returns 404 after the App exists (SI-14).
func uiHandler(d *Deps) http.Handler {
	dist, err := fs.Sub(ui.Dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		p := path.Clean(r.URL.Path)
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/auth/") {
			http.NotFound(w, r)
			return
		}
		if p == "/setup" || strings.HasPrefix(p, "/setup/") {
			exists, err := github.Exists(r.Context(), d.Pool)
			if err != nil {
				http.Error(w, "database error", http.StatusInternalServerError)
				return
			}
			if exists {
				http.NotFound(w, r)
				return
			}
		}
		if p != "/" {
			if f, err := dist.Open(strings.TrimPrefix(p, "/")); err == nil {
				st, _ := f.Stat()
				_ = f.Close()
				if st != nil && !st.IsDir() {
					if strings.HasPrefix(p, "/assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "the UI is not built: run just ui-build", http.StatusNotFound)
			return
		}
		nonce, err := newNonce()
		if err != nil {
			http.Error(w, "nonce", http.StatusInternalServerError)
			return
		}
		page := bytes.Replace(index, []byte("<head>"), []byte(`<head><meta name="csp-nonce" content="`+nonce+`" />`), 1)
		w.Header().Set("Content-Security-Policy", PageCSP(nonce))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
}

// PageCSP is the policy of the app page. Everything comes from the same
// origin. A style element is allowed only with the nonce of this response,
// which the UI hands to the libraries that insert style elements.
func PageCSP(nonce string) string {
	return "default-src 'self'; style-src 'self' 'nonce-" + nonce + "'"
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
