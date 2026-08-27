# WHMCS Container Hosting MVP Integration Plan

## 1. Purpose

Build a minimal WHMCS integration and lightweight Go orchestration controller for provisioning and managing website containers behind Caddy.

The MVP deliberately avoids becoming a general-purpose hosting platform. WHMCS remains responsible for billing, customer records, service records, and manual administrator actions. The Go controller performs host-local lifecycle operations and derives current state from Docker, ZFS, per-service TOML files, and individual Caddy configuration files.

The implementation must remain independent of WHM and must not depend on cPanel account creation, cPanel users, `/home`-based paths, Apache, `.htaccess`, or user switching.

---

## 2. MVP goals

### 2.1 Primary goals

- Provide a lightweight WHMCS provisioning module supporting only:
  - Provision
  - Suspend
  - Resume
  - Terminate
  - Delete/purge as an explicit manual action
  - Upgrade or downgrade to a selected container image version
  - Read-only sync from the controller
- Provide a small Go daemon exposing an HTTPS JSON API.
- Authenticate all non-health API requests with a shared bearer token.
- Use the native Go Docker client.
- Store per-service desired metadata in plain TOML files.
- Derive observed state from Docker, ZFS, service TOML, and Caddy configuration wherever possible.
- Use ZFS datasets for site storage.
- Use one individual Caddyfile per service, imported by a master Caddyfile.
- Run Caddy in a container with its configuration directory mounted.
- Preserve failed deployments for debugging.
- Implement blue/green upgrade behavior based only on the supplied deployment script's deployment logic.
- Support basic operational visibility through a WHMCS addon module.
- Send selected lifecycle notifications to Google Chat.

### 2.2 Explicit non-goals

The MVP will not include:

- WHM or cPanel integration.
- Package definitions or package synchronization.
- ZFS quota enforcement.
- Multi-host scheduling or container placement.
- High availability for the controller.
- A database for controller state.
- Automatic termination or deletion from WHMCS cron.
- Automatic downgrade compatibility analysis.
- Container log aggregation in WHMCS.
- Complex billing metrics.
- Per-customer access to raw controller functions.
- Arbitrary Docker configuration supplied by WHMCS.
- Caddy Admin API management.
- Apache configuration, `.htaccess`, or symlink-based web-root switching.
- Reuse of `/home` paths or Unix account switching from the existing script.

---

## 3. Target architecture

```text
WHMCS
├── Provisioning module: modules/servers/moddhosting
│   ├── Provision
│   ├── Suspend
│   ├── Resume
│   ├── Terminate
│   ├── Delete/purge custom action
│   ├── Upgrade/downgrade custom action
│   └── Sync/status read
│
└── Addon module: modules/addons/moddhosting
    ├── Service status page
    ├── Bulk upgrade page
    ├── Controller daemon log view
    └── Administrative action history

HTTPS + Bearer token
        │
        ▼
Go controller daemon
├── HTTP API
├── Docker client
├── ZFS command adapter
├── TOML service repository
├── Caddyfile renderer
├── image-version provider
├── metrics provider interface
├── Google Chat notifier
└── rotating daemon logs

Host resources
├── Docker Engine
├── ZFS pool/datasets
├── Per-service TOML files
├── Per-service Caddyfiles
├── Shared suspension files
├── Container socket directory
└── Caddy container
```

---

## 4. Service lifecycle states

Only these service states are part of the MVP:

- `active`
- `suspended`
- `terminated`
- `deleted`

No intermediate state is persisted as the service's target state.

Operations may report that work is in progress or failed, but the canonical service state remains one of the four values above.

### 4.1 State meanings

#### `active`

- Service TOML exists.
- ZFS dataset exists.
- One live blue or green container is expected to be running.
- The individual Caddyfile reverse proxies production and staging domains to the live socket.

#### `suspended`

- Service TOML exists.
- ZFS dataset and files remain untouched.
- Application containers are stopped.
- The individual Caddyfile serves a local suspension page.

#### `terminated`

- Service TOML exists.
- ZFS dataset and service files remain untouched.
- Application containers are removed.
- The individual Caddyfile is removed or disabled.
- The domains are no longer intentionally served by this service.
- A terminated service may be resumed.

#### `deleted`

- Application containers have been deleted.
- The service Caddyfile is absent.
- The ZFS dataset and service files have been deleted.
- A small TOML tombstone remains outside the deleted dataset.
- The service cannot be resumed without provisioning again.

### 4.2 Allowed transitions

```text
Provision:          absent -> active
Suspend:            active -> suspended
Resume:             suspended -> active
Resume:             terminated -> active
Terminate:          active -> terminated
Terminate:          suspended -> terminated
Delete:             terminated -> deleted
Upgrade/downgrade:  active -> active
```

Reject these transitions:

- Delete from `active`.
- Delete from `suspended`.
- Suspend from `terminated`.
- Upgrade while `suspended`, `terminated`, or `deleted`.
- Resume from `deleted`.

Repeated calls that already match the requested result should be idempotent.

---

## 5. WHMCS PHP provisioning module

## 5.1 Design rules

The PHP module must remain lightweight:

- Validate WHMCS parameters.
- Build a small JSON request.
- Call the controller API.
- Convert the response into a WHMCS success or error result.
- Save only the minimum identifiers and display fields required in WHMCS.
- Perform no Docker, ZFS, Caddy, filesystem, image-registry, or health-check logic.
- Do not duplicate controller business rules.
- Do not write directly to controller TOML or Caddy files.

Recommended module path:

```text
modules/servers/moddhosting/
├── moddhosting.php
├── hooks.php
├── lib/
│   ├── ApiClient.php
│   ├── ApiException.php
│   ├── RequestFactory.php
│   └── ResponseMapper.php
└── templates/
    └── clientarea.tpl
```

## 5.2 WHMCS actions

### Provision

Map WHMCS `CreateAccount` to:

```http
PUT /v1/services/{id}
```

The service ID should be stable and based on the WHMCS service ID:

```text
whmcs-{tblhosting.id}
```

The request contains only fields needed for provisioning, such as:

- Service ID
- Main domain
- Optional staging domain
- Selected image version
- Service display name
- Any explicitly approved runtime inputs copied from product or configurable options

The controller automatically derives the staging domain when blank.

### Suspend

Map WHMCS `SuspendAccount` to:

```http
POST /v1/services/{id}/suspend
```

### Resume

Map WHMCS `UnsuspendAccount` to:

```http
POST /v1/services/{id}/resume
```

### Terminate

Expose termination as an administrator-only manual action.

