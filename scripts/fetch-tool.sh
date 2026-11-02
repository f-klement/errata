#!/bin/sh
# fetch-tool.sh: download a tool's release, verify it, and install it. `make tools` runs it.
#
# Usage: scripts/fetch-tool.sh --sums <url> <how it is signed> [--member <path>] <url> <dest>
#
#   <url>                 the release's archive (.tar.gz) or, with no --member, the binary itself
#   <dest>                where the binary is installed, mode 0755
#   --sums <url>          the release's checksums file; the download's SHA-256 must be on the one
#                         line that names it, by the URL's last part
#   --member <path>       the binary's path inside the archive
#
# and exactly one of:
#   --signed-sums <url>   a Sigstore bundle that signs the checksums file (cosign verify-blob)
#   --provenance <url>    a Sigstore bundle of SLSA provenance that names the download's digest
#                         (cosign verify-blob-attestation), with --type <predicate type>
#   --attestations <owner/name>
#                         the provenance is in GitHub's attestation API for that repository,
#                         not in the release: every attestation the API gives for the
#                         download's digest is tried, and one must verify; with --type
#   --unsigned            the release signs nothing; the checksums file is the only check
#
# A signature is checked against the publisher's identity: --identity <the certificate's
# identity, exactly>, or --identity-regexp <regexp> with --repository <owner/name> and --ref
# <git ref> for a reusable workflow, whose identity is the workflow's and not the
# publisher's. The issuer is always GitHub Actions'. COSIGN names the cosign binary, and JQ the
# jq that reads the attestation API's answer.
#
# The checksums file is verified before it is read, and the provenance after the checksum, so
# a refusal names the first check that failed. Each refusal says REFUSED and exits 1; nothing
# is installed unless every check passed. The attestation API answering anything but 200 (403
# when its limit of 60 calls an hour is spent, which the refusal says when it resets) is a
# refusal too; GITHUB_TOKEN, when set, is sent to it alone. The trust root is Sigstore's,
# which cosign fetches through TUF into ~/.sigstore.
set -eu
ISSUER=https://token.actions.githubusercontent.com
API=https://api.github.com
refuse() { echo "fetch-tool: REFUSED: $*" >&2; exit 1; }

sums= member= signed= prov= att= type= unsigned= id= idre= repo= ref=
while [ "$#" -gt 2 ]; do
    case $1 in
        --unsigned) unsigned=1; shift; continue ;;
        --sums) sums=$2 ;;
        --member) member=$2 ;;
        --signed-sums) signed=$2 ;;
        --provenance) prov=$2 ;;
        --attestations) att=$2 ;;
        --type) type=$2 ;;
        --identity) id=$2 ;;
        --identity-regexp) idre=$2 ;;
        --repository) repo=$2 ;;
        --ref) ref=$2 ;;
        *) refuse "unknown argument $1" ;;
    esac
    shift 2
done
[ "$#" -eq 2 ] || refuse "usage: fetch-tool.sh --sums <url> (--signed-sums <url> | --provenance <url> --type <type> | --attestations <owner/name> --type <type> | --unsigned) [--member <path>] <url> <dest>"
url=$1 dest=$2
[ -n "$sums" ] || refuse "$url: no --sums, the release's checksums file"
n=0
for m in "$signed" "$prov" "$att" "$unsigned"; do [ -z "$m" ] || n=$((n + 1)); done
[ "$n" -eq 1 ] || refuse "$url: give exactly one of --signed-sums, --provenance, --attestations and --unsigned"
[ -z "$prov$att" ] || [ -n "$type" ] || refuse "$url: provenance needs --type"
if [ -z "$unsigned" ]; then
    # Exactly one identity, and a regexp only with the repository and the ref it ran from.
    if [ -n "$id" ] && [ -z "$idre" ]; then :
    elif [ -z "$id" ] && [ -n "$idre" ] && [ -n "$repo" ] && [ -n "$ref" ]; then :
    else refuse "$url: a signature needs --identity, or --identity-regexp with --repository and --ref"
    fi
    COSIGN=${COSIGN:-cosign}
