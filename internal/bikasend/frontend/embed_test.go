package frontend

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- the WebSocket handshake (RFC 6455) defines Sec-WebSocket-Accept with SHA-1.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BikaSolution/bikasend/internal/bikasend/core"
	"github.com/BikaSolution/bikasend/pkg/sigv4"
)

// bridge stands in for the Wails runtime: window.go.api.App.X(...) becomes POST /__call/X.
const bridge = `window.go = { api: { App: new Proxy({}, { get: (_, method) => async (...args) => {
  const r = await fetch('/__call/' + method, { method: 'POST', body: JSON.stringify(args) });
  const out = await r.json();
  if (out.error) throw out.error;
  return out.result;
} }) } };`

const uiSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "UI test API", "version": "1"},
  "servers": [{"url": "BACKEND"}],
  "components": {"securitySchemes": {"key": {"type": "apiKey", "in": "header", "name": "X-API-Key"}}},
  "security": [{"key": []}],
  "paths": {"/items": {"get": {"summary": "List items", "tags": ["items"], "responses": {"200": {"description": "ok"}}}}}
}`

type noAWS struct{}

func (noAWS) Path() string { return "" }
func (noAWS) Identity(context.Context, string) (string, error) {
	return "", errors.New("no aws in tests")
}
func (noAWS) SignIn(context.Context, string) error { return errors.New("no aws in tests") }
func (noAWS) Secrets(context.Context, string, string, []string) (map[string]string, error) {
	return nil, errors.New("no aws in tests")
}
func (noAWS) Parameters(context.Context, string, string, []string) (map[string]string, error) {
	return nil, errors.New("no aws in tests")
}
func (noAWS) Credentials(context.Context, string) (sigv4.Credentials, error) {
	return sigv4.Credentials{}, errors.New("no aws in tests")
}

func chromePath() string {
	if p := os.Getenv("BIKASEND_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "headless-shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "darwin" {
		if p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"; fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// serveCall runs one bound method on the real core service, the way Wails would.
func serveCall(svc *core.Service) http.HandlerFunc {
	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()
	return func(w http.ResponseWriter, r *http.Request) {
		reply := func(result any, err error) {
			out := map[string]any{"result": result}
			if err != nil {
				out["error"] = err.Error()
			}
			_ = json.NewEncoder(w).Encode(out)
		}
		m := reflect.ValueOf(svc).MethodByName(strings.TrimPrefix(r.URL.Path, "/__call/"))
		if !m.IsValid() {
			reply(nil, errors.New("not available in UI tests"))
			return
		}
		var raw []json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			reply(nil, err)
			return
		}
		args := []reflect.Value{}
		for t := range m.Type().Ins() {
			if t == ctxType {
				args = append(args, reflect.ValueOf(context.Background()))
				continue
			}
			v := reflect.New(t)
			if len(raw) > 0 {
				if err := json.Unmarshal(raw[0], v.Interface()); err != nil {
					reply(nil, err)
					return
				}
				raw = raw[1:]
			}
			args = append(args, v.Elem())
		}
		var result any
		var err error
		for _, o := range m.Call(args) {
			if o.Type().Implements(errType) {
				if !o.IsNil() {
					err, _ = reflect.TypeAssert[error](o)
				}
				continue
			}
			result = o.Interface()
		}
		reply(result, err)
	}
}

// devtools is a minimal Chrome DevTools Protocol client on the standard library: one page, JSON
// over a WebSocket. It keeps the UI test free of third-party modules and browser downloads.
type devtools struct {
	cmd     *exec.Cmd
	conn    net.Conn
	br      *bufio.Reader
	mu      sync.Mutex
	nextID  int
	pending map[int]chan cdpReply
	errs    []string
}

type cdpReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// startChrome runs headless Chrome with a fresh profile and its background networking off.
func startChrome(t *testing.T, chrome string) *devtools {
	t.Helper()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0", // #nosec G204 -- the test's own browser path.
		"--user-data-dir="+t.TempDir(), "--no-first-run", "--no-default-browser-check", "--disable-extensions",
		"--disable-background-networking", "--disable-sync", "--disable-component-update", "--disable-default-apps",
		"--window-size=1280,900", "about:blank")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if _, ws, ok := strings.Cut(sc.Text(), "DevTools listening on "); ok {
				found <- ws
			}
		}
	}()
	var browserWS string
	select {
	case browserWS = <-found:
	case <-time.After(20 * time.Second):
		t.Fatal("Chrome did not start its DevTools server")
	}
	u, err := url.Parse(browserWS)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + u.Host + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	var targets []struct {
		Type string `json:"type"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	err = json.NewDecoder(resp.Body).Decode(&targets)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.Type == "page" {
			d := &devtools{cmd: cmd, pending: map[int]chan cdpReply{}}
			if err := d.dial(tg.WS); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.conn.Close() })
			go d.read()
			return d
		}
	}
	t.Fatal("Chrome has no page target")
	return nil
}

