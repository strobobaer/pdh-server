# Zertifikate für Nextcloud aus dem PDH

Das PDH hat eine eigene **Zertifizierungsstelle (CA)**. Sie stellt drei Arten von Zertifikaten aus. Jedes kommt als passwortgeschützte **.p12-Datei** zum Herunterladen.

| Art | Wofür | Wer stellt aus | Wo |
|---|---|---|---|
| **Persönlich (Nextcloud-App)** | Das Handy oder der Laptop einer Person weist sich bei Nextcloud aus. Nur Geräte mit gültigem Zertifikat kommen hinein. | die Person selbst oder ein Admin | Mein Konto → Konto & Dienste; Core-Einstellungen → Zertifikate |
| **Gerät / Firma** | Ein Zertifikat für mehrere Geräte, z. B. alle Firmenhandys einer Abteilung | Admin | Core-Einstellungen → Zertifikate |
| **Server (Nextcloud)** | Nextcloud ist unter einem eigenen Namen oder einer IP im Firmennetz erreichbar (z. B. `https://nextcloud.local`), ohne öffentliches Zertifikat | Admin | Core-Einstellungen → Zertifikate |

Recht: **„Zertifikate (Nextcloud-App, Geräte, Server) ausstellen & sperren“**, standardmäßig nur Administratoren. Persönliche Zertifikate kann sich jede Person selbst ausstellen, solange unter Core-Einstellungen → Zertifikate die **Selbstbedienung** an ist. Je Person sind höchstens fünf gleichzeitig gültig.

---

## 1. Überblick und Begriffe

- **CA-Zertifikat** (`/pki/ca.crt`): der öffentliche Teil der Zertifizierungsstelle. Geräte und nginx brauchen ihn, um PDH-Zertifikate zu prüfen. Er ist ohne Anmeldung abrufbar und enthält kein Geheimnis.
- **Sperrliste** (`/pki/crl.pem`): gesperrte Zertifikate. nginx holt sie regelmäßig ab.
- **.p12-Datei**: Zertifikat, privater Schlüssel und CA in einer Datei, geschützt mit einem Passwort im Format `Abcd-efgh-jkmn-pqrs`. Das Passwort zeigt das PDH direkt nach dem Ausstellen an und danach jederzeit über **Passwort**.
- **Sperren**: Ein gesperrtes Zertifikat lässt sich nicht mehr herunterladen und nicht entsperren. nginx lehnt es ab, sobald es die Sperrliste neu geholt hat (spätestens nach 6 Stunden).

> **Wichtig:** Die CA und alle .p12-Dateien liegen verschlüsselt in der PDH-Datenbank. Der Schlüssel dafür hängt an `PDH_AUTH_JWTSECRET` (bzw. `PDH_CREDENTIALS_KEY`). Wird dieses Geheimnis geändert, sind CA und Zertifikate unbrauchbar und müssen neu ausgestellt werden. Die Datensicherung des PDH enthält die CA mit.

Fingerabdruck der CA: Core-Einstellungen → Zertifikate (SHA-256). Damit prüft man auf einem Gerät, ob die richtige CA installiert ist.

---

## 2. Persönliche Zertifikate für die Nextcloud-App

### 2a. Einmalig: Zertifikatspflicht in nginx einschalten (Administration)

Auf dem Server, auf dem nginx vor Nextcloud läuft, im PDH-Verzeichnis:

```bash
sudo bash scripts/configure-nextcloud-client-certs.sh --site /etc/nginx/sites-available/nextcloud
```

Das Skript
1. holt CA und Sperrliste vom PDH (`http://127.0.0.1:8090`, sonst `--pdh-url` angeben),
2. schreibt `/etc/nginx/snippets/pdh-client-cert.conf` und bindet es in den HTTPS-server-Block ein (Sicherung: `….bak.pki`),
3. richtet den Timer `pdh-pki-crl` ein, der die Sperrliste alle 6 Stunden holt,
4. prüft die Konfiguration (`nginx -t`) und lädt nginx neu.

