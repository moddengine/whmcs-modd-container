<?php

declare(strict_types=1);

use ModdHosting\ApiClient;
use WHMCS\Database\Capsule;

if (!defined('WHMCS')) {
    exit('This file cannot be accessed directly');
}

require_once dirname(__DIR__, 2) . '/servers/moddhosting/lib/ApiException.php';
require_once dirname(__DIR__, 2) . '/servers/moddhosting/lib/ApiClient.php';

/** @return array<string, mixed> */
function moddhosting_config(): array
{
    return [
        'name' => 'Modd Hosting',
        'description' => 'Administrative controller visibility and manual lifecycle actions.',
        'version' => '1.0.0',
        'author' => 'ModdEngine',
        'language' => 'english',
    ];
}

/** @return array<string, mixed> */
function moddhosting_activate(): array
{
    return ['status' => 'success', 'description' => 'Modd Hosting addon activated'];
}

/** @return array<string, mixed> */
function moddhosting_deactivate(): array
{
    return ['status' => 'success', 'description' => 'Modd Hosting addon deactivated'];
}

/** @param array<string, mixed> $vars */
function moddhosting_output(array $vars): void
{
    if (empty($_SESSION['adminid'])) {
        echo '<div class="alert alert-danger">Administrator access required.</div>';
        return;
    }
    $page = (string) ($_GET['page'] ?? 'overview');
    try {
        if ($_SERVER['REQUEST_METHOD'] === 'POST') {
            check_token('WHMCS.admin.default');
        }
        echo moddhosting_nav((string) $vars['modulelink']);
        match ($page) {
            'services' => moddhosting_services_page($vars),
            'service' => moddhosting_service_page($vars),
            'bulk' => moddhosting_bulk_page($vars),
            'log' => moddhosting_log_page(),
            default => moddhosting_overview_page(),
        };
    } catch (\Throwable $error) {
        echo '<div class="alert alert-danger">' . moddhosting_h($error->getMessage()) . '</div>';
    }
}

function moddhosting_overview_page(): void
{
    $client = moddhosting_addon_client();
    $health = $client->request('GET', '/v1/health');
    $info = $client->request('GET', '/v1/info');
    echo '<h2>Controller overview</h2><table class="table table-striped"><tbody>';
    moddhosting_row('Health', (string) ($health['status'] ?? 'unknown'));
    foreach ([
        'Version' => 'version',
        'Build commit' => 'commit',
        'Build date' => 'build_date',
        'Docker API' => 'docker_api_version',
        'ZFS prefix' => 'zfs_prefix',
        'State path' => 'services_dir',
        'Caddy service path' => 'caddy_service_config_dir',
        'Traffic drain' => 'traffic_drain',
        'Metrics' => 'metrics_provider',
    ] as $label => $key) {
        moddhosting_row($label, (string) ($info[$key] ?? 'unknown'));
    }
    foreach (($info['service_counts'] ?? []) as $state => $count) {
        moddhosting_row(ucfirst((string) $state), (string) $count);
    }
    echo '</tbody></table>';
}

