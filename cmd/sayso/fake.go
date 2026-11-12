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
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
)

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
			w.WriteHeader(code)
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