// dial opens the WebSocket (RFC 6455) to a page target.
func (d *devtools) dial(ws string) error {
	u, err := url.Parse(ws)
	if err != nil {
		return err
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host, key)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return err
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) // #nosec G401 -- fixed by RFC 6455.
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return fmt.Errorf("websocket handshake: %s", resp.Status)
	}
	d.conn, d.br = conn, br
	return nil
}

// write sends one masked text frame, as a WebSocket client must.
func (d *devtools) write(msg []byte) error {
	hdr := []byte{0x81}
	switch n := len(msg); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xffff:
		hdr = append(hdr, 0x80|126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	body := make([]byte, len(msg))
	for i := range msg {
		body[i] = msg[i] ^ mask[i%4]
	}
	_, err := d.conn.Write(append(append(hdr, mask...), body...))
	return err
}

// read routes replies to their callers and records page exceptions until the socket closes.
func (d *devtools) read() {
	var msg []byte
	for {
		head := make([]byte, 2)
		if _, err := io.ReadFull(d.br, head); err != nil {
			return
		}
		n := uint64(head[1] & 0x7f)
		switch n {
		case 126:
			b := make([]byte, 2)
			if _, err := io.ReadFull(d.br, b); err != nil {
				return
			}
			n = uint64(binary.BigEndian.Uint16(b))
		case 127:
			b := make([]byte, 8)
			if _, err := io.ReadFull(d.br, b); err != nil {
				return
			}
			n = binary.BigEndian.Uint64(b)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(d.br, payload); err != nil {
			return
		}
		switch op := head[0] & 0x0f; {
		case op == 0x8:
			return // close
		case op >= 0x9:
			continue // ping or pong: no message data
		}
		msg = append(msg, payload...)
		if head[0]&0x80 == 0 {
			continue // more fragments follow
		}
		var m struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			cdpReply
		}
		if json.Unmarshal(msg, &m) == nil {
			d.mu.Lock()
			if ch, ok := d.pending[m.ID]; ok && m.ID != 0 {
				delete(d.pending, m.ID)
				ch <- m.cdpReply
			} else if m.Method == "Runtime.exceptionThrown" {
				d.errs = append(d.errs, string(m.Params))
			}
			d.mu.Unlock()
		}
		msg = nil
	}
}

func (d *devtools) call(method string, params any) (json.RawMessage, error) {
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	ch := make(chan cdpReply, 1)
	d.pending[id] = ch
	d.mu.Unlock()
	msg, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	err = d.write(msg)
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, r.Error.Message)
		}
		return r.Result, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("%s: no reply", method)
	}
}

// eval runs JS in the page and decodes its value into out (nil to ignore), awaiting promises.
func (d *devtools) eval(expr string, out any) error {
	raw, err := d.call("Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		return fmt.Errorf("%s: %s", expr, r.Exception.Text)
	}
	if out == nil || len(r.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result.Value, out)
}

