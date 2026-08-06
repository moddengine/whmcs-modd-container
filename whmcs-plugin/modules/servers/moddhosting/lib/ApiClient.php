<?php

declare(strict_types=1);

namespace ModdHosting;

final class ApiClient
{
    /** @var array<string, self> */
    private static array $instances = [];

    private readonly \CurlHandle $handle;

    private function __construct(
        private readonly string $baseUrl,
        private readonly string $token,
        private readonly int $timeout = 125,
    ) {
        if (!str_starts_with($baseUrl, 'https://')) {
            throw new \InvalidArgumentException('Controller URL must use HTTPS');
        }
        $handle = curl_init();
        if ($handle === false) {
            throw new \RuntimeException('Unable to initialise cURL');
        }
        $this->handle = $handle;
    }

    public static function forController(string $baseUrl, string $token, int $timeout = 125): self
    {
        $baseUrl = rtrim($baseUrl, '/');
        $key = hash('sha256', $baseUrl . "\0" . $token . "\0" . $timeout);
        return self::$instances[$key] ??= new self($baseUrl, $token, $timeout);
    }

    /**
     * @param array<string, mixed>|null $body
     * @return array<string, mixed>
     */
    public function request(string $method, string $path, ?array $body = null): array
    {
        curl_reset($this->handle);
        $headers = ['Accept: application/json', 'Authorization: Bearer ' . $this->token];
        $options = [
            CURLOPT_URL => $this->baseUrl . $path,
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_HTTPHEADER => $headers,
            CURLOPT_TIMEOUT => $this->timeout,
            CURLOPT_CONNECTTIMEOUT => 10,
            CURLOPT_SSL_VERIFYPEER => true,
            CURLOPT_SSL_VERIFYHOST => 2,
        ];
        if ($body !== null) {
            $encoded = json_encode($body, JSON_THROW_ON_ERROR);
            $headers[] = 'Content-Type: application/json';
            $options[CURLOPT_HTTPHEADER] = $headers;
            $options[CURLOPT_POSTFIELDS] = $encoded;
        }
        curl_setopt_array($this->handle, $options);
        $response = curl_exec($this->handle);
        $curlError = curl_error($this->handle);
        $status = (int) curl_getinfo($this->handle, CURLINFO_RESPONSE_CODE);

        if ($response === false) {
            throw new ApiException('Controller connection failed: ' . $curlError);
        }
        try {
            $decoded = json_decode($response, true, 64, JSON_THROW_ON_ERROR);
        } catch (\JsonException) {
            throw new ApiException('Controller returned invalid JSON', $status);
        }
        if (!is_array($decoded)) {
            throw new ApiException('Controller returned an invalid response', $status);
        }
        if ($status < 200 || $status >= 300) {
            $error = is_array($decoded['error'] ?? null) ? $decoded['error'] : [];
            throw new ApiException(
                (string) ($error['message'] ?? 'Controller request failed'),
                $status,
                (string) ($error['code'] ?? 'controller_error'),
            );
        }
        return $decoded;
    }
}
