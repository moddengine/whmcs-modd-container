# Modd Hosting for WHMCS

Modd Hosting connects WHMCS to a small Go controller that provisions and
manages website application containers on a single Linux host.

WHMCS remains responsible for customers, billing, products, and administrator
actions. The controller owns the host-local work:

- Docker container lifecycle;
- ZFS datasets and site storage;
- blue/green deployments with health checks;
- per-service Caddy routing;
- suspension, termination, and permanent deletion;
- service state stored as TOML; and
- optional Google Chat lifecycle notifications.

The project does not depend on WHM, cPanel, Apache, `.htaccess`, Unix hosting
accounts, or `/home`-based paths.

## Repository layout

```text
go-service/       Go controller, OpenAPI contract, and host packaging
whmcs-plugin/     WHMCS provisioning and administrator addon modules
.github/          Tagged-release workflow
```

The full design and lifecycle rules are in
[whmcs-container-hosting-mvp-plan.md](whmcs-container-hosting-mvp-plan.md).

## How it works

```text
WHMCS ── HTTPS + bearer token ──> Caddy ── HTTP loopback ──> controller
                                                               │
                                            ┌──────────────────┼──────────────┐
                                            ▼                  ▼              ▼
                                          Docker              ZFS       service TOML
                                            │                                 │
                                            └──── Unix sockets ──> Caddy <────┘
```

Each WHMCS service becomes `whmcs-<service-id>`. Provisioning creates a ZFS
dataset, the required directory skeleton, a blue container, and an individual
Caddy service file. Upgrades deploy to the inactive blue/green slot, wait for
its Unix-socket health check, switch Caddy, drain existing traffic, and remove
the old container.

Lifecycle states are:

- `active`: application traffic routes to the live container;
- `suspended`: containers are stopped and Caddy serves the suspension page;
- `terminated`: containers and routing are removed, but data remains;
- `deleted`: containers and the ZFS dataset are removed, leaving a tombstone.

Deletion is only allowed after termination. Automatic WHMCS termination is
deliberately disabled; termination and deletion are administrator actions in
**Addons > Modd Hosting**.

## Requirements

- Linux with Docker Engine and ZFS;
- `useradd`, `groupadd`, `userdel`, and `groupdel` for per-service identities;
- a Caddy container or trusted Caddy proxy;
- the configured Docker network;
- locally available application images; and
- WHMCS with PHP 8.1 or newer and cURL.

The controller runs as root because it manages ZFS, Docker, Caddy files, and
service identities and storage. Each `whmcs-<id>` service uses a matching host
user and group with UID/GID `10000 + id`; its dataset, socket directories, and
container process use that identity. Do not add systemd filesystem namespace
options such as `ProtectSystem`, `ProtectHome`, or `PrivateTmp`:
controller-created ZFS mounts and Unix socket directories must be visible to
Docker and the host.

## Install the controller

Download the Linux x86-64 controller from a tagged GitHub release, or build it:

```sh
(cd go-service && ./build.sh test && VERSION=dev ./build.sh)
```

Install the binary and supplied files (replace `go-service/controller` with
the downloaded release binary when using a release):

```sh
sudo install -m 0755 go-service/controller /usr/local/bin/modd-hosting-controller
sudo install -d -m 0750 /etc/modd-hosting /var/lib/modd-hosting/caddy/services
sudo install -d -m 0750 /var/lib/modd-hosting/services /var/lib/modd-hosting/tombstones
sudo install -d -m 0750 /srv/modd-suspended /run/whmcs
sudo install -m 0640 go-service/config.example.toml /etc/modd-hosting/controller.toml
sudo install -m 0644 go-service/packaging/suspended/index.html /srv/modd-suspended/index.html
openssl rand -hex 32 | sudo tee /etc/modd-hosting/api-token >/dev/null
sudo chmod 0600 /etc/modd-hosting/api-token
```

Review the configuration before starting the service. Install
`go-service/packaging/modd-hosting-controller.service` as a systemd unit and
`go-service/packaging/logrotate` under `/etc/logrotate.d/`.

Caddy must:

- import `/etc/caddy/services/*.caddy`;
- mount the controller's `caddy.service_config_dir` at `/etc/caddy/services`;
- mount the host socket tree referenced by `deployment.socket` at the same path inside the container;
- mount `caddy.suspension_root` read-only at the same path; and
- expose the controller to WHMCS over HTTPS with a trusted certificate.

The controller itself serves HTTP. Keep its listener on loopback when Caddy
runs on the host. If Caddy runs in a container, use host networking or bind the
controller to a firewall-restricted address reachable only by Caddy.

## Controller configuration

The controller reads TOML from `/etc/modd-hosting/controller.toml` by default:

```sh
modd-hosting-controller -config /path/to/controller.toml
```

Start with [go-service/config.example.toml](go-service/config.example.toml).
Durations use Go notation such as `500ms`, `10s`, or `2m`.

### Server and authentication

| Setting | Purpose |
| --- | --- |
| `server.listen` | HTTP address used by the controller. Prefer loopback. |
| `server.request_timeout` | Maximum API read and write duration. |
| `server.shutdown_timeout` | Time allowed for graceful shutdown. |
| `auth.bearer_token_file` | File containing the shared WHMCS bearer token. |

The token file must contain one non-empty secret. All API endpoints except
`GET /v1/health` require it. The same value is stored as the WHMCS server
password.

