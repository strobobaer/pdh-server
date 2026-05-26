<?php
namespace OCA\PDH\Service;

use OCP\IConfig;
use OCP\IURLGenerator;

class PdhApiService {
    private IConfig $config;
    private IURLGenerator $urlGenerator;

    public function __construct(IConfig $config, IURLGenerator $urlGenerator) {
        $this->config = $config;
        $this->urlGenerator = $urlGenerator;
    }

    public function baseUrl(): string {
        return rtrim($this->config->getAppValue('pdh', 'base_url', 'http://127.0.0.1:8090'), '/');
    }

    public function status(): array {
        $url = $this->baseUrl() . '/health';
        $ctx = stream_context_create([
            'http' => [
                'method' => 'GET',
                'timeout' => 3,
                'ignore_errors' => true,
            ],
        ]);
        $body = @file_get_contents($url, false, $ctx);
        if ($body === false) {
            return [
                'success' => false,
                'status' => 'offline',
                'baseUrl' => $this->baseUrl(),
            ];
        }
        $decoded = json_decode($body, true);
        return [
            'success' => true,
            'status' => 'online',
            'baseUrl' => $this->baseUrl(),
            'response' => $decoded ?? $body,
        ];
    }
}
