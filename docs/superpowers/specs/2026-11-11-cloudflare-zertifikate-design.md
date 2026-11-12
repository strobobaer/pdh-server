# Cloudflare Stufe 2: Zertifikate von Let's Encrypt und dieselben Namen im Haus

Stand: 2026-11-11 · Status: Entwurf, wartet auf Prüfung ·
Baut auf: Cloudflare Stufe 1 (DNS, Proxy, Tunnel-Ziele im PDH, 0.77.9) und
`2026-11-03-ausfallschutz-spiegelung-design.md` ·
Version: nächste Korrekturversion nach 0.77.9 · Migration: 157

## 1. Ziel und Umfang

Heute sind PDH und Nextcloud nur über den Cloudflare-Tunnel unter ihrem Namen
mit gültigem Zertifikat erreichbar. Im Haus gibt es nur die Adresse des Servers:
unverschlüsselt oder mit einem Zertifikat der PDH-eigenen Zertifizierungsstelle,
dem Handys und Clients nicht trauen. Die Strecke Cloudflare → Server ist nicht
mit einem geprüften Zertifikat gesichert.

Neu:

1. **Zertifikate von Let's Encrypt:** Das PDH holt und erneuert sie selbst per
   DNS-01 über das vorhandene Cloudflare-API-Token. Von außen muss kein Port
   offen sein.
2. **Dieselben Namen im Haus:** nginx nimmt auf Port 443 den PDH-Namen und den
   Nextcloud-Namen an. Zeigen die Namen im Firmennetz auf die mitwandernde
   Adresse, arbeiten Browser, Desktop-Client und Handy-App im Haus direkt und
   ohne Warnung.
3. **Full (strict):** Auf Knopfdruck stellt das PDH das Ziel im Tunnel auf
   `https://<name>` mit Prüfung um und setzt die Domain auf „Full (strict)“.

Anwender-Entscheidungen (2026-11-11): Zweck C (Haus und Strecke Cloudflare →
Server), Weg 1 (nginx als gemeinsame Tür). DNS im Haus ist die UniFi UDM Pro;
der Anwender gibt dem PDH dafür einen API-Schlüssel, das PDH trägt die Namen
dort selbst ein (Angaben vom 2026-11-12, siehe Abschnitt 7).

Nicht in diesem Teil:

- Gemeinsame Anmeldung (das PDH als Anmeldedienst für Nextcloud) – eigenes
  Teilprojekt direkt danach, baut auf diesem auf.
- Zertifikate für andere Dienste (n8n, pgAdmin, Samba).
- Andere DNS-Anbieter als Cloudflare, andere Zertifizierungsstellen.
- Sammelzertifikate (`*.domain`).

## 2. Begriffe

- **Name:** ein Hostname, für den ein Zertifikat geführt wird, z. B.
  `nc.firma.de`.
- **Prüf-Eintrag:** der TXT-Eintrag `_acme-challenge.<name>`, mit dem
  Let's Encrypt den Besitz des Namens prüft.
- **Testumgebung:** die Staging-Umgebung von Let's Encrypt. Ihre Zertifikate
  sind nicht vertrauenswürdig, verbrauchen aber kein Kontingent.
- **Tür:** nginx auf Port 443 des aktiven Servers.

## 3. Namen und Zertifikate

- Vorgeschlagen werden der PDH-Name aus `PDH_CLOUDFLARE_HOSTNAME` und der
  Nextcloud-Name aus `PDH_NEXTCLOUD_BASEURL`. Weitere Namen lassen sich in der
  Karte ergänzen.
- Ein Name muss in der Domain liegen, für die das API-Token gilt
  (`PDH_CLOUDFLARE_ZONE` bzw. aus dem öffentlichen Hostnamen abgeleitet). Andere
  Namen lehnt die Karte ab.
- Je Name ein eigenes Zertifikat mit genau diesem Namen. Ein Fehler betrifft so
  nur einen Dienst.
