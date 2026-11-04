<?php

declare(strict_types=1);

use ModdHosting\MatomoClient;
use ModdHosting\MatomoException;
use WHMCS\Database\Capsule;

function moddhosting_parse_matomo_id(mixed $value): ?int
{
    if ($value === null || $value === "") {
        return null;
    }
    if ((!is_string($value) && !is_int($value)) || preg_match("/^[1-9][0-9]*$/D", (string) $value) !== 1
        || strlen((string) $value) > 10 || (int) $value > 4294967295) {
        throw new \InvalidArgumentException("Matomo site ID must be a positive integer up to 4294967295, or blank.");
    }
    return (int) $value;
}

/**
 * @template T
 * @param callable(): T $callback
 * @return T
 */
function moddhosting_matomo_lock(int $serviceId, callable $callback): mixed
{
    $connection = Capsule::connection();
    $name = "moddhosting:matomo:" . $serviceId;
    $lock = $connection->selectOne("SELECT GET_LOCK(?, 30) AS acquired", [$name]);
    if ((int) ($lock->acquired ?? 0) !== 1) {
        throw new \RuntimeException("Matomo service configuration is busy; retry later.");
    }
    try {
        return $callback();
    } finally {
        $connection->selectOne("SELECT RELEASE_LOCK(?) AS released", [$name]);
    }
}

/** @param array<string, mixed> $params */
function moddhosting_matomo_field(array $params): string
{
    $id = Capsule::table("mod_moddhosting_services")->where("service_id", (int) $params["serviceid"])->value("matomo_id");
    $pending = Capsule::table("mod_moddhosting_services")->where("service_id", (int) $params["serviceid"])->value("matomo_attempt");
    $lastError = Capsule::table("mod_moddhosting_services")->where("service_id", (int) $params["serviceid"])->value("matomo_last_error");
    $status = $lastError === null ? "Status: Not Configured" : ($lastError === "" ? "Status: OK" : "Error: " . (string) $lastError);
    return "<div class=\"form-inline\"><input type=\"text\" name=\"moddhosting_matomo_id\" class=\"form-control\" style=\"width:180px;max-width:100%\" inputmode=\"numeric\" pattern=\"[1-9][0-9]*\" value=\""
        . moddhosting_escape((string) ($id ?? "")) . "\"> <span data-moddhosting-status=\"matomo\" style=\"white-space:normal;overflow-wrap:anywhere\">"
        . moddhosting_escape($status) . "</span></div><small>Positive site ID, or blank for automatic creation on the next Create/Deploy. Changes apply on the next Deploy/Unsuspend. Clearing never deletes a Matomo site.</small>"
        . ($pending ? "<p class=\"text-warning\">A previous creation needs reconciliation. Deploy will look up this Matomo site group (or legacy name): <code>" . moddhosting_escape((string) $pending) . "</code>. <label><input type=\"checkbox\" name=\"moddhosting_matomo_reset\" value=\"1\"> Clear pending attempt (verify the old site first to avoid duplicates)</label></p>" : "");
}

