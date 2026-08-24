# Repo Scaffold: Release, Homebrew Tap, Claude Skill — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `headroom mcp serve` an install path that is not `go build`, by adding a tag-push release workflow, a Homebrew tap formula, and a Claude skill.

**Architecture:** One GitHub Actions workflow, triggered by a `v*` tag push, builds six `CGO_ENABLED=0` binaries from a single `ubuntu-latest` runner, verifies each is byte-reproducible by building it twice, publishes them as a GitHub Release, and fires a `repository_dispatch` at `dobbo-ca/homebrew-taps`. The tap repo already owns the formula-rewriting logic; this plan only seeds the formula file it requires. A `skills/headroom/SKILL.md` teaches an agent to route large tool output through the `headroom_compress` MCP tool.

**Tech Stack:** GitHub Actions, Go 1.25 (`CGO_ENABLED=0`), `softprops/action-gh-release@v2`, `peter-evans/repository-dispatch@v3`, `actions/create-github-app-token@v3`, Homebrew formula DSL.

**Spec:** `docs/superpowers/specs/2026-06-08-headroom-go-core-design.md` §8 v0.1 item 13 ("Repo scaffold: … Uplift release + Homebrew tap, Claude skill"). Item 13's "Uplift" is superseded by an explicit user decision — see Global Constraints.

**Beads issue:** `hr-47g.32`

---

## Global Constraints

Every task's requirements implicitly include this section.

- **`CGO_ENABLED=0` everywhere.** Never add cgo. This is the only reason a single Linux runner can produce darwin and windows binaries.
- **Reproducible builds.** The only `-ldflags` value that varies per build is `-X main.version=<tag>`. Do **not** stamp a timestamp or a commit SHA. `cmd/headroom/main.go` declares exactly one such variable:
  ```go
  // version is stamped at build time with -ldflags "-X main.version=<tag>".
  var version = "0.1.0-dev"
  ```
  There is no `main.commit` and no `main.date`. Referencing either breaks the build.
- **No Uplift, no git-cliff, no `CHANGELOG.md`.** The sibling repo `graphify-go` uses `gembaadvantage/uplift-action@v2` with default config on every push to `main`. headroom-go deliberately does not. Releases are cut by pushing a tag. Do not add `.uplift.yml`, `.cliff.toml`, or `CHANGELOG.md`.
- **Artifact names are not free.** The tap's `update-formulas.yml` rewrites formulas with these fixed `sed` patterns:
  ```
  ${FORMULA}-v\?[0-9.]*-darwin-arm64.tar.gz
  ${FORMULA}-v\?[0-9.]*-linux-amd64.tar.gz
  ```
  with `FORMULA=headroom-go`. Archive names MUST therefore be exactly `headroom-go-<version>-<os>-<arch>.tar.gz`, where `<version>` includes the leading `v` (e.g. `headroom-go-v0.1.0-darwin-arm64.tar.gz`). The formula file MUST be named `Formula/headroom-go.rb`.
- **Binary name is `headroom`, module/formula name is `headroom-go`.** The archive contains a binary called `headroom` (or `headroom.exe` on Windows). Do not rename either to match the other.
- **Go version `"1.25"`** in `actions/setup-go@v5`, matching `.github/workflows/ci.yml`.
- **A workflow that has never run is unverified.** Reading YAML and declaring it correct is not a review. Every gate must be executed and its output pasted.
- **Weakening a check to get green is an automatic rejection.** If a gate fails, fix the thing under test, never the gate.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `.github/workflows/release.yml` | Create | Tag-push → build 6 targets → verify reproducibility → GitHub Release → dispatch to tap |
| `skills/headroom/SKILL.md` | Create | Teach an agent to compress large tool output through the headroom MCP tools |
| `README.md` | Modify | Replace the `go install`-only Install section; point at the skill |
| `Formula/headroom-go.rb` (in `dobbo-ca/homebrew-taps`) | Create | Seed the formula the tap updater requires; blocked on the first real release |

---

## Task 1: Release workflow

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `cmd/headroom` (package `main`, with `var version`), `dobbo-ca/homebrew-taps` `repository_dispatch` type `update-formula`.
- Produces: release assets named `headroom-go-<version>-<os>-<arch>.tar.gz` (darwin, linux) and `headroom-go-<version>-windows-<arch>.zip`, each with a sibling `.sha256` file containing only the bare hex digest. Task 4's formula consumes the four `tar.gz` names and their digests.

**Background the implementer needs:**

