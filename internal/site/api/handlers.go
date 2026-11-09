package api

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

//go:embed templates static
var templates embed.FS

// staticFiles maps a file name to its embedded path; the request value never becomes a path.
var staticFiles = func() map[string]string {
	entries, err := fs.ReadDir(templates, "static")
	if err != nil {
		panic(err)
	}
	paths := map[string]string{}
	for _, e := range entries {
		paths[e.Name()] = path.Join("static", e.Name())
	}
	return paths
}()

// cssURL changes with the file, so a deploy never serves new HTML with a cached old stylesheet.
var cssURL = func() string {
	css, err := templates.ReadFile("static/site.css")
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(css)
	return "/static/site.css?v=" + hex.EncodeToString(sum[:4])
}()

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"seg": url.PathEscape,
	"css": func() string { return cssURL },
}).ParseFS(templates, "templates/*.html"))

type pageData struct {
	Latest      *site.Release
	Primary     *site.Download
	Recommended string
	Older       []site.Release
	Releases    []site.Release
	Unavailable bool
	Year        int
}

func indexHandler(svc site.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := pageData{Year: time.Now().Year()}
		releases, err := svc.Releases(r.Context())
		if err != nil {
			slog.Error("list releases", "error", err)
			data.Unavailable = true
		}
		if len(releases) > 0 {
			data.Latest, data.Older = &releases[0], releases[1:]
			data.Recommended = platformFromUserAgent(r.UserAgent())
			for i, d := range data.Latest.Downloads {
				if d.Platform == data.Recommended || data.Primary == nil {
					data.Primary = &data.Latest.Downloads[i]
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The recommended download depends on the User-Agent; a shared cache must not mix them.
		w.Header().Set("Vary", "User-Agent")
		if err := pages.ExecuteTemplate(w, "index.html", data); err != nil {
			slog.Error("render index", "error", err)
		}
	})
}

func changelogHandler(svc site.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := pageData{Year: time.Now().Year()}
		releases, err := svc.Releases(r.Context())
		if err != nil {
			slog.Error("list releases", "error", err)
			data.Unavailable = true
		}
		data.Releases = releases
		if len(releases) > 0 {
			data.Latest = &releases[0]
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pages.ExecuteTemplate(w, "changelog.html", data); err != nil {
			slog.Error("render changelog", "error", err)
		}
	})
}

func latestHandler(svc site.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, err := svc.Latest(r.Context())
		switch {
		case errors.Is(err, site.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no releases"})
			return
		case err != nil:
			slog.Error("latest release", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		latest := site.LatestRelease{
			Tag:        rel.Tag,
			Name:       rel.Name,
			Prerelease: rel.Prerelease,
			Published:  rel.Published.UTC().Format(time.RFC3339),
			NotesURL:   notesURL(rel.Tag),
			Downloads:  []site.LatestDownload{},
		}
		for _, d := range rel.Downloads {
			latest.Downloads = append(latest.Downloads, site.LatestDownload{
				Platform: d.Platform,
				Name:     d.Name,
				Size:     d.Size,
				SHA256:   d.SHA256,
				URL:      "/downloads/" + url.PathEscape(rel.Tag) + "/" + url.PathEscape(d.Name),
			})
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, http.StatusOK, latest)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "error", err)
	}
}

func notesURL(tag string) string {
	return "/changelog#" + url.PathEscape(tag)
}

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Author  atomAuthor  `xml:"author"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	ID        string      `xml:"id"`
	Title     string      `xml:"title"`
	Updated   string      `xml:"updated"`
	Published string      `xml:"published"`
	Link      atomLink    `xml:"link"`
	Content   atomContent `xml:"content"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// feedHandler links relative to the feed URL; the Host header is client input and never becomes a link.
func feedHandler(svc site.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releases, err := svc.Releases(r.Context())
		if err != nil {
			slog.Error("list releases", "error", err)
			http.Error(w, "release information is unavailable, try again shortly", http.StatusServiceUnavailable)
			return
		}
		feed := atomFeed{
			ID:      "urn:bikasend:releases",
			Title:   "Bikasend releases",
			Updated: time.Unix(0, 0).UTC().Format(time.RFC3339),
			Author:  atomAuthor{Name: "Bikasend"},
			Links: []atomLink{
				{Rel: "self", Type: "application/atom+xml", Href: "/feed.xml"},
				{Rel: "alternate", Type: "text/html", Href: "/changelog"},
			},
			Entries: []atomEntry{},
		}
		for i, rel := range releases {
			published := rel.Published.UTC().Format(time.RFC3339)
			if i == 0 {
				feed.Updated = published
			}
			title := rel.Name
			if title == "" {
				title = rel.Tag
			}
			feed.Entries = append(feed.Entries, atomEntry{
				ID:        "urn:bikasend:release:" + url.PathEscape(rel.Tag),
				Title:     title,
				Updated:   published,
				Published: published,
				Link:      atomLink{Rel: "alternate", Type: "text/html", Href: notesURL(rel.Tag)},
				Content:   atomContent{Type: "text", Text: plainNotes(rel.Notes)},
			})
		}
		w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, xml.Header)
		enc := xml.NewEncoder(w)
		enc.Indent("", "  ")
		if err := enc.Encode(feed); err != nil {
			slog.Error("write feed", "error", err)
		}
	})
}

func plainNotes(notes []site.Note) string {
	blocks := []string{}
	for _, n := range notes {
		if n.Kind == "ul" {
			blocks = append(blocks, "- "+strings.Join(n.Items, "\n- "))
			continue
		}
		blocks = append(blocks, n.Text)
	}
	return strings.Join(blocks, "\n\n")
}

// downloadHandler redirects to GitHub's signed URL; only tag/name pairs from the release list resolve.
func downloadHandler(svc site.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := svc.DownloadURL(r.Context(), r.PathValue("tag"), r.PathValue("name"))
		switch {
		case errors.Is(err, site.ErrNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			slog.Error("download url", "error", err)
			http.Error(w, "download unavailable, try again shortly", http.StatusBadGateway)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, u, http.StatusFound) // #nosec G710 -- u is the https URL the GitHub API returned; tag and name only select from the cached release list.
	})
}

// staticHandler serves only files embedded in static/; name is "" for the fixed-name routes.
func staticHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := name
		if file == "" {
			file = r.PathValue("name")
		}
		embedded, ok := staticFiles[file]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if strings.HasSuffix(embedded, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		http.ServeFileFS(w, r, templates, embedded)
	})
}

func platformFromUserAgent(ua string) string {
	switch {
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		return "macOS"
	case strings.Contains(ua, "Linux") && !strings.Contains(ua, "Android"):
		return "Linux"
	}
	return ""
}