/** @param array<string, mixed> $vars */
function moddhosting_services_page(array $vars): void
{
    $services = moddhosting_addon_client()->request('GET', '/v1/services')['services'] ?? [];
    $state = trim((string) ($_GET['state'] ?? ''));
    $version = trim((string) ($_GET['version'] ?? ''));
    $search = strtolower(trim((string) ($_GET['q'] ?? '')));
    $services = array_filter($services, static function (array $service) use ($state, $version, $search): bool {
        if ($state !== '' && ($service['state'] ?? '') !== $state) {
            return false;
        }
        if ($version !== '' && ($service['version'] ?? '') !== $version) {
            return false;
        }
        $haystack = strtolower(($service['id'] ?? '') . ' ' . ($service['main_domain'] ?? '') . ' ' . ($service['staging_domain'] ?? ''));
        return $search === '' || str_contains($haystack, $search);
    });
    echo '<h2>Services</h2><form method="get" class="form-inline">'
        . '<input type="hidden" name="module" value="moddhosting"><input type="hidden" name="page" value="services">'
        . '<input class="form-control" name="q" placeholder="Domain or service ID" value="' . moddhosting_h($search) . '"> '
        . '<input class="form-control" name="state" placeholder="State" value="' . moddhosting_h($state) . '"> '
        . '<input class="form-control" name="version" placeholder="Version" value="' . moddhosting_h($version) . '"> '
        . '<button class="btn btn-default">Filter</button></form>';
    echo '<table class="table table-striped"><thead><tr><th>ID</th><th>Domains</th><th>State</th><th>Deploy</th><th>Version</th><th>Running</th><th>Disk</th><th>Email</th><th>Traffic</th><th>Updated</th></tr></thead><tbody>';
    foreach ($services as $service) {
        $id = (string) $service['id'];
        $running = count(array_filter($service['containers'] ?? [], static fn(array $item): bool => !empty($item['running'])));
        $url = $vars['modulelink'] . '&page=service&id=' . rawurlencode($id);
        echo '<tr><td><a href="' . moddhosting_h($url) . '">' . moddhosting_h($id) . '</a></td>'
            . '<td>' . moddhosting_h((string) ($service['main_domain'] ?? '')) . '<br><small>' . moddhosting_h((string) ($service['staging_domain'] ?? '')) . '</small></td>'
            . '<td>' . moddhosting_h((string) ($service['state'] ?? '')) . '</td>'
            . '<td>' . moddhosting_h((string) ($service['live_deploy'] ?? '')) . '</td>'
            . '<td>' . moddhosting_h((string) ($service['version'] ?? '')) . '</td>'
            . '<td>' . $running . '</td><td>' . number_format((int) ($service['dataset_used_bytes'] ?? 0)) . '</td>'
            . '<td>' . number_format((int) ($service['metrics']['email_sends'] ?? 0)) . '</td>'
            . '<td>' . number_format((int) ($service['metrics']['monthly_traffic_bytes'] ?? 0)) . '</td>'
            . '<td>' . moddhosting_h((string) ($service['updated_at'] ?? '')) . '</td></tr>';
    }
    echo '</tbody></table>';
}

/** @param array<string, mixed> $vars */
function moddhosting_service_page(array $vars): void
{
    $id = moddhosting_valid_id((string) ($_GET['id'] ?? ''));
    $client = moddhosting_addon_client_for_id($id);
    if ($_SERVER['REQUEST_METHOD'] === 'POST') {
        $action = (string) ($_POST['action'] ?? '');
        if ($action === 'terminate' && ($_POST['confirmation'] ?? '') === 'TERMINATE') {
            $client->request('POST', '/v1/services/' . rawurlencode($id) . '/terminate');
            echo '<div class="alert alert-success">Service terminated.</div>';
        } elseif ($action === 'delete-confirm') {
            moddhosting_delete_confirmation($vars, $id);
            return;
        } elseif ($action === 'delete' && ($_POST['confirmation'] ?? '') === 'DELETE') {
            $current = $client->request('GET', '/v1/services/' . rawurlencode($id));
            if (($current['state'] ?? '') !== 'terminated') {
                throw new \RuntimeException('Service must still be terminated before deletion.');
            }
            $client->request('DELETE', '/v1/services/' . rawurlencode($id));
            echo '<div class="alert alert-success">Service data permanently deleted.</div>';
            return;
        } elseif ($action === 'upgrade') {
            $client->request('POST', '/v1/services/' . rawurlencode($id) . '/upgrade', [
                'version' => trim((string) ($_POST['version'] ?? '')),
                'confirm_downgrade' => isset($_POST['confirm_downgrade']),
            ]);
            echo '<div class="alert alert-success">Deployment changed successfully.</div>';
        } else {
            throw new \RuntimeException('Confirmation text did not match.');
        }
    }
    $service = $client->request('GET', '/v1/services/' . rawurlencode($id));
    $versions = $client->request('GET', '/v1/image/versions')['versions'] ?? [];
    echo '<h2>' . moddhosting_h($id) . '</h2><pre>' . moddhosting_h(json_encode($service, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES)) . '</pre>';
    $token = generate_token('plain');
    if (($service['state'] ?? '') === 'active') {
        echo '<h3>Upgrade or downgrade</h3><form method="post"><input type="hidden" name="token" value="' . moddhosting_h($token) . '">'
            . '<input type="hidden" name="action" value="upgrade"><select name="version" class="form-control">';
        foreach ($versions as $version) {
            $tag = (string) ($version['version'] ?? '');
            echo '<option value="' . moddhosting_h($tag) . '"' . ($tag === ($service['version'] ?? '') ? ' selected' : '') . '>' . moddhosting_h($tag) . '</option>';
        }
        echo '</select><label><input type="checkbox" name="confirm_downgrade"> I confirm this may be a downgrade and compatibility is not guaranteed.</label><br>'
            . '<button class="btn btn-primary">Deploy version</button></form>';
    }
    if (in_array(($service['state'] ?? ''), ['active', 'suspended'], true)) {
        echo '<h3>Terminate</h3><p>Containers stop and routing is removed; data remains. Type TERMINATE.</p>'
            . '<form method="post"><input type="hidden" name="token" value="' . moddhosting_h($token) . '"><input type="hidden" name="action" value="terminate">'
            . '<input name="confirmation" class="form-control" autocomplete="off"><button class="btn btn-warning">Terminate Hosting Service</button></form>';
    }
    if (($service['state'] ?? '') === 'terminated') {
        echo '<h3>Delete permanently</h3><p>This destroys the ZFS dataset and cannot be undone.</p>'
            . '<form method="post"><input type="hidden" name="token" value="' . moddhosting_h($token) . '"><input type="hidden" name="action" value="delete-confirm">'
            . '<button class="btn btn-danger">Continue to permanent deletion</button></form>';
    }
}

