# Frontend Rules

## Desktop app (`internal/bikasend/frontend/dist/`)

- `index.html` (markup + CSP meta), `app.css`, `app.js`. No inline `<script>`, `<style>` or
  `style=` attributes: the CSP blocks them. Set styles through classes or `el.style.prop`.
- Build DOM with the `h()` helper in `app.js`. It only ever sets text, never HTML.
- Plain HTML, CSS and JS. No npm, no framework, no build step, no CDN. The app must work offline.
- Call Go through `window.go.api.App.<Method>(...)` (Wails names it after the Go package `api`).
  Each returns a Promise; a Go `error` rejects it. Builds run with `-skipbindings`; do not commit
  a generated `wailsjs/` folder.
- Native elements before custom widgets: `<select>`, `<textarea>`, `<details>`, `<input type="search">`.
- Theme with CSS variables and `prefers-color-scheme`. Both light and dark must be readable.
- Untrusted text goes in with `textContent` (`10-security.md`).
- Keyboard: `⌘/Ctrl+Enter` send, `⌘K` palette, `⌘E` variables, `⌘T` new tab, `⌘F` find in the
  response. Keep them working and list new ones in the welcome screen.
- The UI never decides on secrets: it sends templates with `{{variables}}` and Go resolves them.
- `localStorage` access is wrapped in try/catch. It holds no secrets.

## Download site (`internal/site/api/templates/`)

- `layout.html` (shared head, header, footer, notes), `index.html`, `changelog.html`, embedded
  with `//go:embed`. CSS lives in `static/site.css`. No JS, no `<style>` blocks, no `style=`
  attributes: the CSP blocks them.
- No external fonts, images or CSS: the CSP allows only `'self'`. Static files live in
  `internal/site/api/static/` and are served from `/static/{name}` (allowlisted at start-up).
- Font: Open Sans (SIL OFL 1.1, free to use and ship), self-hosted (`OpenSans.woff2`, Latin
  subset, variable weights 300–800). Keep `OFL-OpenSans.txt` next to every copy. The app uses the
  same file. Only use fonts with an open licence (OFL or Apache 2.0).
- Brand: navy and cyan from the mascot. Accent `#0369a1` light, `#38bdf8` dark.
- Icons: the paper-plane mark, not the mascot. `static/icon.svg` is the master; `favicon.ico`,
  `apple-touch-icon.png`, `icon-192.png`, `icon-512.png` and `build/bikasend/appicon.png` are
  renders of the same shapes. Change them together, then delete `build/bikasend/windows/icon.ico`
  so Wails rebuilds it.
- The gopher mascot (`img/`) is for the README and the site hero only.
- Site text and the hero illustration stay generic: example names like `Example API` and `/items`,
  never real services, endpoints or company data.
- Platform icons are generic shapes, not vendor logos.
- Path segments in links go through `seg` (`url.PathEscape`).
- Check light and dark mode and a 375px wide screen after every change.
