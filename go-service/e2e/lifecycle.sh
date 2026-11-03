#!/usr/bin/env bash
set -euo pipefail

api=${API_URL:-http://127.0.0.1:8443/v1}
token_path=${TOKEN_PATH:-/modd/whmcs-api/api-token}
site_root=${SITE_ROOT:-/modd/sites}
scratch_root=${SCRATCH_ROOT:-/modd/scratch}
socket_root=${SOCKET_ROOT:-/run/whmcs}
stamp=$(date -u +%m%d%H%M)
service_id=${SERVICE_ID:-whmcs-$((10#$stamp))}
version=${IMAGE_VERSION:-isolation-qa}
upgrade_version=${UPGRADE_IMAGE_VERSION:-$version-upgrade}
unhealthy_version=${UNHEALTHY_IMAGE_VERSION:-$version-unhealthy}
public_ipv4=${PUBLIC_IPV4:-203.0.113.10}

[[ $service_id =~ ^whmcs-([1-9][0-9]*)$ ]] || { echo "invalid SERVICE_ID: $service_id" >&2; exit 2; }
uid=$((10000 + BASH_REMATCH[1]))
token=$(sudo cat "$token_path")
auth=(-H "Authorization: Bearer $token")

request() {
	curl --fail-with-body --silent --show-error "${auth[@]}" "$@"
}

wait_phase() {
	local id=$1 phase=$2
	for _ in {1..180}; do
		request "$api/services/$id" | grep -q "\"phase\":\"$phase\"" && return
		sleep 1
	done
	echo "timed out waiting for $id phase $phase" >&2
	return 1
}

request -X PUT -H 'Content-Type: application/json' \
	-d "{\"main_domain\":\"$service_id.test\",\"public_ipv4\":\"$public_ipv4\",\"version\":\"$version\",\"display_name\":\"Isolation E2E\"}" \
	"$api/services/$service_id" > /tmp/modd-e2e-provision.json
grep -q '"state":"active"' /tmp/modd-e2e-provision.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-provision.json
test "$(getent passwd "$service_id" | cut -d: -f3-4)" = "$uid:$uid"
test "$(getent group "$service_id" | cut -d: -f3)" = "$uid"
test -z "$(sudo find "$site_root/$service_id" \( ! -uid "$uid" -o ! -gid "$uid" \) -print -quit)"
sudo test -d "$scratch_root/$service_id/blue/cache"
test -z "$(sudo find "$scratch_root/$service_id" \( ! -uid "$uid" -o ! -gid "$uid" \) -print -quit)"
for deploy in blue green; do
	test "$(sudo stat -c '%u:%g' "$socket_root/$service_id-$deploy")" = "$uid:$uid"
done
container="WHMCS-${service_id#whmcs-}-blue"
test "$(sudo docker inspect -f '{{.Config.User}}' "$container")" = "$uid:$uid"
test "$(sudo docker inspect -f '{{.State.Running}}' "$container")" = true
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
echo 'provision and isolation checks passed'
wait_phase "$service_id" running

request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$upgrade_version\",\"confirm_downgrade\":true}" \
	"$api/services/$service_id/upgrade" > /tmp/modd-e2e-upgrade.json
grep -q '"state":"active"' /tmp/modd-e2e-upgrade.json
grep -q "\"version\":\"$upgrade_version\"" /tmp/modd-e2e-upgrade.json
wait_phase "$service_id" running
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
test "$(sudo docker ps --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
sudo test ! -e "$site_root/$service_id/blue"
sudo test ! -e "$scratch_root/$service_id/blue"
sudo test -d "$scratch_root/$service_id/green/cache"
echo 'upgrade replacement check passed'

request -X POST "$api/services/$service_id/terminate" > /tmp/modd-e2e-terminate.json
grep -q '"state":"terminated"' /tmp/modd-e2e-terminate.json
wait_phase "$service_id" stopped
test -z "$(sudo docker ps -a --filter "label=au.modd.service-id=$service_id" --format '{{.ID}}')"
sudo test ! -e "$site_root/$service_id/blue"
sudo test ! -e "$site_root/$service_id/green"
sudo test ! -e "$scratch_root/$service_id"
echo 'termination check passed'

request -X PUT -H 'Content-Type: application/json' \
	-d "{\"main_domain\":\"redeployed-$service_id.test\",\"staging_domain\":\"staging-$service_id.test\",\"public_ipv4\":\"203.0.113.11\",\"version\":\"$version\",\"display_name\":\"Redeployed E2E\"}" \
	"$api/services/$service_id" > /tmp/modd-e2e-redeploy.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-redeploy.json
wait_phase "$service_id" running
request "$api/services/$service_id" > /tmp/modd-e2e-redeployed.json
grep -q '"state":"active"' /tmp/modd-e2e-redeployed.json
grep -q '"main_domain":"redeployed-'"$service_id"'.test"' /tmp/modd-e2e-redeployed.json
grep -q '"staging_domain":"staging-'"$service_id"'.test"' /tmp/modd-e2e-redeployed.json
grep -q '"version":"'"$version"'"' /tmp/modd-e2e-redeployed.json
test "$(sudo docker ps --filter "label=au.modd.service-id=$service_id" -q | wc -l)" = 1
echo 'terminated service redeployment check passed'

request -X POST "$api/services/$service_id/terminate" > /tmp/modd-e2e-reterminate.json
grep -q '"state":"terminated"' /tmp/modd-e2e-reterminate.json
wait_phase "$service_id" stopped

request -X DELETE "$api/services/$service_id" > /tmp/modd-e2e-delete.json
grep -q '"state":"deleted"' /tmp/modd-e2e-delete.json
! getent passwd "$service_id" >/dev/null
! getent group "$service_id" >/dev/null
test ! -e "$site_root/$service_id"
test ! -e "$socket_root/$service_id-blue"
test ! -e "$socket_root/$service_id-green"
test -z "$(sudo docker ps -a --filter "name=WHMCS-${service_id#whmcs-}-" --format '{{.Names}}')"
echo 'purge cleanup checks passed'

unhealthy_service_id=whmcs-$((10#${service_id#whmcs-} + 1))
unhealthy_container="WHMCS-${unhealthy_service_id#whmcs-}-green"
request -X PUT -H 'Content-Type: application/json' \
	-d "{\"main_domain\":\"$unhealthy_service_id.test\",\"public_ipv4\":\"$public_ipv4\",\"version\":\"$version\",\"display_name\":\"Unhealthy E2E\"}" \
	"$api/services/$unhealthy_service_id" > /tmp/modd-e2e-unhealthy-provision.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-unhealthy-provision.json
wait_phase "$unhealthy_service_id" running
request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$unhealthy_version\",\"confirm_downgrade\":true}" \
	"$api/services/$unhealthy_service_id/upgrade" > /tmp/modd-e2e-unhealthy.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-unhealthy.json
for _ in {1..120}; do
	sudo docker inspect "$unhealthy_container" >/dev/null 2>&1 && break
	sleep 1
done
sudo docker inspect "$unhealthy_container" >/dev/null 2>&1 || {
	exit 1
}
test "$(sudo docker inspect -f '{{.State.Running}}' "$unhealthy_container")" = true
test ! -S "$socket_root/$unhealthy_service_id-green/http.sock"
request "$api/services/$unhealthy_service_id" > /tmp/modd-e2e-pending.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-pending.json
grep -q '"live_deploy":"blue"' /tmp/modd-e2e-pending.json
old_unhealthy_id=$(sudo docker inspect -f '{{.Id}}' "$unhealthy_container")

request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$upgrade_version\",\"confirm_downgrade\":true}" \
	"$api/services/$unhealthy_service_id/upgrade" > /tmp/modd-e2e-unhealthy-upgrade.json
grep -q '"state":"active"' /tmp/modd-e2e-unhealthy-upgrade.json
wait_phase "$unhealthy_service_id" running
test "$(sudo docker inspect -f '{{.Id}}' "$unhealthy_container")" != "$old_unhealthy_id"
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$unhealthy_service_id" -q | wc -l)" = 1
test "$(sudo docker ps --filter "label=au.modd.service-id=$unhealthy_service_id" -q | wc -l)" = 1
echo 'unhealthy upgrade replaced without waiting for failure'

# Stop an upgrade while its candidate is still waiting for health.
request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$unhealthy_version\",\"confirm_downgrade\":true}" \
	"$api/services/$unhealthy_service_id/upgrade" > /tmp/modd-e2e-pending-suspend.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-pending-suspend.json
request -X POST "$api/services/$unhealthy_service_id/suspend" > /tmp/modd-e2e-suspend.json
wait_phase "$unhealthy_service_id" stopped
test -z "$(sudo docker ps --filter "label=au.modd.service-id=$unhealthy_service_id" -q)"
request -X POST "$api/services/$unhealthy_service_id/resume" > /tmp/modd-e2e-resume.json
wait_phase "$unhealthy_service_id" running
echo 'suspend abandons an unhealthy upgrade and retains the last healthy version'

for action in suspend terminate; do
	request -X POST "$api/services/$unhealthy_service_id/terminate" > /tmp/modd-e2e-before-resume.json
	wait_phase "$unhealthy_service_id" stopped
	request -X PUT -H 'Content-Type: application/json' \
		-d "{\"main_domain\":\"$unhealthy_service_id.test\",\"public_ipv4\":\"$public_ipv4\",\"version\":\"$unhealthy_version\"}" \
		"$api/services/$unhealthy_service_id" > /tmp/modd-e2e-bad-resume.json
	grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-bad-resume.json
	grep -q '"operation":"resume"' /tmp/modd-e2e-bad-resume.json
	grep -q '"configured":false' /tmp/modd-e2e-bad-resume.json
	request -X POST "$api/services/$unhealthy_service_id/$action" > /tmp/modd-e2e-abort-resume.json
	wait_phase "$unhealthy_service_id" stopped
	test -z "$(sudo docker ps --filter "label=au.modd.service-id=$unhealthy_service_id" -q)"
	sudo test -d "$site_root/$unhealthy_service_id/site"
	# Resume the retained bad version, then replace it before it can become healthy.
	request -X POST "$api/services/$unhealthy_service_id/resume" > /tmp/modd-e2e-retry-resume.json
	grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-retry-resume.json
	request -X POST -H 'Content-Type: application/json' \
		-d "{\"version\":\"$upgrade_version\",\"confirm_downgrade\":true}" \
		"$api/services/$unhealthy_service_id/upgrade" > /tmp/modd-e2e-replace-resume.json
	grep -q '"operation":"resume"' /tmp/modd-e2e-replace-resume.json
	if [[ $action == suspend ]]; then
		grep -q '"mode":"suspended"' /tmp/modd-e2e-replace-resume.json
	else
		grep -q '"configured":false' /tmp/modd-e2e-replace-resume.json
	fi
	wait_phase "$unhealthy_service_id" running
	request "$api/services/$unhealthy_service_id" > /tmp/modd-e2e-fixed-resume.json
	grep -q "\"version\":\"$upgrade_version\"" /tmp/modd-e2e-fixed-resume.json
	test "$(sudo docker ps -a --filter "label=au.modd.service-id=$unhealthy_service_id" -q | wc -l)" = 1
	echo "$action aborts a pending resume; a later resume accepts a fixed version immediately"
done

request -X POST "$api/services/$unhealthy_service_id/terminate" > /tmp/modd-e2e-unhealthy-terminate.json
wait_phase "$unhealthy_service_id" stopped
test -z "$(sudo docker ps -a --filter "label=au.modd.service-id=$unhealthy_service_id" -q)"
request -X DELETE "$api/services/$unhealthy_service_id" > /tmp/modd-e2e-unhealthy-delete.json
grep -q '"state":"deleted"' /tmp/modd-e2e-unhealthy-delete.json
echo 'unhealthy deployment cleanup checks passed'

pending_service_id=whmcs-$((10#${service_id#whmcs-} + 2))
request -X PUT -H 'Content-Type: application/json' \
	-d "{\"main_domain\":\"$pending_service_id.test\",\"public_ipv4\":\"$public_ipv4\",\"version\":\"$unhealthy_version\"}" \
	"$api/services/$pending_service_id" > /tmp/modd-e2e-bad-provision.json
grep -q '"phase":"waiting_for_health"' /tmp/modd-e2e-bad-provision.json
grep -q '"configured":false' /tmp/modd-e2e-bad-provision.json
request -X POST -H 'Content-Type: application/json' \
	-d "{\"version\":\"$upgrade_version\",\"confirm_downgrade\":true}" \
	"$api/services/$pending_service_id/upgrade" > /tmp/modd-e2e-replace-provision.json
grep -q '"operation":"provision"' /tmp/modd-e2e-replace-provision.json
grep -q '"configured":false' /tmp/modd-e2e-replace-provision.json
wait_phase "$pending_service_id" running
request "$api/services/$pending_service_id" > /tmp/modd-e2e-fixed-provision.json
grep -q "\"version\":\"$upgrade_version\"" /tmp/modd-e2e-fixed-provision.json
test "$(sudo docker ps -a --filter "label=au.modd.service-id=$pending_service_id" -q | wc -l)" = 1
request -X POST "$api/services/$pending_service_id/terminate" > /tmp/modd-e2e-pending-terminate.json
wait_phase "$pending_service_id" stopped
request -X DELETE "$api/services/$pending_service_id" > /tmp/modd-e2e-pending-delete.json
grep -q '"state":"deleted"' /tmp/modd-e2e-pending-delete.json
echo 'initial provisioning accepts a fixed version without waiting for failure'
