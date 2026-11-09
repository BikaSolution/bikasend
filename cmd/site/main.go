package main

import (
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BikaSolution/bikasend/internal/site/api"
	"github.com/BikaSolution/bikasend/internal/site/core"
	"github.com/BikaSolution/bikasend/pkg/github"
	"github.com/BikaSolution/bikasend/pkg/httpserve"
)

func main() {
	host := flag.String("host", "127.0.0.1", "listen address; 0.0.0.0 inside Docker")
	port := flag.Int("port", 8091, "listen port")
	repo := flag.String("repo", "BikaSolution/bikasend", "GitHub repo that publishes the releases")
	apiURL := flag.String("github-api", "https://api.github.com", "GitHub API base URL")
	behindTunnel := flag.Bool("behind-cloudflare", false, "trust CF-Connecting-IP from private peers; set only behind the Cloudflare tunnel")
	publicURL := flag.String("public-url", "", "the site's own https origin, for canonical links and the sitemap")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Read-only token for a private repo. Comes from the environment only, never a flag (flags show in ps).
	token := os.Getenv("GITHUB_TOKEN")
	gh, err := github.New(&http.Client{Timeout: 15 * time.Second}, *apiURL, *repo, token)
	if err != nil {
		slog.Error("github client", "error", err)
		os.Exit(1)
	}
	svc := core.NewService(gh, 5*time.Minute)

	origin, err := api.ParsePublicURL(*publicURL)
	if err != nil {
		slog.Error("public url", "error", err)
		os.Exit(1)
	}
	srv := httpserve.New(*port, *host, api.Routes(svc, api.Options{TrustCloudflare: *behindTunnel, PublicURL: origin, Repo: *repo}))
	go func() {
		slog.Info("site listening", "host", *host, "port", *port, "repo", *repo, "token_set", token != "", "behind_cloudflare", *behindTunnel, "public_url", origin)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("site stopped", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	if err := srv.Close(); err != nil {
		slog.Error("shutdown", "error", err)
	}
}