### ZFS and state

| Setting | Purpose |
| --- | --- |
| `zfs.dataset_prefix` | Parent dataset; a service becomes `<prefix>/whmcs-123`. |
| `zfs.mount_prefix` | Absolute mount root; a service becomes `<root>/whmcs-123`. |
| `state.services_dir` | Live per-service TOML records. |
| `state.tombstones_dir` | Records retained after permanent deletion. |
| `state.templates_dir` | Optional `conf.json` and `plug.json` templates. |

If a template file is absent, the controller creates that service file with
an empty JSON object. Back up the services and tombstones directories; site
content follows the ZFS pool's snapshot and replication policy.

### Caddy

| Setting | Purpose |
| --- | --- |
| `caddy.service_config_dir` | Host directory for generated service Caddyfiles. |
| `caddy.suspension_root` | Directory containing the shared `index.html`. |
| `caddy.active_template` | Active service Caddyfile template, repeated per domain; supports `{domain}`, `{service_id}`, and `{slot}`. |
| `caddy.validate_command` | Argument array run after a service-file change. |
| `caddy.reload_command` | Argument array run after successful validation. |

Commands are executed directly, without a shell. Keep each executable and
argument as a separate TOML array item. The example commands validate and
reload a Caddy container named `caddy`. Change the container name or config
path if your Caddy deployment differs.

The master Caddyfile is operator-owned; the controller only creates files in
`service_config_dir`.

### Docker

| Setting | Purpose |
| --- | --- |
| `docker.network` | Existing Docker network joined by every service container. |
| `docker.image_repository` | Allowed image repository, without a tag. |
| `docker.binds` | Fixed bind mounts applied to every service container. |
| `docker.environment` | Additional fixed `KEY=value` environment entries. |

Available versions are local tags matching
`<docker.image_repository>:<version>`. Pull the required tag before selecting
it in WHMCS:

```sh
docker pull your-registry/whmcs-runtime:v21.6.24
```

Bind entries accept these placeholders:

| Placeholder | Value |
| --- | --- |
| `{mountpoint}` | Service ZFS mountpoint. |
| `{service_id}` | Stable ID such as `whmcs-123`. |
| `{slot}` | `blue` or `green`. |

The same placeholders expand in `docker.environment` entries. The controller
also supplies `ME_SITE`, `ME_INSTANCE`, and the slot-specific socket bind
automatically. The image must create `http.sock` in that socket directory. See
[go-service/docs/container-runtime.md](go-service/docs/container-runtime.md)
before changing mounts.

### Deployment and domains

| Setting | Purpose |
| --- | --- |
| `deployment.health_path` | HTTP path checked through the new Unix socket. |
| `deployment.health_attempts` | Maximum checks before deployment failure. |
| `deployment.health_initial_delay` | Delay before the first check. |
| `deployment.health_backoff_increment` | Increasing delay between checks. |
| `deployment.traffic_drain` | Wait before removing the old deployment. |
| `deployment.socket` | Host, container, and Caddy-visible Unix-socket path template; supports `{service_id}` and `{slot}`. |
| `domains.staging_suffix` | Suffix used when WHMCS supplies no staging domain. |

For example, `example.com` with suffix `staging.com` becomes
`example-com.staging.com`. A deployment is routed only after the health
endpoint returns HTTP 2xx. Failed provision and upgrade artifacts remain in
place for diagnosis.

### Notifications and logging

| Setting | Purpose |
| --- | --- |
| `google_chat.webhook_url_file` | File containing a Google Chat webhook URL. |
| `logging.path` | JSON daemon log read by the API and WHMCS addon. |

Set `webhook_url_file` to an empty string to disable notifications. An
unreadable configured webhook disables notifications with a warning but does
not stop the controller.

Logs are written to stderr and `logging.path`. Rotation is handled by the
supplied host logrotate configuration.

## Install and configure WHMCS

Install the release ZIP into the WHMCS root, or copy the module directories:

```sh
cp -R whmcs-plugin/modules/servers/moddhosting /path/to/whmcs/modules/servers/
cp -R whmcs-plugin/modules/addons/moddhosting /path/to/whmcs/modules/addons/
```

In WHMCS:

1. Add a server using module **Modd Container Hosting**.
2. Set its hostname to the controller's HTTPS Caddy hostname.
3. Set the public Caddy HTTPS port, normally `443`.
4. Store the bearer token as the server password.
5. Create a product using that server.
6. On each service, select **Image Version** from the controller-provided list,
   then save the service before provisioning it.
7. Leave **Staging Domain** blank for automatic derivation.
8. Activate **Modd Hosting** under addon modules and restrict it to trusted
   administrator roles.
9. Set a unique **Docker Hub Webhook Token**, then copy the webhook URL from
   the addon's **Docker images** page into the Docker Hub repository settings.

The provisioning module creates, suspends, resumes, and displays services. The
addon provides controller status, manual termination and deletion, upgrades,
bulk upgrades, and the controller log.
The Docker images page also pulls the latest stable `v*` tag or an exact
PR/dev tag on every configured controller.

## Development

Run controller tests:

```sh
cd go-service
./build.sh test
```

Run PHP static analysis:

```sh
cd whmcs-plugin
composer install
composer analyse
```

The API contract is [go-service/openapi.yaml](go-service/openapi.yaml).
Tagged pushes matching `v*` build and publish the controller and WHMCS ZIP as
GitHub release assets.
