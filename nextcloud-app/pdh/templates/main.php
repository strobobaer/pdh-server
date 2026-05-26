<?php
script('pdh', 'pdh-main');
style('pdh', 'pdh-main');
$pdhPublicUrl = $_['pdhPublicUrl'] ?? 'https://pdh.strobl-home.net';
?>
<div id="pdh-app" class="pdh-app" data-pdh-public-url="<?php p($pdhPublicUrl); ?>">
    <div class="pdh-toolbar">
        <div>
            <h2>PDH</h2>
            <div id="pdh-status" class="pdh-status">Status wird geladen...</div>
        </div>
        <div class="pdh-toolbar-actions">
            <button class="button" type="button" id="pdh-reload-frame">Aktualisieren</button>
            <a class="button" href="<?php p($pdhPublicUrl); ?>" target="_blank" rel="noreferrer noopener">Extern öffnen</a>
        </div>
    </div>
    <iframe id="pdh-frame" class="pdh-frame" src="<?php p($pdhPublicUrl); ?>" title="PDH" loading="eager"></iframe>
</div>
