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

        public static function schema(): \Illuminate\Database\Schema\Builder {}

        public static function connection(): Connection {}
    }

    final class QueryBuilder
    {
        public function where(string $column, mixed $operatorOrValue, mixed $value = null): self {}

        /** @param list<mixed> $values */
        public function whereIn(string $column, array $values): self {}

        public function orderBy(string $column, string $direction = 'asc'): self {}

        public function join(string $table, string $first, string $operator, string $second): self {}

        public function select(string ...$columns): self {}

        public function first(): ?\stdClass {}

        /** @return iterable<\stdClass> */
        public function get(): iterable {}

        public function value(string $column): mixed {}

        /** @param array<string, mixed> $attributes @param array<string, mixed> $values */
        public function updateOrInsert(array $attributes, array $values): bool {}

        /** @param array<string, mixed> $values */
        public function update(array $values): int {}

        public function delete(): int {}
    }

    final class Connection
    {
        /** @template T @param callable(): T $callback @return T */
        public function transaction(callable $callback): mixed {}
    }
}

namespace Illuminate\Database\Schema {
    final class Builder
    {
        public function hasTable(string $table): bool {}

        public function hasColumn(string $table, string $column): bool {}

        /** @param callable(Blueprint): void $callback */
        public function create(string $table, callable $callback): void {}

        /** @param callable(Blueprint): void $callback */
        public function table(string $table, callable $callback): void {}
    }

    final class Blueprint
    {
        public function unsignedInteger(string $column): ColumnDefinition {}

        public function string(string $column, int $length = 255): ColumnDefinition {}
    }

    final class ColumnDefinition
    {
        public function primary(): self {}

        public function default(mixed $value): self {}
    }
}

namespace WHMCS\Config {
    final class Setting
    {
        public static function getValue(string $name): string {}
    }
}
