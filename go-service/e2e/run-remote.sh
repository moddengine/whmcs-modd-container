#!/usr/bin/env bash
set -euo pipefail

if (( $# != 3 )); then
	echo "usage: $0 user@host ssh-key controller-binary" >&2
	exit 2
fi

target=$1
key=$2
controller=$3
controller_path=${CONTROLLER_PATH:-/modd/whmcs-api/modd-hosting-controller}
image_version=${IMAGE_VERSION:-isolation-qa}
upgrade_image_version=${UPGRADE_IMAGE_VERSION:-$image_version-upgrade}
unhealthy_image_version=${UNHEALTHY_IMAGE_VERSION:-$image_version-unhealthy}
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$tmp/app" "$here/app.go"
ssh_args=(-F /dev/null -i "$key" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$tmp/known_hosts")
ssh "${ssh_args[@]}" "$target" 'sudo rm -f /tmp/modd-e2e-controller /tmp/modd-e2e-app /tmp/modd-e2e-Dockerfile /tmp/modd-e2e-Dockerfile-unhealthy /tmp/modd-e2e-lifecycle.sh'
scp "${ssh_args[@]}" "$controller" "$target:/tmp/modd-e2e-controller"
scp "${ssh_args[@]}" "$tmp/app" "$target:/tmp/modd-e2e-app"
scp "${ssh_args[@]}" "$here/Dockerfile" "$target:/tmp/modd-e2e-Dockerfile"
scp "${ssh_args[@]}" "$here/Dockerfile.unhealthy" "$target:/tmp/modd-e2e-Dockerfile-unhealthy"
scp "${ssh_args[@]}" "$here/lifecycle.sh" "$target:/tmp/modd-e2e-lifecycle.sh"
printf -v lifecycle_env 'API_URL=%q TOKEN_PATH=%q SITE_ROOT=%q SOCKET_ROOT=%q IMAGE_VERSION=%q UPGRADE_IMAGE_VERSION=%q UNHEALTHY_IMAGE_VERSION=%q SERVICE_ID=%q' \
	"${API_URL:-http://127.0.0.1:8443/v1}" "${TOKEN_PATH:-/modd/whmcs-api/api-token}" \
	"${SITE_ROOT:-/modd/sites}" "${SOCKET_ROOT:-/run/whmcs}" "$image_version" "$upgrade_image_version" "$unhealthy_image_version" "${SERVICE_ID:-}"
ssh "${ssh_args[@]}" "$target" \
	"sudo install -m 0755 /tmp/modd-e2e-controller '$controller_path' && sudo systemctl restart modd-hosting-controller && sudo docker build -q -t whmcs-runtime:$image_version -f /tmp/modd-e2e-Dockerfile /tmp && sudo docker tag whmcs-runtime:$image_version whmcs-runtime:$upgrade_image_version && sudo docker build -q -t whmcs-runtime:$unhealthy_image_version -f /tmp/modd-e2e-Dockerfile-unhealthy /tmp && $lifecycle_env bash /tmp/modd-e2e-lifecycle.sh"