Do not permit normal WHMCS automation to automatically invoke controller termination.

Implementation options:

1. Do not implement automatic `TerminateAccount`; expose an admin custom button instead.
2. Implement `TerminateAccount` only as a guarded no-op/error explaining that manual termination is required, while the custom button performs the real action.

Preferred MVP approach:

- Use an admin custom button named `Terminate Hosting Service`.
- Require an explicit confirmation page in the addon module.
- The button calls:

```http
POST /v1/services/{id}/terminate
```

### Delete/purge

Expose only through the WHMCS administrator interface.

Requirements:

- Fetch current service state first.
- Permit the action only when state is `terminated`.
- Display a destructive-action warning.
- Require a second confirmation that data will be permanently removed.
- Call:

```http
DELETE /v1/services/{id}
```

Never wire this action to automatic WHMCS cancellation, termination, or cron behavior.

### Upgrade/downgrade

Expose an administrator interface that:

1. Calls `GET /v1/image/versions`.
2. Displays available versions.
3. Shows the currently deployed version.
4. Allows selecting any available version.
5. Detects whether the selected version sorts or resolves as older than the current version when possible.
6. Displays a downgrade warning.
7. Requires explicit confirmation for downgrade.
8. Calls:

```http
POST /v1/services/{id}/upgrade
```

The request should contain:

```json
{
  "version": "v21.6.24",
  "confirm_downgrade": true
}
```

The controller must not attempt to guess whether a downgrade is safe. It only requires confirmation when a downgrade is requested.

After success, update WHMCS service metadata with the deployed version and current controller status.

Packages are explicitly excluded from the MVP.

### Sync

Sync is read-only and calls:

```http
GET /v1/services/{id}
```

The PHP side may update WHMCS display metadata with:

- Controller state
- Container existence/running status
- Active deployment color
- Deployed version
- Disk storage
- Email sends
- Monthly traffic
- Main domain
- Staging domain
- Last controller check
- Last controller-reported error, when present

Sync must not push WHMCS state back to the controller and must not automatically change lifecycle state.

---

## 6. WHMCS addon module

Recommended path:

```text
modules/addons/moddhosting/
├── moddhosting.php
├── hooks.php
├── lib/
├── templates/
└── assets/
```

### 6.1 MVP administration pages

#### Controller overview

Show:

- Controller health
- Controller version/build info
- Docker connectivity
- Caddy configuration path visibility
- ZFS prefix visibility
- Count of active, suspended, terminated, and deleted tombstones

#### Services page

Use:

```http
GET /v1/services
```

Display:

- Service ID
- Main domain
- Staging domain
- State
- Live deployment: blue or green
- Version
- Container status
- Disk usage
- Email sends
- Monthly traffic
- Last updated time

Provide filters for:

- State
- Version
- Domain
- Service ID

#### Service detail page

Display:

- Full controller status response
- Current and inactive deployment slots
- Container names and labels
- Socket paths
- Dataset path
- Caddyfile path
- Current version
- Lifecycle actions allowed from current state
- Upgrade/downgrade form

Do not expose container stdout/stderr logs in the MVP.

#### Bulk upgrades

Allow an administrator to:

- Select active services.
- Select a target version fetched from `GET /v1/image/versions`.
- Review current versions.
- Confirm upgrades.
- Explicitly confirm any selected downgrades.
- Execute operations sequentially or with a small configured concurrency limit.
- Display success/failure per service.

Bulk execution should call the same per-service upgrade endpoint and should not introduce a separate bulk controller endpoint for MVP.

- No container logs.
- No arbitrary file selection.
- No query parameter allowing custom paths.

---

## 7. API authentication and transport

## 7.1 HTTPS

- All controller API traffic must use HTTPS.
- The controller may terminate TLS directly or sit behind a trusted local reverse proxy.
- Certificate validation must remain enabled in PHP.
- Do not provide a production option to disable TLS verification.

## 7.2 Bearer token

All endpoints except `GET /v1/health` require:

```http
Authorization: Bearer <shared-token>
```

Requirements:

- Store the token in WHMCS encrypted server/module credentials.
- Store the controller token in the main controller TOML configuration or a separate restricted secret file.
- Never place the token in URLs.
- Never write the token into normal logs.
- Compare tokens using a constant-time comparison.
- Return `401 Unauthorized` when absent or invalid.
- Return a generic authentication error without indicating which part failed.

## 7.3 Request behavior

- JSON request and response bodies.
- Strict request size limit.
- Explicit `Content-Type: application/json` for requests with bodies.
- Bounded client and server timeouts.
- Structured error responses.
- Idempotent behavior where practical.
- Generate and return a request ID.

Suggested headers:

```http
X-Request-ID: <uuid>
```

---

## 8. OpenAPI specification

Create an `openapi.yaml` file as the API contract.

Required endpoints:

```text
GET    /v1/health
GET    /v1/info
GET    /v1/image/versions
PUT    /v1/services/{id}
POST   /v1/services/{id}/suspend
POST   /v1/services/{id}/resume
POST   /v1/services/{id}/terminate
DELETE /v1/services/{id}
POST   /v1/services/{id}/upgrade
GET    /v1/services/{id}
GET    /v1/services
```

### 8.1 Endpoint behavior

#### `GET /v1/health`

Unauthenticated shallow liveness check.

Return only whether the daemon can serve requests.

Do not expose configuration, paths, tokens, Docker details, or ZFS details.

#### `GET /v1/info`

Authenticated controller information:

- Controller version
- Build commit
- Build date
- Docker API version
- Configured ZFS prefix
- Configured service-state path
- Configured Caddy service-config path
- Configured traffic-drain period
- Metrics provider status

Do not return secrets.

#### `GET /v1/image/versions`

Return versions available for deployment.

Initial implementation may derive versions from locally available Docker images matching the configured repository, or from a simple configured external version source.

Response should distinguish:

- Version/tag
- Image reference
- Whether it is locally present
- Optional created timestamp, when known

Only images from the configured repository are permitted.

#### `PUT /v1/services/{id}`

Provision a new service.

- Reject invalid IDs.
- Reject duplicate service IDs unless the existing service can be safely treated as the same idempotent request.
- Create the ZFS dataset.
- Create service directories.
- Render initial service configuration files.
- Create the service TOML.
- Start the first deployment slot.
- Health-check it.
- Render and enable the per-service Caddyfile only after a healthy initial container.
- Preserve all artifacts on failure.
- Send Google Chat success or failure notification.

#### `POST /v1/services/{id}/suspend`