- Schlüssel: ECDSA P-256, je Erneuerung ein neuer Schlüssel.

## 4. Aufteilung

### 4.1 PDH (nur der aktive Server)

- Führt das Konto bei Let's Encrypt (ein Konto je Umgebung, Schlüssel
  verschlüsselt in der Datenbank wie die Schlüssel der PDH-eigenen
  Zertifizierungsstelle).
- Ablauf je Name:
  1. Auftrag bei Let's Encrypt anlegen.
  2. Prüf-Eintrag über die vorhandene Cloudflare-Anbindung anlegen
     (`CreateRecord`), TTL so kurz wie möglich.
  3. Warten, bis der Eintrag bei den zuständigen Nameservern von Cloudflare
     sichtbar ist (Abfrage direkt dort, höchstens 2 Minuten).
  4. Prüfung auslösen, Zertifikat abholen.
  5. Prüf-Eintrag löschen – auch nach einem Fehler.
  6. Zertifikat und Schlüssel verschlüsselt speichern, dann an den Agenten
     geben (4.2).
- Neues Paket `internal/integrations/acme` (Ablauf, ohne Web und Datenbank,
  mit Schnittstellen für den DNS-Anbieter und die Uhr) und
  `internal/web/certs.go` (Speichern, Zeitplan, Karte). Die Bibliothek
  `golang.org/x/crypto/acme` ist im Modul schon enthalten.
- Am Spiegel (Lesemodus) und während der Wartung startet nichts.

### 4.2 Update-Agent (root)

Neue Schnittstelle `POST /v1/certs/install` (wie die vorhandenen
Nextcloud-Schnittstellen des Agenten, mit Token):

- Legt Zertifikat und Schlüssel unter `/etc/ssl/pdh/<name>/fullchain.pem` und
  `privkey.pem` ab (root, Gruppe des nginx-Benutzers, Schlüssel 0640), erst in
  eine Nachbardatei, dann umbenennen.
- Schreibt die nginx-Seite `pdh.conf` für den PDH-Namen: Port 443 mit dem
  Zertifikat, Weiterreichen an das PDH (`http://127.0.0.1:<Port>`), mit den
  Kopfzeilen für die echte Adresse, WebSocket und Server-Sent-Events ohne
  Puffer; Port 80 leitet auf 443 um.
- Stellt in der Nextcloud-Seite nur die beiden Zertifikatszeilen auf das neue
  Zertifikat um. Die Seite bleibt sonst, wie das Installationsskript sie
  geschrieben hat.
- Prüft mit `nginx -t` und lädt nginx neu. Schlägt die Prüfung fehl, stellt er
  die vorigen Dateien wieder her und meldet den Fehler zurück.
- `GET /v1/certs/status` nennt je Name Ablaufdatum und Fingerabdruck der Datei,
  damit die Karte Abweichungen zwischen Datenbank und Datei zeigt.
- Docker-Betrieb (Testsystem): Die Schnittstelle lehnt ab; die Karte sagt, dass
  Zertifikate nur im systemd-Betrieb eingerichtet werden.

nginx muss vorhanden sein. Wo es fehlt (Server ohne Nextcloud), richtet es
`install_update.sh` ein, sobald das erste Zertifikat geholt werden soll; bis
dahin sagt die Karte, dass nginx fehlt.

### 4.3 Verbund

- `/etc/ssl/pdh` wird neue Quelle der Datei-Spiegelung und der Sicherung
  (neben `nginx`, `ssl-pdh-nextcloud`, `letsencrypt`). Nach einem Tausch hat der
  neue aktive Server dieselben Zertifikate.
- Die Datenbank mit Konto und Zertifikaten kommt über den vorhandenen Spiegel.
- Nur der aktive Server holt und erneuert. Nach einem Rollenwechsel vergleicht
  der neue aktive Server Datenbank und Dateien (4.2 `status`) und installiert
  bei Abweichung neu.
