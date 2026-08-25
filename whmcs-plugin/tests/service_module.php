<?php

declare(strict_types=1);

define('WHMCS', true);
require_once dirname(__DIR__) . '/modules/servers/moddhosting/moddhosting.php';

$properties = new class {
    /** @var array<string, string> */
    public array $values = ['Staging Hostname' => 'preview'];

    public function get(string $name): ?string
    {
        return $this->values[$name] ?? null;
    }

    /** @param array<string, string> $values */
    public function save(array $values): void
    {
        $this->values = array_merge($this->values, $values);
    }
};
$params = ['model' => (object) ['serviceProperties' => $properties]];

assert(moddhosting_selected_staging($params, 'staging.test') === 'preview.staging.test');
assert(moddhosting_valid_staging_label(str_repeat('a', 32)));
assert(!moddhosting_valid_staging_label(str_repeat('a', 33)));