- Stop all service containers.
- Render a Caddyfile that serves the shared suspension page.
- Reload or validate Caddy configuration.
- Set target state to `suspended`.
- Send Google Chat notification.

#### `POST /v1/services/{id}/resume`

For `suspended`:

- Start the currently selected live deployment.
- Perform health check.
- Restore reverse proxy Caddyfile.
- Set target state to `active`.

For `terminated`:

- Recreate a deployment container from the last recorded version when required.
- Start the deployment.
- Perform health check.
- Restore the service Caddyfile.
- Set target state to `active`.

If the resumed container is unhealthy:

- Leave it running for debugging.
- Do not route Caddy to it.
- Keep the prior state.
- Return an error.

Send a Google Chat notification on successful resume.

#### `POST /v1/services/{id}/terminate`

- Remove all service containers.
- Remove the individual Caddyfile.
- Reload or validate Caddy.
- Preserve the ZFS dataset and all service files.
- Set state to `terminated`.
- Send Google Chat notification.

#### `DELETE /v1/services/{id}`

- Require current state `terminated`.
- Delete all service containers.
- Remove any service Caddyfile.
- Destroy the service ZFS dataset recursively.
- Write a TOML tombstone outside the destroyed dataset.
- Mark state as `deleted` in the tombstone.
- Send Google Chat notification.

#### `POST /v1/services/{id}/upgrade`

- Require state `active`.
- Validate target version exists in `GET /v1/image/versions` output.
- Require `confirm_downgrade=true` when target version is identified as older than current.
- Run blue/green deployment logic.
- Preserve failed new containers and files for debugging.
- Never switch Caddy to an unhealthy deployment.
- On success, switch Caddy to the new deployment.
- Wait configured traffic-drain period, default 10 seconds.
- Stop and remove the old deployment container.
- Keep the old deployment files unless a later cleanup policy is added.
- Send Google Chat notification on upgrade failure.

#### `GET /v1/services/{id}`

Return combined desired and observed status.

#### `GET /v1/services`

Return active, suspended, and terminated services, plus observed container details.

Deleted services may be included only when an explicit query option such as `include_deleted=true` is added to the OpenAPI specification. Default listing should exclude deleted tombstones.

---

## 9. Controller configuration TOML

Example:

```toml
[server]
listen = "127.0.0.1:8443"
request_timeout = "120s"
shutdown_timeout = "15s"

[auth]
bearer_token_file = "/etc/modd-hosting/api-token"

[tls]
certificate = "/etc/modd-hosting/tls/server.crt"
private_key = "/etc/modd-hosting/tls/server.key"

[zfs]
dataset_prefix = "tank/modd/sites"
mount_prefix = "/modd/sites"

[state]
services_dir = "/var/lib/modd-hosting/services"
tombstones_dir = "/var/lib/modd-hosting/tombstones"

[caddy]
service_config_dir = "/var/lib/modd-hosting/caddy/services"
suspension_root = "/var/lib/modd-hosting/caddy/suspended"
container_name = "caddy"
validate_command = ["docker", "exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile"]
reload_command = ["docker", "exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"]

[docker]
network = "udo-net"
image_repository = "whmcs-runtime"

[deployment]
health_path = "/~/health/check"
health_attempts = 30
health_initial_delay = "3s"
health_backoff_increment = "2s"
traffic_drain = "10s"
socket = "/run/whmcs/{service_id}-{slot}/http.sock"

[domains]
staging_suffix = "staging.com"

[metrics]
provider = "mock"

[google_chat]
webhook_url_file = "/etc/modd-hosting/google-chat-url"

level = "info"
max_size_mb = 50
max_backups = 10
max_age_days = 30
compress = true
```

### 9.1 Configuration validation

At daemon startup verify:

- Dataset prefix is valid.
- Mount prefix is absolute.
- State and tombstone directories are writable.
- Caddy service directory exists and is writable.
- Suspension page exists.
- Docker is reachable.
- Required Docker network exists.
- Configured image repository is non-empty.
- Traffic drain is non-negative.
- Health attempts and delays are valid.
- Token file can be read and is not empty.
- TLS files can be read.
- Log directory is writable.

Fail startup on unsafe or invalid core configuration.

Metrics and Google Chat may report degraded configuration without preventing startup when intentionally disabled or mocked.

---

## 10. Per-service TOML

Store each live service record outside the service's ZFS dataset so the controller can still locate it when the dataset is unavailable.

Suggested path:

```text
/var/lib/modd-hosting/services/whmcs-123.toml
```

Example:

```toml
id = "whmcs-123"
state = "active"
main_domain = "mysite.com"
staging_domain = "mysite-com.staging.com"
version = "v21.6.24"
live_deploy = "green"
created_at = "2026-07-29T00:00:00Z"
updated_at = "2026-07-29T00:30:00Z"

[zfs]
dataset = "tank/modd/sites/whmcs-123"
mountpoint = "/modd/sites/whmcs-123"

[paths]
caddyfile = "/var/lib/modd-hosting/caddy/services/whmcs-123.caddy"

[deploy.blue]
socket = "/run/whmcs/whmcs-123-blue/http.sock"
container = "WHMCS-123-blue"

[deploy.green]
socket = "/run/whmcs/whmcs-123-green/http.sock"
container = "WHMCS-123-green"
```

### 10.1 TOML writing requirements

- Write to a temporary file in the same directory.
- `fsync` when practical.
- Atomically rename into place.
- Keep a backup of the previous file for diagnostic purposes.
- Never write tokens or Google Chat URLs into per-service TOML.

### 10.2 Deleted tombstone

Suggested path:

```text
/var/lib/modd-hosting/tombstones/whmcs-123.toml
```

Example:

```toml
id = "whmcs-123"
state = "deleted"
main_domain = "mysite.com"
staging_domain = "mysite-com.staging.com"
last_version = "v21.6.24"
deleted_at = "2026-07-29T01:00:00Z"
former_dataset = "tank/modd/sites/whmcs-123"
```

Tombstones are diagnostic records and do not imply that service data remains.

---

## 11. Staging-domain derivation

When the request does not include a staging domain:

1. Normalize the production domain to lowercase.
2. Remove a trailing dot.
3. Replace dots with hyphens.
4. Append the configured staging suffix.

Example:

```text
mysite.com -> mysite-com.staging.com
www.example.com.au -> www-example-com-au.staging.com
```

Validate the derived hostname length and label lengths.

Allow the request to provide an explicit staging domain when required.

---

## 12. Required folder structure

