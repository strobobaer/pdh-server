<?php
namespace OCA\PDH\Controller;

use OCP\AppFramework\Controller;
use OCP\AppFramework\Http\Attribute\NoAdminRequired;
use OCP\AppFramework\Http\Attribute\NoCSRFRequired;
use OCP\AppFramework\Http\TemplateResponse;
use OCP\IRequest;

class PageController extends Controller {
    private ?string $userId;

    public function __construct(string $appName, IRequest $request, ?string $userId) {
        parent::__construct($appName, $request);
        $this->userId = $userId;
    }

    #[NoAdminRequired]
    #[NoCSRFRequired]
    public function index(): TemplateResponse {
        return new TemplateResponse('pdh', 'main', [
            'userId' => $this->userId ?? '',
        ]);
    }
}
