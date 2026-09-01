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
# TWO CHANNELS, and they are not symmetrical (OR-116):
#
#   production  cut from main, tagged vX.Y.Z, updates the tap and the bucket
#   beta        cut from develop, tagged vX.Y.Z-beta.N, marked prerelease on
#               the forge, and touches NEITHER installer
#
# The tap and the bucket never move on a beta. That is the whole rule: a
# production user runs `brew upgrade`, and an installer that can serve a
# prerelease hands them a build nobody promoted. Semver makes it worse rather
# than better -- v0.9.0-beta.1 sorts BELOW v0.9.0 -- so the mistake is not
# self-correcting on the next release either.
#
# Usage: scripts/release.sh v0.1.0 [--dry-run]
#        scripts/release.sh v0.1.0-beta.1 --beta [--dry-run]
set -euo pipefail

VERSION_TAG=""
DRY_RUN=""
CHANNEL="production"

die() { echo "ERROR: $*" >&2; exit 1; }
step() { echo; echo "==> $*"; }
run() { if [ -n "$DRY_RUN" ]; then echo "    [dry-run] $*"; else "$@"; fi; }

# A loop rather than positional $2, so --beta and --dry-run can be given in
# either order. `scripts/release.sh v1.0.0 --beta --dry-run` reading --beta as
# "not --dry-run" and publishing for real is exactly the class of mistake this
# script exists to make impossible.
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --beta)    CHANNEL="beta" ;;
    -*)        die "unknown option '$arg' (usage: $0 vX.Y.Z [--beta] [--dry-run])" ;;
    *)         [ -z "$VERSION_TAG" ] || die "two versions given: '$VERSION_TAG' and '$arg'"
               VERSION_TAG="$arg" ;;
  esac
done

SOURCE_REPO="NjAIAgents/orion"
RELEASES_REPO="NjAIAgents/orion-releases"
TAP_REPO="navjyotnishant/homebrew-tap"
BUCKET_REPO="navjyotnishant/scoop-bucket"
# Overridable for repositories that name their branches differently, and so
# the script's later stages can be exercised without first merging.
if [ "$CHANNEL" = "beta" ]; then
  RELEASE_BRANCH="${ORION_WORK_BRANCH:-develop}"
  # Only the releases repo is written on a beta, so only it is required.
  PUSH_TARGETS="$RELEASES_REPO"
else
  RELEASE_BRANCH="${ORION_RELEASE_BRANCH:-main}"
  PUSH_TARGETS="$RELEASES_REPO $TAP_REPO $BUCKET_REPO"
fi