- Übrig gebliebene Prüf-Einträge (Tausch mitten im Ablauf) räumt der aktive
  Server beim nächsten Lauf auf: Er löscht nur `_acme-challenge`-Einträge der
  geführten Namen.
- nginx läuft je Rolle wie in Teil 6 festgelegt; daran ändert dieser Teil
  nichts.

## 5. Erneuerung

- Einmal täglich prüft der aktive Server alle Namen. Erneuert wird 30 Tage vor
  Ablauf.
- Scheitert die Erneuerung, bleibt das alte Zertifikat in Betrieb; nächster
  Versuch am folgenden Tag.
- Alarm an die Administratoren ab 14 Tage vor Ablauf, täglich ab 7 Tagen, und
  sofort, wenn ein Zertifikat abgelaufen ist.
- Meldet Let's Encrypt ein erschöpftes Kontingent, merkt sich das PDH die
  genannte Sperrzeit und versucht es erst danach wieder.

## 6. Cloudflare umstellen

Erst wenn alle geführten Namen ein gültiges Zertifikat der echten Umgebung
haben, ist der Knopf „Tunnel und Domain auf Full (strict) umstellen“ aktiv:

- Ziel im Tunnel je Name: `https://<name>` mit Prüfung des Zertifikats; der
  Tunnel erreicht den Namen auf dem Server selbst.
- Verschlüsselung der Domain: „Full (strict)“.
- Vorher prüft das PDH vom Server aus, dass `https://<name>` lokal mit gültigem
  Zertifikat antwortet. Sonst wird nichts umgestellt.
- Nichts davon geschieht von selbst. Jede Änderung geht wie in Stufe 1 als
  Nachricht an alle Administratoren und ins Protokoll.
- „Zurückstellen“ setzt Ziel und Verschlüsselung auf den Stand vor der
  Umstellung; das PDH merkt sich diesen Stand.

## 7. Namen im Haus

- Beide Namen zeigen im Firmennetz auf die mitwandernde Adresse (ohne Verbund:
  auf die Adresse des Servers). Öffentlich zeigen sie weiter auf Cloudflare.
- Das PDH fragt jeden Namen beim DNS des Servers ab und zeigt in der Karte
  „im Haus direkt“ (Antwort ist eine eigene Adresse des Verbunds) oder
  „über Cloudflare“.
- **Mit UniFi-Schlüssel** trägt das PDH die Namen selbst ein:
  - Einstellungen in der Karte: Adresse der UDM Pro, API-Schlüssel (in UniFi
    Network angelegt), Standort (Vorgabe: der einzige bzw. „default“). Der
    Schlüssel liegt verschlüsselt in der Datenbank und wird nie angezeigt.
  - Die UDM Pro hat ein selbst ausgestelltes Zertifikat. Beim Speichern zeigt
    das PDH dessen Fingerabdruck; der Anwender bestätigt ihn einmal. Danach
    spricht das PDH nur mit einem Gerät, das diesen Fingerabdruck zeigt.
    Ändert er sich, ruht alles, bis der Anwender neu bestätigt.
  - Je Name ein Knopf „Im Haus eintragen“: lokaler DNS-Eintrag Typ A auf die
    mitwandernde Adresse. Steht dort schon ein Eintrag mit anderer Adresse,
    zeigt die Karte ihn und fragt vor dem Überschreiben.
  - Das PDH merkt sich die Kennung des Eintrags und entfernt beim Entfernen
    eines Namens nur Einträge, die es selbst angelegt hat. Andere Einträge der
    UDM Pro liest und ändert es nicht.
  - Die tägliche Prüfung meldet einen fehlenden oder abweichenden Eintrag in
    der Karte; sie ändert ihn nicht von selbst.
  - Schnittstelle: die amtliche von UniFi Network
    (`/proxy/network/integration/v1/sites/<id>/dns/policies`, Kopfzeile
    `X-API-KEY`); kennt das Gerät sie nicht (ältere Version), die bisherige
    (`/proxy/network/v2/api/site/<name>/static-dns`). Diese Angaben stammen aus
    Quellen Dritter und werden vor der Umsetzung am Gerät des Anwenders geprüft.
