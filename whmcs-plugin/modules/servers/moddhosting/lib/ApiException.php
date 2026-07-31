<?php

declare(strict_types=1);

namespace ModdHosting;

final class ApiException extends \RuntimeException
{
    public function __construct(
        string $message,
        public readonly int $status = 0,
        public readonly string $codeName = 'controller_error',
    ) {
        parent::__construct($message);
    }
}