The folder structure is modeled from the useful deployment separation in the existing script, without `/home`, Unix users, `.htaccess`, or Apache-specific content.

Example service mountpoint:

```text
/modd/sites/whmcs-123/
├── site/
│   └── data/
├── backup/
├── shared/
│   └── secrets/
├── blue/
│   ├── cache/
│   ├── run/
│   └── debug/
└── green/
    ├── cache/
    ├── run/
    └── debug/
```

Host socket paths remain outside the dataset:

```text
/run/whmcs/
├── whmcs-123-blue/
│   └── http.sock
└── whmcs-123-green/
    └── http.sock
```

Controller state and Caddy configuration remain outside the dataset:

```text
/var/lib/modd-hosting/
├── services/
│   └── whmcs-123.toml
├── tombstones/
├── caddy/
│   ├── services/
│   │   └── whmcs-123.caddy
│   └── suspended/
│       └── index.html
└── templates/
```

### 12.1 Skeleton creation

Provisioning creates:

- ZFS dataset.
- Dataset mountpoint.
- `site/data`.
- `backup`.
- `shared/secrets` when required by the runtime.
- Blue cache/run/debug directories.
- Green cache/run/debug directories.
- External blue and green socket directories.
- Required configuration files from templates.

The exact runtime mounts must be finalized after mapping the current Docker command into the Go Docker client.

---

## 13. ZFS behavior

### 13.1 Provision

Example dataset:

```text
tank/modd/sites/whmcs-123
```

Example mountpoint:

```text
/modd/sites/whmcs-123
```

Implementation:

- Validate the service ID before including it in any dataset name.
- Use fixed command arguments, not shell string interpolation.
- Create the dataset with an explicit mountpoint.
- Do not set a quota for MVP.
- If any later provisioning step fails, leave the dataset and all created files in place.
- Record failure details in daemon logs and service TOML when possible.

### 13.2 Suspend

No ZFS changes.

### 13.3 Terminate

No ZFS changes.

### 13.4 Delete

- Verify service state is `terminated`.
- Verify the dataset exactly matches the configured prefix and expected service ID.
- Stop and remove containers first.
- Remove Caddy configuration first.
- Destroy the dataset recursively.
- Do not follow arbitrary mountpoints or user-supplied paths.
- Create the tombstone only after destruction succeeds.

---

## 14. Docker container model

Use the native Go Docker client.

### 14.1 Required labels

Every managed container must include:

```text
au.modd.managed=true
au.modd.service-id=whmcs-123
au.modd.version=v21.6.24
au.modd.app=whmcs
au.modd.deploy=blue
```

For green deployment:

```text
au.modd.deploy=green
```

These labels are the primary way status calls identify managed service containers.

### 14.2 Naming convention

```text
WHMCS-{service-id}-{deploy}
```

Example:

```text
WHMCS-123-blue
WHMCS-123-green
```

### 14.3 Container settings

Translate the required runtime behavior from the current deployment script into Go Docker API structures, while excluding:

- `sudo -u`.
- Unix account lookup.
- `/home/{user}` paths.
- `.htaccess` generation.
- Apache web-root symlinks.

Retain only settings still required by the application image, including where applicable:

- Configured image repository and selected tag.
- Required bind mounts.
- Blue/green cache separation.
- Blue/green socket separation.
- Site and backup storage mounts.
- Required shared secrets mounts.
- MySQL socket mount, only when still required.
- Configured Docker network.
- `ME_SITE` and `ME_INSTANCE` environment variables or their confirmed replacements.
- Restart policy.

The exact Docker settings must be documented in a dedicated `container_spec.go` builder and covered by unit tests.

### 14.4 Failed containers

All provisioning and upgrade failures must preserve:

- Created ZFS dataset.
- Created files.
- New container, even when unhealthy.
- New socket directory.
- Service TOML and failure metadata.

Do not automatically remove an unhealthy new deployment.

To avoid restart loops interfering with debugging, decide whether failed unhealthy containers should remain running under the configured restart policy or have restart disabled after failure. The MVP plan recommends leaving the container running exactly as started unless operational testing shows this causes unacceptable churn.

---

## 15. Blue/green deployment logic

The MVP must reproduce only these deployment behaviors from the supplied script:

1. Determine the current live deployment.
2. Choose the opposite deployment slot.
3. Remove any pre-existing container occupying the target slot before starting a replacement.
4. Remove a stale socket file for the target slot.
5. Start the new container with slot-specific mounts, environment, name, and labels.
6. For first deployment, make the only container live after it passes the required initial checks.
7. For upgrades, health-check the new socket before switching traffic.
8. If health fails, leave the new container and all files in place and return an error.
9. If health succeeds, update the individual service Caddyfile to point to the new socket.
10. Validate and reload Caddy.
11. Wait the configured traffic-drain period, default 10 seconds.
12. Stop and remove the old container.
13. Update the service TOML with the new live slot and version.

Do not reproduce:

- `/home` path calculation.
- Unix account lookup.
- `sudo -u` execution.
- `.htaccess` creation.
- Apache rewrite rules.
- Apache error document behavior.
- Web-root symlink switching.
- cPanel account names.

### 15.1 Live-slot determination

Primary source:

- Service TOML `live_deploy`.

Validation source:

- Current service Caddyfile upstream socket.
- Docker labels and running container state.

When these disagree:

- Do not guess silently.
- Return the discrepancy in status.
- Log the issue.
- Refuse upgrade until the state can be safely resolved, unless an explicit repair operation is later added.

### 15.2 First deployment

Recommended first slot: `blue`.

Provisioning flow:

1. Create files and TOML with intended slot `blue`.
2. Start blue container.
3. Run health check.
4. On success, create active Caddyfile pointing to blue socket.
5. Update TOML state to `active`, live slot `blue`, selected version.

If health fails:

- Preserve blue container and artifacts.
- Do not create active reverse-proxy routing.
- Return provisioning failure.
- Send Google Chat failure notification.

### 15.3 Health check

Carry over the script's behavior:

- Connect over the new deployment Unix socket.
- Request:

```text
/~/health/check
```

- Send headers:

```http
X-Forwarded-Proto: https
X-Skip-Redirect: skip
```

- Treat only HTTP 2xx as healthy.
- Attempt up to 30 times.
- Use increasing delay based on the current script's pattern:

```text
1 + attempt * 2 seconds
```

The implementation should make attempts and delay behavior configurable, while preserving these values as defaults.

Capture:

- Last HTTP status.
- Last response body up to a safe size limit.
- Last connection error.
- Total elapsed time.

