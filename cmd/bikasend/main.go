package main

import (
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
)

// Set at build time: -ldflags "-X main.version=v1.2.3 -X main.updateURL=https://… -X main.repo=owner/name".
var (
	version   = "dev"
	updateURL = ""
	repo      = ""
)

func main() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.TempDir()
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
		Version:   version,
		UpdateURL: updateURL,
		GitHub:    releases,
		ConfigDir: filepath.Join(configDir, "Bikasend"),
		AWS:       func(cli string) core.AWS { return awscli.New(cli) },
		Client:    client,
	})
	if err := wails.Run(api.Options(svc, frontend.Assets)); err != nil {
		panic(err)
	}
}