fi
name=${url##*/}
# Who signed, for the messages.
who=${id:-$idre${repo:+, run by $repo at $ref}}

# The SHA-256 of a file: sha256sum, or shasum -a 256 where there is none, as on macOS.
if command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | cut -d' ' -f1; }
else sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi
# cosign_verify <file> <cosign arguments...>: cosign checks the file with the publisher's
# identity. Its output is in $tmp/cosign.out; shown() prints it under a refusal.
cosign_verify() {
    f=$1
    shift
    if [ -n "$id" ]; then set -- "$@" --certificate-identity "$id"
    else set -- "$@" --certificate-identity-regexp "$idre" --certificate-github-workflow-repository "$repo" --certificate-github-workflow-ref "$ref"
    fi
    "$COSIGN" "$@" --certificate-oidc-issuer "$ISSUER" "$f" >"$tmp/cosign.out" 2>&1 </dev/null
}
shown() { sed 's/^/    /' "${1:-$tmp/cosign.out}" >&2; }
# provenance_verify <bundle>: the signer first, with the claims left unread, then again with
# the download's digest among the subjects. cosign compares the digest before the identity,
# so one call would say "digest" for a provenance from a stranger, and the signer's refusal
# would never show. Returns 2 when the signer is wrong, 1 when the digest is.
provenance_verify() {
    cosign_verify "$tmp/download" verify-blob-attestation --check-claims=false --bundle "$1" --type "$type" || return 2
    cosign_verify "$tmp/download" verify-blob-attestation --bundle "$1" --type "$type" || return 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
get() { curl -sSfL -o "$2" "$1" || refuse "could not download $1"; }
get "$url" "$tmp/download"
get "$sums" "$tmp/sums"

if [ -n "$signed" ]; then
    get "$signed" "$tmp/sums.bundle"
    cosign_verify "$tmp/sums" verify-blob --bundle "$tmp/sums.bundle" ||
        { shown; refuse "the checksums file $sums is not signed by $who"; }
fi

# The one sum the checksums file gives for the name; a name given twice with two sums is refused.
want=$(awk -v f="$name" '$2 == f || $2 == "*" f { print $1 }' "$tmp/sums" | sort -u)
[ -n "$want" ] || refuse "$sums has no line for $name"
[ "$(echo "$want" | wc -l)" -eq 1 ] || refuse "$sums gives $name more than one sum"
got=$(sha "$tmp/download")
[ "$got" = "$want" ] || refuse "checksum mismatch for $url: $got, where $sums says $want"

if [ -n "$prov" ]; then
    get "$prov" "$tmp/provenance"
    r=0; provenance_verify "$tmp/provenance" || r=$?
    [ "$r" != 2 ] || { shown; refuse "the provenance $prov is not signed by $who"; }
    [ "$r" = 0 ] || { shown; refuse "$url is not in the provenance $prov"; }
fi

if [ -n "$att" ]; then
    # The attestations GitHub holds for this digest, each a Sigstore bundle. Every one is tried,
    # since a release can attest one archive more than once, and not every bundle verifies:
    # golangci-lint 2.15.0's linux-amd64 archive has two, its build provenance and GitHub's
    # release attestation, which is of another type and in no log of the public trust root.
    # With GITHUB_TOKEN set and not empty, the call is a token's, at 5000 an hour, not the
    # address's shared 60. The header goes to curl in a file in the private temporary
    # directory, so the token is in no process's arguments, and nothing prints it. Unset, the
    # call is anonymous; there is no fallback to the checksums file alone.
    a="$API/repos/$att/attestations/sha256:$got"
    printf 'Accept: application/vnd.github+json\n' >"$tmp/headers.out"
    if [ -n "${GITHUB_TOKEN:-}" ]; then printf 'Authorization: Bearer %s\n' "$GITHUB_TOKEN" >>"$tmp/headers.out"; fi
    code=$(curl -sSL --max-time 60 -o "$tmp/att.json" -D "$tmp/headers.in" -w '%{http_code}' -H "@$tmp/headers.out" "$a" 2>"$tmp/curl.err") || true
    rm -f "$tmp/headers.out"
    if [ "$code" = 403 ] || [ "$code" = 429 ]; then
        # The rate limit's reset, in seconds since the epoch, as GNU or BSD date shows it.
        reset=$(tr -d '\r' <"$tmp/headers.in" 2>/dev/null | sed -n 's/^[Xx]-[Rr]ate[Ll]imit-[Rr]eset: *\([0-9][0-9]*\)$/\1/p' | tail -n 1)
        if [ -n "$reset" ]; then
            when=$(date -u -d "@$reset" '+%Y-%m-%d %H:%M:%S UTC' 2>/dev/null || date -u -r "$reset" '+%Y-%m-%d %H:%M:%S UTC' 2>/dev/null || echo "$reset")
            refuse "GitHub's attestation API answered $code for $a: its rate limit resets at $when (x-ratelimit-reset $reset). GITHUB_TOKEN set to a token raises it from 60 calls an hour to 5000"
        fi
    fi
    [ "$code" = 200 ] || refuse "GitHub's attestation API answered ${code:-nothing} for $a ($(cat "$tmp/curl.err" "$tmp/att.json" 2>/dev/null | head -c 200 | tr '\n' ' '))"
    "${JQ:-jq}" -c '.attestations | if type == "array" then .[] | .bundle else error("no list") end' "$tmp/att.json" >"$tmp/bundles" 2>/dev/null ||
        refuse "GitHub's attestation API gave no list of attestations for $a"
    k=0 signer= ok=
    while IFS= read -r b; do
        k=$((k + 1))
        printf '%s\n' "$b" >"$tmp/att.$k.json"
        r=0; provenance_verify "$tmp/att.$k.json" || r=$?
        [ "$r" = 0 ] || { echo "attestation $k:"; cat "$tmp/cosign.out"; } >>"$tmp/cosign.all"
        [ "$r" = 2 ] || signer=1
        [ "$r" != 0 ] || { ok=1; break; }
    done <"$tmp/bundles"
    [ "$k" -gt 0 ] || refuse "GitHub has no attestation for $url"
    [ -n "$signer" ] || { shown "$tmp/cosign.all"; refuse "none of the $k attestations GitHub gives for $url is signed by $who"; }
    [ -n "$ok" ] || { shown "$tmp/cosign.all"; refuse "$url is in none of the $k attestations signed by $who"; }
fi

bin=$tmp/download
if [ -n "$member" ]; then
    mkdir "$tmp/x"
    tar -xzf "$tmp/download" -C "$tmp/x" "$member" || refuse "$url has no $member"
    bin=$tmp/x/$member
fi
mkdir -p "$(dirname "$dest")"
install -m 0755 "$bin" "$dest"
if [ -n "$signed" ]; then how="its checksums file signed by $who"
elif [ -n "$prov" ]; then how="its checksums file and its provenance signed by $who"
elif [ -n "$att" ]; then how="its checksums file and its attestation on GitHub signed by $who"
else how="its checksums file only: the release signs nothing"
fi
echo "installed $dest, checked against $how"