- **Ohne UniFi-Schlüssel** (oder mit anderem DNS-Server) legt der Anwender die
  Einträge von Hand an; das PDH zeigt nur den Stand.
- `PDH_PUBLIC_URL` und die vertrauenswürdigen Adressen von Nextcloud bleiben,
  wie sie sind: Es sind schon die Namen.

## 8. Fehlerfälle

| Fall | Verhalten |
|---|---|
| API-Token fehlt oder darf die Domain nicht ändern | Die Karte nennt das fehlende Recht; kein Auftrag beginnt. |
| Prüf-Eintrag wird nicht sichtbar | Abbruch nach 2 Minuten, Eintrag gelöscht, altes Zertifikat bleibt. |
| Let's Encrypt lehnt ab | Abbruch, Eintrag gelöscht, Grund in der Karte. |
| Kontingent erschöpft | Sperrzeit merken, danach neuer Versuch. |
| `nginx -t` schlägt fehl | Vorige Dateien wiederherstellen, Dienste laufen weiter, Fehler in der Karte. |
| Agent nicht erreichbar | Zertifikat bleibt in der Datenbank, Karte zeigt „noch nicht eingerichtet“, nächster Lauf holt es nach. |
| Rollentausch mitten im Ablauf | Neuer aktiver Server räumt Prüf-Einträge auf und beginnt bei Bedarf neu. |
| Datei und Datenbank weichen ab | Neu installieren aus der Datenbank. |
| UDM Pro nicht erreichbar oder Schlüssel ohne Recht | Die Karte nennt den Grund; Zertifikate und nginx sind davon unberührt. |
| Fingerabdruck der UDM Pro geändert | Keine Verbindung, Karte verlangt neue Bestätigung. |
| Name entfernt | Seite bzw. Zertifikatszeilen zurück auf den Stand davor, Dateien gelöscht, Zertifikat bei Let's Encrypt widerrufen. |

## 9. Oberfläche

