# Container runtime contract

The controller starts only local images named
`<docker.image_repository>:<version>` on `docker.network`.

Fixed runtime values:

- user and group `whmcs-<service-id>` with numeric UID/GID `10000 + service-id`;
- labels: `au.modd.managed`, `au.modd.service-id`, `au.modd.version`,
  `au.modd.app`, and `au.modd.deploy`;
- environment: `ME_SITE=<service-id>` and
  `ME_INSTANCE=<service-id>-<slot>`, plus configured fixed values;
- one slot-specific socket directory mounted at the identical host/container path;
- configured binds and environment values may use `{mountpoint}`,
  `{service_id}`, and `{slot}`.

The image must create `http.sock` in the configured socket directory and answer
`GET /~/health/check` over that socket. The example bind paths are placeholders
for the final ModdEngine image contract. Add a MySQL socket bind only if the
image still requires it.

The container runs as the service's numeric UID/GID. Its image must support an
arbitrary non-root user and must not require writes outside the configured
service binds and socket directory.

Caddy must import `/etc/caddy/services/*.caddy`, mount the controller's
`caddy.service_config_dir` there, mount the host socket tree referenced by
`deployment.socket` at the same path, and mount the suspension root read-only.
