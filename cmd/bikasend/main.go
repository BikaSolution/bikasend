package main

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"

	"github.com/BikaSolution/bikasend/internal/bikasend/api"
	"github.com/BikaSolution/bikasend/internal/bikasend/core"
	"github.com/BikaSolution/bikasend/internal/bikasend/frontend"
	"github.com/BikaSolution/bikasend/pkg/awscli"
	"github.com/BikaSolution/bikasend/pkg/github"
	"github.com/BikaSolution/bikasend/pkg/sign"
)

// Set at build time: -ldflags "-X main.version=v1.2.3 -X main.updateURL=https://… -X main.repo=owner/name".
var (
	version   = "dev"
	updateURL = ""
	repo      = ""
)

// releasePub is the public half of the offline release signing key (`just keygen`).
//
//go:embed release.pub
var releasePub []byte

func main() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.TempDir()
	}
	releaseKey, err := sign.ParsePublicKey(releasePub)
	if err != nil {
		panic(err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var releases core.Releases
	if repo != "" {
		gh, err := github.New(client, "https://api.github.com", repo, "")
		if err != nil {
			panic(err)
		}
		releases = gh
	}
	svc := core.NewService(core.Options{
		Version:    version,
		UpdateURL:  updateURL,
		GitHub:     releases,
		Repo:       repo,
		ReleaseKey: releaseKey,
		ConfigDir:  filepath.Join(configDir, "Bikasend"),
		AWS:        func(cli string) core.AWS { return awscli.New(cli) },
		Client:     client,
	})
	if err := wails.Run(api.Options(svc, frontend.Assets)); err != nil {
		panic(err)
	}
}
