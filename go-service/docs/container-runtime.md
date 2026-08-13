# Container lifecycle and troubleshooting

This document describes how the Go controller turns one WHMCS service into
storage, a host identity, Docker containers, Unix sockets, and Caddy routing.
It follows the implementation in `internal/service/manager.go`.

## Runtime model

A service ID must be `whmcs-<positive integer>`. The number maps to a dedicated
host user and group whose UID and GID are `10000 + service-id`. For example,
`whmcs-123` runs as UID/GID `10123`.

Each service has these durable and observed resources:

| Resource | Purpose |
| --- | --- |
| `<state.services_dir>/<id>.toml` | Desired lifecycle state, current/target deployment, paths, and the last error. Writes are atomic and retain a `.bak` of the previous live record. |
| `<zfs.dataset_prefix>/<id>` | Persistent site data, backups, secrets, and slot-local data. |
| `<zfs.mount_prefix>/<id>` | Dataset mountpoint and Docker bind source. |
| Host user/group `<id>` | Owns writable storage and socket directories. |
| `WHMCS-<id>-blue` / `-green` | Alternating Docker deployment slots. |
| `<deployment.socket>` | Slot-specific `http.sock` used by health checks and Caddy. |
| `<caddy.service_config_dir>/<id>.caddy` | Routing for the main and staging domains. |
| `<state.tombstones_dir>/<id>.toml` | Permanent record left after deletion. It prevents accidental reuse of the ID. |

The TOML record is the controller's lifecycle journal, but status is not taken
from TOML alone. `GET /v1/services/{id}` also observes ZFS, all managed Docker
containers, the generated Caddyfile, and metrics. Differences are returned as
warnings.

The controller recognizes four durable service states:

| State | Containers | Caddy | Dataset and identity |
| --- | --- | --- | --- |
| `active` | One live slot normally runs. Both may run during an upgrade. | Proxies to the live slot. | Retained. |
| `suspended` | Stopped, not deleted. | Serves the shared suspension page. | Retained. |
| `terminated` | Deleted. | Service file removed. | Retained. |
| `deleted` | Deleted. | Service file removed. | Dataset, socket directories, and identity deleted; tombstone retained. |

`phase` gives finer progress: `provisioning`, `starting`,
`waiting_for_health`, `routing`, `draining`, `running`, `stopping`, `stopped`,
`deleting`, `deleted`, or `failed`. `operation` records which action owns an
in-progress or failed phase.

All lifecycle endpoints are idempotent for an already-completed identical
request. They return `200` when no new work is needed and `202` when deferred
work remains. While a phase is busy, another lifecycle request returns `409`.

## Container contract

Only a locally available image named
`<docker.image_repository>:<version>` can be started. Images are pulled only
through `POST /v1/image/pull`; callers can supply a tag, but cannot override
the configured repository. The endpoint queues the pull and returns `202`
without waiting for the download.

Every container has:

- user/group `10000 + service-id`;
- labels `au.modd.managed`, `au.modd.service-id`, `au.modd.version`,
  `au.modd.app`, and `au.modd.deploy`;
- `ME_SITE=<service-id>` and `ME_INSTANCE=<service-id>-<slot>`, plus configured
  environment entries;
- configured bind mounts with `{mountpoint}`, `{service_id}`, and `{slot}`
  expanded; and
- the slot socket directory mounted at the identical host/container path.

The image must support an arbitrary non-root UID/GID, write only to writable
binds and its socket directory, create `http.sock`, and answer the configured
health path over that socket. Add a MySQL socket bind only if the image needs
one.

Containers use Docker's `unless-stopped` restart policy and join the configured
existing network. Before creation, the controller creates missing bind-source
directories and changes ownership of writable sources to the service identity.
Read-only bind sources are not changed.

## Provision: create and start

`PUT /v1/services/{id}` accepts the main domain, optional staging domain,
image version, and display name.

1. The controller validates the ID, normalizes domains to lowercase, and
   derives a staging domain when one was omitted. With the example suffix,
   `example.com` becomes `example-com.staging.com`.
2. It rejects an existing tombstone and confirms the exact image tag exists
   locally. An existing live record is accepted only when its domains and
   version exactly match the request.
3. It writes a live TOML record with state `active`, live/target slot `blue`,
   and phase `provisioning`. Persisting intent before host changes makes a
   partial operation visible and retryable.