// TestUI drives the real frontend against the real core in headless Chrome: open, edit, send.
func TestUI(t *testing.T) {
	chrome := chromePath()
	if chrome == "" || testing.Short() {
		t.Skip("no Chrome or Chromium found; set BIKASEND_CHROME to run the UI test")
	}

	var mu sync.Mutex
	var gotKey string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("X-API-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo", "yes")
		_, _ = w.Write([]byte(`[{"id": 1}]`))
	}))
	defer backend.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "openapi.json"), []byte(strings.ReplaceAll(uiSpec, "BACKEND", backend.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := core.NewService(core.Options{Version: "dev", ConfigDir: t.TempDir(), AWS: func(string) core.AWS { return noAWS{} }, Client: http.DefaultClient})

	dist, err := fs.Sub(Assets, "dist")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(dist))
	mux.Handle("/__call/", serveCall(svc))
	app := httptest.NewServer(mux)
	defer app.Close()

	d := startChrome(t, chrome)
	must := func(step string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	call := func(step, method string, params any) {
		t.Helper()
		_, err := d.call(method, params)
		must(step, err)
	}
	// waitFor polls a JS expression until it is true.
	waitFor := func(step, expr string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var ok bool
			if err := d.eval(expr, &ok); err == nil && ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: timed out waiting for %s", step, expr)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// clickText clicks the button under scope whose text is exactly text.
	clickText := func(step, scope, text string) {
		t.Helper()
		var ok bool
		must(step, d.eval(fmt.Sprintf(`(() => { const b = [...document.querySelectorAll(%q)].find((e) => e.textContent.trim() === %q); if (b) b.click(); return !!b; })()`, scope+" button", text), &ok))
		if !ok {
			t.Fatalf("%s: no %q button in %s", step, text, scope)
		}
	}
	// typeInto focuses a field and types text the way a keyboard would, firing input events.
	typeInto := func(step, selector, text string) {
		t.Helper()
		must(step, d.eval(fmt.Sprintf(`document.querySelector(%q).focus()`, selector), nil))
		call(step, "Input.insertText", map[string]any{"text": text})
	}

	call("watch for page errors", "Runtime.enable", nil)
	call("install the Go bridge", "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": bridge})
	call("load the app", "Page.navigate", map[string]any{"url": app.URL})
	waitFor("the app starts", `!!document.querySelector('#open') && typeof openFolder === 'function'`)
	rootJSON, _ := json.Marshal(root)
	must("open the folder", d.eval(fmt.Sprintf("openFolder(%s)", rootJSON), nil))
	waitFor("the operation is listed", `!!document.querySelector('button.op')`)
	must("open the operation", d.eval(`document.querySelector('button.op').click()`, nil))

	var url string
	must("read the URL", d.eval(`document.querySelector('#request input.url').value`, &url))
	if url != "{{baseUrl}}/items" {
		t.Errorf("URL = %q, want {{baseUrl}}/items", url)
	}

	clickText("show the request headers", "#request .subtabs", "Headers")
	waitFor("the API key header row", `[...document.querySelectorAll('.headers-edit input[aria-label="Header name"]')].some((i) => i.value === 'X-API-Key')`)

	// The variable is missing: hover its chip, add it from the card as a session secret.
	must("hover the chip", d.eval(`document.querySelector('.chip.missing').dispatchEvent(new MouseEvent('mouseenter'))`, nil))
	waitFor("the card opens", `!!document.querySelector('.var-card.missing')`)
	clickText("add from the card", ".var-card", "Add")
	waitFor("the secret box is ticked for a key", `document.querySelector('.vc-form .vc-check input').checked && document.querySelector('.vc-form input[type=password]') !== null`)
	typeInto("type the value", ".vc-form input[type=password]", "k-123")
	must("save", d.eval(`document.querySelector('.vc-form').requestSubmit()`, nil))
	waitFor("the card closes after saving", `!document.querySelector('.var-card')`)
	if v := findVar(svc, "X-API-Key"); !v.Secret || !v.Resolved || v.Value != "" {
		t.Errorf("saved variable = %+v, want a resolved session secret with no value in the view", v)
	}

	// Hover the highlighted {{X-API-Key}} inside the header value field with a real mouse move.
	var at []float64
	must("find the highlight", d.eval(`(() => { const r = document.querySelector('.headers-edit .hl mark').getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; })()`, &at))
	call("move the mouse onto it", "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": at[0], "y": at[1]})
	waitFor("the card shows a masked secret", `(() => { const c = document.querySelector('.var-card.secret'); return !!c && c.querySelector('.vc-value.masked') !== null && !c.textContent.includes('k-123'); })()`)
	clickText("edit from the card", ".var-card", "Edit")
	waitFor("the edit form has no prefilled secret", `(() => { const i = document.querySelector('.vc-form input[type=password]'); return !!i && i.value === ''; })()`)
	call("cancel with Escape", "Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27})
	waitFor("the card closes", `!document.querySelector('.var-card')`)

	must("send", d.eval(`document.querySelector('#sendBtn').click()`, nil))
	waitFor("the response arrives", `document.querySelector('#response .status') !== null && document.querySelector('#response').textContent.includes('200')`)
	mu.Lock()
	if gotKey != "k-123" {
		t.Errorf("the backend got X-API-Key %q, want the saved secret", gotKey)
	}
	mu.Unlock()
	clickText("show the response headers", "#response .subtabs", "Headers")
	waitFor("the response headers table", `[...document.querySelectorAll('.headers-view th')].some((th) => th.textContent === 'X-Echo')`)

	// Environments: create one from the drawer, then give it a host.
	must("open the variables drawer", d.eval(`document.querySelector('#varsBtn').click()`, nil))
	waitFor("the environment panel", `!!document.querySelector('.env-panel')`)
	clickText("new environment", ".env-panel", "+ New")
	waitFor("the name field", `!!document.querySelector('#dialog input[aria-label="Environment name"]')`)
	typeInto("name it", `#dialog input[aria-label="Environment name"]`, "staging")
	must("create it", d.eval(`document.querySelector('#dialog form').requestSubmit()`, nil))
	waitFor("staging is selected", `(() => { const s = document.querySelector('.env-panel select'); return !!s && s.value === 'staging'; })()`)
	typeInto("type a host", `.env-panel input[aria-label="Add a host"]`, "api.example.com")
	clickText("save the host", ".env-panel", "Add host")
	waitFor("the host chip", `[...document.querySelectorAll('.host-chip')].some((c) => c.textContent.includes('api.example.com'))`)
	data, err := os.ReadFile(filepath.Join(root, ".bika.json"))
	if err != nil || !strings.Contains(string(data), `"staging"`) || !strings.Contains(string(data), `"api.example.com"`) {
		t.Errorf(".bika.json after the environment editor: %s, %v", data, err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.errs) > 0 {
		t.Errorf("JavaScript exceptions: %v", d.errs)
	}
}

func findVar(svc *core.Service, name string) (v struct {
	Secret, Resolved bool
	Value            string
}) {
	for _, x := range svc.Workspace().Variables {
		if x.Name == name && x.Active {
			v.Secret, v.Resolved, v.Value = x.Secret, x.Resolved, x.Value
		}
	}
	return v
}
