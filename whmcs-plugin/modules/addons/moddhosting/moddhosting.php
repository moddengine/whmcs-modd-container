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
        'version' => 'DEV',
        'author' => 'MODD Pty Ltd',
        'language' => 'english',
        'fields' => [
            'webhookToken' => [
                'FriendlyName' => 'Docker Hub Webhook Token',
                'Type' => 'password',
                'Size' => '64',
                'Description' => 'Generate a unique token, for example with: openssl rand -hex 32',
            ],
        ],
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
            'bulk' => moddhosting_services_page($vars),
            'images' => moddhosting_images_page($vars),
            default => moddhosting_overview_page(),
        };
    } catch (\Throwable $error) {
        echo '<div class="alert alert-danger">' . moddhosting_h($error->getMessage()) . '</div>';
    }
}

/** @param array<string, mixed> $vars */
function moddhosting_images_page(array $vars): void
{
    if ($_SERVER['REQUEST_METHOD'] === 'POST') {
        $action = (string) ($_POST['action'] ?? '');
        $version = $action === 'pull-latest' ? '' : trim((string) ($_POST['version'] ?? ''));
        if ($action !== 'pull-latest' && ($action !== 'pull-version' || !moddhosting_valid_version($version))) {
            throw new \InvalidArgumentException('Enter a valid image version.');
        }
        moddhosting_render_pull_results(moddhosting_pull_all($version));
    }
	$statuses = moddhosting_pull_status_all();
    $token = trim((string) ($vars['webhookToken'] ?? ''));
    echo '<h2>Docker images</h2>';
    if ($token === '') {
        echo '<div class="alert alert-warning">Set a Docker Hub Webhook Token in the addon configuration to enable the webhook URL.</div>';
    } else {
        $webhook = rtrim(\WHMCS\Config\Setting::getValue('SystemURL'), '/') . '/index.php?m=moddhosting&token=' . rawurlencode($token);
        echo '<p>Docker Hub webhook URL:</p><pre>' . moddhosting_h($webhook) . '</pre>';
    }
    $formToken = moddhosting_h(generate_token('plain'));
    echo '<form method="post" class="form-inline"><input type="hidden" name="token" value="' . $formToken . '">'
        . '<input type="hidden" name="action" value="pull-latest"><button class="btn btn-primary">Pull latest v* on all controllers</button></form>'
        . '<h3>Pull a PR/dev version</h3><form method="post" class="form-inline"><input type="hidden" name="token" value="' . $formToken . '">'
        . '<input type="hidden" name="action" value="pull-version"><input class="form-control" name="version" maxlength="128" required placeholder="pr-123"> '
        . '<button class="btn btn-default">Pull specific version on all controllers</button></form>';
	moddhosting_render_pull_statuses($statuses);
	if (array_filter($statuses, static fn(array $status): bool => $status['status'] === 'pending') !== []) {
		$url = $vars['modulelink'] . '&page=images';
		echo '<script>setTimeout(function(){location.href=' . json_encode($url, JSON_HEX_TAG | JSON_HEX_AMP | JSON_HEX_APOS | JSON_HEX_QUOT) . ';},2000);</script>';
	}
}

/** @return list<array{server: string, version?: string, error?: string}> */
function moddhosting_pull_all(string $version): array
{
    $results = [];
    foreach (moddhosting_servers() as $server) {
        $name = moddhosting_server_name($server);
        try {
            $pulled = moddhosting_client_from_server($server)->request('POST', '/v1/image/pull', ['version' => $version]);
            $results[] = ['server' => $name, 'version' => (string) ($pulled['version'] ?? $version)];
        } catch (\Throwable $error) {
            $results[] = ['server' => $name, 'error' => $error->getMessage()];
        }
    }
    if ($results === []) {
        throw new \RuntimeException('No Modd Hosting servers are configured.');
    }
    return $results;
}

