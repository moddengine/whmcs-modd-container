<?php

declare(strict_types=1);

use ModdHosting\ApiClient;

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
        'Image Version' => ['Type' => 'text', 'Size' => '30', 'Description' => 'Local controller image tag'],
        'Staging Domain' => ['Type' => 'text', 'Size' => '50', 'Description' => 'Blank derives it automatically'],
    ];
}

/** @param array<string, mixed> $params */
function moddhosting_CreateAccount(array $params): string
{
    return moddhosting_call($params, 'PUT', '/v1/services/' . moddhosting_service_id($params), [
        'main_domain' => (string) $params['domain'],
        'staging_domain' => trim((string) ($params['configoption2'] ?? '')),
        'version' => trim((string) ($params['configoption1'] ?? '')),
        'display_name' => 'WHMCS service ' . (int) $params['serviceid'],
    ]);
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
        $status = moddhosting_client($params)->request('GET', '/v1/services/' . moddhosting_service_id($params));
        return [
            'Controller State' => moddhosting_escape((string) ($status['state'] ?? 'unknown')),
            'Image Version' => moddhosting_escape((string) ($status['version'] ?? 'unknown')),
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
    return new ApiClient('https://' . $host . ':' . ($port ?: 443), (string) $params['serverpassword']);
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
