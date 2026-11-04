<?php

declare(strict_types=1);

namespace WHMCS\Database {
    final class Capsule
    {
        public static array $services = [];
        public static function table(string $table): QueryBuilder { return new QueryBuilder(); }
        public static function connection(): Connection { return new Connection(); }
        public static function schema(): \Illuminate\Database\Schema\Builder { return new \Illuminate\Database\Schema\Builder(); }
    }
    final class QueryBuilder
    {
        public function __call(string $method, array $args): self { return $this; }
        public function get(): array { return []; }
    }
    final class Connection
    {
        public function transaction(callable $callback): mixed { return $callback(); }
    }
}

namespace Illuminate\Database\Schema {
    final class Builder
    {
        public static array $columns = [];
        public function hasTable(string $table): bool { return self::$columns !== []; }
        public function hasColumn(string $table, string $column): bool { return isset(self::$columns[$column]); }
        public function create(string $table, callable $callback): void { $callback(new Blueprint()); }
        public function table(string $table, callable $callback): void { $callback(new Blueprint()); }
    }
    final class Blueprint
    {
        public function unsignedInteger(string $column): ColumnDefinition { return new ColumnDefinition($column, "unsignedInteger"); }
        public function string(string $column, int $length): ColumnDefinition { return new ColumnDefinition($column, "string"); }
    }
    final class ColumnDefinition
    {
        public function __construct(private string $column, string $type) {
            assert(!isset(Builder::$columns[$column]), "duplicate schema column");
            Builder::$columns[$column] = ["type" => $type, "nullable" => false];
        }
        public function primary(): self { return $this; }
        public function default(mixed $value): self { return $this; }
        public function nullable(): self { Builder::$columns[$this->column]["nullable"] = true; return $this; }
    }
}

namespace {
    use WHMCS\Database\Capsule;
    use Illuminate\Database\Schema\Builder;
    define("WHMCS", true);
    require_once dirname(__DIR__) . "/modules/addons/moddhosting/moddhosting.php";
    // Fresh install and repeated upgrade preserve columns and existing records.
    moddhosting_install();
    assert(Builder::$columns["matomo_id"] === ["type" => "unsignedInteger", "nullable" => true]);
    assert(Builder::$columns["matomo_attempt"]["nullable"]);
    $schema = Builder::$columns;
    moddhosting_install();
    assert(Builder::$columns === $schema);
    Builder::$columns = array_diff_key($schema, array_flip(["matomo_id", "matomo_attempt"]));
    Capsule::$services = [123 => ["image_version" => "v1", "staging_label" => ""]];
    moddhosting_install();
    assert(Builder::$columns === $schema);
    assert(Capsule::$services[123] === ["image_version" => "v1", "staging_label" => ""]);

}