The tap repo `dobbo-ca/homebrew-taps` has a workflow `update-formulas.yml` listening on `repository_dispatch` with `types: [update-formula]`. It reads this exact `client_payload` shape and **exits 1 if `Formula/<formula>.rb` does not already exist**:

```
formula, version, darwin_amd64_sha, darwin_arm64_sha, linux_amd64_sha, linux_arm64_sha
```

The GitHub App token is minted from `vars.GH_PUB_APP_CLIENT_ID` and `secrets.GH_PUB_APP_PEM`, which live at the `dobbo-ca` **organization** level (they are not repo secrets on `graphify-go` either, and its release workflow works). The App must be granted access to `homebrew-taps`.

- [ ] **Step 1: Write the workflow file**

Create `.github/workflows/release.yml` with exactly this content:

```yaml
name: Release

on:
  push:
    tags: ["v*"]
  workflow_dispatch:
    inputs:
      tag:
        description: "Existing tag to build and release, e.g. v0.1.0"
        required: true
        type: string

permissions:
  contents: write

jobs:
  # One runner covers every target: headroom-go is CGO_ENABLED=0, so darwin and
  # windows binaries cross-compile from Linux. graphify-go needs native runners
  # only because tree-sitter forces cgo on it.
  build:
    runs-on: ubuntu-latest
    outputs:
      version: ${{ steps.version.outputs.version }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.tag || github.ref }}

      - uses: actions/setup-go@v5
        with:
          go-version: "1.25"

      - name: Resolve version
        id: version
        env:
          INPUT_TAG: ${{ inputs.tag }}
        run: |
          set -euo pipefail
          VERSION="${INPUT_TAG:-$GITHUB_REF_NAME}"
          case "$VERSION" in
            v[0-9]*) ;;
            *) echo "refusing to release non-version ref: $VERSION" >&2; exit 1 ;;
          esac
          echo "version=$VERSION" >> "$GITHUB_OUTPUT"

      # Version is the only thing stamped in. A timestamp or commit SHA in
      # -ldflags would make the binaries unreproducible, and main.go declares
      # no variable to hold either.
      - name: Build release artifacts
        env:
          VERSION: ${{ steps.version.outputs.version }}
          CGO_ENABLED: "0"
        run: |
          set -euo pipefail
          mkdir -p dist
          for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
            GOOS="${target%/*}"
            GOARCH="${target#*/}"
            BIN=headroom
            [ "$GOOS" = "windows" ] && BIN=headroom.exe
            echo "--- building $GOOS/$GOARCH"
            GOOS="$GOOS" GOARCH="$GOARCH" go build \
              -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
              -o "$BIN" ./cmd/headroom

            # Reproducibility gate: the same source and the same flags must
            # produce the same bytes. Rebuild and compare before packaging.
            GOOS="$GOOS" GOARCH="$GOARCH" go build \
              -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
              -o "${BIN}.again" ./cmd/headroom
            if ! cmp -s "$BIN" "${BIN}.again"; then
              echo "build is not reproducible for $GOOS/$GOARCH" >&2
              exit 1
            fi
            rm "${BIN}.again"

            NAME="headroom-go-${VERSION}-${GOOS}-${GOARCH}"
            if [ "$GOOS" = "windows" ]; then
              zip -q "dist/${NAME}.zip" "$BIN"
              ARCHIVE="dist/${NAME}.zip"
            else
              tar czf "dist/${NAME}.tar.gz" "$BIN"
              ARCHIVE="dist/${NAME}.tar.gz"
            fi
            sha256sum "$ARCHIVE" | awk '{print $1}' > "${ARCHIVE}.sha256"
            rm "$BIN"
          done
          ls -lh dist/

      - uses: actions/upload-artifact@v4
        with:
          name: dist
          path: dist/

  release:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/download-artifact@v4
        with:
          name: dist
          path: dist

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          tag_name: ${{ needs.build.outputs.version }}
          name: ${{ needs.build.outputs.version }}
          generate_release_notes: true
          files: dist/*
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

  # The tap's update-formulas.yml rewrites Formula/headroom-go.rb in place and
  # exits 1 if that file does not already exist. Seed it before the first tag.
  update-homebrew:
    needs: [build, release]
    runs-on: ubuntu-latest
    steps:
      - name: Generate GitHub App Token
        id: app-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.GH_PUB_APP_CLIENT_ID }}
          private-key: ${{ secrets.GH_PUB_APP_PEM }}
          owner: dobbo-ca
          repositories: homebrew-taps

      - uses: actions/download-artifact@v4
        with:
          name: dist
          path: dist

      - name: Extract SHA256 checksums
        id: checksums
        run: |
          set -euo pipefail
          for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
            key="$(echo "$target" | tr '-' '_')"
            echo "${key}=$(cat dist/headroom-go-*-${target}.tar.gz.sha256)" >> "$GITHUB_OUTPUT"
          done

      - name: Trigger Homebrew tap update
        uses: peter-evans/repository-dispatch@v3
        with:
          token: ${{ steps.app-token.outputs.token }}
          repository: dobbo-ca/homebrew-taps
          event-type: update-formula
          client-payload: |
            {
              "formula": "headroom-go",
              "version": "${{ needs.build.outputs.version }}",
              "darwin_amd64_sha": "${{ steps.checksums.outputs.darwin_amd64 }}",
              "darwin_arm64_sha": "${{ steps.checksums.outputs.darwin_arm64 }}",
              "linux_amd64_sha": "${{ steps.checksums.outputs.linux_amd64 }}",
              "linux_arm64_sha": "${{ steps.checksums.outputs.linux_arm64 }}"
            }
```

