package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
	"github.com/BikaSolution/bikasend/pkg/sigv4"
)

// AWS is what the service needs from the aws CLI wrapper (pkg/awscli).
type AWS interface {
	Path() string
	Identity(ctx context.Context, profile string) (string, error)
	SignIn(ctx context.Context, profile string) error
	Secrets(ctx context.Context, profile, region string, ids []string) (map[string]string, error)
	Parameters(ctx context.Context, profile, region string, names []string) (map[string]string, error)
	Credentials(ctx context.Context, profile string) (sigv4.Credentials, error)
}

var _ bikasend.Service = (*Service)(nil)

type Options struct {
	Version   string
	UpdateURL string
	ConfigDir string // per-user folder for globals.json, settings.json, history.json
	AWS       func(cliPath string) AWS
	Client    *http.Client // for token and update calls
}

type sessionValue struct {
	value  string
	secret bool
}

type oauthToken struct {
	value   string
	expires time.Time
}

type stored struct {
	Settings bikasend.Settings `json:"settings"`
	Recent   []string          `json:"recent"`
	LastEnv  map[string]string `json:"lastEnv"`
	Trusted  map[string]string `json:"trusted"` // folder → sha256 of its bikasend.json
}

type Service struct {
	opts Options

	mu       sync.Mutex
	root     *os.Root
	rootPath string
	specs    []bikasend.Spec
	docs     *docs
	ops      map[string]opDetail
	snapshot map[string]time.Time

	folder     configFile
	folderErr  string
	folderHash string
	global     configFile
	globalErr  string
	env        string

	session map[string]sessionValue
	prompts map[string]string
	secrets map[string]string // "aws:<id>" or "ssm:<name>" → raw value
	aws     bikasend.AWSStatus
	awsCLI  AWS
	creds   map[string]sigv4.Credentials
	tokens  map[string]oauthToken
	clients map[string]*http.Client
	jars    map[string]http.CookieJar

	inflight map[string]context.CancelFunc
	uploads  map[string]bool
	history  []bikasend.HistoryEntry
	state    stored
}

func NewService(opts Options) *Service {
	s := &Service{
		opts:     opts,
		session:  map[string]sessionValue{},
		prompts:  map[string]string{},
		secrets:  map[string]string{},
		creds:    map[string]sigv4.Credentials{},
		tokens:   map[string]oauthToken{},
		clients:  map[string]*http.Client{},
		jars:     map[string]http.CookieJar{},
		inflight: map[string]context.CancelFunc{},
		uploads:  map[string]bool{},
		history:  []bikasend.HistoryEntry{},
		aws:      bikasend.AWSStatus{State: "none"},
		state: stored{
			Settings: bikasend.Settings{Theme: "system", FontSize: 13, CheckUpdates: true},
			Recent:   []string{},
			LastEnv:  map[string]string{},
			Trusted:  map[string]string{},
		},
	}
	s.loadState()
	s.awsCLI = opts.AWS(s.state.Settings.AWSCLI)
	s.loadGlobal()
	s.loadHistory()
	return s
}

// Open scans root for specs and reads its bikasend.json.
func (s *Service) Open(root string) ([]bikasend.Spec, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, errors.Join(bikasend.ErrScan, err)
	}
	specs, d, ops, err := scanSpecs(r.FS())
	if err != nil {
		_ = r.Close()
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != nil {
		if s.rootPath != root {
			s.resetFolderState()
		}
		_ = s.root.Close()
	}
	s.root, s.rootPath, s.specs, s.docs, s.ops = r, root, specs, d, ops
	s.loadFolder()
	s.loadGlobal()
	s.snapshot = s.fingerprint()
	s.state.Recent = slices.DeleteFunc(s.state.Recent, func(p string) bool { return p == root })
	s.state.Recent = append([]string{root}, s.state.Recent...)
	if len(s.state.Recent) > 10 {
		s.state.Recent = s.state.Recent[:10]
	}
	if env, ok := s.state.LastEnv[root]; ok && s.envConfig(env) != nil {
		s.env = env
	} else if s.envConfig(s.env) == nil {
		s.env = s.firstEnv()
	}
	s.saveState()
	return specs, nil
}

