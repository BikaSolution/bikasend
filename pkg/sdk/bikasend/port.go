package bikasend

import "context"

type Service interface {
	// Open scans root for specs and reads its bikasend.json.
	Open(root string) ([]Spec, error)
	// Changed reports whether a spec file or bikasend.json changed since the last Open.
	Changed() bool
	Workspace() Workspace
	TrustFolder() (Workspace, error)
	CreateConfig() (Workspace, error)
	SelectEnvironment(name string) (Workspace, error)

	SetVariable(scope, name string, in VariableInput) (Workspace, error)
	DeleteVariable(scope, name string) (Workspace, error)
	SetPrompt(name, value string) (Workspace, error)
	// Reveal returns a variable's value; call it only on an explicit user action.
	Reveal(name string) (string, error)

	LoadSecrets(ctx context.Context) (Workspace, error)
	SignIn(ctx context.Context) (Workspace, error)
	ForgetSecrets() Workspace

	// AllowUpload marks a path the user chose in the file dialog; uploads accept only those.
	AllowUpload(path string)
	Preview(r Request) Preview
	Send(ctx context.Context, r Request) (Response, error)
	Cancel(id string)
	ClearCookies()

	History() []HistoryEntry
	ClearHistory() error
	SaveSettings(s Settings) (Workspace, error)

	Curl(r Request) (string, error)
	ImportCurl(cmd string) (Request, error)
	CheckUpdate(ctx context.Context) (Update, error)
}
