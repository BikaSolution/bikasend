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
)

// Set at build time: -ldflags "-X main.version=v1.2.3 -X main.updateURL=https://…".
var (
	version   = "dev"
	updateURL = ""
)

func main() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.TempDir()
	}
	svc := core.NewService(core.Options{
		Version:   version,
		UpdateURL: updateURL,
		ConfigDir: filepath.Join(configDir, "Bikasend"),
		AWS:       func(cli string) core.AWS { return awscli.New(cli) },
		Client:    &http.Client{Timeout: 15 * time.Second},
	})
	if err := wails.Run(api.Options(svc, frontend.Assets)); err != nil {
		panic(err)
	}
}
