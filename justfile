# Bikasend tasks. `just` lists them.

set shell := ["bash", "-euo", "pipefail", "-c"]

app_dir := "cmd/bikasend"
bin := "build/bikasend/bin"

# Linux builds against WebKitGTK 4.1 (libwebkit2gtk-4.1), as release.yml does.
tags := if os() == "linux" { "-tags webkit2_41" } else { "" }
lint_tags := if os() == "linux" { "--build-tags webkit2_41" } else { "" }

# wails is installed with `go install`, so Go's bin folder must be on PATH.
export PATH := env("PATH") + ":" + `go env GOPATH` + "/bin"

[private]
default:
    @just --list

[doc("Run all tests")]
test:
    go test {{ tags }} ./...

[doc("go vet")]
vet:
    go vet {{ tags }} ./...

[doc("golangci-lint")]
lint:
    golangci-lint run {{ lint_tags }} ./...

[doc("Check the layer rules in .go-arch-lint.yml")]
arch_lint:
    go run github.com/fe3dback/go-arch-lint@v1.15.0 check

# 2 workers with a heap target; on Linux also a 3 GB cgroup, so an overflow kills the fuzzer and
# nothing else. Go would otherwise start one worker per CPU.
[doc("Fuzz both spec parsers for 2 minutes each (CI runs 30s)")]
fuzz:
    #!/usr/bin/env bash
    set -euo pipefail
    cap=()
    if systemd-run --user --scope -q true 2>/dev/null; then
      cap=(systemd-run --user --scope -q -p MemoryMax=3G -p MemorySwapMax=0)
    fi
    for target in FuzzParseJSON FuzzParseYAML; do
      ${cap[@]+"${cap[@]}"} env GOMEMLIMIT=512MiB go test ./internal/bikasend/core/ -run '^$' -parallel 2 -fuzztime 2m -fuzz "$target"
    done

# @latest on purpose: an audit with a stale scanner misses new issues.
[doc("govulncheck (known CVEs) and gosec (insecure patterns)")]
security:
    go run golang.org/x/vuln/cmd/govulncheck@latest {{ tags }} ./...
    go run github.com/securego/gosec/v2/cmd/gosec@latest -quiet {{ tags }} ./...

[doc("Run the app with live reload (browser view at http://localhost:34115)")]
dev:
    cd {{ app_dir }} && wails dev -skipbindings {{ tags }}

[doc("Build the app for this OS into build/bikasend/bin/")]
app:
    cd {{ app_dir }} && wails build -clean -skipbindings -trimpath {{ tags }}

[doc("Build, then start the app like an installed one")]
run: app
    {{ if os() == "macos" { "open " + bin + "/Bikasend.app" } else { "./" + bin + "/Bikasend" } }}

# Checks first and asks before anything leaves this machine. The tag starts release.yml, which
# builds every platform. Tags are protected on GitHub: only an admin can move or delete one.
[doc("Tag a release: just release v0.2.0 (no version shows the next ones)")]
[positional-arguments]
release version="":
    #!/usr/bin/env bash
    set -euo pipefail
    last="$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)"
    feature="$(echo "$last" | awk -F. '{printf "%s.%d.0", $1, $2 + 1}')"
    fix="$(echo "$last" | awk -F. '{printf "%s.%s.%d", $1, $2, $3 + 1}')"
    v="${1:-}"
    if [ -z "$v" ]; then echo "usage: just release $feature (new feature) or just release $fix (fixes, upgrades); last release: $last"; exit 1; fi
    [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "the version must look like v1.2.3, got $v"; exit 1; }
    [ "$(git branch --show-current)" = main ] || { echo "release from main only"; exit 1; }
    [ -z "$(git status --porcelain)" ] || { echo "commit or stash your changes first"; exit 1; }
    git fetch -q --tags origin main
    [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "main differs from origin/main: pull or push first"; exit 1; }
    if git rev-parse -q --verify "refs/tags/$v" >/dev/null || [ -n "$(git ls-remote --tags origin "refs/tags/$v")" ]; then echo "$v already exists"; exit 1; fi
    [ "$(printf '%s\n%s\n' "$last" "$v" | sort -V | tail -1)" = "$v" ] || { echo "$v is not newer than $last"; exit 1; }
    just --justfile "{{ justfile() }}" vet test
    file="{{ app_dir }}/wails.json"
    cur="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' "$file")"
    echo; echo "Release $v (last: $last) from $(git rev-parse --short HEAD)"
    [ "$cur" = "${v#v}" ] || echo "  bump $file productVersion $cur -> ${v#v} (new commit on main)"
    echo "  Site text checked against the code? See .agents/rules/90-website-audit.md"
    read -r -p "Push $v and start the release build? [y/N] " ok || ok=""
    case "$ok" in y|Y) ;; *) echo "cancelled, nothing changed"; exit 1 ;; esac
    if [ "$cur" != "${v#v}" ]; then
      NEW="${v#v}" perl -pi -e 's/("productVersion":\s*")[^"]*"/${1}$ENV{NEW}"/' "$file"
      git add "$file"
      TZ=UTC git commit -q -m "chore(release): $v"
      git push -q origin main
    fi
    TZ=UTC git tag -a "$v" -m "Bikasend $v"
    git push -q origin "$v"
    echo "Pushed $v. Follow the build: gh run watch \$(gh run list -w release -L 1 --json databaseId --jq '.[0].databaseId')"