func (s *Service) resetFolderState() {
	s.env = ""
	s.prompts = map[string]string{}
	s.secrets = map[string]string{}
	s.tokens = map[string]oauthToken{}
	s.clients = map[string]*http.Client{}
	s.jars = map[string]http.CookieJar{}
	s.aws = bikasend.AWSStatus{State: "none"}
}

func (s *Service) loadFolder() {
	s.folder, s.folderErr, s.folderHash = configFile{}, "", ""
	data, err := readCapped(s.root.FS(), configName, maxConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		s.folderErr = err.Error()
		return
	}
	sum := sha256.Sum256(data)
	s.folderHash = hex.EncodeToString(sum[:])
	c, err := parseConfig(data, false)
	if err != nil {
		s.folderErr = err.Error()
		return
	}
	s.folder = c
	s.clients = map[string]*http.Client{}
}

func (s *Service) loadGlobal() {
	s.global, s.globalErr = configFile{}, ""
	data, err := os.ReadFile(filepath.Join(s.opts.ConfigDir, "globals.json"))
	if err != nil {
		return
	}
	c, err := parseConfig(data, true)
	if err != nil {
		s.globalErr = err.Error()
		return
	}
	s.global = c
}

// trusted reports whether the folder's bikasend.json may be used: the user trusted this exact content.
func (s *Service) trusted() bool {
	return s.folderHash != "" && s.state.Trusted[s.rootPath] == s.folderHash
}

func (s *Service) TrustFolder() (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return bikasend.Workspace{}, bikasend.ErrNoFolder
	}
	if s.folderHash != "" {
		s.state.Trusted[s.rootPath] = s.folderHash
		s.clients = map[string]*http.Client{}
		if s.envConfig(s.env) == nil {
			s.env = s.firstEnv()
		}
		s.saveState()
	}
	return s.workspace(), nil
}

// fingerprint records modification times of the spec files and bikasend.json.
func (s *Service) fingerprint() map[string]time.Time {
	out := map[string]time.Time{}
	if s.root == nil {
		return out
	}
	names := []string{configName}
	for _, sp := range s.specs {
		names = append(names, sp.Path)
	}
	for name := range s.docs.cache {
		names = append(names, name)
	}
	for _, n := range names {
		if info, err := s.root.Stat(n); err == nil {
			out[n] = info.ModTime()
		}
	}
	return out
}

// Changed reports whether a spec file or bikasend.json changed since the last Open.
// New spec files show up on the next manual rescan; only known files are watched.
func (s *Service) Changed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return false
	}
	now := s.fingerprint()
	if len(now) != len(s.snapshot) {
		return true
	}
	for k, t := range now {
		if !s.snapshot[k].Equal(t) {
			return true
		}
	}
	return false
}

func (s *Service) Workspace() bikasend.Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace()
}

func (s *Service) workspace() bikasend.Workspace {
	w := bikasend.Workspace{
		Root:         s.rootPath,
		Environments: []bikasend.EnvironmentView{},
		Environment:  s.env,
		AWS:          s.aws,
		Settings:     s.state.Settings,
		Recent:       slices.Clone(s.state.Recent),
		Version:      s.opts.Version,
		ConfigError:  s.folderErr,
		Trusted:      s.trusted(),
		Prompts:      []string{},
	}
	w.AWS.CLI = s.awsCLI.Path()
	if s.globalErr != "" {
		w.ConfigError = strings.TrimSpace(w.ConfigError + " globals.json: " + s.globalErr)
	}
	if s.folderHash != "" {
		w.ConfigFile = configName
	}
	for _, name := range s.envNames() {
		e := s.envConfig(name)
		v := bikasend.EnvironmentView{
			Name:       name,
			Hosts:      slices.Clone(e.Hosts),
			Production: strings.HasPrefix(strings.ToLower(name), "prod"),
			OAuth2:     e.OAuth2 != nil,
			SigV4:      e.SigV4 != nil,
		}
		if e.AWS != nil {
			v.AWSProfile, v.AWSRegion = e.AWS.Profile, e.AWS.Region
		}
		w.Environments = append(w.Environments, v)
	}
	w.Variables = s.variableViews()
	for _, v := range w.Variables {
		if v.Active && v.Kind == bikasend.KindPrompt && !v.Resolved {
			w.Prompts = append(w.Prompts, v.Name)
		}
	}
	return w
}

