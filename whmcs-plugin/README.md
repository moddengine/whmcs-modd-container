# WHMCS Modd Hosting modules

Copy `modules/servers/moddhosting` and `modules/addons/moddhosting` into the
matching WHMCS directories.

Create a WHMCS server using module `Modd Container Hosting`:

- hostname: the HTTPS Caddy proxy hostname;
- port: public Caddy HTTPS port (normally 443);
- password: shared bearer token (WHMCS stores this encrypted).

Choose the image version on each service's admin page; choices come from the
selected controller with the most recent image first. Save the service before
running Create for initial provisioning or Deploy to reconcile its hostname and image.
The staging hostname field accepts a label of up to 32 characters and displays
the controller's configured suffix beside it. Per-service image versions and
staging labels are kept in `mod_moddhosting_services`, not customer-visible
custom fields.
The service page shows the hostname currently deployed by the controller and
whether it matches WHMCS. Activate the `Modd Hosting` addon and grant access
only to administrator roles that may perform hosting lifecycle actions.

Set the addon's **Docker Hub Webhook Token** to a unique value such as the
output of `openssl rand -hex 32`. The Docker images page shows the URL to add
as a Docker Hub repository webhook. Stable `v*` pushes are pulled by every
configured controller; other pushed tags are ignored. The same page can pull
the latest stable tag or an exact PR/dev tag manually and polls each controller
every two seconds until its pull completes or fails.

WHMCS termination removes containers and routing while retaining customer
data. Permanent purging, upgrades, bulk upgrades, and status live in
**Addons > Modd Hosting**. The service's **Deploy** action applies its current
hostname, staging hostname, IP, and image version. An active service is recreated
on the inactive blue/green slot even when its version is unchanged; a terminated
service is restored with its retained data. Each configured controller has a
named Services button; select active rows there to run a controller-local bulk upgrade.
The service page also shows the latest DNS status, error, and successful sync
time; use **Reconnect DNS** there to queue the current domain and IP again.

Static analysis:

```sh
composer install
composer analyse
```
