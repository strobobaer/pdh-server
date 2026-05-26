<?php
namespace OCA\PDH\Controller;

use OCP\AppFramework\Controller;
use OCP\AppFramework\Http\Attribute\NoAdminRequired;
use OCP\AppFramework\Http\Attribute\NoCSRFRequired;
use OCP\AppFramework\Http\ContentSecurityPolicy;
use OCP\AppFramework\Http\TemplateResponse;
use OCP\IConfig;
use OCP\IRequest;

class PageController extends Controller {
    private ?string $userId;
    private IConfig $config;

    public function __construct(string $appName, IRequest $request, ?string $userId, IConfig $config) {
        parent::__construct($appName, $request);
        $this->userId = $userId;
        $this->config = $config;
    }

    #[NoAdminRequired]
    #[NoCSRFRequired]
    public function index(): TemplateResponse {
        $pdhPublicUrl = $this->config->getAppValue('pdh', 'public_url', 'https://pdh.strobl-home.net');
        $pdhOrigin = $this->originFromUrl($pdhPublicUrl);

        $response = new TemplateResponse('pdh', 'main', [
            'userId' => $this->userId ?? '',
            'pdhPublicUrl' => $pdhPublicUrl,
        ]);

        $policy = new ContentSecurityPolicy();
        $policy->addAllowedFrameDomain($pdhOrigin);
        $policy->addAllowedConnectDomain($pdhOrigin);
        $response->setContentSecurityPolicy($policy);

        return $response;
    }

    private function originFromUrl(string $url): string {
        $parts = parse_url($url);
        if (!is_array($parts) || empty($parts['scheme']) || empty($parts['host'])) {
            return 'https://pdh.strobl-home.net';
        }
        $origin = $parts['scheme'] . '://' . $parts['host'];
        if (!empty($parts['port'])) {
            $origin .= ':' . $parts['port'];
        }
        return $origin;
    }
}