Never include secrets in health-check logs or notifications.

### 15.4 Traffic switch

Switch traffic by atomically replacing only the individual service Caddyfile.

Order:

1. Render new file to a temporary path.
2. Validate the rendered content.
3. Atomically rename it over the service file.
4. Validate full Caddy configuration.
5. Reload Caddy.
6. Confirm reload command succeeded.
7. Begin the 10-second drain period.

If Caddy validation or reload fails:

- Restore the previous service Caddyfile.
- Attempt to validate and reload the previous configuration.
- Leave the new container running for debugging.
- Keep the old container running.
- Return failure.

### 15.5 Drain and old-container removal

- Default drain period: 10 seconds.
- Configurable through main TOML.
- After drain, stop and remove only the old deployment container.
- Confirm the old container belongs to the service by labels before removal.
- Never remove a container solely from a user-controlled name.

---

## 16. Caddy configuration

## 16.1 Master Caddyfile

The master Caddyfile is manually maintained and imports service files:

```caddyfile
import /etc/caddy/services/*.caddy
```

The controller must not rewrite the master Caddyfile.

The host directory containing individual files is mounted into the Caddy container.

## 16.2 Active service file

Example:

```caddyfile
mysite.com, mysite-com.staging.com {
    reverse_proxy unix//run/whmcs/whmcs-123-blue/http.sock
}
```

The Caddy container must have the host socket root mounted at the same path, or at a consistently translated container path.

## 16.3 Suspended service file

Example:

```caddyfile
mysite.com, mysite-com.staging.com {
    root * /srv/modd-suspended
    try_files {path} /index.html
    file_server
}
```

The suspension page directory is mounted read-only into the Caddy container.

## 16.4 Termination

Remove:

```text
/etc/caddy/services/whmcs-123.caddy
```

Then validate and reload Caddy.

## 16.5 Safety checks

- Service IDs must map to fixed filenames.
- Domains must pass strict hostname validation.
- Caddyfile content must be rendered from a fixed template.
- Never accept raw Caddyfile directives from WHMCS.
- Always validate before reload.
- Keep the previous file until replacement succeeds.
- Log validation output with safe truncation.

---

## 17. Status and sync model

The controller should remain as stateless as practical.

`GET /v1/services/{id}` combines:

- Service TOML.
- Tombstone, when deleted.
- ZFS dataset existence and used storage.
- Docker containers discovered by labels.
- Current running/stopped container status.
- Caddyfile existence and parsed upstream socket.
- Metrics provider values.

Suggested response:

```json
{
  "id": "whmcs-123",
  "state": "active",
  "main_domain": "mysite.com",
  "staging_domain": "mysite-com.staging.com",
  "version": "v21.6.24",
  "live_deploy": "green",
  "dataset": {
    "name": "tank/modd/sites/whmcs-123",
    "exists": true,
    "used_bytes": 123456789
  },
  "caddy": {
    "configured": true,
    "mode": "proxy",
    "socket": "/run/whmcs/whmcs-123-green/http.sock"
  },
  "containers": [
    {
      "name": "WHMCS-123-green",
      "deploy": "green",
      "version": "v21.6.24",
      "exists": true,
      "running": true,
      "healthy": true,
      "labels": {
        "au.modd.managed": "true",
        "au.modd.service-id": "whmcs-123",
        "au.modd.version": "v21.6.24",
        "au.modd.app": "whmcs",
        "au.modd.deploy": "green"
      }
    }
  ],
  "metrics": {
    "email_sends": 0,
    "monthly_traffic_bytes": 0,
    "source": "mock"
  },
  "warnings": []
}
```

### 17.1 Read-only sync rule

WHMCS sync must never:

- Start or stop containers.
- Modify Caddyfiles.
- Create or destroy ZFS datasets.
- Change controller TOML.
- Automatically reconcile discrepancies.

It reports observed state only.

### 17.2 Discrepancies

Return warnings for cases such as:

- TOML says active but no running container exists.
- Caddy points to blue but TOML says green.
- Dataset is missing.
- More than one deployment is running.
- Container labels do not match service TOML.
- Caddyfile exists for a terminated service.
- Deleted tombstone exists alongside a recreated live service.

---

## 18. Metrics provider

Metrics required in status:

- Disk storage.
- Email sends.
- Monthly traffic.

### 18.1 Disk storage

Read from ZFS, preferably an exact machine-readable property such as used bytes.

### 18.2 Email and traffic

Use an interface:

```go
type MetricsProvider interface {
    GetServiceMetrics(ctx context.Context, serviceID string) (ServiceMetrics, error)
}
```

Initial implementation:

- `MockMetricsProvider` returns zero values and `source = "mock"`.

Later implementation may call an external service without changing the public API.

Metrics failure must not make lifecycle status unavailable. Return metrics as unavailable with a warning.

---

## 19. Google Chat notifications

Send messages for:

- Provision success.
- Provision failure.
- Suspend success.
- Resume success.
- Upgrade failure.
- Terminate success.
- Delete success.

No upgrade-success message is required for MVP unless later requested.

### 19.1 Message contents

Include relevant safe fields:

- Operation.
- Success or failure.
- Service ID.
- Main domain URL.
- Staging domain URL.
- Version.
- Live deployment slot.
- Controller host identifier.
- Short error summary.
- Request ID.

The earlier mention of package should be omitted because packages are excluded from MVP.

### 19.2 Failure behavior

Google Chat delivery failure must:

- Be logged.
- Not roll back a successful hosting operation.
- Be included as a warning in the API response where useful.
- Never expose the webhook URL.

Use the current Google Chat webhook format accepted by the configured endpoint, but isolate message rendering behind an interface so payload format can be updated.

---

## 20. Logging and observability

## 20.1 Structured daemon logs

Log:

- Timestamp.
- Level.
- Request ID.
- Operation.
- Service ID.
- Domain when safe.
- Target version.
- Deployment slot.
- Duration.
- Result.
- Safe error details.

Do not log:

- Bearer tokens.
- Google Chat webhook URLs.
- File contents containing secrets.
- Unbounded health response bodies.

Write structured logs to stderr. Under systemd, operators read and retain them
through journald.

## 20.2 Health and info

`GET /v1/health` should stay shallow and fast.

`GET /v1/info` may include component checks, but should not block for long operations.

---

## 21. Error-handling policy

### 21.1 Preserve failures

For provision and upgrade failures:

- Leave ZFS datasets.
- Leave skeleton directories.
- Leave generated config files.
- Leave unhealthy containers.
- Leave sockets or stale socket paths for inspection.
- Preserve the previous active deployment during failed upgrades.
- Record the failure in logs and TOML metadata where possible.