# The private key never leaves this machine: back up the key file and the passphrase together
# (a password manager). Commit cmd/bikasend/release.pub; every build after that checks it.
[doc("Make the offline release signing key (once)")]
keygen:
    #!/usr/bin/env bash
    set -euo pipefail
    key="${BIKASEND_SIGNING_KEY:-$HOME/.config/bikasend/release-signing.key}"
    read -rs -p "New passphrase (12 or more characters): " p1; echo
    read -rs -p "Same passphrase again: " p2; echo
    [ "$p1" = "$p2" ] || { echo "the passphrases differ; nothing written"; exit 1; }
    printf '%s\n' "$p1" | go run ./cmd/sign keygen -key "$key" -pub cmd/bikasend/release.pub
    echo "Back up $key and its passphrase now. Then commit cmd/bikasend/release.pub."

# Waits for the release build, checks that GitHub Actions built each file (provenance) and that
# GitHub's digests match, signs SHA256SUMS with the offline key, then asks before it publishes.
[doc("Sign and publish a draft release: just publish v0.3.0")]
[positional-arguments]
publish version="":
    #!/usr/bin/env bash
    set -euo pipefail
    v="${1:-}"
    [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "usage: just publish v1.2.3"; exit 1; }
    key="${BIKASEND_SIGNING_KEY:-$HOME/.config/bikasend/release-signing.key}"
    [ -f "$key" ] || { echo "no signing key at $key (just keygen, or set BIKASEND_SIGNING_KEY)"; exit 1; }
    repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
    [ "$(gh release view "$v" --json isDraft --jq .isDraft)" = true ] || { echo "$v is not a draft release"; exit 1; }
    run="$(gh run list -w release --branch "$v" -L 1 --json databaseId --jq '.[0].databaseId')"
    echo "Waiting for the release build of $v..."
    gh run watch "$run" --exit-status --interval 30 >/dev/null
    dir="$(mktemp -d)"
    trap 'rm -rf -- "$dir"' EXIT
    gh release download "$v" -D "$dir" -p "Bikasend-$v-*.zip" -p "Bikasend-$v-modules.txt"
    for f in "$dir"/*; do gh attestation verify "$f" --repo "$repo" >/dev/null; done
    echo "Provenance: every file was built by this repo's release workflow."
    sum() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
    gh release view "$v" --json assets --jq '.assets[] | select(.name | test("(zip|modules\\.txt)$")) | (.digest | ltrimstr("sha256:")) + "  " + .name' \
      | (cd "$dir" && sum -c --quiet -)
    echo "GitHub's digests match the files."
    git show "$v:cmd/bikasend/release.pub" > "$dir/release.pub"
    [ -s "$dir/release.pub" ] || { echo "the app at $v has no release key; run just keygen and release again"; exit 1; }
    sums="$dir/Bikasend-$v-SHA256SUMS"
    go run ./cmd/sign sums -out "$sums" "$dir"/Bikasend-"$v"-*.zip "$dir/Bikasend-$v-modules.txt"
    cat "$sums"
    read -rs -p "Passphrase for $key: " pass; echo
    printf '%s\n' "$pass" | go run ./cmd/sign sign -key "$key" "$sums"
    go run ./cmd/sign verify -pub "$dir/release.pub" "$sums"
    read -r -p "Upload the signed checksums and publish $v? [y/N] " ok || ok=""
    case "$ok" in y|Y) ;; *) echo "cancelled; $v stays a draft"; exit 1 ;; esac
    gh release upload "$v" "$sums" "$sums.sig" --repo "$repo"
    gh release edit "$v" --draft=false --latest --repo "$repo"
    echo "Published $v. The site lists it within 5 minutes."

# A private repo needs GITHUB_TOKEN: fine-grained, read-only Contents on the bikasend repo.
[doc("Run the download site at http://localhost:8091")]
site:
    go run ./cmd/site

# Use deploy/site for a named tunnel.
[doc("Quick public URL (*.trycloudflare.com) for the local site")]
tunnel:
    cloudflared tunnel --url http://localhost:8091