[ -n "$VERSION_TAG" ] || die "usage: $0 vX.Y.Z [--beta] [--dry-run]"
# The tag shape is per channel, and a mismatch names BOTH sides. "tag must
# look like v1.2.3" is unhelpful when the caller's actual mistake was asking
# for the wrong channel, which is the more common of the two.
# The SHAPES live in scripts/tag-channel.sh, which is also what the release
# workflow uses to derive a channel it was not told (OR-255). Two copies of
# "is this a beta" drift, and the direction they drift in is a prerelease
# reaching the tap. What stays here is the comparison and its wording: this
# script is TOLD the channel and checks the tag agrees, which is a different
# question from the workflow's, and the mismatch names BOTH sides -- "tag must
# look like v1.2.3" is unhelpful when the caller's actual mistake was asking
# for the wrong channel, which is the more common of the two.
#
# ${0%/*} rather than `dirname $0`: this script runs under a deliberately
# minimal PATH in its own tests, and dirname is not on it. Parameter expansion
# is a shell builtin and cannot be missing.
IMPLIED="$("${0%/*}/tag-channel.sh" "$VERSION_TAG")" || die "$VERSION_TAG is not a release tag.
  Expected vX.Y.Z for production, or vX.Y.Z-beta.N for a beta."
if [ "$IMPLIED" != "$CHANNEL" ]; then
  case "$CHANNEL" in
    production) die "'$VERSION_TAG' is a prerelease tag and this is the production channel.
  A prerelease reaching the tap means 'brew upgrade' hands a beta to a stable
  user. Pass --beta to cut it as one." ;;
    beta) die "a beta tag must look like v1.2.3-beta.4 so it sorts below v1.2.3
  under semver, got '$VERSION_TAG'" ;;
  esac
fi
VERSION="${VERSION_TAG#v}"

# ---------------------------------------------------------------- preflight

step "Preflight ($CHANNEL channel, from $RELEASE_BRANCH)"
command -v gh  >/dev/null || die "gh not found"
command -v go  >/dev/null || die "go not found"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated. Run: gh auth login"

# Push access to every target this channel writes, checked up front.
# Discovering halfway through that the tap is unwritable leaves a published
# release and a stale formula, which is worse than not having started.
for r in $PUSH_TARGETS; do
  perm="$(gh api "repos/$r" --jq '.permissions.push' 2>/dev/null || echo false)"
  [ "$perm" = "true" ] || die "no push access to $r (gh account: $(gh api user --jq .login))"
  echo "    push ok: $r"
done

# The channel/branch pairing, named on BOTH sides when it is wrong. Being told
# "you are on develop" is not actionable until the reader knows which channel
# wanted which branch -- and getting this pair backwards is the mistake that
# publishes a beta as production.
branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$branch" != "$RELEASE_BRANCH" ]; then
  if [ "$CHANNEL" = "beta" ]; then
    die "a beta is cut from $RELEASE_BRANCH only, and you are on $branch."
  fi
  die "a production release is cut from $RELEASE_BRANCH only, and you are on $branch.
  Promote $branch through a pull request first, or pass --beta to cut a
  prerelease from where you are."
fi

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

# ------------------------------------------------------------ release notes

# Release notes come from CHANGELOG.md, not from a generic sentence.
#
# The generic line ("Binaries for orion vX.Y.Z") is the same for every
# release, which makes the release page useless for the one question it is
# asked: should I upgrade, and what changes if I do. Thirty-three commits
# deserve better than a filename.
#
# Falls back to the generic line when there is no matching section, because a
# missing changelog entry must not block a release -- but it says so, so the
# omission is visible rather than silent.
notes_file="$(mktemp)"
extract_notes() {
  [ -f CHANGELOG.md ] || return 1
  awk -v tag="## $VERSION_TAG" '
    $0 == tag { found = 1; next }
    found && /^## / { exit }
    found { print }
  ' CHANGELOG.md > "$notes_file"
  [ -s "$notes_file" ]
}

if extract_notes; then
  echo "    notes from CHANGELOG.md ($(wc -l < "$notes_file" | tr -d " ") lines)"
elif [ "$CHANNEL" = "beta" ]; then
  # EXPECTED on a beta, not a warning. Fragments are collated into
  # CHANGELOG.md at the production release, so a prerelease cut from develop
  # legitimately has no section of its own, and telling the operator to
  # generate one would have them collate a version that has not shipped.
  echo "    no CHANGELOG.md section (expected for a prerelease); listing what is new"
  {
    printf 'Prerelease from %s. Not published to the Homebrew tap or the Scoop bucket.\n\n' \
      "$RELEASE_BRANCH"
    printf 'Commits since the last production release:\n\n'
    git log --format='- %h %s' "origin/${ORION_RELEASE_BRANCH:-main}..HEAD" 2>/dev/null || true
  } > "$notes_file"
else
  echo "WARNING: no '## $VERSION_TAG' section in CHANGELOG.md; using a generic note." >&2
  echo "         Generate one with: orion changelog --version $VERSION_TAG" >&2
  printf 'Binaries for orion %s. Each archive includes LICENSE and NOTICE.\n' \
    "$VERSION_TAG" > "$notes_file"
fi

# ---------------------------------------------------------------- the gate

step "Gate: build, vet, gofmt, tests"
# The gate lives in its own script (OR-259) so it can be run against a
# deliberately broken tree in a test. It names the step that failed and shows
# what that step printed, which this block did not: it ended in
# `go test ./... >/dev/null`, and a v0.8.9 release stopped here -- promotion
# already merged -- with "exit status 1" as the entire account of why.
"${0%/*}/release-gate.sh"

# ---------------------------------------------------------------- artifacts

step "Building archives for $VERSION_TAG"
# The tag must exist before `make dist`, because VERSION comes from
# git describe. Building first would stamp the binaries with the previous
# tag plus a commit count, and the archive names would not match the
# manifests rendered a moment later.
run git tag -a "$VERSION_TAG" -m "orion $VERSION_TAG"
if [ -n "$DRY_RUN" ]; then
  echo "    [dry-run] make dist"
  [ "$CHANNEL" = "beta" ] || echo "    [dry-run] scripts/render-packaging.sh $VERSION"
else
  make dist
  # The formula and the manifest are only rendered on the channel that
  # publishes them. Rendering them for a beta would leave a valid-looking
  # orion.rb in dist/ pointing at a prerelease -- one copy away from the tap.
  [ "$CHANNEL" = "beta" ] || scripts/render-packaging.sh "$VERSION"
  ls -1 dist/*.tar.gz dist/*.zip dist/checksums.txt
fi

# ---------------------------------------------------------------- publish

step "Publishing to $RELEASES_REPO"
run git push origin "$VERSION_TAG"
# --prerelease is what keeps a beta out of "latest" on the forge, which is
# what `gh release download` and every naive script resolve to.
prerelease_flag=""
[ "$CHANNEL" = "beta" ] && prerelease_flag="--prerelease"
if [ -n "$DRY_RUN" ]; then
  echo "    [dry-run] gh release create $VERSION_TAG --repo $RELEASES_REPO $prerelease_flag ..."
else
  gh release create "$VERSION_TAG" \
    --repo "$RELEASES_REPO" \
    --title "orion $VERSION_TAG" \
    --notes-file "$notes_file" \
    $prerelease_flag \
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

if [ "$CHANNEL" = "beta" ]; then
  step "Not touching $TAP_REPO or $BUCKET_REPO"
  echo "    a beta never reaches a production installer: 'brew upgrade' would"
  echo "    otherwise hand this build to a stable user, and semver would keep"
  echo "    offering it as an upgrade because $VERSION_TAG sorts below ${VERSION_TAG%%-*}."
else
  publish_manifest "$TAP_REPO"    "dist/packaging/orion.rb"   "Formula/orion.rb"
  publish_manifest "$BUCKET_REPO" "dist/packaging/orion.json" "bucket/orion.json"
fi

step "Done"
echo "  release   https://github.com/$RELEASES_REPO/releases/tag/$VERSION_TAG"
if [ "$CHANNEL" = "beta" ]; then
  # NOT the brew line. Printing it here would tell the operator to verify a
  # prerelease by installing from a tap that deliberately does not have it,
  # and the install they got would be the previous production build.
  echo "  verify    gh release download $VERSION_TAG --repo $RELEASES_REPO"
  echo "  note      prerelease: the tap and the bucket still serve production"
else
  echo "  verify    brew update && brew install navjyotnishant/tap/orion"
fi
# An `[ ... ] && echo` as the final statement exits 1 when the test is false,
# and under `set -e` that becomes the script's exit code: a successful release
# reporting failure. Use an if, not a short-circuit, in tail position.
if [ -n "$DRY_RUN" ]; then
  echo "  (dry run: nothing was tagged, pushed or published)"
fi
