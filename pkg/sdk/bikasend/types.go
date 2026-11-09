package bikasend

type Spec struct {
	Path       string      `json:"path"`
	Title      string      `json:"title"`
	Version    string      `json:"version"`
	Servers    []string    `json:"servers"`
	Operations []Operation `json:"operations"`
	Error      string      `json:"error,omitempty"`
}

type Operation struct {
	ID          string        `json:"id"`
	Method      string        `json:"method"`
	Path        string        `json:"path"`
	Summary     string        `json:"summary"`
	Description string        `json:"description"`
	Tag         string        `json:"tag"`
	Deprecated  bool          `json:"deprecated"`
	Params      []Param       `json:"params"`
	Headers     string        `json:"headers"`
	BodyType    string        `json:"bodyType"`
	Body        string        `json:"body"`
	Form        []Field       `json:"form"`
	Responses   []ResponseDoc `json:"responses"`
}

type Param struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Description string `json:"description"`
	Example     string `json:"example"`
	Required    bool   `json:"required"`
}

type ResponseDoc struct {
	Status      string `json:"status"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

// Field is a form field. When File is set, Value is a path on disk.
type Field struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	File    bool   `json:"file"`
	Enabled bool   `json:"enabled"`
}

type KV struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// Request is a template: any string may hold {{variables}}, resolved in Go at send time.
type Request struct {
	ID            string  `json:"id"`
	Method        string  `json:"method"`
	URL           string  `json:"url"`
	PathParams    []KV    `json:"pathParams"`
	Query         []KV    `json:"query"`
	Headers       string  `json:"headers"`
	BodyType      string  `json:"bodyType"` // none, json, text, xml, form, multipart
	Body          string  `json:"body"`
	Form          []Field `json:"form"`
	Auth          string  `json:"auth"`   // "", "oauth2", "sigv4"
	Server        string  `json:"server"` // the spec server; fills {{baseUrl}} at the lowest precedence
	Operation     string  `json:"operation"`
	AllowInsecure bool    `json:"allowInsecure"`
	// AllowHost is a host the user confirmed for secrets when no environment lists it.
	AllowHost string `json:"allowHost"`
}

type Timing struct {
	DNSMs       int64 `json:"dnsMs"`
	ConnectMs   int64 `json:"connectMs"`
	TLSMs       int64 `json:"tlsMs"`
	FirstByteMs int64 `json:"firstByteMs"`
	TotalMs     int64 `json:"totalMs"`
}

type Response struct {
	Status      string   `json:"status"`
	StatusCode  int      `json:"statusCode"`
	Headers     string   `json:"headers"`
	Body        string   `json:"body"`
	BodyBase64  string   `json:"bodyBase64,omitempty"`
	ContentType string   `json:"contentType"`
	Size        int      `json:"size"`
	Truncated   bool     `json:"truncated"`
	DurationMs  int64    `json:"durationMs"`
	Timing      Timing   `json:"timing"`
	Redacted    bool     `json:"redacted"`
	Issues      []string `json:"issues"`
}

// Preview is a resolved request with secret values masked.
type Preview struct {
	Method    string   `json:"method"`
	URL       string   `json:"url"`
	Headers   string   `json:"headers"`
	Body      string   `json:"body"`
	Used      []string `json:"used"`
	Missing   []string `json:"missing"`
	Secret    bool     `json:"secret"`
	Warning   string   `json:"warning"`
	Error     string   `json:"error"`
	Insecure  bool     `json:"insecure"`
	BlockedBy string   `json:"blockedBy"`
}

const (
	ScopeSession     = "session"
	ScopeEnvironment = "environment"
	ScopeFolder      = "folder"
	ScopeGlobal      = "global"
	ScopeSpec        = "spec"
)

const (
	KindText   = "text"
	KindSecret = "secret" // session only: typed by hand, masked
	KindPrompt = "prompt"
	KindAWS    = "aws"
	KindSSM    = "ssm"
	KindEnv    = "env"
)

// VariableInput is what the editor sends. Value is the text, env name, secret ID or parameter name.
type VariableInput struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Key   string `json:"key"`
	Label string `json:"label"`
}

type VariableView struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Kind     string `json:"kind"`
	Secret   bool   `json:"secret"`
	Value    string `json:"value"`
	Ref      string `json:"ref"`
	Resolved bool   `json:"resolved"`
	Active   bool   `json:"active"`
	Shadows  string `json:"shadows"`
	Error    string `json:"error"`
}

type EnvironmentView struct {
	Name       string   `json:"name"`
	Hosts      []string `json:"hosts"`
	Production bool     `json:"production"`
	AWSProfile string   `json:"awsProfile"`
	AWSRegion  string   `json:"awsRegion"`
	OAuth2     bool     `json:"oauth2"`
	SigV4      bool     `json:"sigv4"`
}

type AWSStatus struct {
	State    string `json:"state"` // none, signed-out, loaded, error
	Profile  string `json:"profile"`
	Region   string `json:"region"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
	LoadedAt string `json:"loadedAt"`
	Error    string `json:"error"`
	CLI      string `json:"cli"`
}

type Settings struct {
	Theme        string `json:"theme"` // system, light, dark
	FontSize     int    `json:"fontSize"`
	SaveHistory  bool   `json:"saveHistory"`
	CheckUpdates bool   `json:"checkUpdates"`
	Proxy        string `json:"proxy"`
	AWSCLI       string `json:"awsCli"`
}

type Workspace struct {
	Root         string            `json:"root"`
	ConfigFile   string            `json:"configFile"`
	ConfigError  string            `json:"configError"`
	Trusted      bool              `json:"trusted"`
	Environments []EnvironmentView `json:"environments"`
	Environment  string            `json:"environment"`
	AWS          AWSStatus         `json:"aws"`
	Variables    []VariableView    `json:"variables"`
	Prompts      []string          `json:"prompts"`
	Settings     Settings          `json:"settings"`
	Recent       []string          `json:"recent"`
	Version      string            `json:"version"`
}

type HistoryEntry struct {
	Time        string  `json:"time"`
	Environment string  `json:"environment"`
	Request     Request `json:"request"`
	Status      int     `json:"status"`
	DurationMs  int64   `json:"durationMs"`
}

type Update struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	URL       string `json:"url"`
}
