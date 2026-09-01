#!/bin/sh
# Print the release channel a tag implies: "production" or "beta".
#
# ONE definition of the tag shapes, because there are now two callers that
# need it and they need it for different reasons (OR-255):
#
#   scripts/release.sh    is TOLD the channel (--beta) and must check the tag
#                         agrees, so a slip of the flag cannot publish a
#                         prerelease to the tap.
#   .github/workflows/    is given only a tag and must DERIVE the channel,
#   release.yml           because a workflow_dispatch input is free text and
#                         nothing else in that file knows what a beta is.
#
# Two copies of "is this a beta" is how the two drift, and the direction they
# drift in is the dangerous one: `brew upgrade` handing a prerelease to a
# stable user, with semver never offering a way back up because v1.2.3-beta.4
# sorts BELOW v1.2.3.
#
# Exits 2 and explains on a tag that matches neither shape. Guessing a channel
# for a malformed tag is the one thing this must never do.

set -eu

tag="${1-}"
[ -n "$tag" ] || {
	echo "usage: $0 vX.Y.Z | vX.Y.Z-beta.N" >&2
	exit 2
}

case "$tag" in
	# Beta first: it is the more specific pattern, and a v1.2.3-beta.4 also
	# matches the production prefix test below.
	v[0-9]*.[0-9]*.[0-9]*-beta.[0-9]*)
		echo beta
		;;
	*-*)
		echo "'$tag' has a prerelease suffix that is not a beta.
  Only vX.Y.Z-beta.N is recognised, so that it sorts below vX.Y.Z under
  semver. Anything else would be published to an unknown channel." >&2
		exit 2
		;;
	v[0-9]*.[0-9]*.[0-9]*)
		echo production
		;;
	*)
		echo "'$tag' is not a release tag. Expected vX.Y.Z for production, or
  vX.Y.Z-beta.N for a beta." >&2
		exit 2
		;;
esac
