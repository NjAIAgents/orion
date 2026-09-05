#!/bin/sh
# Tag the SOURCE repository for a release, idempotently.
#
#   scripts/tag-source.sh vX.Y.Z <commit>
#
# WHY THIS EXISTS (OR-304). The release workflow took a tag as free-text input
# and used it only to NAME things -- the channel, the package-manager version
# string, the dist archive names -- and never created a tag. v0.8.10 shipped
# that way: assets published to NjAIAgents/orion-releases, nothing in the
# source repo naming the commit they were built from. `git log v0.8.10` failed,
# and the only record tying the version to a commit was a release object in a
# different repository. Every local build after it was mislabelled too, because
# `make build` derives VERSION from `git describe --tags`, so develop reported
# itself as commits-past-v0.8.9 while v0.8.10 was in people's hands.
#
# It failed silently and stayed failed: release-gate.sh verifies CI is green
# BEFORE a release, and nothing verified afterwards that the tag existed.
#
# TWO CALLERS, ONE DEFINITION, for the same reason tag-channel.sh has two:
#
#   .github/workflows/    tags the commit it just built, before publishing any
#   release.yml           asset, so nothing ships that cannot be traced back.
#   an operator           backfills a release that was published untagged, or
#                         repairs one by hand.
#
# WHAT IT REFUSES TO DO. It never moves a tag. A tag that already names a
# different commit is an error, not something to force past: the version is
# already published under the old meaning, and re-pointing it silently rewrites
# what every checkout, every `git describe` and every archived build referred
# to. Refusing costs one confused operator; moving costs a fleet of machines
# that disagree about what v0.8.10 was.
#
# RE-RUNNING A RELEASE IS SAFE. A tag that already names THIS commit is the
# expected state of a second run, not a failure -- exit 0 and say so. A release
# workflow that could only ever be dispatched once would make every transient
# publish failure permanent.

set -eu