- [ ] **Step 2: Lint the workflow**

Run: `actionlint .github/workflows/release.yml`
Expected: no output, exit status 0.

- [ ] **Step 3: Execute the build stage for real**

The `Build release artifacts` step is plain shell plus `go build`. Run the identical
loop locally so the six targets, the reproducibility gate, and the archive names
are all proven before the workflow ever runs. From the repo root:

```bash
set -euo pipefail
export VERSION=v0.0.0-local CGO_ENABLED=0
rm -rf dist && mkdir -p dist
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  GOOS="${target%/*}"; GOARCH="${target#*/}"
  BIN=headroom; [ "$GOOS" = "windows" ] && BIN=headroom.exe
  GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o "$BIN" ./cmd/headroom
  GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o "${BIN}.again" ./cmd/headroom
  cmp "$BIN" "${BIN}.again" || { echo "NOT REPRODUCIBLE $GOOS/$GOARCH"; exit 1; }
  rm "${BIN}.again"
  NAME="headroom-go-${VERSION}-${GOOS}-${GOARCH}"
  if [ "$GOOS" = "windows" ]; then zip -q "dist/${NAME}.zip" "$BIN"; ARCHIVE="dist/${NAME}.zip";
  else tar czf "dist/${NAME}.tar.gz" "$BIN"; ARCHIVE="dist/${NAME}.tar.gz"; fi
  shasum -a 256 "$ARCHIVE" | awk '{print $1}' > "${ARCHIVE}.sha256"
  rm "$BIN"
done
ls -l dist/
```

Expected: 12 files in `dist/` — 4 `.tar.gz`, 2 `.zip`, and 6 `.sha256`. No
`NOT REPRODUCIBLE` line. Paste the `ls -l dist/` output into the review.

Note: the workflow uses `sha256sum` (GNU, present on `ubuntu-latest`); macOS
has `shasum -a 256` instead. Both print the digest first, so the `awk '{print $1}'`
is identical. Do not change the workflow to `shasum` — the runner is Linux.

- [ ] **Step 4: Prove the artifact runs**

Unpack the native archive and start the MCP server against it. `mcp serve` reads
the protocol from stdin, so an empty stdin makes it exit cleanly instead of hanging:

```bash
tar xzf dist/headroom-go-v0.0.0-local-darwin-arm64.tar.gz -C /tmp
/tmp/headroom --version
/tmp/headroom mcp serve --ccr-backend memory < /dev/null; echo "exit=$?"
```

Expected: `--version` prints `headroom version v0.0.0-local`, proving the
`-X main.version` stamp reached the binary. `mcp serve` exits `0`.

- [ ] **Step 5: Confirm the archive layout Homebrew expects**

Run: `tar tzf dist/headroom-go-v0.0.0-local-linux-amd64.tar.gz`
Expected: exactly one line, `headroom`. The formula's `bin.install "headroom"`
requires the binary at the archive root, not under a directory.

- [ ] **Step 6: Clean up and commit**

```bash
rm -rf dist
git add .github/workflows/release.yml
git commit -m "ci: tag-push release workflow with reproducible cross-compiled artifacts"
```

---

## Task 2: Claude skill

**Files:**
- Create: `skills/headroom/SKILL.md`

**Interfaces:**
- Consumes: the three MCP tools `headroom_compress`, `headroom_retrieve`, `headroom_stats` as documented in `README.md`.
- Produces: nothing other tasks depend on.