func (s *Service) envNames() []string {
	if !s.trusted() {
		return []string{}
	}
	names := make([]string, 0, len(s.folder.Environments))
	for n := range s.folder.Environments {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (s *Service) firstEnv() string {
	names := s.envNames()
	if slices.Contains(names, "local") {
		return "local"
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func (s *Service) envConfig(name string) *environment {
	if name == "" || !s.trusted() {
		return nil
	}
	e, ok := s.folder.Environments[name]
	if !ok {
		return nil
	}
	return &e
}

func (s *Service) SelectEnvironment(name string) (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "" && s.envConfig(name) == nil {
		return s.workspace(), fmt.Errorf("%w: %q", bikasend.ErrUnknownEnv, name)
	}
	if name != s.env {
		s.secrets = map[string]string{}
		s.aws = bikasend.AWSStatus{State: "none"}
	}
	s.env = name
	if s.rootPath != "" {
		s.state.LastEnv[s.rootPath] = name
		s.saveState()
	}
	return s.workspace(), nil
}

// definitions returns the variable definitions of each file scope, narrowest first.
func (s *Service) definitions() []struct {
	scope string
	vars  map[string]varValue
} {
	var out []struct {
		scope string
		vars  map[string]varValue
	}
	if e := s.envConfig(s.env); e != nil {
		out = append(out, struct {
			scope string
			vars  map[string]varValue
		}{bikasend.ScopeEnvironment, e.Variables})
	}
	if s.trusted() {
		out = append(out, struct {
			scope string
			vars  map[string]varValue
		}{bikasend.ScopeFolder, s.folder.Variables})
	}
	out = append(out, struct {
		scope string
		vars  map[string]varValue
	}{bikasend.ScopeGlobal, s.global.Variables})
	return out
}

// lookup resolves one name: session, environment, folder, global, then the spec server for baseUrl.
func (s *Service) lookup(server string) func(string) binding {
	return func(name string) binding {
		if v, ok := s.session[name]; ok {
			return binding{value: v.value, secret: v.secret, scope: bikasend.ScopeSession, found: true}
		}
		for _, d := range s.definitions() {
			if v, ok := d.vars[name]; ok {
				b := s.valueOf(name, v)
				b.scope = d.scope
				return b
			}
		}
		if name == "baseUrl" && server != "" {
			return binding{value: server, scope: bikasend.ScopeSpec, found: true}
		}
		return binding{reason: "is not defined in any scope"}
	}
}

func (s *Service) valueOf(name string, v varValue) binding {
	switch v.kind() {
	case bikasend.KindText:
		return binding{value: *v.Text, secret: false, found: true}
	case bikasend.KindPrompt:
		if p, ok := s.prompts[name]; ok {
			return binding{value: p, secret: true, found: true}
		}
		return binding{reason: "needs a value for this session"}
	case bikasend.KindEnv:
		if val, ok := envValue(v.Env); ok {
			return binding{value: val, secret: true, found: true}
		}
		return binding{reason: "reads $" + v.Env + ", which is not set"}
	case bikasend.KindSSM:
		if raw, ok := s.secrets["ssm:"+v.SSM]; ok {
			return binding{value: raw, secret: true, found: true}
		}
		return binding{reason: "needs AWS parameters loaded"}
	}
	raw, ok := s.secrets["aws:"+v.AWS]
	if !ok {
		return binding{reason: "needs AWS secrets loaded"}
	}
	if v.Key == "" {
		return binding{value: raw, secret: true, found: true}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return binding{reason: fmt.Sprintf("secret %q is not JSON, so key %q cannot be read", v.AWS, v.Key)}
	}
	val, ok := m[v.Key]
	if !ok {
		return binding{reason: fmt.Sprintf("secret %q has no key %q", v.AWS, v.Key)}
	}
	return binding{value: str(val), secret: true, found: true}
}

func (s *Service) variableViews() []bikasend.VariableView {
	views := []bikasend.VariableView{}
	active := map[string]bool{}
	add := func(view bikasend.VariableView) {
		if !active[view.Name] {
			view.Active = true
			active[view.Name] = true
		}
		views = append(views, view)
	}
	names := make([]string, 0, len(s.session))
	for n := range s.session {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		v := s.session[n]
		view := bikasend.VariableView{Name: n, Scope: bikasend.ScopeSession, Kind: bikasend.KindText, Secret: v.secret, Resolved: true}
		if v.secret {
			view.Kind = bikasend.KindSecret
		} else {
			view.Value = v.value
		}
		add(view)
	}
	for _, d := range s.definitions() {
		keys := make([]string, 0, len(d.vars))
		for n := range d.vars {
			keys = append(keys, n)
		}
		slices.Sort(keys)
		for _, n := range keys {
			v := d.vars[n]
			b := s.valueOf(n, v)
			view := bikasend.VariableView{Name: n, Scope: d.scope, Kind: v.kind(), Secret: isSecretKind(v.kind()), Ref: v.ref(), Resolved: b.found, Error: b.reason}
			if view.Kind == bikasend.KindPrompt {
				view.Ref = v.Prompt
			}
			if !view.Secret {
				view.Value = b.value
			}
			add(view)
		}
	}
	for i := range views {
		if !views[i].Active {
			for _, w := range views {
				if w.Active && w.Name == views[i].Name {
					views[i].Shadows = w.Scope
				}
			}
		}
	}
	return views
}

func (s *Service) SetVariable(scope, name string, in bikasend.VariableInput) (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !namePattern.MatchString(name) {
		return s.workspace(), fmt.Errorf("%w: name %q must be letters, digits, _ . -", bikasend.ErrInvalidVariable, name)
	}
	if scope == bikasend.ScopeSession {
		switch in.Kind {
		case bikasend.KindText, bikasend.KindSecret:
			s.session[name] = sessionValue{value: in.Value, secret: in.Kind == bikasend.KindSecret || secretName(name)}
			return s.workspace(), nil
		}
		return s.workspace(), fmt.Errorf("%w: session values are text or secret", bikasend.ErrInvalidVariable)
	}

	var v varValue
	switch in.Kind {
	case bikasend.KindText:
		v.Text = new(in.Value)
	case bikasend.KindPrompt:
		v.IsPrompt, v.Prompt = true, in.Label
	case bikasend.KindAWS:
		v.AWS, v.Key = strings.TrimSpace(in.Value), strings.TrimSpace(in.Key)
	case bikasend.KindSSM:
		v.SSM = strings.TrimSpace(in.Value)
	case bikasend.KindEnv:
		v.Env = strings.TrimSpace(in.Value)
	default:
		return s.workspace(), fmt.Errorf("%w: kind %q", bikasend.ErrInvalidVariable, in.Kind)
	}
	if v.kind() != bikasend.KindText && v.kind() != bikasend.KindPrompt && v.AWS+v.SSM+v.Env == "" {
		return s.workspace(), fmt.Errorf("%w: %s needs a value", bikasend.ErrInvalidVariable, in.Kind)
	}
	if err := guardFileValue(name, v); err != nil {
		return s.workspace(), err
	}
	err := s.editFile(scope, func(c *configFile, env *environment) {
		target := &c.Variables
		if env != nil {
			target = &env.Variables
		}
		if *target == nil {
			*target = map[string]varValue{}
		}
		(*target)[name] = v
	})
	return s.workspace(), err
}

func (s *Service) DeleteVariable(scope, name string) (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope == bikasend.ScopeSession {
		delete(s.session, name)
		delete(s.prompts, name)
		return s.workspace(), nil
	}
	err := s.editFile(scope, func(c *configFile, env *environment) {
		if env != nil {
			delete(env.Variables, name)
			return
		}
		delete(c.Variables, name)
	})
	return s.workspace(), err
}

// editFile changes bikasend.json (folder, environment) or globals.json (global) and writes it back.
func (s *Service) editFile(scope string, change func(c *configFile, env *environment)) error {
	switch scope {
	case bikasend.ScopeGlobal:
		c := s.global
		change(&c, nil)
		data, err := marshalConfig(c)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(s.opts.ConfigDir, 0o700); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(s.opts.ConfigDir, "globals.json"), data); err != nil {
			return err
		}
		s.global = c
		return nil
	case bikasend.ScopeFolder, bikasend.ScopeEnvironment:
	default:
		return fmt.Errorf("%w: scope %q", bikasend.ErrInvalidVariable, scope)
	}
	if s.root == nil {
		return bikasend.ErrNoFolder
	}
	if s.folderHash != "" && !s.trusted() {
		return fmt.Errorf("%w: trust this folder's %s before editing it", bikasend.ErrInvalidConfig, configName)
	}
	if s.folderErr != "" {
		return fmt.Errorf("%w: fix %s first: %s", bikasend.ErrInvalidConfig, configName, s.folderErr)
	}
	c := s.folder
	if scope == bikasend.ScopeEnvironment {
		env, ok := c.Environments[s.env]
		if !ok {
			return fmt.Errorf("%w: pick an environment first", bikasend.ErrUnknownEnv)
		}
		change(&c, &env)
		c.Environments[s.env] = env
	} else {
		change(&c, nil)
	}
	data, err := marshalConfig(c)
	if err != nil {
		return err
	}
	tmp := configName + ".tmp"
	if err := s.root.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := s.root.Rename(tmp, configName); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	s.folder, s.folderHash = c, hex.EncodeToString(sum[:])
	// Our own write keeps the trust; a change from outside asks again.
	s.state.Trusted[s.rootPath] = s.folderHash
	s.snapshot = s.fingerprint()
	s.saveState()
	return nil
}

// CreateConfig writes a starter bikasend.json with a local environment.
func (s *Service) CreateConfig() (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return s.workspace(), bikasend.ErrNoFolder
	}
	if s.folderHash != "" {
		return s.workspace(), nil
	}
	base := "http://localhost:8080"
	if len(s.specs) > 0 && len(s.specs[0].Servers) > 0 {
		base = s.specs[0].Servers[0]
	}
	s.folder = configFile{Environments: map[string]environment{
		"local": {Variables: map[string]varValue{"baseUrl": {Text: new(base)}}},
	}}
	s.folderHash = "new"
	s.state.Trusted[s.rootPath] = "new"
	s.env = "local"
	err := s.editFile(bikasend.ScopeFolder, func(*configFile, *environment) {})
	return s.workspace(), err
}

func (s *Service) SetPrompt(name, value string) (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts[name] = value
	return s.workspace(), nil
}

// Reveal returns one variable's current value. The UI calls it only on an explicit click.
func (s *Service) Reveal(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.lookup("")(name)
	if !b.found {
		return "", fmt.Errorf("%w: {{%s}} %s", bikasend.ErrUndefinedVariable, name, b.reason)
	}
	e := newExpansion(s.lookup(""))
	v := e.expand(b.value)
	return v, e.problem()
}

func (s *Service) SaveSettings(in bikasend.Settings) (bikasend.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.FontSize < 10 || in.FontSize > 20 {
		in.FontSize = 13
	}
	if !slices.Contains([]string{"system", "light", "dark"}, in.Theme) {
		in.Theme = "system"
	}
	in.Proxy = strings.TrimSpace(in.Proxy)
	if in.Proxy != "" {
		if err := checkProxy(in.Proxy); err != nil {
			return s.workspace(), err
		}
	}
	if in.AWSCLI != s.state.Settings.AWSCLI {
		s.awsCLI = s.opts.AWS(in.AWSCLI)
	}
	if !in.SaveHistory {
		_ = os.Remove(filepath.Join(s.opts.ConfigDir, "history.json"))
	}
	s.state.Settings = in
	s.clients = map[string]*http.Client{}
	s.saveState()
	return s.workspace(), nil
}

func (s *Service) loadState() {
	data, err := os.ReadFile(filepath.Join(s.opts.ConfigDir, "settings.json"))
	if err != nil {
		return
	}
	var st stored
	if json.Unmarshal(data, &st) != nil {
		return
	}
	if st.LastEnv == nil {
		st.LastEnv = map[string]string{}
	}
	if st.Trusted == nil {
		st.Trusted = map[string]string{}
	}
	if st.Recent == nil {
		st.Recent = []string{}
	}
	if st.Settings.FontSize == 0 {
		st.Settings.FontSize = 13
	}
	if st.Settings.Theme == "" {
		st.Settings.Theme = "system"
	}
	s.state = st
}

func (s *Service) saveState() {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil || s.opts.ConfigDir == "" {
		return
	}
	if os.MkdirAll(s.opts.ConfigDir, 0o700) == nil {
		_ = writeAtomic(filepath.Join(s.opts.ConfigDir, "settings.json"), data)
	}
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
