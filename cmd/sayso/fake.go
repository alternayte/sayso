package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
)

// delivered maps the status of a webhook delivery to the answer of a
// control route: 0 means the API was not reachable.
func delivered(code int) int {
	if code == 0 {
		return http.StatusBadGateway
	}
	return code
}

func writeFake(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fakeCmd() *cobra.Command {
	c := &cobra.Command{Use: "fake", Short: "Run the test fakes (demo and e2e only)"}
	var githubAddr, modelAddr string
	serve := &cobra.Command{Use: "serve", Short: "Serve the fake GitHub for the acme organization and the fake model endpoint", RunE: func(c *cobra.Command, _ []string) error {
		log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		gh := fakegh.New()
		mux := http.NewServeMux()
		mux.Handle("/", gh.Handler())
		mux.HandleFunc("POST /_fake/signin-as/{login}", func(w http.ResponseWriter, r *http.Request) {
			gh.SignInAs(r.PathValue("login"))
			w.WriteHeader(http.StatusNoContent)
		})
		// Install the App on repositories: ?repos=acme/payments,acme/web
		mux.HandleFunc("POST /_fake/install", func(w http.ResponseWriter, r *http.Request) {
			_, code := gh.Install(strings.Split(r.URL.Query().Get("repos"), ",")...)
			w.WriteHeader(delivered(code))
		})
		// Pull requests for UI flows. The body is {"files": {...}, "author": "...", "title": "..."}.
		type pullBody struct {
			Files  map[string]string `json:"files"`
			Author string            `json:"author"`
			Title  string            `json:"title"`
			Body   string            `json:"body"`
			Merged bool              `json:"merged"`
		}
		read := func(r *http.Request) (pullBody, string, int) {
			var b pullBody
			_ = json.NewDecoder(r.Body).Decode(&b)
			n, _ := strconv.Atoi(r.URL.Query().Get("number"))
			return b, r.URL.Query().Get("repo"), n
		}
		mux.HandleFunc("POST /_fake/main", func(w http.ResponseWriter, r *http.Request) {
			b, repo, _ := read(r)
			writeFake(w, map[string]string{"sha": gh.CommitMain(repo, b.Files)})
		})
		mux.HandleFunc("POST /_fake/pulls", func(w http.ResponseWriter, r *http.Request) {
			b, repo, _ := read(r)
			if b.Author == "" {
				b.Author = "dev-alice"
			}
			p := gh.OpenPull(repo, fakegh.PullOptions{Author: b.Author, Title: b.Title, Body: b.Body, Files: b.Files})
			_, code := gh.PullEvent(repo, p.Number, "opened", nil)
			w.Header().Set("X-Delivery-Status", strconv.Itoa(code))
			writeFake(w, map[string]any{"number": p.Number, "head_sha": p.HeadSHA})
		})
		mux.HandleFunc("POST /_fake/push", func(w http.ResponseWriter, r *http.Request) {
			b, repo, n := read(r)
			_, code := gh.PushFiles(repo, n, false, b.Files)
			w.Header().Set("X-Delivery-Status", strconv.Itoa(code))
			writeFake(w, map[string]any{"head_sha": gh.Repo(repo).Pulls[n].HeadSHA})
		})
		mux.HandleFunc("POST /_fake/close", func(w http.ResponseWriter, r *http.Request) {
			b, repo, n := read(r)
			gh.SetPull(repo, n, func(p *fakegh.Pull) { p.State, p.Merged = "closed", b.Merged })
			_, code := gh.PullEvent(repo, n, "closed", nil)
			w.WriteHeader(delivered(code))
		})
		// Published state, for assertions: check runs, summary and review comments.
		mux.HandleFunc("GET /_fake/state", func(w http.ResponseWriter, r *http.Request) {
			repo := r.URL.Query().Get("repo")
			n, _ := strconv.Atoi(r.URL.Query().Get("number"))
			writeFake(w, map[string]any{"checks": gh.CheckRuns(), "issue_comments": gh.IssueComments(repo, n), "review_comments": gh.ReviewComments(repo), "reviews": gh.Reviews()})
		})
		srv := &http.Server{Addr: githubAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		msrv := &http.Server{Addr: modelAddr, Handler: fakemodel.New().Handler(), ReadHeaderTimeout: 10 * time.Second}
		ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
			_ = msrv.Shutdown(sctx)
		}()
		errc := make(chan error, 2)
		for _, s := range []*http.Server{srv, msrv} {
			go func() {
				if err := s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
					errc <- err
					return
				}
				errc <- nil
			}()
		}
		log.Info("fakes listening", "github", githubAddr, "model", modelAddr)
		if err := <-errc; err != nil {
			stop()
			return err
		}
		return <-errc
	}}
	serve.Flags().StringVar(&githubAddr, "github-listen", ":9001", "address of the fake GitHub")
	serve.Flags().StringVar(&modelAddr, "model-listen", ":9002", "address of the fake model endpoint")
	c.AddCommand(serve)
	return c
}