Ohne `--site` gibt das Skript die Zeile aus, die du selbst in den server-Block schreibst.

**Ausnahmen:** Zugriffe von `127.0.0.1` und `::1` brauchen kein Zertifikat. Dazu gehören der **Cloudflare-Tunnel** (`cloudflared` verbindet sich lokal) und das **PDH selbst** (Dateiablage, Deck, Konten). Weitere Netze, z. B. das Firmen-WLAN:

```bash
sudo bash scripts/configure-nextcloud-client-certs.sh --exempt '^(127\.|::1$|192\.168\.1\.)' --site …
```

Zurücknehmen: `sudo bash scripts/configure-nextcloud-client-certs.sh --remove --site /etc/nginx/sites-available/nextcloud`

> **Browser:** Weil nginx nach einem Zertifikat fragt, zeigt der Browser beim Öffnen von Nextcloud ein Auswahlfenster. Mit installiertem Zertifikat dort auswählen. Ohne Zertifikat antwortet Nextcloud mit „403 Forbidden“, außer aus den ausgenommenen Netzen.

### 2b. Zertifikat ausstellen (jede Person selbst)

1. Im PDH oben rechts auf den eigenen Namen → **Mein Konto** → Reiter **Konto & Dienste**.
2. Karte **Zertifikat für die Nextcloud-App** → bei **Gerät** z. B. „Handy“ eintragen → **Zertifikat ausstellen**.
3. Das **Passwort** erscheint direkt darunter. Notieren oder das Fenster offen lassen.
4. **.p12** herunterladen, am besten direkt auf dem Gerät, auf dem es gebraucht wird.

Für jedes Gerät ein eigenes Zertifikat. Geht ein Gerät verloren: dort **Sperren**.

Admins stellen persönliche Zertifikate unter Core-Einstellungen → Zertifikate → **Persönlich** für jede Person aus.

### 2c. Auf dem Gerät einrichten

**Android (Nextcloud-App)**
1. Die .p12-Datei antippen (Downloads), oder: **Einstellungen → Sicherheit → Weitere Sicherheitseinstellungen → Verschlüsselung & Anmeldedaten → Zertifikat installieren → VPN- und App-Nutzerzertifikat**. Die Menünamen weichen je nach Hersteller leicht ab.
2. Passwort eingeben, einen Namen vergeben (z. B. „PDH Nextcloud“).
3. Nextcloud-App öffnen und anmelden. Android fragt, welches Zertifikat die App nutzen darf → **PDH Nextcloud** wählen → **Zulassen**.

**iPhone / iPad (Nextcloud-App)**
- iOS gibt über *Einstellungen → Profil* installierte Zertifikate **nur an Safari und Apple-Apps** weiter, nicht an die Nextcloud-App. Die App muss die .p12 also selbst importieren.
- Aktuelle Versionen der Nextcloud-App bieten auf der Anmeldeseite den Import eines **Client-Zertifikats** an: .p12 aus „Dateien“ wählen, Passwort eingeben.
- Bietet die installierte Version das nicht an, kommt das iPhone mit Zertifikatspflicht nicht in Nextcloud. Dann das iPhone über ein ausgenommenes Netz (Firmen-WLAN, `--exempt`) oder über den Cloudflare-Tunnel anbinden.

**Windows / macOS (Nextcloud-Desktop-Client)**
1. Konto hinzufügen, Serveradresse eingeben.
2. Der Assistent meldet „SSL-Client-Zertifikat erforderlich“ → .p12-Datei auswählen → Passwort eingeben.
3. Für den Browser zusätzlich: .p12 doppelklicken und importieren (Windows: „Aktueller Benutzer“; macOS: Schlüsselbund „Anmeldung“).

**Prüfen:** Nextcloud öffnet sich ohne „403“. In nginx steht für den Zugriff `$ssl_client_verify = SUCCESS`.

---

## 3. Geräte- bzw. Firmenzertifikat

