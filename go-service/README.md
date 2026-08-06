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
