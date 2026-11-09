#!/bin/sh
# theatarr-demo: a Theatarr server on the invented demo dataset (docs/demo.md) for development and UI checks. It runs on an
# internal-only Docker network, so nothing it tries (indexers, debrid, TMDb, arrs in the example policy) leaves the
# machine. Other containers reach it at http://theatarr-demo:7700 by joining the network: docker run --network theatarr-demo ...
#
#   scripts/devserver.sh up [IMAGE]      seed (first time) and start; IMAGE defaults to theatarr:<git describe>, as make image tags it
#   scripts/devserver.sh reseed [IMAGE]  wipe the data, seed again anchored at now, start
#   scripts/devserver.sh down            stop and remove the container (the data volume stays; reseed replaces it)
#
# Sign in as admin with the demo password (docs/demo.md); it is demo-only and printed by every seed.
#
# The policy is the image's example policy with this checkout's demo overlay (examples/demo) mounted into it: a
# parallel version per arr-fed series version, so every series has two copies (docs/demo.md "Two copies"). Seed and
# serve both see it; reseed after changing it.
#
# Settings: with TRASH set to a TRaSH-Guides checkout (TRASH=/path/to/Guides scripts/devserver.sh up), serve runs with
# database settings, as a real install does: the demo seed imports the policy, overlay and active versions
# included, with the checkout mounted for the format specifications and invented keys for the example providers and
# indexers (the network is internal: they reach nothing). Without TRASH the demo seeder uses its bundled example format specifications.
set -eu
NET=theatarr-demo
VOL=theatarr-demo-data
NAME=theatarr-demo
POL=/usr/share/theatarr/examples/policy
ROOT=$(cd "$(dirname "$0")/.." && pwd)
IMAGE=${2:-theatarr:$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}
TRASH=${TRASH:-}

# run is docker run with the demo overlay mounted into the example policy.
run() {
	docker run -v "$ROOT/examples/demo/versions-theatarr.yaml:$POL/versions-theatarr.yaml:ro" \
		-v "$ROOT/examples/demo/library.yaml:$POL/library.yaml:ro" "$@"
}
net() { docker network inspect "$NET" >/dev/null 2>&1 || docker network create --internal "$NET" >/dev/null; }
seed() { run --rm --network none -v "$VOL":/data "$IMAGE" demo seed -db /data/theatarr.db -policy "$POL" "$@"; }
# settings supplies optional TRaSH preset files and invented credentials. Both modes use the seeded database.
settings() {
	if [ -z "$TRASH" ]; then
		echo "db"
		return
	fi
	echo "-v $(cd "$TRASH" && pwd):/trash:ro -e THEATARR_TRASH=/trash"
	for env in $(sed -n 's/^ *api_key_env: *\([A-Z0-9_]*\).*/\1/p' "$ROOT/examples/policy/providers.yaml" \
		"$ROOT/examples/policy/indexers.yaml" | sort -u); do
		echo "-e $env=invented-demo-key"
	done
	echo "db"
}
start() {
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	opts=$(settings)
	source=$(echo "$opts" | tail -n 1)
	# shellcheck disable=SC2046 # the options are words without spaces
	run -d --name "$NAME" --network "$NET" -v "$VOL":/data $(echo "$opts" | sed '$d') "$IMAGE" \
		serve -db /data/theatarr.db -policy "$POL" -listen 0.0.0.0:7700 >/dev/null
	echo "$NAME: $IMAGE on network $NET, http://$NAME:7700 ($source settings)"
}

case "${1:-}" in
up)
	net
	# The image has no shell to look inside the volume: a new volume means a first seed.
	if ! docker volume inspect "$VOL" >/dev/null 2>&1; then
		seed
	fi
	start
	;;
reseed)
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	net
	seed -force
	start
	;;
down)
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	;;
*)
	echo "usage: $0 up|reseed|down [IMAGE]" >&2
	exit 2
	;;
esac
