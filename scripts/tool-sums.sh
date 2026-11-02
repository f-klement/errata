#!/bin/sh
# tool-sums.sh: rewrite the Makefile's COSIGN_SHA256_* lines for the pinned COSIGN_VERSION.
#
# cosign is the one tool pinned by hand: it checks every other tool's signature, so nothing
# checks it but its pin (Makefile, scripts/fetch-tool.sh). Run this after changing
# COSIGN_VERSION, which is what a Renovate merge request for cosign does. It reads cosign's own
# checksums file at that version, so the sums come from the release, not from a download this
# machine happened to receive.
#
# With COSIGN set to a cosign binary, as `make tool-sums` sets it to the one in BIN, the
# checksums file must first carry the signature of cosign's release identity, from Sigstore's
# install docs. It must be a cosign from before the bump: one at the version being pinned is
# refused, since the new one checking itself proves nothing. Renovate's post-upgrade task has
# no cosign, so it rewrites the sums unchecked, and a person checks them before the merge
# (ci/README.md, Tool archives and their signatures).
set -eu
MK="$(cd "$(dirname "$0")/.." && pwd)/Makefile"
ver() { sed -n "s/^$1_VERSION *:= *\([^ ]*\)$/\1/p" "$MK"; }
sum() { awk -v f="$2" '$2 == f { print $1 }' "$1"; }
set_sum() {
    [ -n "$2" ] || { echo "no checksum for $1 in the published file" >&2; exit 1; }
    sed -i.bak "s/^\($1 *:= *\)[0-9a-f]*$/\1$2/" "$MK" && rm -f "$MK.bak"
}
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

v=$(ver COSIGN)
base=https://github.com/sigstore/cosign/releases/download/$v
curl -sSfL -o "$T/c" "$base/cosign_checksums.txt"
if [ -n "${COSIGN:-}" ]; then
    # The new cosign checking its own release proves nothing, and it is what BIN holds when
    # make installed it from the bumped Makefile, as ci/run-suite.sh's tools directory does.
    have=$("$COSIGN" version 2>/dev/null | sed -n 's/^GitVersion: *\([^ ]*\)$/\1/p') || have=
    [ -n "$have" ] || { echo "tool-sums: REFUSED: $COSIGN gave no GitVersion, so it cannot be told from the cosign $v it would check" >&2; exit 1; }
    [ "$have" != "$v" ] || { echo "tool-sums: REFUSED: $COSIGN is cosign $have, the version being pinned; check $v's checksums file with the cosign from before the bump" >&2; exit 1; }
    curl -sSfL -o "$T/c.bundle" "$base/cosign_checksums.txt.sigstore.json"
    "$COSIGN" verify-blob --bundle "$T/c.bundle" \
        --certificate-identity keyless@projectsigstore.iam.gserviceaccount.com \
        --certificate-oidc-issuer https://accounts.google.com "$T/c" ||
        { echo "tool-sums: REFUSED: cosign_checksums.txt of $v is not signed by cosign's release identity" >&2; exit 1; }
    checked="checked with $COSIGN"
else
    checked="NOT checked against its signature: no COSIGN was given"
fi
set_sum COSIGN_SHA256_linux_amd64 "$(sum "$T/c" cosign-linux-amd64)"
set_sum COSIGN_SHA256_linux_arm64 "$(sum "$T/c" cosign-linux-arm64)"
set_sum COSIGN_SHA256_darwin_amd64 "$(sum "$T/c" cosign-darwin-amd64)"
set_sum COSIGN_SHA256_darwin_arm64 "$(sum "$T/c" cosign-darwin-arm64)"

echo "cosign $v: sums from its published checksums file, $checked"
