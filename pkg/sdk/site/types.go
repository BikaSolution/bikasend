package site

import "time"

type Release struct {
	Tag        string
	Name       string
	URL        string
	Prerelease bool
	Published  time.Time
	Notes      []Note
	Downloads  []Download
	Files      []Download
}

// Note is one block of release notes: a heading, a paragraph or a list.
type Note struct {
	Kind  string // "h", "p" or "ul"
	Text  string
	Items []string
}

type Download struct {
	Name     string
	Platform string
	Size     string
	SHA256   string
}

// LatestRelease is the body of GET /latest.json. URLs are relative to the site.
type LatestRelease struct {
	Tag        string           `json:"tag"`
	Name       string           `json:"name"`
	Prerelease bool             `json:"prerelease"`
	Published  string           `json:"published"`
	NotesURL   string           `json:"notesUrl"`
	Downloads  []LatestDownload `json:"downloads"`
}

type LatestDownload struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Size     string `json:"size"`
	SHA256   string `json:"sha256"`
	URL      string `json:"url"`
}