/** @param array<string, mixed> $vars */
function moddhosting_delete_confirmation(array $vars, string $id): void
{
    $action = $vars['modulelink'] . '&page=service&id=' . rawurlencode($id);
    echo '<h2>Final deletion confirmation</h2><div class="alert alert-danger">All site data will be permanently destroyed. Type DELETE to continue.</div>'
        . '<form method="post" action="' . moddhosting_h($action) . '"><input type="hidden" name="token" value="' . moddhosting_h(generate_token('plain')) . '">'
        . '<input type="hidden" name="action" value="delete"><input name="confirmation" class="form-control" autocomplete="off">'
        . '<button class="btn btn-danger">Permanently delete service</button></form>';
}

/** @param array<string, mixed> $vars */
function moddhosting_bulk_page(array $vars): void
{
    $client = moddhosting_addon_client();
    if ($_SERVER['REQUEST_METHOD'] === 'POST') {
        $version = trim((string) ($_POST['version'] ?? ''));
        $confirm = isset($_POST['confirm_downgrade']);
        $ids = array_values(array_filter((array) ($_POST['services'] ?? []), static fn(string $id): bool => preg_match('/^whmcs-[1-9][0-9]*$/', $id) === 1));
        echo '<h2>Bulk upgrade results</h2><table class="table"><tbody>';
        foreach ($ids as $id) {
            try {
                $client->request('POST', '/v1/services/' . rawurlencode($id) . '/upgrade', ['version' => $version, 'confirm_downgrade' => $confirm]);
                echo '<tr><td>' . moddhosting_h($id) . '</td><td class="text-success">Success</td></tr>';
            } catch (\Throwable $error) {
                echo '<tr><td>' . moddhosting_h($id) . '</td><td class="text-danger">' . moddhosting_h($error->getMessage()) . '</td></tr>';
            }
        }
        echo '</tbody></table>';
        return;
    }
    $search = strtolower(trim((string) ($_GET['q'] ?? '')));
    $services = array_filter($client->request('GET', '/v1/services')['services'] ?? [], static function (array $service) use ($search): bool {
        $haystack = strtolower(($service['id'] ?? '') . ' ' . ($service['main_domain'] ?? '') . ' ' . ($service['staging_domain'] ?? ''));
        return ($service['state'] ?? '') === 'active' && ($search === '' || str_contains($haystack, $search));
    });
    $versions = $client->request('GET', '/v1/image/versions')['versions'] ?? [];
    echo '<h2>Bulk upgrade</h2><form method="get" class="form-inline">'
        . '<input type="hidden" name="module" value="moddhosting"><input type="hidden" name="page" value="bulk">'
        . '<input class="form-control" name="q" placeholder="Domain or service ID" value="' . moddhosting_h($search) . '"> '
        . '<button class="btn btn-default">Filter</button></form>'
        . '<form method="post"><input type="hidden" name="token" value="' . moddhosting_h(generate_token('plain')) . '">'
        . '<table class="table"><thead><tr><th>Service</th><th>Domains</th><th>Version</th></tr></thead><tbody>';
    foreach ($services as $service) {
        echo '<tr><td><label><input type="checkbox" name="services[]" value="' . moddhosting_h((string) $service['id']) . '"> '
            . moddhosting_h((string) $service['id']) . '</label></td>'
            . '<td>' . moddhosting_h((string) ($service['main_domain'] ?? '')) . '<br><small>' . moddhosting_h((string) ($service['staging_domain'] ?? '')) . '</small></td>'
            . '<td>' . moddhosting_h((string) $service['version']) . '</td></tr>';
    }
    echo '</tbody></table><select name="version" class="form-control">';
    foreach ($versions as $version) {
        echo '<option value="' . moddhosting_h((string) $version['version']) . '">' . moddhosting_h((string) $version['version']) . '</option>';
    }
    echo '</select><label><input type="checkbox" name="confirm_downgrade"> Confirm possible downgrades</label><br>'
        . '<button class="btn btn-primary">Run sequentially</button></form>';
}

