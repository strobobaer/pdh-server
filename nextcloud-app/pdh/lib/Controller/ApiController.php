<?php
namespace OCA\PDH\Controller;

use OCA\PDH\Service\PdhApiService;
use OCP\AppFramework\Controller;
use OCP\AppFramework\Http\JSONResponse;
use OCP\IRequest;

class ApiController extends Controller {
    private PdhApiService $api;

    public function __construct(string $appName, IRequest $request, PdhApiService $api) {
        parent::__construct($appName, $request);
        $this->api = $api;
    }

    public function status(): JSONResponse {
        return new JSONResponse($this->api->status());
    }
}
