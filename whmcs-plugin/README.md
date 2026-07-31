# WHMCS Modd Hosting modules

Copy `modules/servers/moddhosting` and `modules/addons/moddhosting` into the
matching WHMCS directories.

Create a WHMCS server using module `Modd Container Hosting`:

- hostname: the HTTPS Caddy proxy hostname;
- port: controller port (normally 8443);
- password: shared bearer token (WHMCS stores this encrypted).

Set each product's image version in module setting 1. Activate the `Modd
Hosting` addon and grant access only to administrator roles that may perform
hosting lifecycle actions.

Automatic WHMCS termination intentionally returns an error. Termination,
permanent deletion, upgrades, bulk upgrades, status, and controller logs live
in **Addons > Modd Hosting**.

Static analysis:

```sh
composer install
composer analyse
```