# The directory this script lives in, for finding its siblings.
#
# ${0%/*} alone was not enough. It strips to the last FORWARD slash, and on
# Windows $0 arrives as a backslash path containing none -- so the expansion
# returned the whole path unchanged and the caller asked for
# ...\tag-source.sh/tag-channel.sh, which does not exist. Measured on the
# Windows CI leg: eight tests, exit 127 (OR-346). An operator running the
# backfill by hand there would have hit the same wall.
#
# Backslashes are folded to forward slashes first; every Windows API and
# every shell here accepts them, so one form is enough for the strip below.
#
# Still parameter expansion rather than `dirname`: these scripts run under a
# deliberately minimal PATH in their own tests and dirname is not on it, so a
# builtin is the only thing guaranteed to be present.
here() {
	d=$1
	# Strip the leaf after whichever separator comes LAST. Two expansions
	# rather than a tr: tr is not a builtin and these scripts run under a
	# starved PATH in their own tests, which is the same reason dirname was
	# ruled out. ${d##*/} and ${d##*\\} each remove everything up to their
	# own separator, so the SHORTER result came from the later one.
	a=${d##*/}
	b=${d##*\\}
	if [ ${#a} -le ${#b} ]; then leaf=$a; else leaf=$b; fi
	case $d in
	"$leaf") printf '.' ;;
	*) printf '%s' "${d%"$leaf"}." ;;
	esac
}

tag="${1-}"
commit="${2-}"

if [ -z "$tag" ] || [ -z "$commit" ]; then
	echo "usage: $0 vX.Y.Z <commit>" >&2
	exit 2
fi

# Ask the shared classifier rather than growing a second copy of the shapes.
# It prints the channel; here only its verdict matters -- a string that names
# no channel is not a release tag, and tagging the source repo with one puts a
# name into 70-odd tags' worth of history that nothing else will ever resolve.
"$(here "$0")/tag-channel.sh" "$tag" >/dev/null

# Peel to a commit and fail loudly on anything unresolvable. A backfill is
# given a SHA by hand and the whole point is that it lands on the commit that
# was actually released, not on whatever a branch name resolves to today.
#
# --quiet suppresses git's own "fatal: Needed a single revision", which never
# names the reference it choked on. An operator who typo'd a SHA into a
# backfill gets a sentence about revisions and no way to tell WHICH argument
# was wrong; every other refusal here names the commits involved, and this one
# is the likeliest to be hit by hand.
if ! sha="$(git rev-parse --quiet --verify "${commit}^{commit}" 2>/dev/null)"; then
	echo "cannot resolve '${commit}' to a commit in this repository.

  Nothing was tagged. A release tag has to name the commit that was actually
  built, so an unresolvable reference is refused rather than guessed at -- a
  tag on the wrong commit is harder to notice, and harder to undo, than the
  missing tag this script exists to prevent.

  Check the SHA. If this is a shallow clone, fetch the commit first." >&2
	exit 1
fi

# The remote is the source of truth, not the local tag list: a clone can be
# missing a tag that exists, or hold one that was never pushed.
if git ls-remote --exit-code --tags origin "refs/tags/${tag}" >/dev/null 2>&1; then
	# Fetch into FETCH_HEAD rather than refs/tags/, so inspecting a published
	# tag cannot leave a rewritten one behind in the caller's clone.
	git fetch --quiet origin "refs/tags/${tag}"
	published="$(git rev-parse --verify 'FETCH_HEAD^{commit}')"
	if [ "$published" = "$sha" ]; then
		echo "tag ${tag} already published on ${sha}; nothing to do"
		exit 0
	fi
	echo "refusing to move ${tag}.

  published on  ${published}
  this run has  ${sha}

  ${tag} is already released under the first commit. Moving it would rewrite
  what every existing checkout, archive and 'git describe' means by ${tag}.
  Cut a new version instead, or delete the release deliberately by hand if it
  was published in error." >&2
	exit 1
fi

# Not on the remote. A local tag of the same name is either the same intent
# repeated -- push it -- or a stale one from an abandoned attempt, which gets
# the same refusal as a published one rather than being quietly overwritten.
if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null 2>&1; then
	local_sha="$(git rev-parse --verify "refs/tags/${tag}^{commit}")"
	if [ "$local_sha" != "$sha" ]; then
		echo "refusing to move ${tag}.

  local tag on  ${local_sha}
  this run has  ${sha}

  An unpushed ${tag} already exists here on a different commit. Delete it
  deliberately with 'git tag -d ${tag}' if it was a mistake." >&2
		exit 1
	fi
else
	# An annotated tag records a tagger, so an identity has to exist. A
	# runner with none makes git either fail with its own opaque message or
	# -- on a host where it can guess -- stamp the release with a
	# synthesised user@hostname that names nobody.
	#
	# Checked rather than assumed: the workflow's `configure git` step sets
	# one, and this catches the case where it did not run, or ran into a
	# different HOME than this script sees.
	# NOT `git var GIT_COMMITTER_IDENT`: that SUCCEEDS with a guess. With no
	# configured identity git synthesises one from the OS -- on a laptop
	# "Name <user@Hostname.local>", on a runner "runner@fv-az123.internal" --
	# and stamps the release with a tagger nobody can be reached at. The
	# guess is what has to be caught, so ask for the configured values.
	if [ -z "${GIT_COMMITTER_NAME:-}$(git config --get user.name || true)" ] ||
		[ -z "${GIT_COMMITTER_EMAIL:-}$(git config --get user.email || true)" ]; then
		echo "refusing to tag ${tag}: no committer identity.

  An annotated tag records who made it, and nothing here can say who that is.
  Set user.name and user.email, or pass GIT_COMMITTER_NAME and
  GIT_COMMITTER_EMAIL, and run this again.

  Nothing was tagged and nothing was pushed." >&2
		exit 1
	fi
	# Annotated, not lightweight: it carries who tagged it and when, and
	# `git describe` prefers it.
	git tag -a "$tag" -m "orion $tag" "$sha"
fi

git push origin "refs/tags/${tag}"
echo "tagged ${tag} on ${sha}"