/** @param array<string, mixed> $params */
function moddhosting_matomo_id(array $params, bool $create, string $stagingDomain = ""): int
{
    $token = "";
    $savedId = 0;
    try {
        return moddhosting_matomo_lock((int) $params["serviceid"], static function () use ($params, $create, $stagingDomain, &$token, &$savedId): int {
            $query = Capsule::table("mod_moddhosting_services")->where("service_id", (int) $params["serviceid"]);
            try {
                $id = moddhosting_parse_matomo_id($query->value("matomo_id"));
                $savedId = $id ?? 0;
                if (!$create) {
                    return $savedId;
                }
                $settings = Capsule::table("tbladdonmodules")->where("module", "moddhosting");
                $url = (string) (clone $settings)->where("setting", "matomoUrl")->value("value");
                $token = (string) (clone $settings)->where("setting", "matomoToken")->value("value");
                $client = new MatomoClient($url, $token);
                $domain = strtolower(rtrim(trim((string) ($params["domain"] ?? "")), "."));
                $stagingDomain = strtolower(rtrim(trim($stagingDomain), "."));
                foreach (array_filter([$domain, $stagingDomain], static fn(string $host): bool => $host !== "") as $host) {
                    if (filter_var($host, FILTER_VALIDATE_DOMAIN, FILTER_FLAG_HOSTNAME) === false) {
                        throw new \InvalidArgumentException("Matomo requires valid main and staging domains.");
                    }
                }
                if ($domain === "") {
                    throw new \InvalidArgumentException("Matomo requires a valid main domain.");
                }
                $site = ["siteName" => $domain . " #" . (int) $params["serviceid"], "urls[0]" => "https://" . $domain];
                if ($stagingDomain !== "" && $stagingDomain !== $domain) {
                    $site["urls[1]"] = "https://" . $stagingDomain;
                }
                $created = false;
                if ($id === null) {
                    $attempt = (string) ($query->value("matomo_attempt") ?? "");
                    if ($attempt !== "") {
                        $sites = $client->request("SitesManager.getPatternMatchSites", ["pattern" => $attempt, "filter_limit" => "-1"]);
                        $matches = [];
                        if (is_array($sites)) {
                            foreach ($sites as $match) {
                                if (is_array($match) && (($match["group"] ?? "") === $attempt || ($match["name"] ?? "") === $attempt)) {
                                    $matches[] = moddhosting_parse_matomo_id($match["idsite"] ?? null);
                                }
                            }
                        }
                        if (count($matches) !== 1 || $matches[0] === null) {
                            throw new \RuntimeException("Matomo creation remains unresolved or ambiguous; review the pending site before clearing the ID.");
                        }
                        $id = $matches[0];
                    } else {
                        $attempt = "WHMCS " . (int) $params["serviceid"] . " " . bin2hex(random_bytes(16));
                        // Persist outside a transaction so a crash after the remote call remains recoverable.
                        if (!Capsule::table("mod_moddhosting_services")->updateOrInsert(
                            ["service_id" => (int) $params["serviceid"]], ["matomo_attempt" => $attempt],
                        )) {
                            throw new \RuntimeException("Unable to persist Matomo creation attempt.");
                        }
                        try {
                            $id = moddhosting_parse_matomo_id($client->request("SitesManager.addSite", $site + ["group" => $attempt, "excludeUnknownUrls" => "1"]));
                            if ($id === null) {
                                throw new \RuntimeException("Matomo returned no site ID; the result is unknown.");
                            }
                        } catch (MatomoException $error) {
                            if ($error->definite) {
                                $query->update(["matomo_attempt" => null]);
                            }
                            throw $error;
                        }
                        $created = true;
                    }
                    if ($query->update(["matomo_id" => $id, "matomo_attempt" => null]) !== 1) {
                        throw new \RuntimeException("Unable to persist Matomo site ID; the creation attempt is retained for recovery.");
                    }
                    // Save a recovered legacy ID before renaming its marker-bearing site.
                    $savedId = $id;
                }
                if (!$created) {
                    $result = $client->request("SitesManager.updateSite", ["idSite" => (string) $id] + $site);
                    if (!is_array($result) || ($result["result"] ?? "") !== "success") {
                        throw new \RuntimeException("Matomo returned an invalid site update response.");
                    }
                }
                if (!$created && !isset($site["urls[1]"])) {
                    $aliases = $client->request("SitesManager.setSiteAliasUrls", ["idSite" => (string) $id]);
                    if (!is_int($aliases) || $aliases < 0) {
                        throw new \RuntimeException("Matomo returned an invalid alias reset response.");
                    }
                }
                Capsule::table("mod_moddhosting_services")->updateOrInsert(
                    ["service_id" => (int) $params["serviceid"]], ["matomo_last_error" => ""],
                );
                return $savedId;
            } catch (\Throwable $error) {
                $message = moddhosting_matomo_error($error, $token);
                try {
                    Capsule::table("mod_moddhosting_services")->updateOrInsert(
                        ["service_id" => (int) $params["serviceid"]], ["matomo_last_error" => $message],
                    );
                } catch (\Throwable) {
                    // Status persistence must not prevent hosting when the database is unavailable.
                }
                throw $error;
            }
        });
    } catch (\Throwable $error) {
        try {
            logModuleCall("moddhosting", "Matomo warning", ["service_id" => (int) $params["serviceid"]], moddhosting_matomo_error($error, $token), [], $token === "" ? [] : [$token]);
        } catch (\Throwable) {
            // Logging must not prevent hosting when the WHMCS database is unavailable.
        }
        return $savedId;
    }
}

function moddhosting_matomo_error(\Throwable $error, string $token): string
{
    $message = $token === "" ? $error->getMessage() : str_replace($token, "[REDACTED]", $error->getMessage());
    return preg_match("/^.{0,1024}/us", $message, $match) === 1 ? $match[0] : "Matomo synchronization failed.";
}
