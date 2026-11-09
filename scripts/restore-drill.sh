#!/bin/sh
# Restore drill (docs/restore.md): back up the theatarr-demo server, restore the backup set into a fresh volume, start
# the restored copy beside it on the internal theatarr-demo network, and prove it comes up ready with the same titles,
# versions, links and users. Demo data only (docs/demo.md); nothing leaves the machine: the one-off steps run with no
# network, the restored server on the internal-only theatarr-demo network.
#
#   scripts/restore-drill.sh [IMAGE]    IMAGE defaults to the image theatarr-demo runs; it must have theatarr backup
#
# Env: THEATARR_DEMO_NAME (theatarr-demo), THEATARR_DEMO_VOL (theatarr-demo-data), THEATARR_DEMO_NET (theatarr-demo) name the source server; KEEP=1 leaves the
# restored container (<name>-drill) and volume (<volume>-drill) for a look. The backup set stays in the source
# volume's backups/ directory, like a nightly one. Exit status 0 means the drill passed.
set -eu
SRC=${THEATARR_DEMO_NAME:-theatarr-demo}
VOL=${THEATARR_DEMO_VOL:-theatarr-demo-data}
NET=${THEATARR_DEMO_NET:-theatarr-demo}
DRILL=$SRC-drill
DVOL=$VOL-drill
POL=/usr/share/theatarr/examples/policy

step() { echo "== $*"; }
fail() {
	echo "DRILL FAILED: $*" >&2
	exit 1
}
once() { docker run --rm --network none "$@"; }
cleanup() {
	if [ "${KEEP:-}" != 1 ]; then
		docker rm -f "$DRILL" >/dev/null 2>&1 || true
		docker volume rm -f "$DVOL" >/dev/null 2>&1 || true
	fi
}

[ "$(docker inspect -f '{{.State.Running}}' "$SRC" 2>/dev/null)" = true ] ||
	fail "$SRC is not running (scripts/devserver.sh up)"
IMAGE=${1:-$(docker inspect -f '{{.Config.Image}}' "$SRC")}
trap cleanup EXIT

step "1. back up $SRC ($IMAGE): database, policy, manifest"
out=$(once -v "$VOL":/data "$IMAGE" backup now -db /data/theatarr.db -policy "$POL" -dir /data/backups)
echo "$out"
SET=$(echo "$out" | head -n 1)
case "$SET" in
/data/backups/theatarr-*) ;;
*) fail "no backup set in the output of theatarr backup now" ;;
esac
NAME=$(basename "$SET")

step "2. fingerprint of the backup (titles, versions, links, users)"
want=$(once -v "$VOL":/data "$IMAGE" backup fingerprint -db "$SET.db")
echo "$want"

step "3. restore $NAME into a fresh volume $DVOL"
docker rm -f "$DRILL" >/dev/null 2>&1 || true
docker volume rm -f "$DVOL" >/dev/null 2>&1 || true
once -v "$VOL":/src:ro -v "$DVOL":/data "$IMAGE" \
	backup restore -from "/src/backups/$NAME" -db /data/theatarr.db -policy-out /data/policy

step "4. start the restored copy as $DRILL on $NET"
docker run -d --name "$DRILL" --network "$NET" -v "$DVOL":/data "$IMAGE" \
	serve -db /data/theatarr.db -policy /data/policy -listen 0.0.0.0:7700 >/dev/null
i=0
until docker exec "$DRILL" theatarr healthcheck >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 90 ]; then
		docker logs --tail 40 "$DRILL" >&2
		fail "$DRILL not ready after 90 s"
	fi
	sleep 1
done
echo "ready: /readyz answers 200 (database answers, schema current)"

step "5. the restored copy has the same titles, versions, links and users"
got=$(docker exec "$DRILL" theatarr backup fingerprint -db /data/theatarr.db)
echo "$got"
[ "$got" = "$want" ] || fail "fingerprints differ"

step "6. health checks of the restored copy (offline: the backup and provider checks may warn)"
docker exec "$DRILL" theatarr health -db /data/theatarr.db || true

echo "DRILL PASSED: $NAME restored, ready, same titles, versions, links and users"