**Background the implementer needs:**

The sibling repo's skill lives at `graphify-go/skills/graphify/SKILL.md`. It is a
usage guide for an installed CLI, with YAML frontmatter carrying only `name` and
`description`, where the description is written as a trigger list. Match that shape.

The tool contract, copied from `README.md`:

| Tool | Arguments | Returns |
|---|---|---|
| `headroom_compress` | `content` (required), `query` | `compressed`, `hash`, `content_type`, `original_tokens`, `compressed_tokens`, `bytes_saved`, `steps_applied`, `cache_keys` |
| `headroom_retrieve` | `hash` (required), `query` (reserved) | `found`, `source` (`local`, `proxy`, or `none`), `hash`, `content` |
| `headroom_stats` | none | Session counters: calls, bytes, tokens, and CCR hit rates |

Invariant I5: `headroom_compress` never returns text costing more tokens than its
input. When compression would not help it returns the original verbatim, with
`bytes_saved: 0` and an empty `hash`.

- [ ] **Step 1: Write the skill file**

Create `skills/headroom/SKILL.md` with exactly this content:

```markdown
---
name: headroom
description: Use when a command or tool has returned a large blob of output — a log file, a diff, a JSON payload, a search result, a test run — and you are about to read or quote it. Compress it through the headroom MCP server first, then read the compressed form. Triggers on large tool output, "this log is huge", "trim this diff", "too many tokens", "context is filling up", and on any output over a few hundred lines.
---

# headroom — compress tool output before you read it

Large tool output is the main way a context window fills up. headroom shrinks
logs, diffs, JSON, and search results by 60–95% while keeping the parts that
answer a question. The original is never lost: it is stored under a hash you
can retrieve.

## The loop

1. A tool returns a big blob.
2. Call `headroom_compress` with `content` set to that blob, and `query` set to
   what you are trying to find out. The query steers which lines survive.
3. Read the `compressed` field. Work from that.
4. If the compressed form dropped something you now need, call
   `headroom_retrieve` with the `hash` to get the original back.

## When to reach for it

- A log or test run over a few hundred lines.
- A diff you only need the shape of.
- A JSON API response with a long, repetitive array.
- A search or grep result with many hits.

## When not to

- Output under about 50 lines. The round trip costs more than it saves.
- Source code you are about to edit. Read the file directly; you need exact
  bytes and exact line numbers.
- Anything you must quote verbatim — an error string, a checksum, a command.
  Compression is not byte-preserving for the parts it rewrites.

## Reading the result

`headroom_compress` returns `original_tokens` and `compressed_tokens` alongside
`compressed`. It never returns something more expensive than what you gave it:
when compression would not help, you get the original back unchanged, with
`bytes_saved: 0` and an empty `hash`. An empty `hash` therefore means there is
nothing to retrieve, because nothing was replaced.

`steps_applied` names which transforms ran, and `content_type` names what
headroom decided the blob was. Both are useful when the result surprises you.

`headroom_stats` reports session totals — calls, bytes, tokens, and cache hit
rates. Use it to answer "how much did this actually save?", not as part of the
compress loop.

## If the tools are missing

The server is a local binary. Install and register it:

```bash
brew install dobbo-ca/taps/headroom-go
claude mcp add headroom -- headroom mcp serve
```

Compressed content is stored in a local SQLite file at `~/.headroom/ccr.db`.
Nothing leaves the machine.
```

- [ ] **Step 2: Verify the frontmatter parses**

Run:
```bash
python3 -c "
import sys,re
t=open('skills/headroom/SKILL.md').read()
m=re.match(r'^---\n(.*?)\n---\n', t, re.S)
assert m, 'no frontmatter block'
import yaml; d=yaml.safe_load(m.group(1))
assert set(d)=={'name','description'}, d.keys()
assert d['name']=='headroom', d['name']
print('ok:', d['name'], '-', len(d['description']), 'char description')
"
```
Expected: a line starting `ok: headroom -`. If `yaml` is not installed, run
`python3 -m pip install --quiet pyyaml` first.

- [ ] **Step 3: Check the claims against the code**

Every factual claim in the skill must hold. Verify these three by reading the
source, and paste what you found:

1. `~/.headroom/ccr.db` is the default CCR path — check `internal/paths` and the
   `--ccr-path` default in `cmd/headroom/mcp.go`.
