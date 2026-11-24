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
    [ -s cmd/bikasend/release.pub ] || { echo "cmd/bikasend/release.pub is empty: run just keygen and commit it first, or this build can never update itself"; exit 1; }
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

# Checks the draft holds exactly the expected files, that release.yml built each one from this
# tag's commit (provenance) and that GitHub's digests match. Builds the signer from the tag's own
# source, signs SHA256SUMS with the offline key, and asks before it publishes.
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
    commit="$(git rev-list -n 1 "refs/tags/$v")"
    files=("Bikasend-$v-linux-amd64.zip" "Bikasend-$v-macos-universal.zip" "Bikasend-$v-modules.txt" "Bikasend-$v-windows-amd64.zip")
    draft_ok() {
      [ "$(gh release view "$v" --repo "$repo" --json isDraft --jq .isDraft)" = true ] || { echo "$v is not a draft release"; return 1; }
      [ "$(gh release view "$v" --repo "$repo" --json assets --jq '[.assets[].name] | sort | join(" ")')" = "${files[*]}" ] \
        || { echo "the draft must hold exactly: ${files[*]}"; return 1; }
    }
    sum() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
    digests() { gh release view "$v" --repo "$repo" --json assets --jq '.assets[] | (.digest | ltrimstr("sha256:")) + "  " + .name' | sort -k2; }

    run="$(gh run list -w release --branch "$v" -L 1 --json databaseId --jq '.[0].databaseId')"
    echo "Waiting for the release build of $v..."
    gh run watch "$run" --exit-status --interval 30 >/dev/null
    draft_ok
    dir="$(mktemp -d)"
    trap 'rm -rf -- "$dir"' EXIT
    mkdir "$dir/files" "$dir/src"
    for f in "${files[@]}"; do gh release download "$v" --repo "$repo" -D "$dir/files" -p "$f"; done
    for f in "${files[@]}"; do
      gh attestation verify "$dir/files/$f" --repo "$repo" --signer-workflow "$repo/.github/workflows/release.yml" \
        --source-ref "refs/tags/$v" --source-digest "$commit" --deny-self-hosted-runners >/dev/null
    done
    echo "Provenance: release.yml built every file from $v ($commit) on GitHub's runners."
    digests > "$dir/digests"
    (cd "$dir/files" && sum -c --strict --quiet "$dir/digests")
    echo "GitHub's digests match the files."

    # The signer and this recipe handle the key and the passphrase: they must be the tag's own, and a
    # change since the last release must be read first.
    signer=(pkg/sign cmd/sign justfile go.mod)
    git diff --quiet "$v" -- "${signer[@]}" || { echo "run just publish from a clean checkout of $v: ${signer[*]} differ from the tag"; exit 1; }
    prev="$(git describe --tags --abbrev=0 "$v^" 2>/dev/null || true)"
    if [ -n "$prev" ] && ! git diff --quiet "$prev" "$v" -- "${signer[@]}"; then
      git --no-pager diff "$prev" "$v" -- "${signer[@]}"
      read -r -p "The signer or this recipe changed since $prev (diff above). Type 'reviewed' once you have read it: " seen || seen=""
      [ "$seen" = reviewed ] || { echo "cancelled; $v stays a draft"; exit 1; }
    fi
    git archive "$v" go.mod go.sum pkg/sign cmd/sign | tar -x -C "$dir/src"
    (cd "$dir/src" && GOFLAGS=-mod=readonly go build -trimpath -o "$dir/signer" ./cmd/sign)
    git show "$v:cmd/bikasend/release.pub" > "$dir/release.pub"
    [ -s "$dir/release.pub" ] || { echo "the app at $v has no release key; run just keygen and release again"; exit 1; }

    sums="$dir/Bikasend-$v-SHA256SUMS"
    (cd "$dir/files" && "$dir/signer" sums -out "$sums" "${files[@]}")
    cat "$sums"
    read -rs -p "Passphrase for $key: " pass; echo
    printf '%s\n' "$pass" | "$dir/signer" sign -key "$key" "$sums"
    unset pass
    "$dir/signer" verify -pub "$dir/release.pub" "$sums"
    read -r -p "Upload the signed checksums and publish $v? [y/N] " ok || ok=""
    case "$ok" in y|Y) ;; *) echo "cancelled; $v stays a draft"; exit 1 ;; esac
    draft_ok
    digests | cmp -s - "$dir/digests" || { echo "the draft changed while you signed; nothing published"; exit 1; }
    gh release upload "$v" "$sums" "$sums.sig" --repo "$repo"
    gh release edit "$v" --draft=false --latest --repo "$repo"
    digests | grep -v SHA256SUMS | diff -q - <(sort -k2 "$sums") >/dev/null \
      || { echo "WARNING: the published files no longer match the signed list. Check the release now."; exit 1; }
    echo "Published $v. The site lists it within 5 minutes."

# A private repo needs GITHUB_TOKEN: fine-grained, read-only Contents on the bikasend repo.
[doc("Run the download site at http://localhost:8091")]
site:
    go run ./cmd/site

# Use deploy/site for a named tunnel.
[doc("Quick public URL (*.trycloudflare.com) for the local site")]
tunnel:
    cloudflared tunnel --url http://localhost:8091
