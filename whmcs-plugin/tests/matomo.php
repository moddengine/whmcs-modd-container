<?php

declare(strict_types=1);

namespace WHMCS\Database {
    final class Capsule
    {
        public static array $services = [];
        public static array $settings = ["matomoUrl" => "https://stats.example", "matomoToken" => "secret-token"];
        public static bool $locked = false;
        public static bool $busy = false;
        public static bool $failIdWrite = false;
        public static bool $failMarkerWrite = false;
        public static function table(string $table): QueryBuilder { return new QueryBuilder($table); }
        public static function connection(): Connection { return new Connection(); }
        }
    final class Connection
    {
        public function selectOne(string $sql, array $bindings): object
        {
            assert($bindings === ["moddhosting:matomo:123"]);
            if (str_contains($sql, "GET_LOCK")) {
                assert(!Capsule::$locked, "lock acquired twice");
                Capsule::$locked = !Capsule::$busy;
                return (object) ["acquired" => (int) Capsule::$locked];
            }
            assert(Capsule::$locked);
            Capsule::$locked = false;
            return (object) ["released" => 1];
        }
        public function transaction(callable $callback): mixed { return $callback(); }
    }
    final class QueryBuilder
    {
        private array $filters = [];
        public function __construct(private string $table) {}
        public function where(string $key, mixed $value): self { $this->filters[$key] = $value; return $this; }
        public function join(string ...$args): self { return $this; }
        public function whereIn(string $key, array $values): self { return $this; }
        public function select(string ...$args): self { return $this; }
        public function get(): array { return []; }
        public function value(string $column): mixed
        {
            if ($this->table === "tbladdonmodules") {
                return Capsule::$settings[$this->filters["setting"]] ?? null;
            }
            return Capsule::$services[$this->filters["service_id"]][$column] ?? null;
        }
        public function updateOrInsert(array $keys, array $values): bool
        {
            $this->filters = $keys;
            $this->update($values);
            return true;
        }
        public function update(array $values): int
        {
            if (array_key_exists("matomo_id", $values) || array_key_exists("matomo_attempt", $values)) {
                assert(Capsule::$locked, "Matomo write outside the shared lock");
            }
            if (Capsule::$failMarkerWrite && isset($values["matomo_attempt"])) {
                throw new \RuntimeException("marker write failed");
            }
            if (Capsule::$failIdWrite && isset($values["matomo_id"])) {
                throw new \RuntimeException("ID write failed");
            }
            $id = $this->filters["service_id"];
            Capsule::$services[$id] = array_merge(Capsule::$services[$id] ?? [], $values);
            return 1;
        }
    }
}


namespace ModdHosting {
    function curl_init(): \CurlHandle { return \curl_init(); }
    function curl_reset(\CurlHandle $handle): void { $GLOBALS["curlOptions"] = []; }
    function curl_setopt_array(\CurlHandle $handle, array $options): bool { $GLOBALS["curlOptions"] = $options; return true; }
    function curl_exec(\CurlHandle $handle): string|false
    {
        $options = $GLOBALS["curlOptions"];
        $isMatomo = str_contains($options[CURLOPT_URL], "stats.example");
        if ($isMatomo) {
            assert(\WHMCS\Database\Capsule::$locked);
            assert(str_starts_with($options[CURLOPT_URL], "https://"));
            assert(!str_contains($options[CURLOPT_URL], "secret-token"));
            assert($options[CURLOPT_FOLLOWLOCATION] === false);
            assert($options[CURLOPT_PROTOCOLS] === CURLPROTO_HTTPS);
            assert($options[CURLOPT_POST] === true);
            parse_str($options[CURLOPT_POSTFIELDS], $body);
            assert($body["token_auth"] === "secret-token");
            if ($body["method"] === "SitesManager.addSite") {
                assert(\WHMCS\Database\Capsule::$services[123]["matomo_attempt"] === $body["siteName"], "marker missing before request");
                assert($body["urls"] === "https://example.com");
            }
            $GLOBALS["matomoRequests"][] = $body;
            assert($GLOBALS["responses"] !== [], "unexpected Matomo request");
            $next = array_shift($GLOBALS["responses"]);
            [$GLOBALS["curlStatus"], $response] = $next;
            $GLOBALS["curlErrno"] = $next[2] ?? 0;
            return $response;
        }
        $GLOBALS["controllerRequests"][] = $options;
        $GLOBALS["curlStatus"] = 200;
        if ($GLOBALS["controllerOffline"] ?? false) {
            return false;
        }
        if (str_ends_with($options[CURLOPT_URL], "/v1/info")) {
            return "{\"staging_suffix\":\"stage.example\"}";
        }
        if (str_ends_with($options[CURLOPT_URL], "/v1/image/versions")) {
            return "{\"versions\":[{\"version\":\"v1\"}]}";
        }
        return "{}";
    }
    function curl_getinfo(\CurlHandle $handle, int $option): int { return $GLOBALS["curlStatus"]; }
    function curl_error(\CurlHandle $handle): string { return ""; }
    function curl_errno(\CurlHandle $handle): int { return $GLOBALS["curlErrno"] ?? 0; }
    function curl_close(\CurlHandle $handle): void {}
}

