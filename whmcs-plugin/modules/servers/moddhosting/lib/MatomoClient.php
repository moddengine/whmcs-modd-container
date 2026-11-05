<?php

declare(strict_types=1);

namespace ModdHosting;

final class MatomoException extends \RuntimeException
{
    public function __construct(string $message, public readonly bool $definite = false)
    {
        parent::__construct($message);
    }
}

final class MatomoClient
{
    public function __construct(private readonly string $url, private readonly string $token)
    {
        $parts = parse_url($url);
        if (($parts["scheme"] ?? "") !== "https" || empty($parts["host"])
            || isset($parts["user"]) || isset($parts["pass"]) || isset($parts["query"]) || isset($parts["fragment"]) || trim($token) === "") {
            throw new \InvalidArgumentException("Configure a shared HTTPS Matomo URL and API token in the addon settings.");
        }
    }

    /** @param array<string, string> $params */
    public function request(string $method, array $params): mixed
    {
        $handle = curl_init();
        if ($handle === false) {
            throw new MatomoException("Unable to initialise Matomo cURL.", true);
        }
        try {
            curl_setopt_array($handle, [
                CURLOPT_URL => rtrim($this->url, "/") . "/index.php",
                CURLOPT_POST => true,
                CURLOPT_POSTFIELDS => http_build_query($params + [
                    "module" => "API", "method" => $method, "format" => "JSON", "token_auth" => $this->token,
                ]),
                CURLOPT_HTTPHEADER => ["Content-Type: application/x-www-form-urlencoded", "Accept: application/json"],
                CURLOPT_RETURNTRANSFER => true,
                CURLOPT_FOLLOWLOCATION => false,
                CURLOPT_PROTOCOLS => CURLPROTO_HTTPS,
                CURLOPT_TIMEOUT => 20,
                CURLOPT_CONNECTTIMEOUT => 5,
                CURLOPT_SSL_VERIFYPEER => true,
                CURLOPT_SSL_VERIFYHOST => 2,
            ]);
            $response = curl_exec($handle);
            $status = (int) curl_getinfo($handle, CURLINFO_RESPONSE_CODE);
            $errno = curl_errno($handle);
        } finally {
            curl_close($handle);
        }
        // A timeout or malformed response may follow a successful remote creation.
        if (!is_string($response)) {
            throw new MatomoException("Matomo connection failed.", in_array($errno, [
                CURLE_COULDNT_RESOLVE_HOST, CURLE_COULDNT_RESOLVE_PROXY, CURLE_COULDNT_CONNECT,
                CURLE_URL_MALFORMAT, CURLE_UNSUPPORTED_PROTOCOL, CURLE_SSL_CACERT,
            ], true));
        }
        try {
            $result = json_decode($response, true, 64, JSON_THROW_ON_ERROR);
        } catch (\JsonException) {
            throw new MatomoException("Matomo returned invalid JSON; the result is unknown.");
        }
        if (is_array($result) && ($result["result"] ?? null) === "error") {
            // Do not log remote text: it can echo the request token.
            throw new MatomoException("Matomo reported an API error; the creation result may be unknown.");
        }
        if ($status < 200 || $status >= 300) {
            throw new MatomoException("Matomo HTTP request failed (" . $status . "); the result is unknown.");
        }
        // Matomo renders scalar API results inside a single "value" field.
        return is_array($result) && array_keys($result) === ["value"] ? $result["value"] : $result;
    }
}
