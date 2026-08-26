#!/usr/bin/env bash
# Release Orion from a developer machine, using the gh login already present.
#
# Why this exists alongside the GitHub Actions workflow: Actions runs on
# GitHub's servers with no access to your local gh credentials, so it needs
# personal access tokens for the releases repo, the tap and the bucket. For a
# single-maintainer tool that is three secrets to create, store and rotate in
# order to do something your own authenticated CLI can already do.
#
# The trade Actions would have given you is a clean-machine build. This script
# buys back what it can: it refuses to release from a dirty tree, refuses from
# the wrong branch, and runs the full gate before anything is published. What
# it cannot prove is that the build works somewhere other than here.
#
# Usage: scripts/release.sh v0.1.0 [--dry-run]
set -euo pipefail

VERSION_TAG="${1:-}"
DRY_RUN=""
[ "${2:-}" = "--dry-run" ] && DRY_RUN=1

SOURCE_REPO="NjAIAgents/orion"
RELEASES_REPO="NjAIAgents/orion-releases"
TAP_REPO="navjyotnishant/homebrew-tap"
BUCKET_REPO="navjyotnishant/scoop-bucket"
# Overridable for repositories that name their release branch differently,
# and so the script's later stages can be exercised without first merging.
RELEASE_BRANCH="${ORION_RELEASE_BRANCH:-main}"

die() { echo "ERROR: $*" >&2; exit 1; }
step() { echo; echo "==> $*"; }
run() { if [ -n "$DRY_RUN" ]; then echo "    [dry-run] $*"; else "$@"; fi; }

[ -n "$VERSION_TAG" ] || die "usage: $0 vX.Y.Z [--dry-run]"
case "$VERSION_TAG" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) die "tag must look like v1.2.3, got '$VERSION_TAG'" ;;
esac
VERSION="${VERSION_TAG#v}"

# ---------------------------------------------------------------- preflight

step "Preflight"
command -v gh  >/dev/null || die "gh not found"
command -v go  >/dev/null || die "go not found"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated. Run: gh auth login"

# Push access to every target, checked up front. Discovering halfway through
# that the tap is unwritable leaves a published release and a stale formula,
# which is a worse state than not having started.
for r in "$RELEASES_REPO" "$TAP_REPO" "$BUCKET_REPO"; do
  perm="$(gh api "repos/$r" --jq '.permissions.push' 2>/dev/null || echo false)"
  [ "$perm" = "true" ] || die "no push access to $r (gh account: $(gh api user --jq .login))"
  echo "    push ok: $r"
done

branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = "$RELEASE_BRANCH" ] || die "releases are cut from $RELEASE_BRANCH, you are on $branch.
  Merge develop into $RELEASE_BRANCH through a pull request first."

[ -z "$(git status --porcelain)" ] || die "working tree is dirty. A release must be reproducible from a commit."

git fetch -q origin "$RELEASE_BRANCH"
[ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/$RELEASE_BRANCH")" ] \
  || die "$RELEASE_BRANCH differs from origin/$RELEASE_BRANCH. Push or pull first."

if git rev-parse "$VERSION_TAG" >/dev/null 2>&1; then
  die "tag $VERSION_TAG already exists locally"
fi
if gh release view "$VERSION_TAG" --repo "$RELEASES_REPO" >/dev/null 2>&1; then
  die "release $VERSION_TAG already published to $RELEASES_REPO"
fi

# ---------------------------------------------------------------- the gate

step "Gate: build, vet, gofmt, tests"
go build ./...
go vet ./...
unformatted="$(gofmt -l . || true)"
[ -z "$unformatted" ] || die "gofmt would change: $unformatted"
go test ./... >/dev/null
echo "    all green"

# ---------------------------------------------------------------- artifacts

step "Building archives for $VERSION_TAG"
# The tag must exist before `make dist`, because VERSION comes from
# git describe. Building first would stamp the binaries with the previous
# tag plus a commit count, and the archive names would not match the
# manifests rendered a moment later.
run git tag -a "$VERSION_TAG" -m "orion $VERSION_TAG"
if [ -n "$DRY_RUN" ]; then
  echo "    [dry-run] make dist && scripts/render-packaging.sh $VERSION"
else
  make dist
  scripts/render-packaging.sh "$VERSION"
  ls -1 dist/*.tar.gz dist/*.zip dist/checksums.txt
fi

# ---------------------------------------------------------------- publish

step "Publishing to $RELEASES_REPO"
run git push origin "$VERSION_TAG"
if [ -n "$DRY_RUN" ]; then
  echo "    [dry-run] gh release create $VERSION_TAG --repo $RELEASES_REPO ..."
else
  gh release create "$VERSION_TAG" \
    --repo "$RELEASES_REPO" \
    --title "orion $VERSION_TAG" \
    --notes "Binaries for orion $VERSION_TAG. Source is maintained privately; each archive includes LICENSE and NOTICE." \
    dist/orion_*.tar.gz dist/orion_*.zip dist/checksums.txt
fi

publish_manifest() {
  local repo="$1" src="$2" dest="$3"
  local tmp; tmp="$(mktemp -d)"
  step "Updating $repo"
  if [ -n "$DRY_RUN" ]; then
    echo "    [dry-run] copy $src -> $repo:$dest and push"
    return
  fi
  gh repo clone "$repo" "$tmp/r" -- --depth 1 -q
  mkdir -p "$(dirname "$tmp/r/$dest")"
  cp "$src" "$tmp/r/$dest"
  git -C "$tmp/r" add "$dest"
  if git -C "$tmp/r" diff --staged --quiet; then
    echo "    unchanged, nothing to push"
  else
    git -C "$tmp/r" commit -q -m "orion $VERSION"
    git -C "$tmp/r" push -q
    echo "    pushed $dest"
  fi
  rm -rf "$tmp"
}

publish_manifest "$TAP_REPO"    "dist/packaging/orion.rb"   "Formula/orion.rb"
publish_manifest "$BUCKET_REPO" "dist/packaging/orion.json" "bucket/orion.json"

step "Done"
echo "  release   https://github.com/$RELEASES_REPO/releases/tag/$VERSION_TAG"
echo "  verify    brew update && brew install navjyotnishant/tap/orion"
# An `[ ... ] && echo` as the final statement exits 1 when the test is false,
# and under `set -e` that becomes the script's exit code: a successful release
# reporting failure. Use an if, not a short-circuit, in tail position.
if [ -n "$DRY_RUN" ]; then
  echo "  (dry run: nothing was tagged, pushed or published)"
fi
