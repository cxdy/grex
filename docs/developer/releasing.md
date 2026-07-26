# Releasing

grex uses [semantic versioning](https://semver.org/) driven by
[Conventional Commits](https://www.conventionalcommits.org/). Maintainers cut
releases by pushing a version tag; [GoReleaser](https://goreleaser.com/) builds
binaries and container images from that tag.

Published notes and install coordinates are mirrored on the docs site under
[Releases](../releases/index.md) (changelog regenerated on each docs build
from GitHub Releases).

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

3. Create and push the tag (requires a **clean** working tree):

   ```sh
   just release-tag
   ```

   That recipe:

   - Fetches remote tags so `svu` sees the latest release
   - Runs `svu next` (for example `v0.2.0`)
   - Bumps `deploy/charts/grex/Chart.yaml` `version` and `appVersion` to the
     SemVer **without** the leading `v` (for example `0.2.0`), and commits
     `chore(release): bump helm chart to …` when they change
   - Creates the git tag on that commit
   - Pushes the commit and the tag to `origin`

4. Watch CI:

   - **GoReleaser** (tag) — binaries, container image, chart `.tgz` on the
     GitHub Release, chart OCI push to GHCR
   - **docs** (push to `main`, path-filtered) — rebuilds Pages, including
     packaging the chart into `https://dennisme.github.io/grex/charts/`

Do **not** create release tags by hand unless you are fixing a one-off mistake;
`just release-tag` keeps app, image, and chart versions aligned.

### Manual equivalent

```sh
git fetch --tags
TAG=$(svu next)
VER=${TAG#v}
# bump deploy/charts/grex/Chart.yaml version + appVersion to $VER and commit
git tag "$TAG"
git push origin HEAD "$TAG"
```

## What the release pipeline publishes

Configuration lives in `.goreleaser.yaml` at the repository root. On a
version tag, GoReleaser (plus the workflow’s Helm steps) publishes:

| Artifact | Destination |
|----------|-------------|
| Cross-compiled binaries (linux / darwin / windows × amd64 / arm64) | GitHub Release assets (archives + `checksums.txt`) |
| Container images | GHCR (`ghcr.io/dennisme/grex`) multi-arch (amd64 + arm64) |
| Helm chart package (`.tgz`) | GitHub Release asset |
| Helm chart (OCI) | `oci://ghcr.io/dennisme/charts/grex` |
| Helm chart (Pages index) | `https://dennisme.github.io/grex/charts/` via the **docs** workflow after the chart bump lands on `main` |
| Release notes (changelog) | GitHub Release body |

Image tags include `{{ .Version }}` (no `v`, matches chart `appVersion`),
`{{ .Tag }}` (with `v`), major/minor convenience tags, and `latest`. The chart
defaults `image.tag` to `Chart.AppVersion`, so a stock `helm install` pulls
the matching GHCR image.

Binaries embed version metadata via `internal/buildinfo` ldflags (same package
as `just build`). The release image is built from `Dockerfile.goreleaser`
(copies the GoReleaser binary into Alpine). The root `Dockerfile` remains for
local source builds and compose/Helm smoke tests.

### Helm chart versioning

| Field | Source | Meaning |
|-------|--------|---------|
| Chart `version` | bumped by `just release-tag` | Helm package / repo version |
| Chart `appVersion` | same SemVer string | Default container image tag |
| Git tag | `svu next` (with `v` prefix) | Triggers GoReleaser |

For grex 1.0, **chart version and app version stay equal** to the grex release.
Chart-only fixes still ship as a normal grex patch release (no separate chart
version stream yet).

**Install after a release:**

```sh
# Chart repository on GitHub Pages (updated when main deploys docs)
helm repo add grex https://dennisme.github.io/grex/charts/
helm repo update
helm upgrade --install grex grex/grex --version 0.2.0 -n grex --create-namespace

# Or pin an OCI artifact from GHCR (available as soon as the tag workflow finishes)
helm upgrade --install grex oci://ghcr.io/dennisme/charts/grex --version 0.2.0 \
  -n grex --create-namespace
```

Pages always packages the chart version currently on `main`. Historical
`.tgz` files are kept on each GitHub Release and on GHCR (OCI).

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
4. **PR references:** keep `(#123)` in squash-merge subjects; GitHub’s release
   UI auto-links `#123` to the pull request (GoReleaser’s changelog `format`
   template cannot run regex rewrites)
5. **Footer** adds a compare URL (full changelog), Docker image, Helm install,
   and docs links

**Squash and merge** PRs with a conventional subject so each line is readable
and carries a PR number, for example:

```text
feat(ui): add attribute chips (#31)
```

becomes roughly:

```markdown
### ✨ Features
- feat(ui): add attribute chips (#31) (@someone)
```

Merge-commit subjects (`Merge pull request #…`) are excluded so the notes
list the feature commits (with PR numbers when squash-merged), not the merge
wrapper.

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
  lists the new tag, binary archives, and `grex-<version>.tgz`
- Confirm the GHCR image `ghcr.io/dennisme/grex:<version>` exists
- Confirm the OCI chart `oci://ghcr.io/dennisme/charts/grex` is pullable at
  that version
- Confirm the docs workflow on `main` finished so
  [the chart repo](https://dennisme.github.io/grex/charts/) indexes the new
  chart (path-filtered; needs the chart bump commit on `main`)
- Confirm the docs workflow also ran on the **release published** event so
  [Releases → Changelog](../releases/changelog.md) includes the new notes

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