function moddhosting_log_page(): void
{
    $lines = moddhosting_addon_client()->request('GET', '/v1/log')['lines'] ?? [];
    echo '<h2>Controller log</h2><pre>' . moddhosting_h(implode("\n", array_map('strval', $lines))) . '</pre>';
}

function moddhosting_addon_client(): ApiClient
{
    $server = Capsule::table('tblservers')->where('type', 'moddhosting')->where('active', 1)->orderBy('id')->first();
    if (!$server) {
        throw new \RuntimeException('No active Modd Hosting server is configured.');
    }
    return moddhosting_client_from_server($server);
}

function moddhosting_addon_client_for_id(string $id): ApiClient
{
    $serviceId = (int) substr($id, 6);
    $server = Capsule::table('tblhosting')
        ->join('tblservers', 'tblservers.id', '=', 'tblhosting.server')
        ->where('tblhosting.id', $serviceId)
        ->where('tblservers.type', 'moddhosting')
        ->select('tblservers.*')
        ->first();
    if (!$server) {
        throw new \RuntimeException('WHMCS service or controller server was not found.');
    }
    return moddhosting_client_from_server($server);
}

function moddhosting_client_from_server(object $server): ApiClient
{
    $host = trim((string) ($server->hostname ?: $server->ipaddress));
    if ($host === '') {
        throw new \RuntimeException('Controller hostname is missing.');
    }
    return ApiClient::forController('https://' . $host . ':' . ((int) $server->port ?: 443), decrypt((string) $server->password));
}

function moddhosting_valid_id(string $id): string
{
    if (preg_match('/^whmcs-[1-9][0-9]*$/', $id) !== 1) {
        throw new \InvalidArgumentException('Invalid service ID.');
    }
    return $id;
}

function moddhosting_nav(string $moduleLink): string
{
    $links = ['overview' => 'Overview', 'services' => 'Services', 'bulk' => 'Bulk upgrades', 'log' => 'Controller log'];
    $html = '<p>';
    foreach ($links as $page => $label) {
        $html .= '<a class="btn btn-default" href="' . moddhosting_h($moduleLink . '&page=' . $page) . '">' . $label . '</a> ';
    }
    return $html . '</p>';
}

function moddhosting_row(string $label, string $value): void
{
    echo '<tr><th>' . moddhosting_h($label) . '</th><td>' . moddhosting_h($value) . '</td></tr>';
}

function moddhosting_h(string $value): string
{
    return htmlspecialchars($value, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
}