Core-Einstellungen → Zertifikate → **Gerät / Firma** → Name (z. B. „Firmenhandys Instandhaltung“) → **Ausstellen**. Einrichtung auf den Geräten wie in 2c.

Ein Gerätezertifikat gilt für alle Geräte, auf denen es installiert ist. **Sperren trifft alle diese Geräte gleichzeitig.** Wo möglich, deshalb lieber persönliche Zertifikate je Gerät nehmen.

---

## 4. Server-Zertifikat für Nextcloud im Firmennetz

Gedacht für den Fall, dass Nextcloud unter einem Namen erreichbar ist, für den es kein öffentliches Zertifikat gibt (z. B. `https://nextcloud.local`, `https://192.168.1.10`). Für die öffentliche Adresse über Cloudflare ist **kein** Server-Zertifikat nötig, das stellt Cloudflare.

1. Core-Einstellungen → Zertifikate → **Server (Nextcloud)** → alle Namen und IPs eintragen, unter denen Nextcloud erreichbar ist, z. B. `nextcloud.local, 192.168.1.10` → **Ausstellen**. Gültigkeit: 825 Tage, mehr akzeptieren Apple-Geräte nicht.
2. **Kette** (`…-fullchain.pem`) und **Schlüssel** (`….key`) herunterladen und auf den nginx-Server kopieren:
   ```bash
   sudo install -m 644 nextcloud.local-fullchain.pem /etc/nginx/pdh-pki/server.crt
   sudo install -m 600 nextcloud.local.key          /etc/nginx/pdh-pki/server.key
   ```
3. Im server-Block von Nextcloud:
   ```nginx
   ssl_certificate     /etc/nginx/pdh-pki/server.crt;
   ssl_certificate_key /etc/nginx/pdh-pki/server.key;
   ```
   dann `sudo nginx -t && sudo systemctl reload nginx`.
4. In Nextcloud die Adresse erlauben: `sudo -u www-data php occ config:system:set trusted_domains 2 --value=nextcloud.local`.
5. **Auf den Geräten der CA vertrauen** (einmalig je Gerät, CA-Datei von `…/pki/ca.crt`, für Windows/iOS `…/pki/ca.crt?format=der`):
   - **Android:** Einstellungen → Sicherheit → Verschlüsselung & Anmeldedaten → Zertifikat installieren → **CA-Zertifikat**. Die Nextcloud-App zeigt beim ersten Verbinden das Server-Zertifikat. Fingerabdruck mit dem PDH vergleichen und **vertrauen**.
   - **iPhone/iPad:** `ca.crt` in Safari öffnen → Profil laden → Einstellungen → Profil installieren → danach **Einstellungen → Allgemein → Info → Zertifikatsvertrauenseinstellungen** → „PDH Zertifizierungsstelle“ einschalten.
   - **Windows:** `pdh-ca.cer` doppelklicken → Zertifikat installieren → Lokaler Computer → **Vertrauenswürdige Stammzertifizierungsstellen**.
   - **macOS:** `ca.crt` doppelklicken → Schlüsselbund „System“ → Zertifikat öffnen → Vertrauen → **Immer vertrauen**.

Die **.p12** des Server-Zertifikats ist für Systeme gedacht, die Zertifikat und Schlüssel in einer Datei erwarten (z. B. Windows/IIS, manche NAS).

Vor Ablauf ein neues Server-Zertifikat ausstellen, Dateien austauschen, nginx neu laden und danach das alte sperren.

---

## 5. Cloudflare