Server-Einstellungen → Cloudflare-Tunnel, neue Karte „Zertifikate
(Let's Encrypt)“ unter der DNS-Tabelle (`widgets/cloudflare.gohtml`):

- Oben: E-Mail für Let's Encrypt, Schalter „Testumgebung“, Knopf „Tunnel und
  Domain auf Full (strict) umstellen“ bzw. „Zurückstellen“.
- Je Name eine Zeile: Name, Zustand (gültig bis, wird geholt mit Schritt,
  Fehler mit Grund), „im Haus direkt“ oder „über Cloudflare“, Knöpfe
  „Jetzt holen/erneuern“, „Im Haus eintragen“ (nur mit UniFi-Schlüssel) und
  „Entfernen“.
- Aufklappbar „Namen im Haus (UniFi)“: Adresse, API-Schlüssel, Standort,
  Fingerabdruck mit „Bestätigen“.
- Ein laufender Auftrag zeigt seinen Schritt; die Seite muss dafür nicht offen
  bleiben.
- Farben, Ecken und Schatten über die Variablen; geprüft in Fiori und Fluent,
  hell und dunkel.

## 10. Rechte und Sicherheit

- Neues Recht `system.certs` „Zertifikate verwalten (Let's Encrypt)“, Kategorie
  „System & Sicherheit“, Migration 157. Es geht an die Rollen, die
  `system.cloudflare` überall haben.
- Neues Recht `system.unifi` „Namen im Haus eintragen (UniFi)“, gleiche
  Kategorie, Migration und Vergabe. Es deckt die UniFi-Einstellungen und
  „Im Haus eintragen“.
- „Entfernen“ prüft den Bereichsadministrator; die Route steht in `gateRules`.
- „Full (strict) umstellen“ braucht zusätzlich `system.cloudflare`.
- Private Schlüssel und der Konto-Schlüssel liegen verschlüsselt in der
  Datenbank, als Datei nur für root und nginx lesbar, und verlassen den Server
  nur über den verschlüsselten Verbund-Kanal und die Sicherung.
- Im Protokoll stehen Namen, Schritte und Fingerabdrücke, nie Schlüssel oder
  Token.
- Der Agent nimmt nur Namen an, die als Hostname gültig sind, und schreibt nur
  unter `/etc/ssl/pdh` und in die zwei nginx-Seiten.

## 11. Tests und Abnahme

Automatisch:

- Ablauf gegen einen Let's-Encrypt- und Cloudflare-Dienst im Speicher: holen,
  erneuern, Abbruch in jedem Schritt, Aufräumen der Prüf-Einträge, Sperrzeit.
- nginx-Seiten als Textvergleich; Umstellen und Zurückstellen der
  Zertifikatszeilen in einer Nextcloud-Seite.
- Agent: Ablegen, Wiederherstellen nach gescheiterter Prüfung, Ablehnung
  ungültiger Namen und im Docker-Betrieb.
- Rechte (`perm_gate`), Karte je Recht, Handbuch-Tests, `go test ./...`.

Labor: neues Szenario „Zertifikat wandert beim Tausch mit“ (Dateien und
Fingerabdruck auf beiden Knoten gleich, nginx-Prüfung grün).

Abnahme auf den Testservern gegen die Testumgebung:

1. Zertifikate für den Test-PDH-Namen und den Nextcloud-Namen holen.
2. Lokale DNS-Einträge auf die mitwandernde Adresse; Browser im Haus zeigt das
   neue Zertifikat (in der Testumgebung mit erwarteter Warnung).
3. Rollentausch: beide Namen bleiben im Haus und von außen erreichbar.
4. Erneuerung erzwingen.
5. Fehlerfall: Token ohne Recht „DNS bearbeiten“.

Danach einmal echte Zertifikate holen, „Full (strict)“ einschalten und im Haus
Browser, Desktop-Client und Handy-App ohne Warnung prüfen.

## 12. Handbuch, Änderungsprotokoll, Version

- Handbuch-Kapitel zu Cloudflare: „So holst du ein Zertifikat“, „So zeigen die
  Namen im Haus auf den Server“ (UniFi UDM Pro: API-Schlüssel anlegen, im PDH
  eintragen, Fingerabdruck bestätigen, „Im Haus eintragen“; für andere
  DNS-Server ein allgemeiner Satz zum Eintrag von Hand),
  „Full (strict) einschalten“, Fehlerbilder. Der Satz „Zertifikate über
  Cloudflare stellt das PDH noch nicht aus“ entfällt. Skizze in
  `help_figures.gohtml` anpassen.
- Kapitel „Administration“: neues Recht.
- `CHANGELOG.md`: `### Neu` und `### Sicherheit`; nächste Korrekturversion.

## 13. Offene Punkte für den Plan

- DNS-Server im Haus: geklärt, UniFi UDM Pro mit API-Schlüssel. Offen: Version
  von UniFi Network am Gerät (bestimmt die Schnittstelle), genaue Felder der
  Anfrage und der Menüpfad für den API-Schlüssel – alles wird vor der
  Umsetzung am Gerät des Anwenders abgelesen.
- Wie der Agent den nginx-Benutzer und den Ort der Seiten je Distribution
  findet (heute Ubuntu: `www-data`, `/etc/nginx/sites-enabled`).
- Ob der Tunnel den Namen auf dem Server über `/etc/hosts` oder über die
  Angabe „Origin Server Name“ im Tunnel-Ziel erreicht.
- Aufbewahrung alter Zertifikate in der Datenbank (Vorschlag: die letzten zwei
  je Name).
