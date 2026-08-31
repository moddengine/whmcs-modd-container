<?php

declare(strict_types=1);

use ModdHosting\ApiClient;
use ModdHosting\ApiException;
use WHMCS\Database\Capsule;

if (!defined('WHMCS')) {
    exit('This file cannot be accessed directly');
}

require_once __DIR__ . '/lib/ApiException.php';
require_once __DIR__ . '/lib/ApiClient.php';

/** @return array<string, mixed> */
function moddhosting_MetaData(): array
{
    return [
        'DisplayName' => 'Modd Container Hosting',
        'APIVersion' => '1.1',
        'RequiresServer' => true,
        'DefaultSSLPort' => 443,
    ];
}

/** @return array<string, mixed> */
function moddhosting_ConfigOptions(): array
{
    return [];
}

/** @param array<string, mixed> $params */
function moddhosting_CreateAccount(array $params): string
{
    try {
        return moddhosting_call($params, 'PUT', '/v1/services/' . moddhosting_service_id($params), [
            'main_domain' => (string) $params['domain'],
			'staging_domain' => moddhosting_selected_staging($params),
			'public_ipv4' => trim((string) $params['serverip']),
            'version' => moddhosting_selected_version($params),
            'display_name' => 'WHMCS service ' . (int) $params['serviceid'],
            'package' => moddhosting_package($params),
            'force_redeploy' => (bool) ($params['force_redeploy'] ?? false),
        ]);
    } catch (\Throwable $error) {
        return $error->getMessage();
    }
}

/** @return array<string, string> */
function moddhosting_AdminCustomButtonArray(): array
{
    return ['Deploy' => 'deploy', 'Reconnect DNS' => 'reconnectDns'];
}

/** @param array<string, mixed> $params */
function moddhosting_deploy(array $params): string
{
    $params['force_redeploy'] = true;
    return moddhosting_CreateAccount($params);
}

/** @param array<string, mixed> $params */
function moddhosting_ChangePackage(array $params): string
{
    $params['force_redeploy'] = true;
    return moddhosting_CreateAccount($params);
}

/** @param array<string, mixed> $params */
function moddhosting_reconnectDns(array $params): string
{
    return moddhosting_call($params, 'POST', '/v1/services/' . moddhosting_service_id($params) . '/dns/reconnect');
}

/** @param array<string, mixed> $params */
function moddhosting_SuspendAccount(array $params): string
{
    return moddhosting_call($params, 'POST', '/v1/services/' . moddhosting_service_id($params) . '/suspend');
}

/** @param array<string, mixed> $params */
function moddhosting_UnsuspendAccount(array $params): string
{
    try {
        return moddhosting_call($params, 'POST', '/v1/services/' . moddhosting_service_id($params) . '/resume', [
            'package' => moddhosting_package($params),
        ]);
    } catch (\Throwable $error) {
        return $error->getMessage();
    }
}

/** @param array<string, mixed> $params */
function moddhosting_TerminateAccount(array $params): string
{
    return moddhosting_call($params, 'POST', '/v1/services/' . moddhosting_service_id($params) . '/terminate');
}

/**
 * @param array<string, mixed> $params
 * @return array<string, mixed>
 */
function moddhosting_TestConnection(array $params): array
{
    try {
        moddhosting_client($params)->request('GET', '/v1/info');
        return ['success' => true, 'error' => ''];
    } catch (\Throwable $error) {
        return ['success' => false, 'error' => $error->getMessage()];
    }
}

/**
 * @param array<string, mixed> $params
 * @return array<string, mixed>
 */
