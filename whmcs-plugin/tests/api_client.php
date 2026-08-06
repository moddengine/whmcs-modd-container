<?php

declare(strict_types=1);

require_once dirname(__DIR__) . '/modules/servers/moddhosting/lib/ApiClient.php';

use ModdHosting\ApiClient;

$first = ApiClient::forController('https://controller.example', 'token');
assert($first === ApiClient::forController('https://controller.example/', 'token'));
assert($first !== ApiClient::forController('https://other.example', 'token'));