/** @return list<array{server: string, status: string, version?: string, error?: string}> */
function moddhosting_pull_status_all(): array
{
	$results = [];
	foreach (moddhosting_servers() as $server) {
		$name = moddhosting_server_name($server);
		try {
			$status = moddhosting_client_from_server($server)->request('GET', '/v1/image/pull');
			$results[] = [
				'server' => $name,
				'status' => (string) ($status['status'] ?? 'unknown'),
				'version' => (string) ($status['version'] ?? ''),
				'error' => (string) ($status['error'] ?? ''),
			];
		} catch (\Throwable $error) {
			$results[] = ['server' => $name, 'status' => 'failed', 'error' => $error->getMessage()];
		}
	}
	return $results;
}

/** @param list<array{server: string, version?: string, error?: string}> $results */
function moddhosting_render_pull_results(array $results): void
{
    echo '<h3>Pull results</h3><table class="table"><tbody>';
    foreach ($results as $result) {
        $error = $result['error'] ?? '';
        echo '<tr><td>' . moddhosting_h($result['server']) . '</td><td class="' . ($error === '' ? 'text-success' : 'text-danger') . '">'
            . moddhosting_h($error === '' ? 'Queued ' . ($result['version'] ?? '') : $error) . '</td></tr>';
    }
    echo '</tbody></table>';
}

/** @param list<array{server: string, status: string, version?: string, error?: string}> $statuses */
function moddhosting_render_pull_statuses(array $statuses): void
{
	if ($statuses === []) {
		return;
	}
	echo '<h3>Current pull status</h3><table class="table"><thead><tr><th>Controller</th><th>Status</th><th>Version</th></tr></thead><tbody>';
	foreach ($statuses as $status) {
		$error = (string) ($status['error'] ?? '');
		$state = (string) $status['status'];
		echo '<tr><td>' . moddhosting_h($status['server']) . '</td><td class="' . ($state === 'failed' ? 'text-danger' : ($state === 'completed' ? 'text-success' : '')) . '">'
			. moddhosting_h($error !== '' ? $state . ': ' . $error : $state) . '</td><td>' . moddhosting_h((string) ($status['version'] ?? '')) . '</td></tr>';
	}
	echo '</tbody></table>';
}

function moddhosting_valid_version(string $version): bool
{
    return preg_match('/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/D', $version) === 1;
}

/** @param array<string, mixed> $vars
 * @return array<string, mixed>
 */