namespace {
    use WHMCS\Database\Capsule;
    use Illuminate\Database\Schema\Builder;

    define("WHMCS", true);
    function logModuleCall(string $module, string $action, mixed $request, mixed $response, mixed $data = "", array $variablesToMask = []): void
    {
        assert(!str_contains(json_encode($response), "secret-token"));
        $GLOBALS["moduleCalls"][] = compact("action", "request", "response");
        if ($GLOBALS["failLog"] ?? false) {
            throw new \RuntimeException("logging unavailable");
        }
    }
    require_once dirname(__DIR__) . "/modules/servers/moddhosting/moddhosting.php";
    $params = ["serviceid" => 123, "domain" => "Example.COM.", "serverhostname" => "controller.example", "serverip" => "192.0.2.1", "serverpassword" => "controller-token"];
    $responses = $matomoRequests = $controllerRequests = $moduleCalls = [];

    Capsule::$services = [123 => ["image_version" => "v1", "staging_label" => ""]];

    foreach ([0, -1, 1.5, true, "0", "-1", "1.0", "4294967296", [], " 1"] as $invalid) {
        assert(moddhosting_updateConfig($params + ["matomo_id" => $invalid]) !== "success");
    }
    assert(moddhosting_updateConfig($params + ["matomo_id" => "4294967295"]) === "success");
    assert(Capsule::$services[123]["matomo_id"] === 4294967295);
    assert(moddhosting_matomo_id($params, true) === 4294967295);
    assert($matomoRequests === []);
    assert(moddhosting_updateConfig($params + ["matomo_id" => ""]) === "success");
    assert(moddhosting_matomo_id($params, false) === 0); // Unsuspend never creates.
    assert($matomoRequests === []);

    $responses = [[200, "71"]];
    assert(moddhosting_CreateAccount($params) === "success");
    assert(Capsule::$services[123]["matomo_id"] === 71);
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    assert(json_decode(end($controllerRequests)[CURLOPT_POSTFIELDS], true)["matomo_id"] === 71);
    assert(moddhosting_deploy($params) === "success");
    assert(moddhosting_ChangePackage($params) === "success");
    assert(count($matomoRequests) === 1);
    assert(moddhosting_UnsuspendAccount($params) === "success");
    assert(json_decode(end($controllerRequests)[CURLOPT_POSTFIELDS], true)["matomo_id"] === 71);
    assert(moddhosting_TerminateAccount($params) === "success");
    assert(Capsule::$services[123]["matomo_id"] === 71);

    // Offline controller leaves the local field visible and writable.
    $offline = $params;
    $offline["serverhostname"] = $offline["serverip"] = "";
    $fields = moddhosting_AdminServicesTabFields($offline);
    assert(isset($fields["Matomo Site ID"], $fields["Controller Error"]));
    $_POST = ["moddhosting_matomo_id" => "82"];
    moddhosting_AdminServicesTabFieldsSave($offline);
    assert(Capsule::$services[123]["matomo_id"] === 82);
    $_POST = [];

    // A form loaded online must still save Matomo edits if the controller drops before Save.
    $onlineFields = moddhosting_AdminServicesTabFields($params);
    assert(str_contains($onlineFields["Image Version"], "moddhosting_fields"));
    $controllerOffline = true;
    $_POST = ["moddhosting_fields" => "1", "modulefields" => ["v1", ""], "moddhosting_matomo_id" => "83"];
    moddhosting_AdminServicesTabFieldsSave($params);
    assert(Capsule::$services[123]["matomo_id"] === 83);
    $_POST["moddhosting_matomo_id"] = "";
    moddhosting_AdminServicesTabFieldsSave($params);
    assert(Capsule::$services[123]["matomo_id"] === null);
    $_POST["modulefields"][0] = "v2";
    $_POST["moddhosting_matomo_id"] = "84";
    try {
        moddhosting_AdminServicesTabFieldsSave($params);
        assert(false, "changed version was accepted without controller validation");
    } catch (\ModdHosting\ApiException) {
    }
    assert(Capsule::$services[123]["image_version"] === "v1");
    assert(Capsule::$services[123]["matomo_id"] === null);
    $controllerOffline = false;
    $_POST = [];

    // No settings or invalid domain must not leave a marker or block hosting.
    moddhosting_updateConfig($params + ["matomo_id" => ""]);
    Capsule::$settings["matomoUrl"] = "http://stats.example";
    assert(moddhosting_deploy($params) === "success");
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    assert(json_decode(end($controllerRequests)[CURLOPT_POSTFIELDS], true)["matomo_id"] === 0);
    Capsule::$settings["matomoUrl"] = "https://stats.example";
    assert(moddhosting_matomo_id(array_replace($params, ["domain" => "bad/domain"]), true) === 0);
    assert(Capsule::$services[123]["matomo_attempt"] === null);