2. The three tool names are spelled exactly as registered — grep `internal/mcp`
   for `headroom_compress`, `headroom_retrieve`, `headroom_stats`.
3. The I5 no-op behaviour (original returned verbatim, `bytes_saved: 0`, empty
   `hash`) matches what `internal/mcp` actually returns.

If any claim is wrong, fix the skill, not the code.

- [ ] **Step 4: Commit**

```bash
git add skills/headroom/SKILL.md
git commit -m "docs: add headroom Claude skill for compressing tool output"
```

---

## Task 3: README install path

**Files:**
- Modify: `README.md` — the `## Install` section, and the `## MCP server` section's build snippet.

**Interfaces:**
- Consumes: the archive and formula names fixed in Task 1 and Task 4.
- Produces: nothing other tasks depend on.

**Background the implementer needs:**

`README.md` currently has exactly this Install section:

```markdown
## Install

    go install github.com/dobbo-ca/headroom-go/cmd/headroom@latest
```

and the MCP server section currently opens with:

````markdown
## MCP server

```bash
go build -o headroom ./cmd/headroom
./headroom mcp serve
```
````

- [ ] **Step 1: Replace the Install section**

Replace the whole `## Install` section (heading and the indented `go install`
line) with:

````markdown
## Install

```bash
brew install dobbo-ca/taps/headroom-go
```

