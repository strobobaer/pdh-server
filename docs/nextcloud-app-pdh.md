# Nextcloud-App PDH

Diese App ist die erste Bruecke zwischen Nextcloud und dem PDH-Go-Backend.

Aktueller Stand:

- Navigationseintrag `PDH` in Nextcloud
- einfache Hauptseite
- Statuscheck gegen PDH `/health`
- Standard-PDH-Link zu `https://pdh.strobl-home.net`

## Voraussetzungen

- Nextcloud installiert unter `/var/www/nextcloud`
- PDH laeuft lokal unter `http://127.0.0.1:8090`
- Nextcloud-Origin laeuft ueber Nginx/Cloudflare auf `https://localhost:8436`
- PHP kann aus Nextcloud heraus `http://127.0.0.1:8090/health` erreichen

Pruefen:

```bash
curl http://127.0.0.1:8090/health
sudo -u www-data php -r 'echo file_get_contents("http://127.0.0.1:8090/health");'
```

## Paket erstellen

```bash
cd ~/pdh
chmod +x scripts/package-nextcloud-app.sh
./scripts/package-nextcloud-app.sh
```

Das Script erstellt:

```text
dist/pdh-nextcloud-app.zip
```

Das Paket-Script prueft vorher:

- doppelte Pfade
- blockierte Dateien wie `.git`, `.DS_Store`, `Thumbs.db`, `*.bak`, `*.tmp`
- ZIP-Inhalt nach dem Packen

## App lokal installieren

```bash
cd ~/pdh
sudo mkdir -p /var/www/nextcloud/apps/pdh
sudo rsync -a --delete nextcloud-app/pdh/ /var/www/nextcloud/apps/pdh/
sudo chown -R www-data:www-data /var/www/nextcloud/apps/pdh
sudo -u www-data php -d apc.enable_cli=1 /var/www/nextcloud/occ app:enable pdh
```

## PDH-Backend-URL setzen

Standard ist:

```text
http://127.0.0.1:8090
```

Explizit setzen:

```bash
sudo -u www-data php -d apc.enable_cli=1 /var/www/nextcloud/occ config:app:set pdh base_url --value='http://127.0.0.1:8090'
```

Pruefen:

```bash
sudo -u www-data php -d apc.enable_cli=1 /var/www/nextcloud/occ config:app:get pdh base_url
```

## Test

```bash
curl -kI https://localhost:8436/apps/pdh/
curl -k https://localhost:8436/apps/pdh/api/status
```

Im Browser:

```text
https://cloud.strobl-home.net/apps/pdh/
```

## Deinstallation

```bash
sudo -u www-data php -d apc.enable_cli=1 /var/www/nextcloud/occ app:disable pdh
sudo rm -rf /var/www/nextcloud/apps/pdh
```

## Naechste Ausbaustufe

1. Tickets lesend anzeigen
2. Stoerungen lesend anzeigen
3. Wartungsauftraege lesend anzeigen
4. Nextcloud-Dateien mit PDH-Datensaetzen verknuepfen
5. Service-Token fuer geschuetzte PDH-API-Endpunkte