    // A lost response triggers lookup only, never a second addSite.
    $responses = [[0, false]];
    assert(moddhosting_matomo_id($params, true) === 0);
    $marker = Capsule::$services[123]["matomo_attempt"];
    assert(is_string($marker) && $marker !== "");
    // Saving unrelated admin fields cannot discard an unresolved creation.
    $_POST = ["moddhosting_matomo_id" => ""];
    moddhosting_AdminServicesTabFieldsSave($offline);
    assert(Capsule::$services[123]["matomo_attempt"] === $marker);
    assert(str_contains(moddhosting_AdminServicesTabFields($offline)["Matomo Site ID"], "moddhosting_matomo_reset"));
    $_POST = [];
    $requestsBefore = count($matomoRequests);
    $responses = [[200, "[]"]];
    assert(moddhosting_matomo_id($params, true) === 0);
    assert(end($matomoRequests)["method"] === "SitesManager.getPatternMatchSites");
    assert(end($matomoRequests)["filter_limit"] === "-1");
    assert(Capsule::$services[123]["matomo_attempt"] === $marker);
    $responses = [[200, json_encode([["name" => $marker, "idsite" => "91"], ["name" => $marker, "idsite" => "92"]])]];
    assert(moddhosting_matomo_id($params, true) === 0);
    $responses = [[200, json_encode([["name" => "prefix " . $marker, "idsite" => "95"], ["name" => $marker, "idsite" => "91"]])]];
    assert(moddhosting_matomo_id($params, true) === 91);
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    assert(count($matomoRequests) === $requestsBefore + 3);

    // A JSON error may follow remote creation; reconcile instead of retrying addSite.
    moddhosting_updateConfig($params + ["matomo_id" => ""]);
    $responses = [[200, "{\"result\":\"error\",\"message\":\"secret-token rejected\"}"]];
    assert(moddhosting_matomo_id($params, true) === 0);
    assert(Capsule::$services[123]["matomo_attempt"] !== null);
    $marker = Capsule::$services[123]["matomo_attempt"];
    $responses = [[200, json_encode([["name" => $marker, "idsite" => "101"]])]];
    assert(moddhosting_matomo_id($params, true) === 101);

    // Connection refused is known to precede remote execution and permits retry.
    moddhosting_updateConfig($params + ["matomo_id" => ""]);
    $responses = [[0, false, CURLE_COULDNT_CONNECT]];
    assert(moddhosting_matomo_id($params, true) === 0);
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    $responses = [[200, "105"]];
    assert(moddhosting_matomo_id($params, true) === 105);

    // Post-creation storage failure retains the marker for reconciliation.
    moddhosting_updateConfig($params + ["matomo_id" => ""]);
    Capsule::$failIdWrite = true;
    $responses = [[200, "111"]];
    assert(moddhosting_matomo_id($params, true) === 0);
    $marker = Capsule::$services[123]["matomo_attempt"];
    Capsule::$failIdWrite = false;
    $responses = [[200, json_encode([["name" => $marker, "idsite" => "111"]])]];
    assert(moddhosting_matomo_id($params, true) === 111);

    // Failure before marker persistence performs no remote operation.
    moddhosting_updateConfig($params + ["matomo_id" => ""]);
    Capsule::$failMarkerWrite = true;
    $requestsBefore = count($matomoRequests);
    assert(moddhosting_matomo_id($params, true) === 0);
    assert(count($matomoRequests) === $requestsBefore);
    Capsule::$failMarkerWrite = false;

    // Competing deploy/manual edits cannot write or send requests without the lock.
    Capsule::$busy = true;
    assert(moddhosting_matomo_id($params, true) === 0);
    assert(moddhosting_updateConfig($params + ["matomo_id" => 121]) !== "success");
    assert(count($matomoRequests) === $requestsBefore);
    Capsule::$busy = false;

    // Invalid/scalar/redirect/HTTP error responses remain pending and never leak tokens.
    foreach ([[200, "not json"], [200, "{}"], [200, "0"], [302, "122"], [500, "122"]] as $response) {
        moddhosting_updateConfig($params + ["matomo_id" => ""]);
        $responses = [$response];
        assert(moddhosting_matomo_id($params, true) === 0);
        assert(Capsule::$services[123]["matomo_attempt"] !== null);
    }
    $_POST = ["moddhosting_matomo_id" => "", "moddhosting_matomo_reset" => "1"];
    moddhosting_AdminServicesTabFieldsSave($offline);
    $_POST = [];
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    $failLog = true;
    Capsule::$busy = true;
    assert(moddhosting_matomo_id($params, true) === 0);
    Capsule::$busy = false;
    $failLog = false;
    assert(!Capsule::$locked);
    assert($responses === []);
}
