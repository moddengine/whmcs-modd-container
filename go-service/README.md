# Modd Hosting controller

Build and test:

```sh
./build.sh test
VERSION=1.0.0 COMMIT=abc123 BUILD_DATE=2026-07-30T00:00:00Z ./build.sh
```

Always use `./build.sh`. It disables CGO and verifies that the result does not
depend on a build-host dynamic loader. If an existing binary reports
`cannot execute: required file not found`, run `./build.sh check`; then rebuild
it.

Install the binary, `config.example.toml`, systemd unit, and logrotate file.
Create the token with `openssl rand -hex 32`, restrict secret files to root,
and expose the HTTP listener only to a Caddy proxy. Caddy must provide an HTTPS
certificate trusted by the WHMCS host. Confirm the image's mounts against
`docs/container-runtime.md` before provisioning.

For image pulls, set `docker.image_repository` to the Docker Hub
`namespace/repository`. `POST /v1/image/pull` with an empty `version` finds the
most recently pushed `v*` tag; a non-empty value pulls that exact tag. The
endpoint returns `202` immediately and logs the background pull result.

The example binds to `127.0.0.1`. For containerized Caddy, use host networking
or bind the controller to a firewall-restricted address on Caddy's network.

Run the release candidate lifecycle test against a disposable host with
passwordless sudo:

```sh
./e2e/run-remote.sh moddadmin@192.168.252.10 /path/to/ssh-key ./controller
```

The test installs the candidate, builds a tiny scratch image, and verifies
provisioning, host ownership, the container UID/GID, termination, and purge.
Set `CONTROLLER_PATH`, `API_URL`, `TOKEN_PATH`, `SITE_ROOT`, `IMAGE_VERSION`,
or `SERVICE_ID` when the test host differs from the playbook layout.

Back up `/var/lib/modd-hosting/services` and
`/var/lib/modd-hosting/tombstones`; site data follows the ZFS pool's normal
snapshot and replication policy.

## Browser status monitoring

The WHMCS server creates a one-hour browser token with
`POST /v1/services/{id}/monitor-token`, authenticated by the controller bearer
credential and a JSON body containing the WHMCS HTTPS `origin`. The browser
then connects to `wss://controller/v1/services/{id}/status/ws` with two
`Sec-WebSocket-Protocol` values: `modd-monitor` and that token. Its automatic
`Origin` header must match the token.

The socket sends a full `status` snapshot immediately, then only when the
repository, Docker, or Caddy view changes. It polls once per second, has no
history, and closes normally after sending the final `deleted` snapshot. See
`openapi.yaml` for the handshake and snapshot schemas.
