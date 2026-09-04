<?php

declare(strict_types=1);

namespace WHMCS\Database {
    final class Capsule
    {
        /** @var array<int, array<string, mixed>> */
        public static array $services = [];

        public static function table(string $table): QueryBuilder
        {
            assert($table === 'mod_moddhosting_services');
            return new QueryBuilder();
        }
    }

    final class QueryBuilder
    {
        /** @param array<string, mixed> $attributes @param array<string, mixed> $values */
        public function updateOrInsert(array $attributes, array $values): bool
        {
            $id = (int) $attributes['service_id'];
            Capsule::$services[$id] = array_merge(Capsule::$services[$id] ?? [], $values);
            return true;
        }
    }
}

namespace {
    use WHMCS\Database\Capsule;

    define('WHMCS', true);

    $moduleCalls = [];
    function logModuleCall(string $module, string $action, mixed $request, mixed $response, mixed $data = '', array $variablesToMask = []): void
    {
        global $moduleCalls;
        $moduleCalls[] = compact('module', 'action', 'request', 'response');
    }

    require_once dirname(__DIR__) . '/modules/servers/moddhosting/moddhosting.php';

    $_POST['modulefields'] = ['another-module-value'];
    moddhosting_AdminServicesTabFieldsSave([]);
    unset($_POST['modulefields']);

assert(moddhosting_ConfigOptions() === []);
assert(moddhosting_valid_staging_label(str_repeat('a', 32)));
assert(!moddhosting_valid_staging_label(str_repeat('a', 33)));
assert(moddhosting_default_staging_label('Example.COM.') === 'example-com');
assert(moddhosting_default_staging_label('one.two.three.example.com') === 'one-two-three-example-com');
assert(moddhosting_default_staging_label('1234567890123456789012345678901.example.com') === '1234567890123456789012345678901');

$hostnameStatus = '<span>In sync</span>';
assert(moddhosting_hostname_summary('', '', $hostnameStatus) === 'not provisioned (Staging: disabled) <span>In sync</span>');
assert(moddhosting_hostname_summary('example.com', 'stage.example.com', $hostnameStatus) === '<a href="https://example.com" target="_blank" rel="noopener noreferrer">example.com</a> (Staging: <a href="https://stage.example.com" target="_blank" rel="noopener noreferrer">stage.example.com</a>) <span>In sync</span>');
assert(str_contains(moddhosting_hostname_summary('bad"<host>', '', $hostnameStatus), 'https://bad&quot;&lt;host&gt;'));

assert(str_contains(moddhosting_hostname_status('example.com', '', ['main_domain' => 'example.com']), 'In sync'));
assert(str_contains(moddhosting_hostname_status('example.com', '', ['phase' => 'routing']), 'Update in progress'));
assert(str_contains(moddhosting_hostname_status('example.com', '', []), 'Not deployed'));
assert(str_contains(moddhosting_hostname_status('example.com', '', ['main_domain' => 'old.example.com']), 'Out of sync'));

assert(moddhosting_dns_summary([]) === 'not provisioned (No Errors) Last Synced: never');
assert(str_contains(moddhosting_dns_summary(['dns_status' => 'in_sync']), 'In sync</span> (No Errors) Last Synced: never'));
assert(str_contains(moddhosting_dns_summary(['dns_status' => 'pending']), 'Pending</span> (No Errors)'));
assert(str_contains(moddhosting_dns_summary(['dns_status' => 'error', 'dns_last_error' => '<failed>', 'dns_synced_at' => '2026-09-03T00:00:00Z']), 'Error</span> (Error: &lt;failed&gt;) Last Synced: 2026-09-03T00:00:00Z'));
assert(moddhosting_dns_summary(['dns_status' => 'disabled']) === 'disabled (No Errors) Last Synced: never');

$model = (object) ['product' => (object) [
    'name' => 'Small Hosting Plan',
    'activeSlug' => (object) ['slug' => 'small'],
    'productGroup' => (object) ['slug' => 'website-hosting', 'name' => 'Website Hosting'],
]];
assert(moddhosting_package([
    'model' => $model,
    'configoptions' => ['email_sends' => 500, 'module_course' => '1', 'module_classes' => '0'],
]) === [
    'email_sends' => '500',
    'group' => 'website-hosting|Website Hosting',
    'module_classes' => '0',
    'module_course' => '1',
    'plan' => 'small|Small Hosting Plan',
]);

assert(moddhosting_package([]) === [
    'group' => 'Unknown|Unknown',
    'plan' => 'Unknown|Unknown',
]);

$manyOptions = [];
for ($i = 0; $i < 19; ++$i) {
    $manyOptions[sprintf('option_%02d', $i)] = $i;
}
$truncated = moddhosting_package(['serviceid' => 123, 'model' => $model, 'configoptions' => $manyOptions]);
assert(count($truncated) === 20);
assert(!isset($truncated['option_18']));
assert($moduleCalls[0] === [
    'module' => 'moddhosting',
    'action' => 'package variables truncated',
    'request' => ['service_id' => 123, 'attempted_count' => 21],
    'response' => ['discarded_codes' => ['option_18']],
]);

try {
    moddhosting_package(['model' => $model, 'configoptions' => ['plan' => 'override']]);
    assert(false, 'reserved package code was accepted');
} catch (\InvalidArgumentException) {
}

$versions = [['version' => 'v26.6.8'], ['version' => 'v26.7.0']];
$params = ['serviceid' => 123];

$_POST = [
    'action' => 'ModuleCustom',
    'serviceid' => '123',
    'func_name' => 'updateConfig',
    'image_version' => 'v26.7.0',
    'staging_hostname' => 'API-Stage',
];
assert(moddhosting_updateConfig($params, $versions) === 'success');
assert(Capsule::$services[123] === ['image_version' => 'v26.7.0', 'staging_label' => 'api-stage']);
$_POST = [];
Capsule::$services = [];

assert(moddhosting_updateConfig($params + ['image_version' => 'v26.6.8'], $versions) === 'success');
assert(Capsule::$services[123] === ['image_version' => 'v26.6.8']);
assert(moddhosting_updateConfig($params + ['image_version' => 'missing'], $versions) === 'Select an image version available from the controller.');
assert(Capsule::$services[123] === ['image_version' => 'v26.6.8']);

assert(moddhosting_updateConfig($params + ['staging_hostname' => 'Example-Stage'], $versions) === 'success');
assert(Capsule::$services[123] === ['image_version' => 'v26.6.8', 'staging_label' => 'example-stage']);
assert(moddhosting_updateConfig($params + ['staging_hostname' => '-invalid'], $versions) === 'Staging hostname must be a valid DNS label of at most 32 characters.');
assert(Capsule::$services[123]['staging_label'] === 'example-stage');

assert(moddhosting_updateConfig($params + ['staging_hostname' => ''], $versions) === 'success');
assert(Capsule::$services[123] === ['image_version' => 'v26.6.8', 'staging_label' => '']);
assert(moddhosting_updateConfig($params, $versions) === 'Provide image_version or staging_hostname.');

Capsule::$services = [];
$_POST = ['moddhosting_fields' => '1', 'modulefields' => ['v26.7.0', 'Same-State']];
moddhosting_AdminServicesTabFieldsSave($params, $versions);
$adminState = Capsule::$services[123];
Capsule::$services = [];
assert(moddhosting_updateConfig($params + ['image_version' => 'v26.7.0', 'staging_hostname' => 'Same-State'], $versions) === 'success');
assert(Capsule::$services[123] === $adminState);
}
