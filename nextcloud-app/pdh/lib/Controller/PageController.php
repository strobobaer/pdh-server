<?php
namespace OCA\PDH\Controller;

use OCP\AppFramework\Controller;
use OCP\AppFramework\Http\TemplateResponse;
use OCP\IRequest;

class PageController extends Controller {
    private ?string $userId;

    public function __construct(string $appName, IRequest $request, ?string $userId) {
        parent::__construct($appName, $request);
        $this->userId = $userId;
    }

    public function index(): TemplateResponse {
        return new TemplateResponse('pdh', 'main', [
            'userId' => $this->userId ?? '',
        ]);
    }
}
