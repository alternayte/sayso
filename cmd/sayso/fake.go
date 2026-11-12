package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	fakegh "github.com/alternayte/sayso/internal/fakes/github"
)

func fakeCmd() *cobra.Command {
	c := &cobra.Command{Use: "fake", Short: "Run the test fakes (demo and e2e only)"}
	var githubAddr string
	serve := &cobra.Command{Use: "serve", Short: "Serve the fake GitHub for the acme organization", RunE: func(c *cobra.Command, _ []string) error {
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
			w.WriteHeader(code)
		})
		srv := &http.Server{Addr: githubAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		log.Info("fake GitHub listening", "addr", githubAddr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}}
	serve.Flags().StringVar(&githubAddr, "github-listen", ":9001", "address of the fake GitHub")
	c.AddCommand(serve)
	return c
}
