<?php
namespace OCA\PDH\Controller;

use OCP\AppFramework\Controller;
use OCP\AppFramework\Http\Attribute\NoAdminRequired;
use OCP\AppFramework\Http\Attribute\NoCSRFRequired;
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
        return new TemplateResponse('pdh', 'main', [
            'userId' => $this->userId ?? '',
            'pdhPublicUrl' => $this->config->getAppValue('pdh', 'public_url', 'https://pdh.strobl-home.net'),
        ]);
    }
}
