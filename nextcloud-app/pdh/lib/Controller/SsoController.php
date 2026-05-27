<?php
namespace OCA\PDH\Controller;

use OCP\AppFramework\Controller;
use OCP\AppFramework\Http\Attribute\NoAdminRequired;
use OCP\AppFramework\Http\Attribute\NoCSRFRequired;
use OCP\AppFramework\Http\RedirectResponse;
use OCP\AppFramework\Http\Response;
use OCP\IConfig;
use OCP\IGroupManager;
use OCP\IRequest;

class SsoController extends Controller {
    private ?string $userId;
    private IConfig $config;
    private IGroupManager $groupManager;

    public function __construct(string $appName, IRequest $request, ?string $userId, IConfig $config, IGroupManager $groupManager) {
        parent::__construct($appName, $request);
        $this->userId = $userId;
        $this->config = $config;
        $this->groupManager = $groupManager;
    }

    #[NoAdminRequired]
    #[NoCSRFRequired]
    public function launch(): Response {
        if ($this->userId === null || $this->userId === '') {
            return new RedirectResponse('/login');
        }

        $groupId = $this->config->getAppValue('pdh', 'sso_group', 'pdh');
        if (!$this->groupManager->isInGroup($this->userId, $groupId)) {
            $response = new Response();
            $response->setStatus(403);
            return $response;
        }

        $secret = $this->config->getAppValue('pdh', 'sso_secret', '');
        if ($secret === '') {
            $response = new Response();
            $response->setStatus(503);
            return $response;
        }

        $publicUrl = rtrim($this->config->getAppValue('pdh', 'public_url', 'https://pdh.strobl-home.net'), '/');
        $next = $this->request->getParam('next', '/');
        if (!is_string($next) || $next === '' || $next[0] !== '/' || str_starts_with($next, '//') || str_contains($next, '://')) {
            $next = '/';
        }

        $ts = (string) time();
        $payload = $this->userId . '|' . $ts;
        $sig = hash_hmac('sha256', $payload, $secret);

        $url = $publicUrl . '/sso/nextcloud?user=' . rawurlencode($this->userId)
            . '&ts=' . rawurlencode($ts)
            . '&sig=' . rawurlencode($sig)
            . '&next=' . rawurlencode($next);

        return new RedirectResponse($url);
    }
}