### 21.2 API errors

Suggested structure:

```json
{
  "error": {
    "code": "health_check_failed",
    "message": "New green deployment did not pass health checks",
    "request_id": "...",
    "details": {
      "deploy": "green",
      "version": "v21.6.24"
    }
  }
}
```

Do not return sensitive paths unnecessarily to non-administrator clients. Since the only intended client is WHMCS administration, safe diagnostic paths may be included where useful, but tokens and secret contents remain prohibited.

### 21.3 HTTP status guidance

- `200`: successful read or idempotent completed action.
- `201`: newly provisioned service.
- `400`: invalid request.
- `401`: missing/invalid token.
- `404`: unknown service or version.
- `409`: invalid lifecycle transition or conflicting existing state.
- `422`: valid request that cannot be completed, such as failed health check.
- `500`: unexpected controller failure.
- `503`: required local dependency unavailable.

---

## 22. Step-by-step build plan

## Phase 1: Finalize runtime contract

- [ ] Document the exact application image repository.
- [ ] Document required environment variables.
- [ ] Document required bind mounts.
- [ ] Confirm whether the MySQL socket bind remains required.
- [ ] Confirm shared-secrets mount requirements.
- [ ] Confirm the container process creates `/run/nginx/nginx.sock` or whether the new public socket should be renamed internally.
- [ ] Define host-to-container mapping for `/run/whmcs/{service}-{deploy}/http.sock`.
- [ ] Confirm Docker network name.
- [ ] Confirm restart policy.
- [ ] Confirm health endpoint and required headers.
- [ ] Record the Docker settings in a version-controlled specification.

Deliverable:

- `docs/container-runtime.md`

Acceptance gate:

- A container can be started manually using the documented settings without `/home`, cPanel users, `.htaccess`, or Apache.

## Phase 2: Go project skeleton

- [ ] Create Go module.
- [ ] Add command entry point.
- [ ] Add configuration loader and validation.
- [ ] Add structured logging.
- [ ] Add graceful shutdown.
- [ ] Add request ID middleware.
- [ ] Add bearer-token middleware.
- [ ] Add JSON error writer.
- [ ] Add TLS server configuration.
- [ ] Add build-version metadata.

Suggested structure:

```text
cmd/controller/main.go
internal/api/
internal/auth/
internal/config/
internal/caddy/
internal/docker/
internal/healthcheck/
internal/metrics/
internal/notify/
internal/service/
internal/state/
internal/zfs/
openapi.yaml
```

Acceptance gate:

- `GET /v1/health` works without authentication.
- `GET /v1/info` rejects missing token and succeeds with valid token.

## Phase 3: OpenAPI contract

- [ ] Define every endpoint.
- [ ] Define bearer security scheme.
- [ ] Define service ID validation.
- [ ] Define lifecycle state enum.
- [ ] Define provision request.
- [ ] Define upgrade request and downgrade confirmation.
- [ ] Define service status response.
- [ ] Define list response.
- [ ] Define image versions response.
- [ ] Define daemon log response.
- [ ] Define structured errors.
- [ ] Add examples.
- [ ] Validate specification in CI.

Acceptance gate:

- OpenAPI validation passes.
- Generated or handwritten Go handlers and PHP client requests match the contract.

## Phase 4: State repository and tombstones

- [ ] Implement per-service TOML read/write.
- [ ] Implement atomic writes.
- [ ] Implement file locking per service.
- [ ] Implement tombstone read/write.
- [ ] Implement service enumeration.
- [ ] Implement validation of service records.
- [ ] Add failure metadata fields.

Acceptance gate:

- Concurrent updates cannot corrupt TOML.
- A deleted tombstone can be listed and read independently of the destroyed dataset.

## Phase 5: Docker adapter

- [ ] Connect using native Go Docker client.
- [ ] Negotiate Docker API version.
- [ ] List containers by labels.
- [ ] Inspect containers.
- [ ] Create containers.
- [ ] Start containers.
- [ ] Stop containers.
- [ ] Remove containers.
- [ ] Validate labels before destructive operations.
- [ ] Implement image-version listing.
- [ ] Implement image existence check.
- [ ] Build container spec from fixed configuration and service data.

Acceptance gate:

- Test containers carry all required labels.
- Status can discover containers without relying only on names.

## Phase 6: ZFS adapter and folder templates

- [ ] Implement dataset existence check.
- [ ] Implement dataset creation with fixed mountpoint.
- [ ] Implement used-byte query.
- [ ] Implement recursive destroy with prefix guards.
- [ ] Create folder skeleton.
- [ ] Preserve partial results on failure.

Acceptance gate:

- Test service provision creates the expected dataset and exact folder structure.

## Phase 7: Caddy adapter

- [ ] Implement active Caddyfile template.
- [ ] Implement suspended Caddyfile template.
- [ ] Implement atomic service-file replacement.
- [ ] Implement file removal.
- [ ] Implement validation command.
- [ ] Implement reload command.
- [ ] Implement rollback to previous file after validation/reload failure.
- [ ] Implement parsing sufficient to report current proxy slot/socket.

Acceptance gate:

- One service can switch between active, suspended, and absent configuration without modifying the master Caddyfile.

## Phase 8: Health checker

- [ ] Implement Unix-socket HTTP transport.
- [ ] Implement health path.
- [ ] Add required forwarding headers.
- [ ] Accept only 2xx.
- [ ] Implement 30 attempts.
- [ ] Implement increasing delay defaults matching the script.
- [ ] Capture bounded diagnostic output.
- [ ] Add cancellation and timeout support.

Acceptance gate:

- Healthy test service passes.
- Unhealthy service exhausts retries and remains running.

## Phase 9: Provision operation

- [ ] Validate request.
- [ ] Derive staging domain when absent.
- [ ] Check tombstone/existing service conflicts.
- [ ] Create service TOML draft.
- [ ] Create ZFS dataset.
- [ ] Create folder skeleton.
- [ ] Render config templates.
- [ ] Start initial blue container.
- [ ] Health-check blue.
- [ ] On health success, render active Caddyfile.
- [ ] Validate/reload Caddy.
- [ ] Commit state `active`.
- [ ] Send success notification.
- [ ] On failure, preserve all artifacts and send failure notification.

Acceptance gate:

- Provision produces a healthy routed service.
- Every tested failure point leaves created resources intact.

## Phase 10: Suspend, resume, terminate, delete

### Suspend