4. It creates the ZFS dataset if absent, using the configured mountpoint.
5. It creates the storage skeleton:

   ```text
   site/data
   site/conf.json
   site/plug.json
   backup
   shared/secrets
   blue/{cache,run,debug}
   green/{cache,run,debug}
   ```

   `conf.json` and `plug.json` come from `state.templates_dir` when available;
   otherwise they contain `{}`. Existing files are preserved.
6. It creates or verifies the host user/group, recursively assigns the dataset
   and socket directories to that identity, and refuses conflicting UID/GID
   assignments.
7. It starts the blue slot. If a blue container already exists, it is reused
   when running or started when stopped; otherwise the controller creates it.
8. It persists phase `waiting_for_health` and health `checking`, starts the
   deferred health/routing work, and returns `202`.
9. After health succeeds, the controller writes the active Caddyfile for both
   domains, validates and reloads Caddy, then commits phase `running`, clears
   the target fields and last error, and sends a best-effort notification.

If a synchronous step fails, the endpoint returns `422`, phase becomes
`failed`, and `last_error` contains the cause. If deferred health or Caddy work
fails, the original `202` has already been returned; polling status reveals
the failure. Provisioning artifacts are deliberately retained for diagnosis.

Retry the identical provision request after correcting the cause. Because an
existing running container is reused, replace or remove it first if correcting
the failure requires a different container. A different domain or version is
not treated as a provision retry.

## Health checks

Provision, resume, and the inactive upgrade slot all pass through the same
health gate before receiving traffic.

1. Wait `deployment.health_initial_delay`.
2. Connect directly to the slot's Unix socket with a five-second dial timeout.
3. Send `GET <deployment.health_path>` with `X-Forwarded-Proto: https` and
   `X-Skip-Redirect: skip`. The complete request has a ten-second timeout.
4. Accept any HTTP `2xx` response. At most 4096 bytes of a failing response are
   retained in the error.
5. Retry up to `deployment.health_attempts`, waiting `1 ×`, `2 ×`, `3 ×`, ...
   `deployment.health_backoff_increment` between attempts.

Success records `healthy` and advances to routing. Exhaustion records
`unhealthy`, changes the lifecycle phase to `failed`, and leaves the container
and socket state available for inspection. During a failed upgrade, the old
healthy slot continues receiving traffic because Caddy is not switched until
the target passes this gate.

Health is deployment evidence recorded during lifecycle work, not a continuous
probe. A later application failure does not automatically change it or trigger
a restart beyond Docker's configured restart policy.

## Caddy service files

The controller owns only `<caddy.service_config_dir>/<id>.caddy`; the operator
owns the master Caddyfile. The master must import the service directory, for
example:

```caddyfile
import /etc/caddy/services/*.caddy
```

Caddy must see the service directory, Unix socket tree, and suspension root at
the same paths referenced by the controller configuration.

For active routing, `caddy.active_template` is expanded once per main/staging
domain. The standard template proxies to the selected slot:

```caddyfile
example.com {
  reverse_proxy unix//run/whmcs/whmcs-123-blue/http.sock
}
```

For suspension, one site block covers both domains and serves the shared
`index.html`. Termination and deletion remove the per-service file.

Updates are transactional as far as the filesystem and Caddy allow:

1. Write and sync a temporary file in the service directory.
2. Atomically rename it over the service file.
3. Run `caddy.validate_command` directly, without a shell.
4. On success, run `caddy.reload_command`.
5. If validation or reload fails, restore the previous file (or remove the new
   one), then validate/reload the restored configuration again.

Removal follows the same rollback rule. Caddy status is derived from the file:
a Unix `reverse_proxy` is `proxy`, another existing file is `suspended`, and no
file is unconfigured.

## Blue/green upgrade

`POST /v1/services/{id}/upgrade` works only for an active service. The image
must already be local. The same version is a no-op; a lower or non-numeric
version requires `confirm_downgrade: true`.

For an upgrade from blue to green:

1. Persist phase `starting`, operation `upgrade`, target slot `green`, and the
   requested target version. Blue remains the recorded live deployment.
2. Re-verify the service identity and ownership.
3. Inspect Caddy. If it is missing or does not proxy to blue, restore routing
   to blue before touching green.
4. Remove any old green container and stale green socket. Container removal
   verifies the managed and service-ID labels before proceeding.
5. Create and start green with the target image.
6. Persist `waiting_for_health` with green health `checking`, start deferred
   work, and return `202`. Blue continues serving traffic.
7. When green is healthy, atomically change the Caddyfile to green and persist
   phase `draining`.