function moddhosting_clientarea(array $vars): array
{
    $status = 200;
    $message = 'Image pull queued.';
    $configured = trim((string) ($vars['webhookToken'] ?? ''));
    $provided = (string) ($_GET['token'] ?? '');
    if ($_SERVER['REQUEST_METHOD'] !== 'POST') {
        $status = 405;
        $message = 'POST required.';
    } elseif ($configured === '' || !hash_equals($configured, $provided)) {
        $status = 403;
        $message = 'Invalid webhook token.';
    } else {
        try {
            $payload = json_decode((string) file_get_contents('php://input'), true, 32, JSON_THROW_ON_ERROR);
            $version = is_array($payload) ? (string) ($payload['push_data']['tag'] ?? '') : '';
            if (!moddhosting_valid_version($version)) {
                throw new \InvalidArgumentException('Docker Hub payload did not contain a valid tag.');
            }
            if (!str_starts_with($version, 'v')) {
                $message = 'Non-v tag ignored.';
            } else {
                foreach (moddhosting_pull_all($version) as $result) {
                    if (isset($result['error'])) {
                        throw new \RuntimeException('One or more controllers failed to pull the image.');
                    }
                }
            }
        } catch (\Throwable $error) {
            $status = 502;
            $message = $error->getMessage();
        }
    }
    http_response_code($status);
    return [
        'pagetitle' => 'Docker image webhook',
        'templatefile' => 'webhook',
        'requirelogin' => false,
        'forcessl' => true,
        'vars' => ['message' => $message],
    ];
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
	$server = moddhosting_selected_server();
	$client = moddhosting_client_from_server($server);
	$services = $client->request('GET', '/v1/services')['services'] ?? [];
	$versions = $client->request('GET', '/v1/image/versions')['versions'] ?? [];
	if ($_SERVER['REQUEST_METHOD'] === 'POST') {
		if (($_POST['action'] ?? '') !== 'bulk-upgrade') {
			throw new \InvalidArgumentException('Invalid services action.');
		}
		$version = trim((string) ($_POST['version'] ?? ''));
		if (!in_array($version, array_column($versions, 'version'), true)) {
			throw new \InvalidArgumentException('Select an image version available on this controller.');
		}
		$available = [];
		foreach ($services as $service) {
			if (($service['state'] ?? '') === 'active') {
				$available[(string) ($service['id'] ?? '')] = true;
			}
		}
		$ids = [];
		foreach ((array) ($_POST['services'] ?? []) as $id) {
			if (is_string($id) && isset($available[$id])) {
				$ids[] = $id;
			}
		}
		if ($ids === []) {
			throw new \InvalidArgumentException('Select at least one active service.');
		}
		echo '<h2>Bulk upgrade results</h2><table class="table"><tbody>';
		foreach ($ids as $id) {
			try {
				$client->request('POST', '/v1/services/' . rawurlencode($id) . '/upgrade', [
					'version' => $version,
					'confirm_downgrade' => isset($_POST['confirm_downgrade']),
				]);
				echo '<tr><td>' . moddhosting_h($id) . '</td><td class="text-success">Success</td></tr>';
			} catch (\Throwable $error) {
				echo '<tr><td>' . moddhosting_h($id) . '</td><td class="text-danger">' . moddhosting_h($error->getMessage()) . '</td></tr>';
			}
		}
		echo '</tbody></table>';
		$services = $client->request('GET', '/v1/services')['services'] ?? [];
	}
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
	$serverID = (int) $server->id;
    echo '<h2>Services — ' . moddhosting_h(moddhosting_server_name($server)) . '</h2><form method="get" class="form-inline">'
        . '<input type="hidden" name="module" value="moddhosting"><input type="hidden" name="page" value="services">'
		. '<input type="hidden" name="server" value="' . $serverID . '">'
        . '<input class="form-control" name="q" placeholder="Domain or service ID" value="' . moddhosting_h($search) . '"> '
        . '<input class="form-control" name="state" placeholder="State" value="' . moddhosting_h($state) . '"> '
        . '<input class="form-control" name="version" placeholder="Version" value="' . moddhosting_h($version) . '"> '
        . '<button class="btn btn-default">Filter</button></form>';
	echo '<form method="post"><input type="hidden" name="token" value="' . moddhosting_h(generate_token('plain')) . '">'
		. '<input type="hidden" name="action" value="bulk-upgrade">'
		. '<table class="table table-striped"><thead><tr><th>Select</th><th>ID</th><th>Domains</th><th>State</th><th>Deploy</th><th>Version</th><th>Running</th><th>Disk</th><th>Email</th><th>Traffic</th><th>Updated</th></tr></thead><tbody>';
    foreach ($services as $service) {
        $id = (string) $service['id'];
        $running = count(array_filter($service['containers'] ?? [], static fn(array $item): bool => !empty($item['running'])));
        $url = $vars['modulelink'] . '&page=service&id=' . rawurlencode($id);
		$checkbox = ($service['state'] ?? '') === 'active' ? '<input type="checkbox" name="services[]" value="' . moddhosting_h($id) . '" aria-label="Select ' . moddhosting_h($id) . '">' : '';
		echo '<tr><td>' . $checkbox . '</td><td><a href="' . moddhosting_h($url) . '">' . moddhosting_h($id) . '</a></td>'
            . '<td>' . moddhosting_h((string) ($service['main_domain'] ?? '')) . '<br><small>' . moddhosting_h((string) ($service['staging_domain'] ?? '')) . '</small></td>'
            . '<td>' . moddhosting_h((string) ($service['state'] ?? '')) . '</td>'
            . '<td>' . moddhosting_h((string) ($service['live_deploy'] ?? '')) . '</td>'
            . '<td>' . moddhosting_h((string) ($service['version'] ?? '')) . '</td>'
            . '<td>' . $running . '</td><td>' . number_format((int) ($service['dataset_used_bytes'] ?? 0)) . '</td>'
            . '<td>' . number_format((int) ($service['metrics']['email_sends'] ?? 0)) . '</td>'
            . '<td>' . number_format((int) ($service['metrics']['monthly_traffic_bytes'] ?? 0)) . '</td>'
            . '<td>' . moddhosting_h((string) ($service['updated_at'] ?? '')) . '</td></tr>';
    }
	echo '</tbody></table><fieldset><legend>Upgrade selected services</legend><label>Target version <select name="version" class="form-control" required>';
	foreach ($versions as $version) {
		$tag = (string) ($version['version'] ?? '');
		echo '<option value="' . moddhosting_h($tag) . '">' . moddhosting_h($tag) . '</option>';
	}
	echo '</select></label><label><input type="checkbox" name="confirm_downgrade"> Confirm possible downgrades</label><br>'
		. '<button class="btn btn-primary">Upgrade selected</button></fieldset></form>';
}