- [ ] Stop managed service containers.
- [ ] Install suspended Caddyfile.
- [ ] Validate/reload Caddy.
- [ ] Set state `suspended`.
- [ ] Notify Google Chat.

### Resume

- [ ] Support suspended-to-active.
- [ ] Support terminated-to-active.
- [ ] Start/recreate last deployment as required.
- [ ] Health-check before routing.
- [ ] Restore active Caddyfile.
- [ ] Set state `active`.
- [ ] Notify Google Chat.

### Terminate

- [ ] Stop all service containers.
- [ ] Remove service Caddyfile.
- [ ] Validate/reload Caddy.
- [ ] Preserve dataset and TOML.
- [ ] Set state `terminated`.
- [ ] Notify Google Chat.

### Delete

- [ ] Require `terminated`.
- [ ] Remove managed containers.
- [ ] Ensure Caddyfile is absent.
- [ ] Destroy ZFS dataset.
- [ ] Remove live service TOML.
- [ ] Write tombstone.
- [ ] Notify Google Chat.

Acceptance gate:

- All allowed transitions work.
- All forbidden transitions return `409` without destructive side effects.

## Phase 11: Upgrade and downgrade

- [ ] Determine live slot from TOML.
- [ ] Verify against Caddy and Docker observations.
- [ ] Choose opposite slot.
- [ ] Verify selected version exists.
- [ ] Require confirmation for identified downgrade.
- [ ] Remove any existing target-slot container after label validation.
- [ ] Remove stale target socket.
- [ ] Start new target-slot container.
- [ ] Health-check target slot.
- [ ] Preserve unhealthy target on failure.
- [ ] Send upgrade-failure Google Chat message.
- [ ] Render Caddyfile for target socket.
- [ ] Validate and reload Caddy.
- [ ] Restore old Caddyfile if switch fails.
- [ ] Wait configured 10-second drain.
- [ ] Stop/remove old container.
- [ ] Update TOML live slot and version.

Acceptance gate:

- Blue-to-green and green-to-blue upgrades work.
- Downgrade requires explicit confirmation.
- Failed health check never changes live traffic.
- Caddy reload failure keeps old deployment live.

## Phase 12: Status, list, metrics, and logs

- [ ] Implement observed state aggregation.
- [ ] Implement discrepancy warnings.
- [ ] Implement ZFS used storage.
- [ ] Implement mock email sends.
- [ ] Implement mock monthly traffic.
- [ ] Implement list endpoint.
- [ ] Implement image versions endpoint.
- [ ] Implement last-250 daemon log endpoint.

Acceptance gate:

- Status remains available when metrics provider fails.
- Status reports mismatches without modifying resources.

## Phase 13: WHMCS provisioning module

- [ ] Implement module metadata.
- [ ] Implement server configuration fields.
- [ ] Implement API client with HTTPS verification.
- [ ] Implement bearer token header.
- [ ] Implement provision.
- [ ] Implement suspend.
- [ ] Implement resume.
- [ ] Block automatic termination.
- [ ] Add manual terminate action.
- [ ] Add manual delete action.
- [ ] Add upgrade/downgrade action.
- [ ] Implement read-only sync.
- [ ] Sanitize module logs.

Acceptance gate:

- PHP performs no local lifecycle operations.
- Every action is a bounded controller request.

## Phase 14: WHMCS addon module

- [ ] Controller overview page.
- [ ] Service list.
- [ ] Service detail.
- [ ] Manual termination confirmation.
- [ ] Manual delete confirmation.
- [ ] Version selector.
- [ ] Downgrade warning/confirmation.
- [ ] Bulk upgrade workflow.
- [ ] Daemon log page.
- [ ] CSRF protection.
- [ ] Administrator permission checks.
- [ ] HTML escaping.

Acceptance gate:

- Destructive actions cannot be invoked by GET requests.
- Only authorized administrators can access controls and logs.

## Phase 15: Packaging and operations

- [ ] Build static or self-contained Go binary.
- [ ] Create systemd unit.
- [ ] Create configuration example.
- [ ] Create token-generation procedure.
- [ ] Create Caddy master import setup instructions.
- [ ] Create Docker socket-mount instructions for Caddy.
- [ ] Create log-rotation setup.
- [ ] Create backup guidance for service TOML and tombstones.
- [ ] Create installation and upgrade runbook.

Acceptance gate:

- Controller restarts without losing service visibility.
- Existing active services are rediscovered from TOML, Docker labels, Caddyfiles, and ZFS.

---

## 23. Testing plan

## 23.1 Unit tests

### Configuration

- Valid configuration loads.
- Missing token fails.
- Invalid dataset prefix fails.
- Negative traffic drain fails.
- Invalid health settings fail.

### Domain handling

- Explicit staging domain is retained.
- Empty staging domain is derived correctly.
- Invalid domains are rejected.
- Hostname length limits are enforced.

### TOML

- Service record round-trips.
- Atomic replacement works.
- Corrupt TOML produces a clear error.
- Tombstone round-trips.

### State transitions

- Every allowed transition succeeds in the state machine.
- Every forbidden transition is rejected.
- Repeated operations are idempotent.

### Docker spec

- Required labels are always present.
- Slot-specific socket mounts differ.
- Slot-specific cache mounts differ.
- Image repository cannot be overridden.
- Container names are deterministic.

### Caddy rendering

- Active config contains only validated domains and expected socket.
- Suspended config contains only fixed suspension directives.
- No input can inject arbitrary Caddy directives.

### Downgrade confirmation

- Same version is accepted.
- Newer version is accepted.
- Older version without confirmation is rejected.
- Older version with confirmation is accepted.
- Unorderable version strings produce a warning and require confirmation when treated as a possible downgrade.

### Log tail

- Returns last 250 lines.
- Handles fewer than 250 lines.
- Handles missing file.
- Enforces byte limit.

## 23.2 Integration-test environment

Create an isolated test host or VM with:

- Docker Engine.
- ZFS test pool.
- Caddy container.
- Test image capable of exposing the required Unix socket.
- Temporary controller directories.
- Mock Google Chat HTTP endpoint.
- Mock metrics provider.
- Test TLS certificate.

Do not run destructive ZFS tests against a production pool.

## 23.3 API integration tests

- Health works without token.
- Every other endpoint rejects missing token.
- Invalid token rejected.
- Valid token accepted.
- Invalid JSON rejected.
- Oversized request rejected.
- Request IDs returned.
- OpenAPI examples pass against the implementation.

## 23.4 Provision tests

### Success

Verify:

- Dataset created.
- Folder skeleton created.
- Templates rendered.
- Blue container created with labels.
- Blue socket appears.
- Health passes.
- Caddyfile created.
- Caddy reload succeeds.
- TOML says active/blue/version.
- Success Chat message sent.

### Failure injection

Inject failure at:

- ZFS create.
- Directory creation.
- Template rendering.
- Docker image lookup.
- Container creation.
- Container start.
- Socket appearance.
- Health check.
- Caddy validation.
- Caddy reload.
- TOML final write.
- Google Chat delivery.

Verify all applicable artifacts remain for debugging.

## 23.5 Suspend tests

- Active container stops.
- Data remains.
- Caddy serves suspension files.
- TOML says suspended.
- Repeated suspend is safe.
- Chat notification is emitted.

## 23.6 Resume tests

### From suspended

- Current deployment starts.
- Health passes.
- Caddy returns to proxy mode.
- TOML says active.

### From terminated

- Missing container is recreated from last version.
- Health passes.
- Caddyfile is restored.
- State becomes active.

### Health failure

- Failed resumed container remains for debugging.
- Caddy is not switched to it.
- Previous state remains.

## 23.7 Terminate tests

- Both blue and green containers are removed.
- Caddyfile removed.
- Dataset remains.
- Files remain.
- TOML says terminated.
- Resume from terminated works.
- Chat notification emitted.

## 23.8 Delete tests

- Delete from active rejected.
- Delete from suspended rejected.
- Delete from terminated succeeds.
- Containers removed.
- Dataset destroyed.
- Live TOML removed.
- Tombstone created.
- Repeated delete is safe or returns a clear already-deleted response.
- Chat notification emitted.

## 23.9 Upgrade tests

### Blue to green

- New green container uses target version.
- Health checks green socket.
- Caddy switches to green.
- Ten-second drain occurs.
- Blue container is removed.
- TOML says green and target version.

### Green to blue

Repeat inverse flow.

### Health failure

- New container remains.
- Old container remains live.
- Caddy remains unchanged.
- Upgrade-failure Chat notification is sent.

### Caddy failure

- Previous Caddyfile restored.
- Both containers remain for debugging.
- Old deployment remains live.

### Downgrade

- Without confirmation: rejected.
- With confirmation: allowed and processed exactly like upgrade.

### Drain timing

- Default is approximately 10 seconds.
- Config override is honored.
- Old container is not stopped before drain completes.

## 23.10 Status and sync tests

Create mismatches and verify they are reported:

- TOML active, container absent.
- TOML green, Caddy blue.
- Two running containers.
- Dataset absent.
- Caddyfile absent.
- Wrong labels.
- Metrics provider failure.

Verify status calls do not repair or modify anything.

## 23.11 WHMCS tests

### Provisioning module

- Correct service ID mapping.
- Correct API endpoint per action.
- Correct bearer header.
- HTTPS verification enabled.
- Error messages map cleanly to WHMCS.
- Secrets are removed from module logs.
- Automatic termination cannot perform destructive action.

### Addon

- Administrator permission checks.
- CSRF checks.
- Manual termination confirmation.
- Manual delete double confirmation.
- Version list handling.
- Downgrade warning.
- Bulk upgrade partial failures.
- Log output escaped.

## 23.12 Security tests

- Path traversal in service ID rejected.
- Caddy injection through domain rejected.
- Docker label spoofing does not permit deleting unrelated containers.
- Dataset prefix guard blocks unrelated ZFS destruction.
- Token absent from logs.
- Webhook URL absent from logs.
- Symlink attacks against service state paths rejected where applicable.
- Log endpoint cannot read arbitrary files.
- API only accepts configured image repository and versions.

## 23.13 Restart and recovery tests

- Restart controller during idle state.
- Restart after container start but before health completion.
- Restart after health success but before Caddy switch.
- Restart after Caddy switch but before TOML update.
- Restart during drain period.
- Restart after old container removal.

After restart, status must expose discrepancies without destroying evidence.

For MVP, automatic transaction recovery is not required, but the operator must have enough status and logs to safely retry or repair.

## 23.14 Scale tests

Use the expected initial service count and at least twice that count.

Measure:

- `GET /v1/services` duration.
- Docker label scan duration.
- ZFS usage-query duration.
- Caddyfile parsing duration.
- WHMCS service-page load time.
- Bulk upgrade behavior with configured low concurrency.

The controller should avoid expensive per-service external calls during list operations where batch querying is possible.

---

## 24. MVP acceptance criteria

The MVP is complete when:

- WHMCS can provision a new service through the controller.
- The service receives a ZFS dataset and expected folder structure.
- The service runs as a labeled Docker container.
- Caddy routes both main and staging domains to the live Unix socket.
- Staging domain is derived when omitted.
- Suspend stops the container and serves the suspension page.
- Resume works from both suspended and terminated states.
- Terminate removes routing but preserves data.
- Delete is manual-only, requires terminated state, removes data, and leaves a tombstone.
- Upgrade toggles blue/green, health-checks the new slot, switches Caddy, drains for 10 seconds, and removes the old container.
- Downgrade is permitted only after an explicit warning and confirmation.
- Failed provision and upgrade attempts leave resources intact for debugging.
- Status is derived from TOML, Docker, ZFS, Caddy, and metrics.
- WHMCS sync is read-only.
- Image versions are available from `GET /v1/image/versions`.
- Google Chat notifications are sent for the required lifecycle events.
- Logs are written to stderr and do not contain secrets.
- No feature depends on WHM, cPanel, Apache, `.htaccess`, `/home` paths, or Unix user switching.

---

## 25. Remaining required confirmations

The project scope is now sufficiently defined for MVP planning. These implementation details still require confirmation before container execution code is finalized:

1. Exact Docker bind mounts that remain required after removing `/home` and per-user paths.
2. Whether `/var/lib/mysql:/run/mysql` remains required and acceptable.
3. Exact secrets directory source and ownership model.
4. Whether the application still creates `nginx.sock` internally, and how that maps to the requested host `http.sock` path.
5. Whether first-time provisioning should require the same health check as upgrades. This plan assumes yes.
6. How `GET /v1/image/versions` discovers versions initially: local Docker images, registry API, or a configured static source.
7. Whether a successful upgrade should also send a Google Chat notification. It is not currently required.
8. Whether failed provision should leave service TOML state absent, or store the closest intended state plus failure metadata. This plan recommends retaining TOML with failure metadata while not inventing a fifth lifecycle state.

These are runtime-contract details rather than new MVP features.