8. Wait `deployment.traffic_drain`, then remove the old blue container.
9. Commit `live_deploy=green`, the new service version, phase `running`, and
   clear the operation/target fields.

The next upgrade repeats the process in the opposite direction.

Failure before the Caddy switch leaves the old deployment serving traffic.
Caddy validation/reload failure restores the prior file. Failure after the
switch, such as failure to remove the old container, can leave Caddy serving
the healthy target while TOML still names the previous live slot; status warns
that Caddy and TOML disagree. Retrying the same upgrade first restores the
recorded live route, recreates the target cleanly, and repeats the switch.

## Suspend and resume

`POST /v1/services/{id}/suspend` preserves both containers and data:

1. Persist phase `stopping` and operation `suspended`.
2. Stop every managed service container with a 20-second Docker stop timeout.
3. Persist state `suspended` and phase `routing`.
4. Replace the Caddyfile with the shared suspension-page configuration.
5. Persist phase `stopped`, clear the operation, and reset deployment health to
   `unknown`.

`POST /v1/services/{id}/resume` accepts either a suspended or terminated
service:

1. Persist phase `starting`, operation `resume`, and target the recorded live
   slot/version.
2. Recreate or verify the service identity and ownership.
3. Start the existing live container after suspension, or create it after
   termination removed it.
4. Persist state `active`, phase `waiting_for_health`, and health `checking`,
   then return `202`.
5. Only after health succeeds, replace the suspension page or missing route
   with the active Caddy proxy and commit phase `running`.

Thus a resumed application never receives traffic merely because Docker
started it. A resume health failure leaves the previous suspension route in
place, or no route if resuming from termination.

## Termination

`POST /v1/services/{id}/terminate` preserves customer data but removes runtime
resources:

1. Persist phase `stopping` and operation `terminated`.
2. Force-remove every managed service container after verifying its management
   and service-ID labels.
3. Persist state `terminated` and phase `routing`.
4. Remove the service Caddyfile, validate Caddy, and reload it.
5. Persist phase `stopped`, clear the operation, and reset deployment health to
   `unknown`.

The ZFS dataset, mountpoint contents, host identity, socket directories, live
slot selection, and version remain so the service can be resumed. Termination
is therefore reversible; deletion is not. Repeating the original provisioning
request redeploys a terminated service when its hostname, IP, and version still
match the retained record.

## Permanent deletion

`DELETE /v1/services/{id}` is accepted only when the durable state is
`terminated`. This guard makes destructive intent explicit.

1. Persist phase `deleting` and operation `delete` in the live record.
2. Remove any remaining managed containers.
3. Remove both slot sockets and service-specific socket directories.
4. Recursively destroy the exact expected ZFS dataset. The ZFS adapter refuses
   a dataset name outside the configured service prefix or one that differs
   from the recorded service ID.
5. Remove the now-empty mountpoint.
6. Remove the host user and group, refusing identities whose UID/GID no longer
   matches the deterministic mapping.
7. Write a tombstone containing the domains, last version, former dataset, and
   deletion time; then remove the live TOML record.
8. In deferred work, remove any Caddy service file. On success, set the
   tombstone phase to `deleted` and return the final notification.

The endpoint returns `202` after the destructive resource cleanup and
tombstone transition; Caddy cleanup may still be running. A failed final Caddy
cleanup leaves a `failed` tombstone and can be retried with the same `DELETE`.
A completed tombstone makes repeated deletion return `200` and continues to
block reprovisioning under that ID.

## Failure and restart behavior

Every failed lifecycle operation records `phase=failed`, retains `operation`,
and writes the underlying message to `last_error`. Correct the external cause
and repeat that same endpoint. A failed operation does not trigger automatic
rollback of Docker, ZFS, identity, or socket changes; inspect observed status
before retrying.

If the controller restarts during a busy or deferred phase, startup marks the
live record or deleting tombstone as failed with:

```text
controller restarted during deferred operation
```

The controller does not infer whether an interrupted external command or
background step completed. This is deliberate: review Docker, Caddy, ZFS, and
the record, then retry the recorded operation. Docker containers may still be
running because their runtime and restart policy are independent of the
controller process.

Google Chat notification failures are logged as warnings and never change the
lifecycle result.

## Troubleshooting workflow

Start with the controller's combined desired and observed view:

```sh
curl -sS -H 'Authorization: Bearer <token>' \
  http://127.0.0.1:8443/v1/services/whmcs-123
```

