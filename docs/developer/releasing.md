# Releasing

grex uses [semantic versioning](https://semver.org/) driven by
[Conventional Commits](https://www.conventionalcommits.org/). Maintainers cut
releases by pushing a version tag; [GoReleaser](https://goreleaser.com/) builds
binaries and container images from that tag.

## Prerequisites

- Push access to the canonical repository (`dennisme/grex`)
- A clean `main` (or the commit you intend to ship), with CI green
- [`svu`](https://github.com/caarlos0/svu) installed:

  ```sh
  brew install svu
  # or:
  go install github.com/caarlos0/svu/v3@latest
  ```

## How the version is chosen

[`svu next`](https://github.com/caarlos0/svu) inspects git history since the
latest tag and prints the next version:

| Commit shape | Version bump |
|--------------|--------------|
| `fix: …` (no breaking) | **patch** |
| `feat: …` (no breaking) | **minor** |
| `BREAKING CHANGE:` footer, or `type!:` | **major** |
| `chore:`, `doc:`, `ci:`, `test:`, … | no bump (same as current tag) |

grex validates commit messages with commitizen (see
[Contributing](contributing.md)). Prefer types that match that schema so both
CI and `svu` agree.

Preview without tagging:

```sh
svu current   # latest tag, if any
svu next      # what the next release tag would be
```

## Cut a release

1. Ensure the commit you want is on the default branch (or checkout that SHA).
2. Confirm the next tag looks right:

   ```sh
   svu next
   ```

3. Create and push the tag:

   ```sh
   just release-tag
   ```

   That target:

   - Fetches remote tags so `svu` sees the latest release
   - Runs `svu next` and creates a local tag (for example `v0.2.0`)
   - Pushes the tag to `origin`

4. Watch the **GoReleaser** workflow (`.github/workflows/goreleaser.yaml`) on
   the tag. When it succeeds, the GitHub Release and published artifacts are
   ready.

Do **not** create release tags by hand unless you are fixing a one-off mistake;
`just release-tag` keeps numbering aligned with commit history.

### Manual equivalent

```sh
git fetch --tags
git tag "$(svu next)"
git push origin "$(svu next)"
```

## What the release pipeline publishes

Configuration lives in `.goreleaser.yaml` at the repository root. On a
version tag, GoReleaser is expected to:

| Artifact | Destination |
|----------|-------------|
| Cross-compiled binaries (linux / darwin / windows × amd64 / arm64) | GitHub Release assets (archives + `checksums.txt`) |
| Container images | GHCR (`ghcr.io/dennisme/grex`) multi-arch (amd64 + arm64) |
| Release notes (changelog) | GitHub Release body |

Image tags include the full semver tag, `vMAJOR`, `vMAJOR.MINOR`, and
`latest`. Binaries embed version metadata via `internal/buildinfo` ldflags
(same package as `just build`).

The release image is built from `Dockerfile.goreleaser` (copies the
GoReleaser binary into Alpine). The root `Dockerfile` remains for local
source builds and compose/Helm smoke tests.

### Pretty release notes (changelog)

GoReleaser builds the GitHub Release body from commits since the previous tag
(see the `changelog` block in `.goreleaser.yaml`):

1. **Source:** GitHub compare API (`changelog.use: github`) so entries can
   include author `@handles`
2. **Grouping** by conventional-commit type (aligned with
   [Contributing](contributing.md) / commitizen):

   | Commit type | Release notes section |
   |-------------|------------------------|
   | `feat` | ✨ Features |
   | `fix` | 🐛 Bug Fixes |
   | `perf` | ⚡ Performance |
   | `ref` | ♻️ Refactors |
   | `revert` | ⏪ Reverts |
   | other (not filtered out) | Other |

3. **Filters** drop noise: `doc`/`docs`, `test`, `chore`, `ci`, `build`,
   `style`, `bump`, and merge commits
4. **Footer** adds Docker image and docs links

Squash-merge PRs with a conventional subject (for example
`feat(ui): add attribute chips`) so the notes stay readable. Merge-commit
subjects are excluded.

**First release / missing previous tag on GitHub:** the `github` changelog
backend needs both tags on the remote. If notes generation fails, temporarily
set `changelog.use: git` in `.goreleaser.yaml`, or create an initial tag on an
earlier commit.

**Preview locally** (optional, needs GoReleaser installed):

```sh
goreleaser changelog
# or a full dry-run without publishing:
goreleaser release --snapshot --clean --skip=publish
```

## Checklist before tagging

- [ ] Target commit is what you intend to ship (usually tip of `main`)
- [ ] Conventional commits since the last tag reflect the intended bump
- [ ] `just test` / `just lint` (and any relevant chart or compose checks) pass
- [ ] Docs and user-facing notes match the change set
- [ ] `svu next` shows the version you expect
- [ ] You are pushing the tag to the repository that runs the release workflow

## After the release

- Confirm the [GitHub Releases](https://github.com/dennisme/grex/releases) page
  lists the new tag and assets
- Confirm the GHCR image exists for that tag (when the Docker steps are enabled)
- If operators install via Helm, chart `appVersion` / image tag may need a
  follow-up bump once chart publishing is wired to the same release flow

## Troubleshooting

**`svu` not found**  
Install it (see [Prerequisites](#prerequisites)).

**Tag already exists**  
`just release-tag` refuses to retag. Either there is nothing bump-worthy since
the last release, or your local tags are stale—run `git fetch --tags` and
check `svu current` / `svu next`.

**Wrong bump level**  
`svu` only sees commit messages. A user-facing break needs `feat!:` / `fix!:`
or a `BREAKING CHANGE:` footer; pure `feat:` commits stay on minor.

**Workflow did not run**  
Confirm the tag was pushed to the repo that owns
`.github/workflows/goreleaser.yaml`, and that the workflow triggers on that
tag pattern.

## Related

- [Contributing](contributing.md) — conventional commits and PR CI
- [Install](../admin/install.md) — how operators obtain binaries and images
- Design notes on versioning and GoReleaser in the
  [SPEC design](../spec/design.md)
