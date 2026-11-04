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
    return "<input type=\"text\" name=\"moddhosting_matomo_id\" class=\"form-control\" inputmode=\"numeric\" pattern=\"[1-9][0-9]*\" value=\""
        . moddhosting_escape((string) ($id ?? "")) . "\"><small>Positive site ID, or blank for automatic creation on the next Create/Deploy. Changes apply on the next Deploy/Unsuspend. Clearing never deletes a Matomo site.</small>"
        . ($pending ? "<p class=\"text-warning\">A previous creation needs reconciliation. Deploy will look up this Matomo site name: <code>" . moddhosting_escape((string) $pending) . "</code>. <label><input type=\"checkbox\" name=\"moddhosting_matomo_reset\" value=\"1\"> Clear pending attempt (verify the old site first to avoid duplicates)</label></p>" : "");
}

/** @param array<string, mixed> $params */
function moddhosting_matomo_id(array $params, bool $create): int
{
    $token = "";
    try {
        return moddhosting_matomo_lock((int) $params["serviceid"], static function () use ($params, $create, &$token): int {
            $query = Capsule::table("mod_moddhosting_services")->where("service_id", (int) $params["serviceid"]);
            $id = moddhosting_parse_matomo_id($query->value("matomo_id"));
            if ($id !== null || !$create) {
                return $id ?? 0;
            }
            $settings = Capsule::table("tbladdonmodules")->where("module", "moddhosting");
            $url = (string) (clone $settings)->where("setting", "matomoUrl")->value("value");
            $token = (string) (clone $settings)->where("setting", "matomoToken")->value("value");
            $client = new MatomoClient($url, $token);
            $domain = strtolower(rtrim(trim((string) ($params["domain"] ?? "")), "."));
            if (filter_var($domain, FILTER_VALIDATE_DOMAIN, FILTER_FLAG_HOSTNAME) === false || $domain === "") {
                throw new \InvalidArgumentException("Matomo requires a valid main domain.");
            }
            $attempt = (string) ($query->value("matomo_attempt") ?? "");
            if ($attempt !== "") {
                $sites = $client->request("SitesManager.getPatternMatchSites", ["pattern" => $attempt, "filter_limit" => "-1"]);
                $matches = [];
                if (is_array($sites)) {
                    foreach ($sites as $site) {
                        if (is_array($site) && ($site["name"] ?? "") === $attempt) {
                            $matches[] = moddhosting_parse_matomo_id($site["idsite"] ?? null);
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
                    $id = moddhosting_parse_matomo_id($client->request("SitesManager.addSite", ["siteName" => $attempt, "urls" => "https://" . $domain]));
                    if ($id === null) {
                        throw new \RuntimeException("Matomo returned no site ID; the result is unknown.");
                    }
                } catch (MatomoException $error) {
                    if ($error->definite) {
                        $query->update(["matomo_attempt" => null]);
                    }
                    throw $error;
                }
            }
            if ($query->update(["matomo_id" => $id, "matomo_attempt" => null]) !== 1) {
                throw new \RuntimeException("Unable to persist Matomo site ID; the creation attempt is retained for recovery.");
            }
            return $id;
        });
    } catch (\Throwable $error) {
        $message = $error->getMessage();
        if ($token !== "") {
            $message = str_replace($token, "[REDACTED]", $message);
        }
        try {
            logModuleCall("moddhosting", "Matomo warning", ["service_id" => (int) $params["serviceid"]], $message, [], $token === "" ? [] : [$token]);
        } catch (\Throwable) {
            // Logging must not prevent hosting when the WHMCS database is unavailable.
        }
        return 0;
    }
}