Check `state`, `phase`, `operation`, `last_error`, `message`, `warnings`, both
`deployments`, `containers`, `dataset_exists`, and `caddy`. A `202` response is
not completion; poll until `running`, `stopped`, `deleted`, or `failed`.

Use the response's `X-Request-ID` to correlate the request with JSON logs:

```sh
sudo systemctl status modd-hosting-controller
sudo journalctl -u modd-hosting-controller --since '30 minutes ago'
```

Inspect the durable record before changing anything manually:

```sh
sudo sed -n '1,240p' /var/lib/modd-hosting/services/whmcs-123.toml
sudo sed -n '1,200p' /var/lib/modd-hosting/tombstones/whmcs-123.toml
```

Paths may differ from the example; `GET /v1/info` reports the configured state
and Caddy directories and ZFS prefix.

### Docker and image failures

```sh
sudo docker ps -a --filter label=au.modd.service-id=whmcs-123
sudo docker inspect WHMCS-123-blue
sudo docker logs --tail 200 WHMCS-123-blue
sudo docker image inspect whmcs-runtime:<version>
sudo docker network inspect udo-net
```

Verify the image tag is local, the configured network exists, labels identify
the expected service and slot, `.Config.User` has the expected UID/GID, mounts
point at the service dataset, and the process stays running. The controller
will refuse removal when management labels do not match; do not bypass that
guard until the ownership of the container is understood.

### Health and socket failures

```sh
sudo ls -la /run/whmcs/whmcs-123-blue
sudo stat /run/whmcs/whmcs-123-blue/http.sock
sudo curl --unix-socket /run/whmcs/whmcs-123-blue/http.sock \
  -H 'X-Forwarded-Proto: https' -H 'X-Skip-Redirect: skip' \
  http://localhost/~/health/check
```

Check that `http.sock` exists at the configured slot path, is a Unix socket,
is visible both on the host and inside the application/Caddy containers, and
is accessible by the service and Caddy identities. Then inspect application
logs and reproduce the exact health headers/path. A redirect, non-2xx response,
slow response, missing socket, or permissions error all fail the gate.

### Caddy failures

```sh
sudo sed -n '1,200p' /var/lib/modd-hosting/caddy/services/whmcs-123.caddy
sudo docker exec caddy caddy validate --config /etc/caddy/Caddyfile
sudo docker logs --tail 200 caddy
```

Confirm the master Caddyfile imports the mounted service directory and that the
socket path in the generated file exists at the same path inside Caddy. Check
the exact output of the configured validate/reload commands in controller
logs. After a failed apply, verify that the file was restored and Caddy still
serves the previous route.

### ZFS, ownership, and deletion failures

```sh
sudo zfs list tank/modd/sites/whmcs-123
sudo zfs get used,mountpoint tank/modd/sites/whmcs-123
getent passwd whmcs-123
getent group whmcs-123
sudo find /modd/sites/whmcs-123 \( ! -uid 10123 -o ! -gid 10123 \) -print -quit
```

Verify the dataset and mountpoint match the configured prefix, the deterministic
UID/GID is not assigned to another account, and writable paths have the service
ownership. Before retrying deletion, remember that earlier deletion steps may
already have removed containers, sockets, the dataset, or the identity.

### Common status warnings

| Warning | Meaning and next check |
| --- | --- |
| `no healthy instance is receiving traffic` | Active state has no Caddy route to a deployment recorded healthy. Check phase, health, and Caddy. This is expected briefly during initial provision/resume. |
| `service is active but no container is running` | Check Docker events/logs and whether a terminate or interrupted start partially completed. |
| `more than one deployment is running` | Expected while upgrading; unexpected in phase `running`. Inspect both slot labels and retry/finish the upgrade before removing anything manually. |
| `Caddy and TOML live deployment disagree` | Routing and the committed `live_deploy` differ, commonly after an interrupted post-switch upgrade. Inspect both healthy slots and retry the recorded upgrade. |
| `dataset is missing` | The live record points at absent storage. Stop before recreating it; determine whether deletion or ZFS recovery occurred. |
| `<dependency> status unavailable` | The status response is partial. Diagnose Docker, ZFS, Caddy-file access, or metrics separately. |

API errors are intentionally distinct: `400` means invalid input, `401` bad
authentication, `404` a missing service or local image, `409` an invalid/busy
lifecycle transition, `422` a host lifecycle step failed, `503` Docker could
not be queried while validating an image, and `500` an internal persistence or
controller error. For `500`, the public message is generic; use its request ID
to find the underlying logged error.
