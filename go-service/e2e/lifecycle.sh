#!/usr/bin/env bash
set -euo pipefail

api=${API_URL:-http://127.0.0.1:8443/v1}
token_path=${TOKEN_PATH:-/modd/whmcs-api/api-token}
site_root=${SITE_ROOT:-/modd/sites}
socket_root=${SOCKET_ROOT:-/run/moddengine}
stamp=$(date -u +%m%d%H%M)
service_id=${SERVICE_ID:-whmcs-$((10#$stamp))}
version=${IMAGE_VERSION:-isolation-qa}
upgrade_version=${UPGRADE_IMAGE_VERSION:-$version-upgrade}
unhealthy_version=${UNHEALTHY_IMAGE_VERSION:-$version-unhealthy}

[[ $service_id =~ ^whmcs-([1-9][0-9]*)$ ]] || { echo "invalid SERVICE_ID: $service_id" >&2; exit 2; }
uid=$((10000 + BASH_REMATCH[1]))
token=$(sudo cat "$token_path")
auth=(-H "Authorization: Bearer $token")

request() {
	curl --fail-with-body --silent --show-error "${auth[@]}" "$@"
}

request -X PUT -H 'Content-Type: application/json' \
	-d "{\"main_domain\":\"$service_id.test\",\"version\":\"$version\",\"display_name\":\"Isolation E2E\"}" \
	"$api/services/$service_id" > /tmp/modd-e2e-provision.json
grep -q '"state":"active"' /tmp/modd-e2e-provision.json
test "$(getent passwd "$service_id" | cut -d: -f3-4)" = "$uid:$uid"
test "$(getent group "$service_id" | cut -d: -f3)" = "$uid"
test -z "$(sudo find "$site_root/$service_id" \( ! -uid "$uid" -o ! -gid "$uid" \) -print -quit)"
for deploy in blue green; do
	test "$(sudo stat -c '%u:%g' "$socket_root/$service_id-$deploy")" = "$uid:$uid"
done
container="moddengine_${service_id}_blue"
test "$(sudo docker inspect -f '{{.Config.User}}' "$container")" = "$uid:$uid"
test "$(sudo docker inspect -f '{{.State.Running}}' "$container")" = true
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
echo 'provision and isolation checks passed'

request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$upgrade_version\",\"confirm_downgrade\":true}" \
	"$api/services/$service_id/upgrade" > /tmp/modd-e2e-upgrade.json
grep -q '"state":"active"' /tmp/modd-e2e-upgrade.json
grep -q "\"version\":\"$upgrade_version\"" /tmp/modd-e2e-upgrade.json
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
test "$(sudo docker ps --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
echo 'upgrade replacement check passed'

request -X POST "$api/services/$service_id/terminate" > /tmp/modd-e2e-terminate.json
grep -q '"state":"terminated"' /tmp/modd-e2e-terminate.json
test -z "$(sudo docker ps -a --filter "label=au.modd.service-id=$service_id" --format '{{.ID}}')"
echo 'termination check passed'

request -X DELETE "$api/services/$service_id" > /tmp/modd-e2e-delete.json
grep -q '"state":"deleted"' /tmp/modd-e2e-delete.json
! getent passwd "$service_id" >/dev/null
! getent group "$service_id" >/dev/null
test ! -e "$site_root/$service_id"
test ! -e "$socket_root/$service_id-blue"
test ! -e "$socket_root/$service_id-green"
test -z "$(sudo docker ps -a --filter "name=moddengine_${service_id}_" --format '{{.Names}}')"
echo 'purge cleanup checks passed'

unhealthy_service_id=whmcs-$((10#${service_id#whmcs-} + 1))
unhealthy_container="moddengine_${unhealthy_service_id}_blue"
curl --fail-with-body --silent --show-error "${auth[@]}" -X PUT -H 'Content-Type: application/json' \
	-d "{\"main_domain\":\"$unhealthy_service_id.test\",\"version\":\"$unhealthy_version\",\"display_name\":\"Unhealthy E2E\"}" \
	"$api/services/$unhealthy_service_id" > /tmp/modd-e2e-unhealthy-provision.json &
provision_pid=$!
for _ in {1..120}; do
	sudo docker inspect "$unhealthy_container" >/dev/null 2>&1 && break
	sleep 1
done
sudo docker inspect "$unhealthy_container" >/dev/null 2>&1 || {
	kill "$provision_pid" 2>/dev/null || true
	wait "$provision_pid" || true
	exit 1
}
test "$(sudo docker inspect -f '{{.State.Running}}' "$unhealthy_container")" = true
test ! -S "$socket_root/$unhealthy_service_id-blue/http.sock"
kill "$provision_pid"
wait "$provision_pid" || true

request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$version\",\"confirm_downgrade\":true}" \
	"$api/services/$unhealthy_service_id/upgrade" > /tmp/modd-e2e-unhealthy-upgrade.json
grep -q '"state":"active"' /tmp/modd-e2e-unhealthy-upgrade.json
grep -q "\"version\":\"$version\"" /tmp/modd-e2e-unhealthy-upgrade.json
! sudo docker inspect "$unhealthy_container" >/dev/null 2>&1
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$unhealthy_service_id" -q | wc -l)" = 1
test "$(sudo docker ps --filter "label=au.modd.service-id=$unhealthy_service_id" -q | wc -l)" = 1
echo 'unhealthy deployment replacement check passed'

request -X POST "$api/services/$unhealthy_service_id/terminate" > /tmp/modd-e2e-unhealthy-terminate.json
test -z "$(sudo docker ps -a --filter "label=au.modd.service-id=$unhealthy_service_id" -q)"
request -X DELETE "$api/services/$unhealthy_service_id" > /tmp/modd-e2e-unhealthy-delete.json
grep -q '"state":"deleted"' /tmp/modd-e2e-unhealthy-delete.json
echo 'unhealthy deployment cleanup checks passed'
