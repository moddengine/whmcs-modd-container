<?php

declare(strict_types=1);

define('WHMCS', true);
require_once dirname(__DIR__) . '/modules/addons/moddhosting/moddhosting.php';

$services = [
	['id' => 'whmcs-2', 'version' => 'v2', 'dataset_used_bytes' => 10],
	['id' => 'whmcs-1', 'version' => 'v10', 'dataset_used_bytes' => 20],
];
moddhosting_sort_services($services, 'version', 'asc');
assert($services[0]['version'] === 'v2');
moddhosting_sort_services($services, 'storage', 'desc');
assert($services[0]['id'] === 'whmcs-1');