- **Über den Tunnel** (`https://cloud.strobl-home.net`) sieht nginx alle Zugriffe von `127.0.0.1`. Das Skript lässt sie deshalb ohne Zertifikat durch. Die Zertifikatspflicht wirkt nur beim **direkten** Zugriff (Portfreigabe, VPN, LAN).
- Soll auch der Weg über Cloudflare ein Zertifikat verlangen, muss **Cloudflare** prüfen:
  - **Mit der PDH-CA:** Cloudflare Zero Trust → Access → *Service Auth → Mutual TLS* → `ca.crt` hochladen und eine Access-Regel „Valid Certificate“ für die Nextcloud-Adresse anlegen. Eigene CAs (Bring your own CA) bietet Cloudflare nur in bestimmten Tarifen (bisher Enterprise) an. Den Tarif vorher prüfen.
  - **Ohne passenden Tarif:** Cloudflare kann eigene Client-Zertifikate ausstellen (*SSL/TLS → Client-Zertifikate* plus WAF-Regel „mTLS“). Diese kommen dann von Cloudflare, nicht aus dem PDH; Sperren geht nur dort.
- Gesperrte PDH-Zertifikate kennt Cloudflare nicht automatisch. Bei Cloudflare-Prüfung das Gerät zusätzlich in der Access-Regel ausschließen oder das Zertifikat dort widerrufen.

---

## 6. Fehlersuche

| Verhalten | Ursache | Lösung |
|---|---|---|
| Nextcloud: **403 Forbidden** | kein oder kein gültiges Zertifikat gesendet | Zertifikat auf dem Gerät installiert? Gesperrt oder abgelaufen? (Core-Einstellungen → Zertifikate) |
| **Alle** Zugriffe mit Zertifikat plötzlich 403 | Sperrliste abgelaufen (nginx prüft ihr Ablaufdatum) | `sudo systemctl start pdh-pki-crl`, Timer prüfen: `systemctl list-timers pdh-pki-crl` |
| PDH-Dateiablage/Deck melden Fehler | PDH greift nicht von 127.0.0.1 auf Nextcloud zu | `PDH_NEXTCLOUD_BASEURL` auf die lokale Adresse stellen oder die Adresse des PDH bei `--exempt` ergänzen |
| Android-App fragt nie nach dem Zertifikat | Zertifikat als „WLAN“ oder „CA“ statt als „VPN- und App-Nutzerzertifikat“ installiert | neu installieren, richtige Art wählen |
| .p12-Import: „Passwort falsch“ | Tippfehler (Bindestriche gehören dazu) | Passwort im PDH erneut anzeigen und kopieren |
| Server-Zertifikat: „nicht vertrauenswürdig“ | CA auf dem Gerät nicht installiert bzw. (iOS) Vertrauen nicht eingeschaltet | Abschnitt 4, Schritt 5 |
| Server-Zertifikat: „Name stimmt nicht“ | Adresse fehlt im Zertifikat | neues Server-Zertifikat mit allen Namen/IPs ausstellen |
| Seite „Zertifikate“: „CA-Schlüssel nicht lesbar“ | `PDH_AUTH_JWTSECRET`/`PDH_CREDENTIALS_KEY` geändert | alten Wert zurück, oder `pki.ca_cert`/`pki.ca_key` in `app_settings` löschen (neue CA, alles neu ausstellen und verteilen) |

---

## Technisches

- CA: RSA 3072, 10 Jahre. Zertifikate: RSA 2048, persönlich/Gerät 1–3 Jahre (Extended Key Usage *clientAuth*, persönlich mit E-Mail als SAN), Server 825 Tage (*serverAuth*, DNS-/IP-SAN).
- .p12: PKCS#12 mit 3DES/SHA-1 (`pbeWithSHA1And3-KeyTripleDES-CBC`), Kette inklusive CA, Passwort 16 Zeichen. Lesen iOS, Android, Windows, macOS und OpenSSL 3 ohne `-legacy`.
- Sperrliste: CRL, 30 Tage gültig, von der CA signiert, enthält gesperrte und noch nicht abgelaufene Zertifikate.
- Speicherung: Tabelle `pki_certificates` (Migration 126); CA in `app_settings` (`pki.ca_cert`, `pki.ca_key`). Schlüssel, p12 und Passwort AES-GCM-verschlüsselt. Beim Sperren werden p12, Passwort und Schlüssel gelöscht.
- Code: `internal/web/pki.go`, `internal/web/pki_web.go`; nginx: `scripts/configure-nextcloud-client-certs.sh`.
