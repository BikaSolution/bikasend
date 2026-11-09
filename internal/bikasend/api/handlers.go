package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

const watchEvery = 2 * time.Second

// App methods are bound to the frontend as window.go.api.App.<Method>. Each one is a JS-callable API.
type App struct {
	ctx context.Context
	svc bikasend.Service

	watchOnce sync.Once
	mu        sync.Mutex
	notified  bool
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Open folder"})
}

func (a *App) PickFile() (string, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose a file to upload"})
	if err == nil && path != "" {
		a.svc.AllowUpload(path)
	}
	return path, err
}

func (a *App) Open(root string) ([]bikasend.Spec, error) {
	specs, err := a.svc.Open(root)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.notified = false
	a.mu.Unlock()
	a.watchOnce.Do(func() { go a.watch() })
	return specs, nil
}

// watch tells the UI once when a spec file or .bika.json changes; Open re-arms it.
func (a *App) watch() {
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			a.mu.Lock()
			notified := a.notified
			a.mu.Unlock()
			if !notified && a.svc.Changed() {
				a.mu.Lock()
				a.notified = true
				a.mu.Unlock()
				runtime.EventsEmit(a.ctx, "workspace:changed")
			}
		}
	}
}

func (a *App) Workspace() bikasend.Workspace {
	return a.svc.Workspace()
}

func (a *App) TrustFolder() (bikasend.Workspace, error) {
	return a.svc.TrustFolder()
}

func (a *App) SelectEnvironment(name string) (bikasend.Workspace, error) {
	return a.svc.SelectEnvironment(name)
}

func (a *App) CreateEnvironment(name string) (bikasend.Workspace, error) {
	return a.svc.CreateEnvironment(name)
}

func (a *App) DeleteEnvironment(name string) (bikasend.Workspace, error) {
	return a.svc.DeleteEnvironment(name)
}

func (a *App) SetHosts(hosts []string) (bikasend.Workspace, error) {
	return a.svc.SetHosts(hosts)
}

func (a *App) SetVariable(scope, name string, in bikasend.VariableInput) (bikasend.Workspace, error) {
	return a.svc.SetVariable(scope, name, in)
}

func (a *App) DeleteVariable(scope, name string) (bikasend.Workspace, error) {
	return a.svc.DeleteVariable(scope, name)
}

func (a *App) SetPrompt(name, value string) (bikasend.Workspace, error) {
	return a.svc.SetPrompt(name, value)
}

func (a *App) Reveal(name string) (string, error) {
	return a.svc.Reveal(name)
}

// CopyVariable puts a value on the clipboard without sending it to the webview.
func (a *App) CopyVariable(name string) error {
	v, err := a.svc.Reveal(name)
	if err != nil {
		return err
	}
	return runtime.ClipboardSetText(a.ctx, v)
}

func (a *App) CopyText(text string) error {
	return runtime.ClipboardSetText(a.ctx, text)
}

func (a *App) LoadSecrets() (bikasend.Workspace, error) {
	return a.svc.LoadSecrets(a.ctx)
}

func (a *App) SignIn() (bikasend.Workspace, error) {
	return a.svc.SignIn(a.ctx)
}

func (a *App) ForgetSecrets() bikasend.Workspace {
	return a.svc.ForgetSecrets()
}

func (a *App) Preview(r bikasend.Request) bikasend.Preview {
	return a.svc.Preview(r)
}

func (a *App) Send(r bikasend.Request) (bikasend.Response, error) {
	return a.svc.Send(a.ctx, r)
}

func (a *App) Cancel(id string) {
	a.svc.Cancel(id)
}

func (a *App) ClearCookies() {
	a.svc.ClearCookies()
}

func (a *App) History() []bikasend.HistoryEntry {
	return a.svc.History()
}

func (a *App) ClearHistory() error {
	return a.svc.ClearHistory()
}

func (a *App) SaveSettings(s bikasend.Settings) (bikasend.Workspace, error) {
	return a.svc.SaveSettings(s)
}

func (a *App) Curl(r bikasend.Request) (string, error) {
	return a.svc.Curl(r)
}

func (a *App) ImportCurl(cmd string) (bikasend.Request, error) {
	return a.svc.ImportCurl(cmd)
}

func (a *App) CheckUpdate() (bikasend.Update, error) {
	return a.svc.CheckUpdate(a.ctx)
}

func (a *App) DownloadUpdate() (string, error) {
	return a.svc.DownloadUpdate(a.ctx)
}

// SaveResponse writes a response body to a file the user picks. isBase64 marks a binary body.
func (a *App) SaveResponse(name, body string, isBase64 bool) error {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "Save response", DefaultFilename: name})
	if err != nil || path == "" {
		return err
	}
	data := []byte(body)
	if isBase64 {
		if data, err = base64.StdEncoding.DecodeString(body); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o600)
}

// OpenSecretSource opens the AWS console page of a variable. The URL is built in Go, never taken from the UI.
func (a *App) OpenSecretSource(name string) error {
	u, err := a.svc.SecretConsoleURL(name)
	if err != nil {
		return err
	}
	runtime.BrowserOpenURL(a.ctx, u)
	return nil
}

// OpenURL opens an http(s) link in the default browser.
func (a *App) OpenURL(link string) error {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("only http and https links can be opened")
	}
	runtime.BrowserOpenURL(a.ctx, u.String())
	return nil
}
