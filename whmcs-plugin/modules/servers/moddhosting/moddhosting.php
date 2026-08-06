<?php

declare(strict_types=1);

use ModdHosting\ApiClient;
use ModdHosting\ApiException;

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
    return [
        'Staging Domain' => ['Type' => 'text', 'Size' => '50', 'Description' => 'Blank derives it automatically'],
    ];
}

/** @param array<string, mixed> $params */
function moddhosting_CreateAccount(array $params): string
{
    try {
        return moddhosting_call($params, 'PUT', '/v1/services/' . moddhosting_service_id($params), [
            'main_domain' => (string) $params['domain'],
            'staging_domain' => trim((string) ($params['configoption1'] ?? '')),
            'version' => moddhosting_selected_version($params),
            'display_name' => 'WHMCS service ' . (int) $params['serviceid'],
        ]);
    } catch (\Throwable $error) {
        return $error->getMessage();
    }
}

/** @param array<string, mixed> $params */
function moddhosting_SuspendAccount(array $params): string
{
    return moddhosting_call($params, 'POST', '/v1/services/' . moddhosting_service_id($params) . '/suspend');
}

/** @param array<string, mixed> $params */
function moddhosting_UnsuspendAccount(array $params): string
{
    return moddhosting_call($params, 'POST', '/v1/services/' . moddhosting_service_id($params) . '/resume');
}

/** @param array<string, mixed> $params */
function moddhosting_TerminateAccount(array $params): string
{
    return 'Automatic termination is disabled. Use Addons > Modd Hosting to terminate manually.';
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
        $selected = trim((string) ($params['model']->serviceProperties->get('Image Version') ?? ''));
        if ($selected === '') {
            $selected = (string) $versions[0]['version'];
        }
        $options = '';
        foreach ($versions as $version) {
            $tag = (string) $version['version'];
            $options .= '<option value="' . moddhosting_escape($tag) . '"' . ($tag === $selected ? ' selected' : '') . '>'
                . moddhosting_escape($tag) . '</option>';
        }
        try {
            $status = $client->request('GET', '/v1/services/' . moddhosting_service_id($params));
        } catch (ApiException $error) {
            if ($error->status !== 404) {
                throw $error;
            }
            $status = [];
        }
        return [
            'Image Version' => '<select name="modulefields[0]" class="form-control">' . $options . '</select>',
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
    $version = trim((string) ($_POST['modulefields'][0] ?? ''));
    if ($version === '') {
        return;
    }
    $available = array_column(moddhosting_versions($params), 'version');
    if (!in_array($version, $available, true)) {
        throw new \InvalidArgumentException('Select an image version available from the controller.');
    }
    $params['model']->serviceProperties->save(['Image Version' => $version]);
}

/**
 * @param array<string, mixed> $params
 * @return array<string, mixed>
 */
function moddhosting_ClientArea(array $params): array
{
    try {
        $status = moddhosting_client($params)->request('GET', '/v1/services/' . moddhosting_service_id($params));
    } catch (\Throwable $error) {
        $status = ['state' => 'unavailable'];
    }
    return ['templatefile' => 'clientarea', 'vars' => ['controllerStatus' => $status]];
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

/** @param array<string, mixed> $params */
function moddhosting_selected_version(array $params): string
{
    $versions = moddhosting_versions($params);
    $version = trim((string) ($params['model']->serviceProperties->get('Image Version') ?? ''));
    if ($version === '') {
        $version = (string) $versions[0]['version'];
        $params['model']->serviceProperties->save(['Image Version' => $version]);
    }
    if (!in_array($version, array_column($versions, 'version'), true)) {
        throw new \InvalidArgumentException('The service image version is not available from the controller.');
    }
    return $version;
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
