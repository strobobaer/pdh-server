<?php
namespace OCA\PDH\AppInfo;

use OCP\AppFramework\App;
use OCP\IContainer;
use OCA\PDH\Service\PdhApiService;

class Application extends App {
    public const APP_ID = 'pdh';

    public function __construct(array $urlParams = []) {
        parent::__construct(self::APP_ID, $urlParams);

        $container = $this->getContainer();
        $container->registerService(PdhApiService::class, function (IContainer $c) {
            return new PdhApiService(
                $c->query('OCP\\IConfig'),
                $c->query('OCP\\IURLGenerator')
            );
        });
    }
}