Or download a binary from [Releases](https://github.com/dobbo-ca/headroom-go/releases)
— darwin, linux, and windows, amd64 and arm64. Or build from source:

```bash
go install github.com/dobbo-ca/headroom-go/cmd/headroom@latest
```

Releases are cut by pushing a `v*` tag. Every binary is built with
`CGO_ENABLED=0` and stamped only with its version, so a rebuild from the same
tag produces the same bytes.
````

- [ ] **Step 2: Replace the build snippet in the MCP server section**

Replace the fenced block directly under `## MCP server` with:

````markdown
```bash
headroom mcp serve
```
````

Leave the rest of that section, including the `claude mcp add` line and the
tool table, untouched — except change `/path/to/headroom mcp serve` to
`headroom mcp serve`, since the binary is now on `PATH`.

- [ ] **Step 3: Add a skill pointer above `## License`**

Insert this section immediately before `## License`:

```markdown
## Claude skill

`skills/headroom/SKILL.md` teaches an agent when to route large tool output
through `headroom_compress` instead of reading it raw. Copy it into your
project's `.claude/skills/` or point your plugin at it.
```

- [ ] **Step 4: Verify no stale instructions remain**

Run: `grep -n "go build -o headroom\|/path/to/headroom" README.md`
Expected: no output. Any hit is a leftover from the pre-install-path README.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: brew and release install paths for headroom"
```

---

## Task 4: Homebrew tap formula (blocked on the first release)

**Files:**
- Create: `Formula/headroom-go.rb` in the **separate** repo `dobbo-ca/homebrew-taps`
  (local checkout: `~/work/dobbo-ca/homebrew-taps`).

**Interfaces:**
- Consumes: the four `.tar.gz` archives and their `.sha256` digests published by Task 1's `release` job.
- Produces: the file `update-formulas.yml` rewrites on every subsequent release.

**Background the implementer needs:**

This task **cannot be completed before a real release exists**, because the
formula must carry four real `sha256` digests of four real published archives.
Do not invent, placeholder, or zero-fill them. The order is:

```
merge Tasks 1-3  ->  push tag v0.1.0  ->  release job publishes archives
                                                   |
                                                   v
                             read the published .sha256 files  ->  this task
```

After the first seed, the tap's own workflow keeps the formula current; this
task never runs again.

The tap updater rewrites the formula with `sed`, so the file must keep this
exact shape — one `url`/`sha256` pair per `Hardware::CPU` branch, `version` on
its own line. `Formula/graphify-go.rb` in the same repo is the working template.

- [ ] **Step 1: Fetch the real digests**

```bash
cd ~/work/dobbo-ca/homebrew-taps
V=v0.1.0
for t in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
  echo "$t $(curl -sL https://github.com/dobbo-ca/headroom-go/releases/download/$V/headroom-go-$V-$t.tar.gz.sha256)"
done
```

Expected: four lines, each a target name and a 64-character hex digest. If any
digest is empty, the release did not publish that asset — stop and report it.

- [ ] **Step 2: Write the formula**

Create `Formula/headroom-go.rb`, substituting the four real digests from Step 1
for `<DARWIN_ARM64_SHA>` and friends, and the real tag for `v0.1.0`:

```ruby
class HeadroomGo < Formula
  desc "LLM context compression: shrink logs, diffs, and JSON before they reach the model"
  homepage "https://github.com/dobbo-ca/headroom-go"
  version "v0.1.0"
  license "Apache-2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/dobbo-ca/headroom-go/releases/download/v0.1.0/headroom-go-v0.1.0-darwin-arm64.tar.gz"
      sha256 "<DARWIN_ARM64_SHA>"
    end
    if Hardware::CPU.intel?
      url "https://github.com/dobbo-ca/headroom-go/releases/download/v0.1.0/headroom-go-v0.1.0-darwin-amd64.tar.gz"
      sha256 "<DARWIN_AMD64_SHA>"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/dobbo-ca/headroom-go/releases/download/v0.1.0/headroom-go-v0.1.0-linux-arm64.tar.gz"
      sha256 "<LINUX_ARM64_SHA>"
    end
    if Hardware::CPU.intel?
      url "https://github.com/dobbo-ca/headroom-go/releases/download/v0.1.0/headroom-go-v0.1.0-linux-amd64.tar.gz"
      sha256 "<LINUX_AMD64_SHA>"
    end
  end

  def install
    bin.install "headroom"
  end

  test do
    assert_match "headroom version", shell_output("#{bin}/headroom --version")
  end
end
```

- [ ] **Step 3: Prove the formula installs**

Run:
```bash
brew install --formula --build-from-source ~/work/dobbo-ca/homebrew-taps/Formula/headroom-go.rb
brew test headroom-go
headroom --version
```
Expected: install succeeds, `brew test` passes, and `--version` prints the tag.
A `SHA256 mismatch` here means Step 1's digest and the published archive
disagree — fix the digest, never the formula's `sha256` check.

- [ ] **Step 4: Verify the tap updater's sed patterns match this file**

The updater must be able to bump this formula. Dry-run its exact substitutions:

```bash
cd ~/work/dobbo-ca/homebrew-taps
cp Formula/headroom-go.rb /tmp/f.rb
FORMULA=headroom-go VERSION=v9.9.9
sed -i '' "s|version \".*\"|version \"$VERSION\"|" /tmp/f.rb
for t in darwin-arm64 darwin-amd64 linux-arm64 linux-amd64; do
  sed -i '' "s|${FORMULA}-v\?[0-9.]*-${t}.tar.gz|${FORMULA}-${VERSION}-${t}.tar.gz|g" /tmp/f.rb
done
sed -i '' "s|/download/v\?[0-9.]*[0-9]/|/download/${VERSION}/|g" /tmp/f.rb
grep -c "v9.9.9" /tmp/f.rb
```

Expected: `9` — one `version` line plus four URLs, each URL containing the tag
twice (download path and filename). Any lower number means a `sed` pattern does
not match this formula's shape, and the tap would silently ship a stale URL.

Note the `sed -i ''` form is BSD/macOS. The tap runner is Linux and uses
`sed -i`. Only the local dry-run needs the `''`.

- [ ] **Step 5: Commit on a branch and open a PR**

```bash
cd ~/work/dobbo-ca/homebrew-taps
git fetch origin
git switch -c feat/headroom-go-formula-6d2f origin/main
git add Formula/headroom-go.rb
git commit -m "feat: add headroom-go formula"
git push -u origin feat/headroom-go-formula-6d2f
gh pr create --repo dobbo-ca/homebrew-taps --fill
```

Do not merge without asking.

---

## Verification (run by the orchestrator, not an implementer)

Every gate below is run directly, not taken on report.

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test -race ./...
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build ./...
test -z "$(gofmt -l .)" || { gofmt -l .; false; }
go vet ./...
actionlint .github/workflows/release.yml
```

Then, after merge, execute the workflow itself and paste the run output:

```bash
gh workflow run release.yml --ref main -f tag=v0.1.0
gh run watch
```

## Known risks, to be reported rather than worked around

- `vars.GH_PUB_APP_CLIENT_ID` and `secrets.GH_PUB_APP_PEM` are not repo-level on
  `dobbo-ca/headroom-go`. They are not repo-level on `graphify-go` either, whose
  release workflow works, so they are organization-level. If the org has not
  shared them with `headroom-go`, or the App is not granted `homebrew-taps`,
  the `update-homebrew` job fails while `build` and `release` still succeed.
  That is the intended failure shape: the release ships, only the tap bump stalls.
- Task 4 cannot run until a tag exists. Tasks 1–3 are independently mergeable.
