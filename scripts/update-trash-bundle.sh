#!/bin/sh
# Regenerates the TRaSH-Guides snapshot the binary embeds (internal/trashbundle): the archive, the commit it was
# taken from and the licence text. Run by hand before a release, then commit the three files and update the commit
# in NOTICE.
#
#   scripts/update-trash-bundle.sh <TRaSH-Guides checkout> [commit]
#
# The checkout is only read (git archive); nothing is fetched. commit defaults to the checkout's HEAD.
set -eu

[ $# -ge 1 ] || { echo "usage: $0 <TRaSH-Guides checkout> [commit]" >&2; exit 2; }
src=$1
out=$(cd "$(dirname "$0")/../internal/trashbundle" && pwd)
commit=$(git -C "$src" rev-parse --verify "${2:-HEAD}^{commit}")

# What format.LoadTrash reads through metadata.json, plus the licence.
paths="metadata.json LICENSE"
for flavour in radarr sonarr; do
  for dir in cf cf-groups quality-profiles; do
    paths="$paths docs/json/$flavour/$dir"
  done
done

# git archive of a commit records its hash in the tar header (git get-tar-commit-id); gzip -n keeps the output the
# same for the same commit.
# shellcheck disable=SC2086
git -C "$src" archive --format=tar "$commit" $paths | gzip -9n > "$out/guides.tar.gz"
git -C "$src" show "$commit:LICENSE" > "$out/LICENSE-TRaSH-Guides"
printf '%s %s\n' "$commit" "$(git -C "$src" show -s --format=%cs "$commit")" > "$out/COMMIT"
echo "bundled TRaSH-Guides $(cat "$out/COMMIT") ($(wc -c < "$out/guides.tar.gz") bytes)"
