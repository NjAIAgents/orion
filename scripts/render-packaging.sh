#!/usr/bin/env bash
# Render the brew formula and scoop manifest from dist/checksums.txt.
#
# Hashes are read from the checksums file the release actually produced,
# never recomputed from a re-download. A formula whose sha256 came from a
# different build than the release is how "SHA256 mismatch" reaches users
# after everything looked green in CI.
set -euo pipefail

VERSION="${1:-}"
DIST="${2:-dist}"
OUT="${3:-dist/packaging}"
LICENSE="${ORION_LICENSE:-}"

[ -n "$VERSION" ] || { echo "usage: $0 <version-without-v> [distdir] [outdir]" >&2; exit 64; }
[ -f "$DIST/checksums.txt" ] || { echo "no $DIST/checksums.txt; run make dist first" >&2; exit 1; }

# Detect the SPDX id from the LICENSE file when it was not set explicitly.
# Detection is deliberately narrow: it recognises only texts it can identify
# with certainty, and otherwise says nothing. Guessing an id from an
# unrecognised licence would put a legal claim about the code into a public
# tap, which is worse than failing the release.
if [ -z "$LICENSE" ] && [ -f LICENSE ]; then
  if grep -q "Apache License" LICENSE && grep -q "Version 2.0, January 2004" LICENSE; then
    LICENSE="Apache-2.0"
  elif grep -qi "MIT License" LICENSE; then
    LICENSE="MIT"
  fi
fi

if [ -z "$LICENSE" ]; then
  echo "ERROR: no licence set." >&2
  echo "  Both a Homebrew formula and a Scoop manifest must declare one, and the" >&2
  echo "  LICENSE file here was not recognised. Set ORION_LICENSE to its SPDX id" >&2
  echo "  (for example Apache-2.0) and re-run. It is deliberately not guessed:" >&2
  echo "  a wrong SPDX id in a public tap is a legal claim about the code." >&2
  exit 1
fi
echo "licence: $LICENSE"

# sha_for returns 1 rather than exiting: it is called in command
# substitution, where an exit would kill only the subshell.
sha_for() {
  local name="$1" line
  line="$(grep -E "[[:space:]]\*?${name}\$" "$DIST/checksums.txt" || true)"
  if [ -z "$line" ]; then
    echo "ERROR: no checksum for $name in $DIST/checksums.txt" >&2
    echo "  A package manifest promises an archive this release does not contain." >&2
    return 1
  fi
  echo "$line" | awk '{print $1}'
}

# Resolve every hash BEFORE rendering anything, so a missing archive aborts
# the whole run. Calling sha_for inline inside sed's $( ) would let its
# failure kill only the subshell: the error prints, rendering continues, and
# the published formula carries an empty sha256.
SHA_DARWIN_ARM64="$(sha_for "orion_v${VERSION}_darwin_arm64.tar.gz")"
SHA_DARWIN_AMD64="$(sha_for "orion_v${VERSION}_darwin_amd64.tar.gz")"
SHA_LINUX_ARM64="$(sha_for "orion_v${VERSION}_linux_arm64.tar.gz")"
SHA_LINUX_AMD64="$(sha_for "orion_v${VERSION}_linux_amd64.tar.gz")"
SHA_WINDOWS_AMD64="$(sha_for "orion_v${VERSION}_windows_amd64.zip")"
SHA_WINDOWS_ARM64="$(sha_for "orion_v${VERSION}_windows_arm64.zip")"

for v in SHA_DARWIN_ARM64 SHA_DARWIN_AMD64 SHA_LINUX_ARM64 SHA_LINUX_AMD64 \
         SHA_WINDOWS_AMD64 SHA_WINDOWS_ARM64; do
  if [ -z "${!v}" ]; then
    echo "ERROR: $v resolved empty; refusing to publish a manifest without a hash." >&2
    exit 1
  fi
done

mkdir -p "$OUT"

render() {
  local tmpl="$1" dest="$2"
  sed \
    -e "s|{{VERSION}}|${VERSION}|g" \
    -e "s|{{LICENSE}}|${LICENSE}|g" \
    -e "s|{{SHA_DARWIN_ARM64}}|${SHA_DARWIN_ARM64}|g" \
    -e "s|{{SHA_DARWIN_AMD64}}|${SHA_DARWIN_AMD64}|g" \
    -e "s|{{SHA_LINUX_ARM64}}|${SHA_LINUX_ARM64}|g" \
    -e "s|{{SHA_LINUX_AMD64}}|${SHA_LINUX_AMD64}|g" \
    -e "s|{{SHA_WINDOWS_AMD64}}|${SHA_WINDOWS_AMD64}|g" \
    -e "s|{{SHA_WINDOWS_ARM64}}|${SHA_WINDOWS_ARM64}|g" \
    "$tmpl" > "$dest"
  echo "rendered $dest"
}

render packaging/homebrew/orion.rb.tmpl "$OUT/orion.rb"
render packaging/scoop/orion.json.tmpl  "$OUT/orion.json"

# Publishing a template with a live placeholder in it is worse than failing.
if grep -q '{{' "$OUT/orion.rb" "$OUT/orion.json"; then
  echo "ERROR: unrendered placeholders remain:" >&2
  grep -n '{{' "$OUT/orion.rb" "$OUT/orion.json" >&2
  exit 1
fi

python3 -c "import json; json.load(open('$OUT/orion.json'))" && echo "scoop manifest is valid JSON"