/** @param array<string, mixed> $vars */
function moddhosting_service_page(array $vars): void
{
    $id = moddhosting_valid_id((string) ($_GET['id'] ?? ''));
    $client = moddhosting_addon_client_for_id($id);
    if ($_SERVER['REQUEST_METHOD'] === 'POST') {
        $action = (string) ($_POST['action'] ?? '');
        if ($action === 'purge' && ($_POST['confirmation'] ?? '') === 'PURGE') {
            $current = $client->request('GET', '/v1/services/' . rawurlencode($id));
            if (($current['state'] ?? '') !== 'terminated') {
                throw new \RuntimeException('Service must still be terminated before purging.');
            }
            $client->request('DELETE', '/v1/services/' . rawurlencode($id));
            echo '<div class="alert alert-success">Service data permanently purged.</div>';
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
    if (($service['state'] ?? '') === 'terminated') {
        echo '<h3>Delete / purge permanently</h3><p>This destroys the ZFS dataset and cannot be undone. Type PURGE.</p>'
            . '<form method="post"><input type="hidden" name="token" value="' . moddhosting_h($token) . '"><input type="hidden" name="action" value="purge">'
            . '<input name="confirmation" class="form-control" autocomplete="off"><button class="btn btn-danger">Permanently purge service</button></form>';
    }
}

function moddhosting_addon_client(): ApiClient
{
    $server = Capsule::table('tblservers')->where('type', 'moddhosting')->where('active', 1)->orderBy('id')->first();
    if (!$server) {
        throw new \RuntimeException('No active Modd Hosting server is configured.');
    }
    return moddhosting_client_from_server($server);
}

/** @return iterable<object> */
function moddhosting_servers(): iterable
{
	return Capsule::table('tblservers')->where('type', 'moddhosting')->orderBy('id')->get();
}

function moddhosting_selected_server(): object
{
	$selected = trim((string) ($_GET['server'] ?? ''));
	$query = Capsule::table('tblservers')->where('type', 'moddhosting');
	if ($selected !== '') {
		if (preg_match('/^[1-9][0-9]*$/D', $selected) !== 1) {
			throw new \InvalidArgumentException('Invalid controller server.');
		}
		$query->where('id', (int) $selected);
	}
	$server = $query->orderBy('id')->first();
	if (!$server) {
		throw new \RuntimeException('No matching Modd Hosting server is configured.');
	}
	return $server;
}

function moddhosting_server_name(object $server): string
{
	return trim((string) ($server->name ?? '')) ?: trim((string) ($server->hostname ?: $server->ipaddress));
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
	$html = '<p><a class="btn btn-default" href="' . moddhosting_h($moduleLink . '&page=overview') . '">Overview</a> ';
	foreach (moddhosting_servers() as $server) {
		$url = $moduleLink . '&page=services&server=' . (int) $server->id;
		$html .= '<a class="btn btn-default" href="' . moddhosting_h($url) . '">Services — ' . moddhosting_h(moddhosting_server_name($server)) . '</a> ';
	}
	$html .= '<a class="btn btn-default" href="' . moddhosting_h($moduleLink . '&page=images') . '">Docker images</a> ';
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
