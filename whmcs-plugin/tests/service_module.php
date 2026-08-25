<?php

declare(strict_types=1);

define('WHMCS', true);
require_once dirname(__DIR__) . '/modules/servers/moddhosting/moddhosting.php';

assert(moddhosting_ConfigOptions() === []);
assert(moddhosting_valid_staging_label(str_repeat('a', 32)));
assert(!moddhosting_valid_staging_label(str_repeat('a', 33)));