function moddhosting_AdminServicesTabFields(array $params): array
{
    try {
        $client = moddhosting_client($params);
        $versions = moddhosting_versions($params);
        $selectedVersion = moddhosting_selected_version($params, $versions);
        $versionOptions = '';
        foreach ($versions as $version) {
            $tag = (string) $version['version'];
            $versionOptions .= '<option value="' . moddhosting_escape($tag) . '"' . ($tag === $selectedVersion ? ' selected' : '') . '>'
                . moddhosting_escape($tag) . '</option>';
        }
        $info = $client->request('GET', '/v1/info');
        $stagingSuffix = moddhosting_staging_suffix($params, $info);
        $requestedStaging = moddhosting_selected_staging($params, $stagingSuffix);
        $stagingLabel = $requestedStaging === '' ? '' : substr($requestedStaging, 0, -strlen('.' . $stagingSuffix));
        try {
            $status = $client->request('GET', '/v1/services/' . moddhosting_service_id($params));
        } catch (ApiException $error) {
            if ($error->status !== 404) {
                throw $error;
            }
            $status = [];
        }
        $requestedHost = strtolower(rtrim(trim((string) ($params['domain'] ?? '')), '.'));
        $deployedHost = (string) ($status['main_domain'] ?? '');
        $deployedStaging = (string) ($status['staging_domain'] ?? '');
        $inSync = $deployedHost !== '' && $requestedHost === $deployedHost
            && $requestedStaging === $deployedStaging;
        $busy = in_array((string) ($status['phase'] ?? ''), ['provisioning', 'starting', 'waiting_for_health', 'routing', 'draining'], true);
        if ($inSync) {
            $hostnameStatus = '<span class="text-success">&#10003; In sync</span>';
        } elseif ($busy) {
            $hostnameStatus = '<span class="text-warning">&#10007; Update in progress &mdash; refresh to confirm</span>';
        } elseif ($deployedHost === '') {
            $hostnameStatus = '<span class="text-warning">&#10007; Not deployed &mdash; run Create or Deploy</span>';
        } else {
            $hostnameStatus = '<span class="text-warning">&#10007; Out of sync &mdash; click Deploy to update the live hostname</span>';
        }
        $dnsStatus = match ((string) ($status['dns_status'] ?? '')) {
            'in_sync' => '<span class="text-success">&#10003; In sync</span>',
            'pending' => '<span class="text-warning">Pending</span>',
            'error' => '<span class="text-danger">&#10007; Error</span>',
            default => $status === [] ? 'not provisioned' : 'disabled',
        };
        $dnsSyncedAt = (string) ($status['dns_synced_at'] ?? '');
        return [
            'Image Version' => '<select name="modulefields[0]" class="form-control">' . $versionOptions . '</select>',
            'Staging Hostname' => '<div class="input-group" style="max-width:500px"><input type="text" name="modulefields[1]" class="form-control" maxlength="32" value="'
                . moddhosting_escape($stagingLabel) . '" placeholder="Blank to disable staging"><span class="input-group-addon">.'
                . moddhosting_escape($stagingSuffix) . '</span></div>',
            'Deployed Hostname' => moddhosting_escape($deployedHost !== '' ? $deployedHost : 'not provisioned'),
            'Deployed Staging Hostname' => moddhosting_escape($deployedStaging !== '' ? $deployedStaging : 'not provisioned'),
            'Hostname Status' => $hostnameStatus,
            'DNS Status' => $dnsStatus,
            'DNS Last Error' => moddhosting_escape((string) ($status['dns_last_error'] ?? '')),
            'DNS Last Synced' => moddhosting_escape($dnsSyncedAt !== '' ? $dnsSyncedAt : 'never'),
            'Controller State' => moddhosting_escape((string) ($status['state'] ?? 'unknown')),
            'Deployed Version' => moddhosting_escape((string) ($status['version'] ?? 'not provisioned')),
            'Live Deploy' => moddhosting_escape((string) ($status['live_deploy'] ?? 'unknown')),
            'Disk Used' => number_format((int) ($status['dataset_used_bytes'] ?? 0)) . ' bytes',
            'Monthly Traffic' => number_format((int) ($status['metrics']['monthly_traffic_bytes'] ?? 0)) . ' bytes',
            'Email Sends' => number_format((int) ($status['metrics']['email_sends'] ?? 0)),
            'Last Controller Check' => gmdate('c'),
            'Controller Error' => moddhosting_escape((string) ($status['last_error'] ?? '')),
        ];
    } catch (\Throwable $error) {
        return ['Controller Error' => moddhosting_escape($error->getMessage())];
    }
}

/** @param array<string, mixed> $params */
function moddhosting_AdminServicesTabFieldsSave(array $params): void
{
    $fields = $_POST['modulefields'] ?? [];
    $version = is_array($fields) ? trim((string) ($fields[0] ?? '')) : '';
    if (!in_array($version, array_column(moddhosting_versions($params), 'version'), true)) {
        throw new \InvalidArgumentException('Select an image version available from the controller.');
    }
    $staging = strtolower(trim((string) ($fields[1] ?? '')));
    if ($staging !== '' && !moddhosting_valid_staging_label($staging)) {
        throw new \InvalidArgumentException('Staging hostname must be a valid DNS label of at most 32 characters.');
    }
    Capsule::table('mod_moddhosting_services')->updateOrInsert(
        ['service_id' => (int) $params['serviceid']],
        ['image_version' => $version, 'staging_label' => $staging],
    );
}

/**
 * @param array<string, mixed> $params
 * @return array<string, mixed>
 */
