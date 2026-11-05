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
the controller's configured suffix beside it; leave it blank to disable staging.
Per-service image versions and staging labels are kept in
`mod_moddhosting_services`, not customer-visible custom fields.
Configurable option names must use `code|Display Label`, where `code` is
lowercase snake case, and their selectable values should likewise use
`value|Display Label`. Product and product-group slugs provide the package
codes. Containers receive `ME_PACKAGE_PLAN=<product-slug>|<product-name>`,
`ME_PACKAGE_GROUP=<group-slug>|<group-name>`, and one `ME_PACKAGE_<CODE>` per
configurable option. Missing plan or group slug/name components are sent as
`Unknown` so provisioning and resume can continue. Packages are limited to 20
variables; excess configurable options are truncated and their codes are
recorded in the WHMCS module log.
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
named Services button; filter or sort the rows, select active services, and
submit a controller-local bulk upgrade. The controller persists the queue and
upgrades one service at a time, including across controller restarts. It
continues after two failures and cancels the remaining queue on the third.
Paid package/configurable-option changes run the same blue/green redeployment.
Resume always recreates the container with current package values. Editing only
a product/group name or slug requires the service's manual **Deploy** action.
The service page also shows the latest DNS status, error, and successful sync
time; use **Reconnect DNS** there to queue the current domain and IP again.
While a lifecycle action or DNS sync is running, the product/service page
refreshes every three seconds and stops when the controller work finishes.

Tests and static analysis:

```sh
composer install
composer test
composer analyse
```

## Matomo site IDs

The service admin page has an editable **Matomo ID**, stored as nullable
`mod_moddhosting_services.matomo_id`. Use a positive integer up to 4294967295;
blank clears the mapping and its `matomo_attempt` creation key. Saving changes
WHMCS only. The next WHMCS **Deploy** or **Unsuspend** passes the ID to the
controller and container as `ME_CONF___matomo___id`. Addon deployments and bulk
upgrades reuse the controller's cached ID; they do not reread WHMCS mappings.

Configure the addon's shared **Matomo URL** and **Matomo API Token** (`matomoUrl`
and `matomoToken`) to enable automatic site creation on WHMCS **Create** or
**Deploy** when the ID is blank. Use a token belonging to a Matomo superuser
(a POST-only token is recommended). The token stays in WHMCS; it is not sent
to controllers or containers. New sites are named exactly `example.com #123`
(normalized main hostname plus WHMCS service ID). Their URL list contains the
full HTTPS main hostname and the full HTTPS staging hostname when enabled;
**Only Track Visits matching URLs** is enabled at creation. Each subsequent
WHMCS Create/Deploy (including package changes) synchronizes that same site's
name, sets its group to `whmcs-<service_id>`, and replaces its URL list, removing
old main or staging hostnames. Existing tracking restrictions are preserved. Unsuspend only
forwards the saved ID and never calls Matomo.

Provisioning continues if Matomo synchronization fails. An already assigned ID
still reaches the controller. Next to the ID, **Status: Not Configured** means
no synchronization has completed since the ID was assigned, **Status: OK** means
the last sync succeeded, and **Error:** shows the last failure. The nullable
`matomo_last_error` column stores these states as NULL, an empty string, or a
bounded error message respectively. Errors also reach the WHMCS module log;
tokens are redacted before storage or logging. Replacing/clearing an ID resets
its status; saving the same ID preserves it. Clearing an ID allows the next Deploy to
create another remote site; it does not delete the previous Matomo site.
An uncertain creation result leaves a pending attempt to prevent duplicates.
The random attempt marker is temporarily saved in the new Matomo site's group.
Once the ID is persisted, the group is changed to `whmcs-<service_id>`; existing
sites receive that group on their next Deploy. Deploy recovers only an exact group match, or an exact site-name
match for pending creations made by older module versions. Recovered IDs are
saved before renaming the site. Check Matomo for the displayed marker and enter
its ID if automatic recovery cannot find exactly one site. An ordinary Save
of an unchanged blank ID preserves that marker; use the explicit reset checkbox
only after resolving the previous attempt. An explicit API blank also resets it.

## Import existing Matomo mappings

Deploy the controller first, then these WHMCS modules and their schema upgrade.
Before enabling automatic creation, back up the WHMCS database and import all
existing mappings, including suspended and terminated services. Keep automatic
creation unconfigured and pause lifecycle jobs and service admin edits during
the import. These SQL helpers require InnoDB tables and `CREATE ROUTINE`
permission; run them in a dedicated connection with the WHMCS database selected,
outside any existing transaction. They create a short-lived procedure, not an
import UI or permanent background job.

From the repository root, open the MySQL/MariaDB client and run:

```sql
SOURCE whmcs-plugin/sql/matomo-stage.sql;
INSERT INTO moddhosting_matomo_input (service_id, matomo_id) VALUES
    ('123', '45'), ('124', '46'); -- replace with your complete mapping
-- The staging file also contains an optional CSV LOAD DATA example.
SOURCE whmcs-plugin/sql/matomo-import.sql;
CALL moddhosting_import_matomo(0); -- preview and validate, no writes
CALL moddhosting_import_matomo(1); -- only after reviewing the preview
DROP PROCEDURE moddhosting_import_matomo;
```

Use WHMCS service IDs, not client IDs. Input IDs must be positive decimal
integers within the unsigned 32-bit range, without leading zeros. Surrounding
spaces are trimmed. CSV files must have no header; use LF line endings.
Preview labels each row `insert`, `fill blank`, `already matches`, `conflict`,
or a validation error, and lists Matomo IDs shared by multiple services for
review. Shared Matomo IDs are permitted. Invalid or duplicate service IDs,
unknown services, products using another module, and conflicting assigned IDs
abort the entire apply with a SQL error and transaction rollback. There is no
service-status filter. Only missing rows and NULL IDs are filled; identical
mappings are no-ops. Image version, staging label and other existing fields are
preserved. Fix rejected input and preview again; do not replace conflicts
automatically. Drop the helper even after a rejected call, and disconnect to
discard the temporary staging tables. Never continue an errored SQL batch with
`--force`.

After import, configure automatic creation and perform controlled WHMCS Deploys
for active services to propagate IDs. Leave suspended services stopped; their
IDs reach the controller on their next intended Unsuspend. A Deploy can restore
a terminated service, so deploy those only when restoration is intended.

For an API-based import, the existing WHMCS `ModuleCustom` action with
`func_name=updateConfig` accepts `matomo_id` alone (a positive integer, or blank
to clear it). Use your existing authenticated WHMCS API integration and check
every result. This path updates one service at a time and has no batch conflict
guard; use the SQL template when you need all-or-nothing import behavior.

The disposable SQL self-check can be run against an empty test database from
the repository root with `SOURCE whmcs-plugin/sql/tests/matomo-smoke.sql;`.
It creates fixture tables and must never run in a WHMCS database.
