<?php

declare(strict_types=1);

use ModdHosting\ApiClient;
use ModdHosting\ApiException;
use WHMCS\Database\Capsule;

function moddhosting_parse_email_domain(mixed $value): string
{
    if (!is_string($value)) {
        throw new \InvalidArgumentException('Email Domain must be a string.');
    }
    $domain = strtolower(trim($value));
    if ($domain === '') {
        return '';
    }
    if (str_ends_with($domain, '.')) {
        $domain = substr($domain, 0, -1);
    }
    if (!str_contains($domain, '.') || str_ends_with($domain, '.')
        || filter_var($domain, FILTER_VALIDATE_DOMAIN, FILTER_FLAG_HOSTNAME) === false) {
        throw new \InvalidArgumentException('Email Domain must be a valid DNS hostname.');
    }
    return $domain;
}

/** @param array<string, mixed> $params */
function moddhosting_email_field(array $params): string
{
    $service = Capsule::table('mod_moddhosting_services')->where('service_id', (int) $params['serviceid'])->first();
    $configured = (string) ($service->email_configured_domain ?? '');
    $error = (string) ($service->email_setup_error ?? '');
    $result = [];
    if ($configured !== '') {
        $result = [$error === '' ? 'Config OK' : 'Last configured', $configured, 'ID ' . (string) ($service->email_relay_id ?? '')];
    }
    if ($error !== '') {
        $result[] = 'Setup failed: ' . $error;
    }
    return '<div class="form-inline"><input type="text" name="moddhosting_email_domain" class="form-control" maxlength="253" value="'
        . moddhosting_escape((string) ($service->email_domain ?? '')) . '"> <span data-moddhosting-status="email-setup">'
        . moddhosting_escape($result === [] ? 'Not configured' : implode(' · ', $result))
        . '</span></div><small>Leave blank to use the saved WHMCS Domain. Save changes before running Setup Email.</small>';
}

/** @param array<string, mixed> $params */
function moddhosting_email_setup(array $params, bool $automatic = false): string
{
    $id = $params['serviceid'] ?? null;
    $serviceId = (is_int($id) || is_string($id)) ? filter_var($id, FILTER_VALIDATE_INT, [
        'options' => ['min_range' => 1, 'max_range' => 2147483647],
    ]) : false;
    if ($serviceId === false) {
        return 'Email setup requires a positive 32-bit WHMCS service ID.';
    }

    try {
        $settings = Capsule::table('tbladdonmodules')->where('module', 'moddhosting');
        $url = trim((string) (clone $settings)->where('setting', 'emailRelayUrl')->value('value'));
        $token = trim((string) (clone $settings)->where('setting', 'emailRelayToken')->value('value'));
        if ($automatic && $url === '' && $token === '') {
            return 'success';
        }

        $result = moddhosting_service_lock('email', $serviceId, static function () use ($params, $automatic, $serviceId, $url, $token): string {
            $services = Capsule::table('mod_moddhosting_services');
            $service = (clone $services)->where('service_id', $serviceId)->first();
            if ($automatic && !empty($service->email_configured_domain)) {
                return 'success';
            }

            $values = ['email_setup_error' => null];
            try {
                $domain = moddhosting_parse_email_domain($service->email_domain ?? '');
                if ($domain === '') {
                    $domain = moddhosting_parse_email_domain($params['domain'] ?? '');
                }
                if ($domain === '') {
                    throw new \InvalidArgumentException('Set Email Domain or save a WHMCS Domain before setting up email.');
                }

                $response = ApiClient::forEmailRelay($url, $token)->request('POST', '/~emailrelay/ses/site', [
                    'domain' => $domain,
                    'whmcsId' => $serviceId,
                ]);
                $relayId = $response['id'] ?? null;
                if (!is_int($relayId) || $relayId < 1 || ($response['domain'] ?? null) !== $domain
                    || ($response['whmcsId'] ?? null) !== $serviceId) {
                    throw new \InvalidArgumentException('Email relay returned an invalid setup result.');
                }
                $values['email_configured_domain'] = $response['domain'];
                $values['email_relay_id'] = $relayId;
            } catch (\InvalidArgumentException $error) {
                $values['email_setup_error'] = $error->getMessage();
            } catch (ApiException $error) {
                $status = $error->status;
                $values['email_setup_error'] = match (true) {
                    $status === 0 => 'The email relay request did not complete. Check the URL and retry.',
                    $status === 401 || $status === 403 => "Email relay denied setup (HTTP $status). Check the setup token and its permissions.",
                    $status >= 200 && $status < 300 => 'Email relay returned an invalid setup response.',
                    default => "Email relay setup failed (HTTP $status).",
                };
            }

            $changes = array_filter($values, static fn(mixed $value, string $column): bool =>
                (string) $value !== (string) ($service->$column ?? ''), ARRAY_FILTER_USE_BOTH);
            if ($changes !== [] && !$services->updateOrInsert(['service_id' => $serviceId], $changes)) {
                throw new \RuntimeException('Could not save the email setup result.');
            }
            return $values['email_setup_error'] ?? 'success';
        });
    } catch (\Throwable) {
        $result = 'WHMCS could not complete email setup. Retry from Setup Email.';
    }

    if ($result !== 'success') {
        try {
            logModuleCall('moddhosting', 'Email setup failed', ['service_id' => $serviceId], $result);
        } catch (\Throwable) {
            // A failed module log must not change the hosting result.
        }
    }
    return $result;
}
