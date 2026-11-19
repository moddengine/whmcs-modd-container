<?php

declare(strict_types=1);

namespace WHMCS\Database {
    final class Capsule
    {
        public static array $services = [];
        public static array $settings = ["matomoUrl" => "https://stats.example", "matomoToken" => "secret-token"];
        public static bool $locked = false;
        public static ?string $lockName = null;
        public static bool $busy = false;
        public static bool $failIdWrite = false;
        public static bool $failMarkerWrite = false;
        public static bool $failStatusWrite = false;
        public static function table(string $table): QueryBuilder { return new QueryBuilder($table); }
        public static function connection(): Connection { return new Connection(); }
        }
    final class Connection
    {
        public function selectOne(string $sql, array $bindings): object
        {
            assert(in_array($bindings, [["moddhosting:matomo:123"], ["moddhosting:email:123"]], true));
            if (str_contains($sql, "GET_LOCK")) {
                assert(!Capsule::$locked, "lock acquired twice");
                Capsule::$locked = !Capsule::$busy;
                Capsule::$lockName = Capsule::$locked ? $bindings[0] : null;
                return (object) ["acquired" => (int) Capsule::$locked];
            }
            assert(Capsule::$locked);
            assert(Capsule::$lockName === $bindings[0]);
            Capsule::$locked = false;
            Capsule::$lockName = null;
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
        public function first(): ?object
        {
            $service = Capsule::$services[$this->filters["service_id"]] ?? null;
            return $service === null ? null : (object) $service;
        }
        public function value(string $column): mixed
        {
            if ($this->table === "tblhosting") {
                assert($this->filters === ["id" => 123] && $column === "userid");
                return 7;
            }
            if ($this->table === "tbladdonmodules") {
                return Capsule::$settings[$this->filters["setting"]] ?? null;
            }
            return Capsule::$services[$this->filters["service_id"]][$column] ?? null;
        }
        public function updateOrInsert(array $keys, array $values): bool
        {
            $this->filters = $keys;
            $before = Capsule::$services[$keys["service_id"]] ?? null;
            $this->update($values);
            return $before !== Capsule::$services[$keys["service_id"]];
        }
        public function update(array $values): int
        {
            if (array_key_exists("matomo_id", $values) || array_key_exists("matomo_attempt", $values) || array_key_exists("matomo_last_error", $values)) {
                assert(Capsule::$lockName === "moddhosting:matomo:123", "Matomo write outside the shared lock");
            }
            if (array_intersect_key($values, array_flip(["email_configured_domain", "email_relay_id", "email_setup_error"])) !== []) {
                assert(Capsule::$lockName === "moddhosting:email:123", "Email setup result written outside the service lock");
            }
            if (Capsule::$failMarkerWrite && isset($values["matomo_attempt"])) {
                throw new \RuntimeException("marker write failed");
            }
            if (Capsule::$failIdWrite && isset($values["matomo_id"])) {
                throw new \RuntimeException("ID write failed");
            }
            if (Capsule::$failStatusWrite && array_key_exists("matomo_last_error", $values)) {
                throw new \RuntimeException("status write failed");
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
                assert(\WHMCS\Database\Capsule::$services[123]["matomo_attempt"] === $body["group"], "marker missing before request");
                assert($body["siteName"] === "example.com #123");
                assert($body["excludeUnknownUrls"] === "1");
                assert($body["urls"][0] === "https://example.com");
            }
            $GLOBALS["matomoRequests"][] = $body;
            if ($body["method"] === "SitesManager.updateSite" || $body["method"] === "SitesManager.setSiteAliasUrls") {
                assert(!isset($body["excludeUnknownUrls"]));
                assert($body["method"] === "SitesManager.updateSite" ? $body["group"] === "whmcs-123" : !isset($body["group"]));
                assert(\WHMCS\Database\Capsule::$services[123]["matomo_id"] === (int) $body["idSite"], "sync before saving site ID");
                $next = array_shift($GLOBALS["updateResponses"]);
                [$GLOBALS["curlStatus"], $response] = $next ?? [200, $body["method"] === "SitesManager.setSiteAliasUrls" ? '{"value":0}' : "{\"result\":\"success\"}"];
                if ($response instanceof \Throwable) {
                    throw $response;
                }
                return $response;
            }
            assert($GLOBALS["responses"] !== [], "unexpected Matomo request");
            $next = array_shift($GLOBALS["responses"]);
            [$GLOBALS["curlStatus"], $response] = $next;
            $GLOBALS["curlErrno"] = $next[2] ?? 0;
            return $response;
        }
        if (str_ends_with($options[CURLOPT_URL], "/~emailrelay/ses/site")) {
            assert(\WHMCS\Database\Capsule::$lockName === "moddhosting:email:123");
            assert($options[CURLOPT_URL] === "https://relay.example/~emailrelay/ses/site");
            assert($options[CURLOPT_CUSTOMREQUEST] === "POST");
            assert(in_array("Authorization: Bearer uku_setup-test-token", $options[CURLOPT_HTTPHEADER], true));
            assert(in_array("Content-Type: application/json", $options[CURLOPT_HTTPHEADER], true));
            assert($options[CURLOPT_SSL_VERIFYPEER] === true && $options[CURLOPT_SSL_VERIFYHOST] === 2);
            assert($options[CURLOPT_FOLLOWLOCATION] === false);
            $GLOBALS["emailRequests"][] = json_decode($options[CURLOPT_POSTFIELDS], true, flags: JSON_THROW_ON_ERROR);
            $GLOBALS["hostingEvents"][] = "relay";
            assert($GLOBALS["emailResponses"] !== [], "unexpected email relay request");
            [$GLOBALS["curlStatus"], $response] = array_shift($GLOBALS["emailResponses"]);
            return $response;
        }
        $GLOBALS["controllerRequests"][] = $options;
        if ($options[CURLOPT_CUSTOMREQUEST] === "PUT") {
            $GLOBALS["provisionEvents"][] = "controller";
        }
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
        if ($options[CURLOPT_CUSTOMREQUEST] === "PUT") {
            $GLOBALS["hostingEvents"][] = "controller";
            [$GLOBALS["curlStatus"], $response] = $GLOBALS["provisionResponse"] ?? [202, "{}"];
            return $response;
        }
        if (str_ends_with($options[CURLOPT_URL], "/v1/services/whmcs-123")) {
            return json_encode($GLOBALS["serviceStatus"] ?? (object) [], JSON_THROW_ON_ERROR);
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
    $responses = $updateResponses = $matomoRequests = $controllerRequests = $moduleCalls = [];

    Capsule::$services = [123 => ["image_version" => "v1", "staging_label" => ""]];
    assert(str_contains(moddhosting_matomo_field($params), "Status: Not Configured"));

    foreach ([0, -1, 1.5, true, "0", "-1", "1.0", "4294967296", [], " 1"] as $invalid) {
        assert(moddhosting_updateConfig($params + ["matomo_id" => $invalid]) !== "success");
    }
    assert(moddhosting_updateConfig($params + ["matomo_id" => "4294967295"]) === "success");
    assert(Capsule::$services[123]["matomo_id"] === 4294967295);
    assert(str_contains(moddhosting_matomo_field($params), "Status: Not Configured"));
    assert(moddhosting_matomo_id($params, false) === 4294967295);
    assert($matomoRequests === []);
    assert(moddhosting_updateConfig($params + ["matomo_id" => ""]) === "success");
    assert(moddhosting_matomo_id($params, false) === 0); // Unsuspend never creates.
    assert($matomoRequests === []);

    $provisionEvents = [];
    assert(moddhosting_CreateAccount(array_replace($params, ["domain" => "_bad.example.com"])) === "WHMCS DNS requires a valid hosting hostname.");
    assert(str_contains(moddhosting_CreateAccount($params), "Install and activate a compatible WHMCS DNS addon"));

    if (!function_exists('whmcs_dns_enable_domain')) {
        function whmcs_dns_cpanel_record_context(int $serverId, string $user, string $domain): array
        {
            assert([$serverId, $user, $domain] === [0, "", "example.com"]);
            return ["client_id" => $GLOBALS["dnsOwner"], "zone" => "example.com"];
        }
        function whmcs_dns_provider_record_config(string $domain): array
        {
            return ["domain_name" => $domain, "provider" => "Bunny", "apikey" => "provider-token"];
        }
        function whmcs_dns_enable_domain(int $clientId, array $config): bool
        {
            assert($clientId === 7 && $config["domain_name"] === "example.com");
            if ($GLOBALS["dnsFails"]) {
                throw new \RuntimeException("provider-token rejected");
            }
            $GLOBALS["provisionEvents"][] = "dns";
            $created = !$GLOBALS["dnsEnabled"];
            $GLOBALS["dnsEnabled"] = true;
            return $created;
        }
    }

    $dnsOwner = 8;
    assert(str_contains(moddhosting_CreateAccount($params), "does not match the hosting service owner"));
    $dnsOwner = 7;
    $dnsFails = true;
    assert(moddhosting_CreateAccount($params) === "WHMCS DNS could not enable hosting for example.com. Check the DNS addon and provider zone.");
    assert($provisionEvents === [] && $matomoRequests === []);
    $dnsFails = $dnsEnabled = false;
    $controllerRequests = [];

    Capsule::$services[123]["staging_label"] = "preview";
    $responses = [[200, '{"value":71}']];
    assert(moddhosting_CreateAccount($params) === "success");
    assert(Capsule::$services[123]["matomo_id"] === 71);
    assert(end($matomoRequests)["siteName"] === "example.com #123");
    assert(end($matomoRequests)["urls"] === ["https://example.com", "https://preview.stage.example"]);
    assert(end($matomoRequests)["group"] === "whmcs-123");
    assert(Capsule::$services[123]["matomo_last_error"] === "");
    assert(str_contains(moddhosting_matomo_field($params), "Status: OK"));
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    assert(json_decode(end($controllerRequests)[CURLOPT_POSTFIELDS], true)["matomo_id"] === 71);
    assert(count(array_filter($controllerRequests, static fn(array $request): bool => str_ends_with($request[CURLOPT_URL], "/v1/info"))) === 1);
    assert($provisionEvents === ["dns", "controller"]);
    assert(moddhosting_CreateAccount($params) === "success"); // The addon returns false for an existing zone.
    assert(moddhosting_deploy($params) === "success");
    assert(moddhosting_ChangePackage($params) === "success");
    assert($provisionEvents === ["dns", "controller", "dns", "controller", "controller", "controller"]);
    assert(count(array_filter($matomoRequests, static fn(array $request): bool => $request["method"] === "SitesManager.addSite")) === 1);
    $beforeUnsuspend = count($matomoRequests);
    assert(moddhosting_UnsuspendAccount($params) === "success");
    assert(count($matomoRequests) === $beforeUnsuspend);
    assert(json_decode(end($controllerRequests)[CURLOPT_POSTFIELDS], true)["matomo_id"] === 71);
    assert(moddhosting_TerminateAccount($params) === "success");
    assert(Capsule::$services[123]["matomo_id"] === 71);

    // Deploy replaces the full URL list and name, and normalizes the group without changing the ID.
    assert(end($matomoRequests)["method"] === "SitesManager.updateSite");
    assert(end($matomoRequests)["urls"] === ["https://example.com", "https://preview.stage.example"]);
    Capsule::$services[123]["staging_label"] = "";
    assert(moddhosting_deploy(array_replace($params, ["domain" => "NEW.Example."])) === "success");
    $sync = $matomoRequests[count($matomoRequests) - 2];
    assert($sync["method"] === "SitesManager.updateSite");
    assert($sync["siteName"] === "new.example #123");
    assert($sync["urls"] === ["https://new.example"]);
    assert($sync["group"] === "whmcs-123" && !isset($sync["excludeUnknownUrls"]));
    assert(end($matomoRequests)["method"] === "SitesManager.setSiteAliasUrls");
    assert(!isset(end($matomoRequests)["urls"]));
    assert(Capsule::$services[123]["matomo_id"] === 71);

    assert(moddhosting_matomo_id($params, true, "example.com") === 71);
    assert(end($matomoRequests)["method"] === "SitesManager.setSiteAliasUrls");

    // Sync errors retain the ID, expose escaped status, redact secrets and never block hosting.
    $updateResponses = [[200, new \RuntimeException("secret-token <unsafe> " . str_repeat("é", 1100))]];
    assert(moddhosting_deploy($params) === "success");
    assert(json_decode(end($controllerRequests)[CURLOPT_POSTFIELDS], true)["matomo_id"] === 71);
    $lastError = Capsule::$services[123]["matomo_last_error"];
    assert(!str_contains($lastError, "secret-token"));
    assert(str_contains($lastError, "[REDACTED]"));
    assert(preg_match_all("/./us", $lastError) === 1024);
    assert(str_contains(moddhosting_matomo_field($params), "Error: [REDACTED] &lt;unsafe&gt;"));
    assert(str_contains(moddhosting_matomo_field($params), "data-moddhosting-status=\"matomo\""));
    $_POST = ["moddhosting_matomo_id" => "71"];
    moddhosting_AdminServicesTabFieldsSave($params);
    $_POST = [];
    assert(Capsule::$services[123]["matomo_last_error"] === $lastError);
    Capsule::$settings["matomoUrl"] = "";
    assert(moddhosting_matomo_id($params, true) === 71);
    assert(Capsule::$services[123]["matomo_last_error"] !== "");
    Capsule::$settings["matomoUrl"] = "https://stats.example";
    $updateResponses = [[200, "{\"result\":\"success\"}"], [200, "{}"]];
    assert(moddhosting_matomo_id($params, true) === 71);
    assert(str_contains(Capsule::$services[123]["matomo_last_error"], "alias reset"));
    Capsule::$failStatusWrite = true;
    assert(moddhosting_matomo_id($params, true) === 71);
    Capsule::$failStatusWrite = false;
    assert(moddhosting_matomo_id($params, true) === 71);
    assert(Capsule::$services[123]["matomo_last_error"] === "");

    // Offline controller leaves the local field visible and writable.
    $offline = $params;
    $offline["serverhostname"] = $offline["serverip"] = "";
    $fields = moddhosting_AdminServicesTabFields($offline);
    assert(isset($fields["Matomo Site ID"], $fields["Controller Error"]));
    $_POST = ["moddhosting_matomo_id" => "82"];
    moddhosting_AdminServicesTabFieldsSave($offline);
    assert(Capsule::$services[123]["matomo_id"] === 82);
    assert(Capsule::$services[123]["matomo_last_error"] === null);
    assert(str_contains(moddhosting_matomo_field($params), "Status: Not Configured"));
    $_POST = [];

    $onlineFields = moddhosting_AdminServicesTabFields($params);
    assert(str_contains($onlineFields["Image Version"], "moddhosting_fields"));

    $serviceStatus = ["version" => "v1", "deployments" => ["blue" => ["runtime" => "running"], "green" => ["runtime" => "running"]]];
    foreach (["blue" => "au-52072", "green" => "au-52073"] as $slot => $serverName) {
        $serviceStatus["live_deploy"] = $slot;
        $serverParams = array_replace($params, ["serverhostname" => "control.$serverName.modd.net.au"]);
        $versionField = moddhosting_AdminServicesTabFields($serverParams)["Deployed Version"];
        $container = "WHMCS-123-" . $slot;
        assert(str_contains($versionField, "v1 ($container) <a"));
        assert(str_contains($versionField, 'target="_blank" rel="noopener noreferrer">View logs</a>'));
        $matched = preg_match('/href="([^"]+)"/', $versionField, $matches);
        assert($matched === 1);
        $url = parse_url(html_entity_decode($matches[1]));
        assert($url["scheme"] === "https" && $url["host"] === "logs.$serverName.modd.net.au");
        assert($url["path"] === "/select/vmui/" && str_starts_with($url["fragment"], "/?"));
        parse_str(substr($url["fragment"], 2), $query);
        assert($query["g0_range_input"] === "24h" && $query["g0_relative_time"] === "last_24_hours" && $query["query"] === "*");
        assert(json_decode($query["extra_stream_filters"], true) === ["f" => "CONTAINER_NAME", "o" => "eq", "v" => $container]);
    }
    assert(!str_contains(moddhosting_AdminServicesTabFields($params)["Deployed Version"], "View logs"));
    $serviceStatus["deployments"]["green"]["runtime"] = "stopped";
    assert(!str_contains(moddhosting_AdminServicesTabFields($serverParams)["Deployed Version"], "View logs"));
    unset($serviceStatus);

    // A form loaded online must still save Matomo edits if the controller drops before Save.
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
    assert(count($matomoRequests) === $requestsBefore + 5);

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
    foreach ([[200, "not json"], [200, "{}"], [200, "0"], [200, '{"value":0}'], [200, '{"value":true}'], [200, '{"value":null}'], [200, '{"value":1.5}'], [302, "122"], [500, "122"]] as $response) {
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

    // New pending requests recover by exact group; legacy name recovery above remains supported.
    Capsule::$services[123]["matomo_attempt"] = "WHMCS 123 group-marker";
    $responses = [[200, json_encode([["name" => "example.com #123", "group" => "WHMCS 123 group-marker", "idsite" => "201"]])]];
    $updateResponses = [[200, "{\"result\":\"error\"}"]];
    assert(moddhosting_matomo_id($params, true, "preview.stage.example") === 201);
    assert(Capsule::$services[123]["matomo_id"] === 201);
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    assert(Capsule::$services[123]["matomo_last_error"] !== "");
    assert(end($matomoRequests)["urls"] === ["https://example.com", "https://preview.stage.example"]);
    assert(moddhosting_matomo_id($params, true, "preview.stage.example") === 201);
    assert(Capsule::$services[123]["matomo_last_error"] === "");
    assert(end($matomoRequests)["group"] === "whmcs-123");

    // A failed group normalization keeps the assigned ID, so retry never creates another site.
    moddhosting_updateConfig($params + ["matomo_id" => ""]);
    $responses = [[200, '{"value":202}']];
    $updateResponses = [[200, '{"result":"error"}']];
    assert(moddhosting_matomo_id($params, true) === 202);
    assert(Capsule::$services[123]["matomo_id"] === 202);
    assert(Capsule::$services[123]["matomo_attempt"] === null);
    assert(Capsule::$services[123]["matomo_last_error"] !== "");
    $creations = count(array_filter($matomoRequests, static fn(array $request): bool => $request["method"] === "SitesManager.addSite"));
    assert(moddhosting_matomo_id($params, true) === 202);
    assert(Capsule::$services[123]["matomo_last_error"] === "");
    assert(count(array_filter($matomoRequests, static fn(array $request): bool => $request["method"] === "SitesManager.addSite")) === $creations);
    // New rows still record configuration failures without a preexisting service record.
    Capsule::$services = [];
    Capsule::$settings["matomoUrl"] = "";
    assert(moddhosting_matomo_id($params, true) === 0);
    assert(Capsule::$services[123]["matomo_last_error"] !== "");
    assert(!isset(Capsule::$services[123]["matomo_attempt"]));

    // Email is a separate action, with automatic setup after an accepted Create only.
    assert(moddhosting_AdminCustomButtonArray()["Setup Email"] === "setupEmail");
    $emailRequests = $emailResponses = $hostingEvents = [];
    Capsule::$services[123] = ["image_version" => "v1", "matomo_id" => 202];
    Capsule::$settings["matomoUrl"] = "https://stats.example";
    assert(moddhosting_CreateAccount($params) === "success");
    assert(str_contains(moddhosting_email_field($params), "Not configured"));
    Capsule::$settings["emailRelayUrl"] = "https://relay.example/";
    assert(moddhosting_CreateAccount($params) === "success");
    assert(str_contains(moddhosting_email_field($params), "Configure an HTTPS Email Relay URL and a UKU setup token"));
    Capsule::$settings["emailRelayToken"] = "uku_setup-test-token";
    $provisionResponse = [422, '{"error":{"message":"Provisioning rejected"}}'];
    assert(moddhosting_CreateAccount($params) === "Provisioning rejected");
    $provisionResponse = [202, "{}"];
    assert(moddhosting_deploy($params) === "success");
    assert(moddhosting_ChangePackage($params) === "success");
    assert(moddhosting_UnsuspendAccount($params) === "success");
    assert($emailRequests === []);

    // A failed setup preserves the result of Create and leaves the next Create free to retry.
    $emailResponses = [[403, "<html>uku_setup-test-token</html>"], [200, '{"id":4294967296,"domain":"example.com","whmcsId":123}']];
    assert(moddhosting_CreateAccount($params) === "success");
    assert(array_slice($hostingEvents, -2) === ["controller", "relay"]);
    assert($emailRequests[0] === ["domain" => "example.com", "whmcsId" => 123]);
    assert(str_contains(moddhosting_email_field($params), "Setup failed: Email relay denied setup (HTTP 403)"));
    assert(moddhosting_CreateAccount($params) === "success");
    assert(moddhosting_CreateAccount($params) === "success");
    assert(count($emailRequests) === 2);
    assert(str_contains(moddhosting_email_field($params), "Config OK · example.com · ID 4294967296"));

    // Saving an override leaves the applied result visible, including when the controller is unavailable.
    assert(isset(moddhosting_AdminServicesTabFields($offline)["Email Domain"]));
    $_POST = ["moddhosting_email_domain" => "  Mail.Example.  "];
    moddhosting_AdminServicesTabFieldsSave($offline);
    $_POST = [];
    assert(Capsule::$services[123]["email_domain"] === "mail.example");
    assert(count($emailRequests) === 2);
    $field = moddhosting_email_field($params);
    assert(str_contains($field, 'value="mail.example"') && str_contains($field, "Config OK · example.com"));

    // Failures, including a lost response, retain the last confirmed domain and ID.
    foreach ([[500, '{"error":{"message":"uku_setup-test-token <unsafe>"}}'], [0, false],
        [200, "not json"], [200, '{"id":0,"domain":"mail.example","whmcsId":123}'],
        [200, '{"id":1234,"domain":"other.example","whmcsId":123}'],
        [200, '{"id":1234,"domain":"mail.example","whmcsId":124}']] as $response) {
        $emailResponses = [$response];
        $result = moddhosting_setupEmail($offline);
        assert($result !== "success" && !str_contains($result, "uku_setup-test-token"));
        assert(end($emailRequests) === ["domain" => "mail.example", "whmcsId" => 123]);
        assert(str_contains(moddhosting_email_field($params), "Last configured · example.com · ID 4294967296 · Setup failed:"));
    }
    assert(!str_contains(json_encode([$moduleCalls, Capsule::$services]), "uku_setup-test-token"));
    assert(end($moduleCalls)["action"] === "Email setup failed");

    $emailResponses = array_fill(0, 2, [200, '{"id":1234,"domain":"mail.example","whmcsId":123}']);
    assert(moddhosting_setupEmail($offline) === "success");
    assert(moddhosting_setupEmail($offline) === "success");
    assert(str_contains(moddhosting_email_field($params), "Config OK · mail.example · ID 1234"));
    foreach ([".", "example.com..", "localhost", "https://example.com", "bad_example.com", [], 42] as $invalid) {
        assert(moddhosting_updateConfig($params + ["email_domain" => $invalid]) !== "success");
    }
    assert(Capsule::$services[123]["email_domain"] === "mail.example");
    assert(moddhosting_updateConfig($params + ["email_domain" => ""]) === "success");
    $emailResponses = [[200, '{"id":1234,"domain":"new.example","whmcsId":123}']];
    assert(moddhosting_setupEmail(array_replace($offline, ["domain" => "NEW.example.", "serviceid" => "123"])) === "success");
    assert(end($emailRequests) === ["domain" => "new.example", "whmcsId" => 123]);
    assert(Capsule::$services[123]["email_domain"] === null);

    $requestCount = count($emailRequests);
    foreach ([0, -1, 2147483648, true, "123x"] as $invalid) {
        assert(str_contains(moddhosting_setupEmail(["serviceid" => $invalid]), "positive 32-bit"));
    }
    assert(str_contains(moddhosting_setupEmail(array_replace($offline, ["domain" => ""])), "save a WHMCS Domain"));
    Capsule::$busy = true;
    assert(moddhosting_setupEmail($offline) !== "success");
    Capsule::$busy = false;
    Capsule::$settings["emailRelayUrl"] = "http://relay.example";
    assert(str_contains(moddhosting_setupEmail($offline), "Configure an HTTPS Email Relay URL"));
    assert(count($emailRequests) === $requestCount);
    assert(!Capsule::$locked);
}