function moddhosting_ClientArea(array $params): array
{
	$id = moddhosting_service_id($params);
	$token = '';
	$expires = '';
	try {
		$client = moddhosting_client($params);
		$status = moddhosting_public_status($client->request('GET', '/v1/services/' . $id));
		$monitor = $client->request('POST', '/v1/services/' . $id . '/monitor-token', ['origin' => moddhosting_origin($params)]);
		$token = is_string($monitor['token'] ?? null) ? $monitor['token'] : '';
		$expires = is_string($monitor['expires_at'] ?? null) ? $monitor['expires_at'] : '';
	} catch (\Throwable $error) {
		$status = ['id' => moddhosting_service_id($params), 'state' => 'unavailable', 'phase' => 'failed', 'message' => 'Live status is temporarily unavailable.', 'deployments' => []];
	}
	return ['templatefile' => 'clientarea', 'vars' => [
		'controllerStatusJSON' => json_encode($status, JSON_HEX_TAG | JSON_HEX_AMP | JSON_HEX_APOS | JSON_HEX_QUOT | JSON_THROW_ON_ERROR),
		'monitorTokenJSON' => json_encode($token, JSON_HEX_TAG | JSON_HEX_AMP | JSON_HEX_APOS | JSON_HEX_QUOT | JSON_THROW_ON_ERROR),
		'monitorExpiryJSON' => json_encode($expires, JSON_HEX_TAG | JSON_HEX_AMP | JSON_HEX_APOS | JSON_HEX_QUOT | JSON_THROW_ON_ERROR),
		'monitorUrlJSON' => json_encode(moddhosting_websocket_url($params, $id), JSON_HEX_TAG | JSON_HEX_AMP | JSON_HEX_APOS | JSON_HEX_QUOT | JSON_THROW_ON_ERROR),
	]];
}

/**
 * @param array<string, mixed> $status
 * @return array<string, mixed>
 */
function moddhosting_public_status(array $status): array
{
	return array_intersect_key($status, array_flip(['id', 'state', 'phase', 'operation', 'live_deploy', 'target_deploy', 'target_version', 'updated_at', 'message', 'deployments']));
}

/** @param array<string, mixed> $params */
function moddhosting_origin(array $params): string
{
	$url = (string) ($params['systemurl'] ?? \WHMCS\Config\Setting::getValue('SystemURL'));
	$parts = parse_url($url);
	if (($parts['scheme'] ?? '') !== 'https' || empty($parts['host'])) {
		throw new \RuntimeException('WHMCS System URL must use HTTPS for live status.');
	}
	return 'https://' . $parts['host'] . (isset($parts['port']) ? ':' . $parts['port'] : '');
}

/** @param array<string, mixed> $params */
function moddhosting_websocket_url(array $params, string $id): string
{
	$host = trim((string) ($params['serverhostname'] ?: $params['serverip']));
	$port = (int) ($params['serverport'] ?? 443) ?: 443;
	return 'wss://' . $host . ':' . $port . '/v1/services/' . rawurlencode($id) . '/status/ws';
}

/**
 * @param array<string, mixed> $params
 * @param array<string, mixed>|null $body
 */
function moddhosting_call(array $params, string $method, string $path, ?array $body = null): string
{
    try {
        $response = moddhosting_client($params)->request($method, $path, $body);
        logModuleCall('moddhosting', $method . ' ' . $path, $body, $response, [], [(string) $params['serverpassword']]);
        return 'success';
    } catch (\Throwable $error) {
        logModuleCall('moddhosting', $method . ' ' . $path, $body, $error->getMessage(), [], [(string) $params['serverpassword']]);
        return $error->getMessage();
    }
}

/** @param array<string, mixed> $params */
function moddhosting_client(array $params): ApiClient
{
    $host = trim((string) ($params['serverhostname'] ?: $params['serverip']));
    if ($host === '') {
        throw new \InvalidArgumentException('Controller hostname is required');
    }
    $port = (int) ($params['serverport'] ?? 443);
    return ApiClient::forController('https://' . $host . ':' . ($port ?: 443), (string) $params['serverpassword']);
}

/**
 * @param array<string, mixed> $params
 * @return list<array<string, mixed>>
 */
function moddhosting_versions(array $params): array
{
    $versions = moddhosting_client($params)->request('GET', '/v1/image/versions')['versions'] ?? [];
    if (is_array($versions)) {
        $versions = array_filter($versions, static fn(mixed $version): bool =>
            is_array($version) && is_string($version['version'] ?? null) && $version['version'] !== '');
    }
    if (!is_array($versions) || $versions === []) {
        throw new \RuntimeException('No image versions are available from the controller.');
    }
    return array_values($versions);
}

/**
 * @param array<string, mixed> $params
 * @param list<array<string, mixed>>|null $versions
 */
