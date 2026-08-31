<?php

declare(strict_types=1);

define('WHMCS', true);

$moduleCalls = [];
function logModuleCall(string $module, string $action, mixed $request, mixed $response, mixed $data = '', array $variablesToMask = []): void
{
    global $moduleCalls;
    $moduleCalls[] = compact('module', 'action', 'request', 'response');
}

require_once dirname(__DIR__) . '/modules/servers/moddhosting/moddhosting.php';

assert(moddhosting_ConfigOptions() === []);
assert(moddhosting_valid_staging_label(str_repeat('a', 32)));
assert(!moddhosting_valid_staging_label(str_repeat('a', 33)));

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
