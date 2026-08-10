<?php

declare(strict_types=1);

namespace {
    function check_token(string $namespace = 'WHMCS.default', ?string $token = null): void {}

    function generate_token(string $type = 'form'): string {}

    function decrypt(string $ciphertext): string {}

    /**
     * @param mixed $request
     * @param mixed $response
     * @param mixed $data
     * @param list<string> $variablesToMask
     */
    function logModuleCall(
        string $module,
        string $action,
        mixed $request,
        mixed $response,
        mixed $data = '',
        array $variablesToMask = [],
    ): void {}
}

namespace WHMCS\Database {
    final class Capsule
    {
        public static function table(string $table): QueryBuilder {}
    }

    final class QueryBuilder
    {
        public function where(string $column, mixed $operatorOrValue, mixed $value = null): self {}

        public function orderBy(string $column, string $direction = 'asc'): self {}

        public function join(string $table, string $first, string $operator, string $second): self {}

        public function select(string ...$columns): self {}

        public function first(): ?\stdClass {}
    }
}

namespace WHMCS\Config {
    final class Setting
    {
        public static function getValue(string $name): string {}
    }
}