function moddhosting_selected_version(array $params, ?array $versions = null): string
{
    $versions ??= moddhosting_versions($params);
    $version = trim((string) (Capsule::table('mod_moddhosting_services')
        ->where('service_id', (int) $params['serviceid'])->value('image_version') ?? ''));
    if ($version === '') {
        $version = (string) $versions[0]['version'];
        Capsule::table('mod_moddhosting_services')->updateOrInsert(
            ['service_id' => (int) $params['serviceid']],
            ['image_version' => $version],
        );
    }
    if (!in_array($version, array_column($versions, 'version'), true)) {
        throw new \InvalidArgumentException('The service image version is not available from the controller.');
    }
    return $version;
}

/**
 * @param array<string, mixed> $params
 * @param array<string, mixed>|null $info
 */
function moddhosting_staging_suffix(array $params, ?array $info = null): string
{
    $info ??= moddhosting_client($params)->request('GET', '/v1/info');
    $suffix = strtolower(trim((string) ($info['staging_suffix'] ?? ''), '.'));
    if ($suffix === '') {
        throw new \RuntimeException('The controller did not provide its staging hostname suffix.');
    }
    return $suffix;
}

/** @param array<string, mixed> $params */
function moddhosting_selected_staging(array $params, ?string $suffix = null): string
{
    $staging = strtolower(trim((string) (Capsule::table('mod_moddhosting_services')
        ->where('service_id', (int) $params['serviceid'])->value('staging_label') ?? ''), '.'));
    if ($staging === '') {
        return '';
    }
    $suffix ??= moddhosting_staging_suffix($params);
    if (str_ends_with($staging, '.' . $suffix)) {
        return $staging;
    }
    if (!moddhosting_valid_staging_label($staging)) {
        throw new \InvalidArgumentException('Staging hostname must be a valid DNS label of at most 32 characters.');
    }
    return $staging . '.' . $suffix;
}

/**
 * @param array<string, mixed> $params
 * @return array<string, string>
 */
function moddhosting_package(array $params): array
{
    $model = $params['model'] ?? null;
    $product = is_object($model) ? ($model->product ?? null) : null;
    $group = is_object($product) ? ($product->productGroup ?? null) : null;
    $activeSlug = is_object($product) ? ($product->activeSlug ?? null) : null;
    $planSlug = is_object($activeSlug) ? trim((string) ($activeSlug->slug ?? '')) : '';
    $planName = is_object($product) ? trim((string) ($product->name ?? '')) : '';
    $groupSlug = is_object($group) ? trim((string) ($group->slug ?? '')) : '';
    $groupName = is_object($group) ? trim((string) ($group->name ?? '')) : '';

    $planSlug = $planSlug === '' ? 'Unknown' : $planSlug;
    $planName = $planName === '' ? 'Unknown' : $planName;
    $groupSlug = $groupSlug === '' ? 'Unknown' : $groupSlug;
    $groupName = $groupName === '' ? 'Unknown' : $groupName;

    $package = ['plan' => $planSlug . '|' . $planName, 'group' => $groupSlug . '|' . $groupName];
    $optionValues = [];
    $options = $params['configoptions'] ?? [];
    if (!is_array($options)) {
        throw new \InvalidArgumentException('WHMCS configurable options must be an array.');
    }
    foreach ($options as $code => $value) {
        if (!is_string($code) || preg_match('/^[a-z][a-z0-9_]{0,62}$/D', $code) !== 1 || isset($package[$code])) {
            throw new \InvalidArgumentException('Invalid or reserved configurable option code: ' . (string) $code);
        }
        if (!is_scalar($value) && $value !== null) {
            throw new \InvalidArgumentException('Configurable option values must be scalar.');
        }
        $optionValues[$code] = (string) $value;
    }
    ksort($optionValues);
    $optionLimit = 20 - count($package);
    if (count($optionValues) > $optionLimit) {
        $discardedCodes = array_keys(array_slice($optionValues, $optionLimit, null, true));
        logModuleCall('moddhosting', 'package variables truncated', [
            'service_id' => (int) ($params['serviceid'] ?? 0),
            'attempted_count' => count($package) + count($optionValues),
        ], ['discarded_codes' => $discardedCodes]);
        $optionValues = array_slice($optionValues, 0, $optionLimit, true);
    }
    $package += $optionValues;
    ksort($package);
    return $package;
}

function moddhosting_valid_staging_label(string $label): bool
{
    return strlen($label) <= 32 && preg_match('/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/D', $label) === 1;
}

/** @param array<string, mixed> $params */
function moddhosting_service_id(array $params): string
{
    return 'whmcs-' . (int) $params['serviceid'];
}

function moddhosting_escape(string $value): string
{
    return htmlspecialchars($value, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
}
