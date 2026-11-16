# MQTT-Broker als eigenes System mit Namen (0.80.20)

Stand: 16.11.2026 · Anwender: „kannst du auch den internen MQTT broker als separates
System ansehen damit ich auch eine subdomain anlegen kann“ – verbinden sollen sich
„alle MQTT clients“. Der Vorschlag (ein Name, im Haus TCP/TLS, von außen WebSocket über
den Tunnel, reines TCP von außen nur über VPN) ist freigegeben („passt so ja“).
Was darüber hinaus beim Lesen des Codes nötig wurde, ist als *Entscheidung Claude* markiert.

## Warum

Der integrierte MQTT-Broker hängt heute an einer MQTT-Import-Verbindung und ist nur über
die IP-Adresse und seine Ports erreichbar. TLS geht nur über von Hand eingetragene
Dateipfade, die der Broker einmal beim Start liest – ein erneuertes Zertifikat wirkt erst
nach einem Neustart, und der zweite Server des Verbunds kennt die Dateien nicht.
PDH und Nextcloud haben dagegen einen Namen, ein Let's-Encrypt-Zertifikat aus der
Zertifikatsliste, einen Eintrag im Haus (UniFi) und ein Ziel im Cloudflare-Tunnel.
Der Broker soll genauso ein System mit eigenem Namen werden, z. B. `mqtt.firma.de`.

## Ziele

1. **Ein Name für den Broker**, gepflegt an der Broker-Verbindung.
2. **Im Haus mit gültigem Zertifikat:** `mqtts://<name>:8883` – Zertifikat aus der
   Zertifikatsliste, Erneuerung ohne Neustart, auf beiden Servern des Verbunds gleich.
3. **Steuerungen ohne TLS bleiben verbunden:** Der bisherige Port (z. B. 1883) läuft
   unverändert weiter.
4. **Apps und Dienste mit einer Adresse für drinnen und draußen:** `wss://<name>`
   (Port 443) – im Haus über nginx, von außen über den Cloudflare-Tunnel.
5. **Einrichten ohne Suchen:** Prüfliste auf der Broker-Seite (Zertifikat, Name im Haus,
   Tunnel) und eine dritte Zeile „MQTT“ bei den Tunnel-Zielen.
6. **Der Name erscheint überall statt der IP:** Client-Einstellungen der Broker-Seite,
   TIA-Baustein.

## Nicht-Ziele

- Reines MQTT über TCP aus dem Internet (zweiter Name ohne Proxy + Portfreigabe 8883).
  Der Broker stünde offen im Netz. Von außen: WebSocket über den Tunnel oder VPN.
- Mehrere Benutzer/ACLs am Broker, Client-Zertifikate (mTLS) am Broker.
- Den bisherigen Port abschalten oder umstellen.
- Externe Broker (Modus „extern“) – dort ändert sich nichts.

## Anti-Ziele

- Eine bestehende Broker-Verbindung verhält sich nach dem Update anders (Ports, TLS per
  Dateipfad, Explorer, Wertübernahme, TIA-Vorbelegung ohne Namen).
- Das PDH oder der Broker startet nicht, weil Cloudflare, UniFi oder die Zertifikate
  nicht eingerichtet oder nicht erreichbar sind. Das sind Zusatzfunktionen; ohne sie
  bleibt alles wie heute.
- „Full (strict)“ lässt sich wegen des MQTT-Namens nicht mehr einschalten, oder ein
  eingeschaltetes „Full (strict)“ macht den Broker von außen unerreichbar.
- Der private Schlüssel landet im Protokoll, in einer Meldung oder in der Seite.

## Ansätze

| | Ansatz | Abwägung |
|---|---|---|
| **A (gewählt)** | Ein Name. Eigener TLS-Port am Broker mit Zertifikat aus der Datenbank. nginx-Seite für den Namen auf 443 → WebSocket-Port. Tunnel-Ziel wie bei PDH/Nextcloud. | `wss://<name>` gilt drinnen und draußen (Handy wechselt zwischen WLAN und Mobilfunk); „Full (strict)“ bleibt möglich, weil nginx den Namen auf 443 mit Zertifikat bedient. Braucht eine neue Rolle im Update-Agenten. |
| B | Tunnel-Ziel direkt auf den WebSocket-Port, keine nginx-Seite | weniger Umbau; aber im Haus zeigt der Name auf die gemeinsame Adresse, dort antwortet auf 443 eine andere Seite – Apps bräuchten zwei Adressen. „Full (strict)“ prüft jeden geführten Namen auf 443 und scheitert. |
| C | Zweiter Name ohne Cloudflare-Proxy, Portfreigabe 8883 an der Firewall | reines TCP von außen; Broker offen im Internet – abgelehnt, siehe Nicht-Ziele. |

*Vom Anwender freigegeben: ein Name, Zertifikat aus der Liste, Tunnel-Zeile, Prüfliste,
Name statt IP, kein offener Port. Entscheidung Claude: eigener TLS-Port neben dem
bisherigen Port und die nginx-Seite (A statt B).*

## Wie es heute ist (Fundstellen)

- Broker-Einstellungen: JSON `config` der Verbindung (`import_export_connections`),
  Schlüssel `int_listen_port`, `int_websocket_port`, `int_tls_cert_path`,
  `int_tls_key_path` … (`internal/web/import_export.go` Liste der erlaubten Schlüssel,
  `internal/web/mqtt_broker.go` `mqttBrokerConfigFromMap`, `reconcileMqttBroker`).
- Broker: `internal/core/mqttbroker/manager.go` – ein TCP-Listener, optional ein
  WebSocket-Listener; beide bekommen dieselbe `tls.Config` aus den Dateipfaden
  (`tls.LoadX509KeyPair`, einmal beim Start).
- Zertifikate: Tabelle `le_certs`, `h.certKey(ctx, name)` liefert Kette und Schlüssel
  (`internal/web/certs_store.go`). Der Update-Agent legt sie nach `/etc/ssl/pdh/<name>/`
  und schreibt für die Rolle `pdh` eine nginx-Seite (`cmd/update-agent/certs.go`
  `certSiteConf`, Rollen `pdh | nextcloud | ""`); Rolle und Upstream kommen aus
  `certRole` / `certInstallBody` (`internal/web/certs.go`).
- Name im Haus: „Im Haus eintragen“ für jeden Namen der Zertifikatsliste
  (`internal/web/certs_lan.go`, Route `POST /admin/server-config/certs/lan`, Recht `system.unifi`).
- Tunnel-Ziele: `cfWantedTargets` (`internal/web/cloudflare_api.go`) kennt fest PDH und
  Nextcloud; „Einrichten“ über `POST /admin/server-config/cloudflare/tunnel`
  (Recht `system.cloudflare`). Bei „Full (strict)“ ist für jeden geführten Namen
  `https://localhost:443` mit Prüfung das Soll (`cfStrictWanted`, `certs_strict.go`).
- Anzeige: Broker-Seite nimmt `r.Host` als Adresse (`MqttBrokerPage`), der TIA-Baustein
  ebenso (`tiaDefaults` in `internal/web/tia_block.go`).

## Datenmodell

Keine Migration, kein neues Recht. Zwei neue Schlüssel in der `config` der Verbindung
(nur Modus „Integrierter Broker“):

| Schlüssel | Feld im Formular | Regeln |
|---|---|---|
| `int_public_host` | „Name des Brokers (Subdomain)“ | leer = wie bisher. Sonst gültiger Hostname, klein geschrieben, ohne Schema/Port/Pfad, höchstens 253 Zeichen; je Name nur eine Broker-Verbindung; nicht der Name des PDH und nicht der von Nextcloud. |
| `int_tls_port` | „TLS-Port (mqtts)“ | leer = aus. 1–65535, ungleich `int_listen_port` und `int_websocket_port`. Nur mit Name. Vorschlag im Feld: 8883. |

Beide in die Liste der erlaubten Schlüssel, in die Feldbezeichnungen der
Verbindungsdetails und in Anlege- und Bearbeiten-Formular. Fehler als Meldung am Formular.

## Verhalten

### Broker (TLS-Port)

- `mqttbroker.Config` bekommt `TLSPort int` und
  `GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)`. Sind beide
  gesetzt, startet ein **zusätzlicher** TCP-Listener auf `TLSPort`
  (`tls.Config{GetCertificate, MinVersion: TLS 1.2}`). Listen-Port und WebSocket-Port
  bleiben, wie sie sind (auch TLS per Dateipfad wie bisher).
- Das Zertifikat ist der Eintrag der Zertifikatsliste mit dem Namen `int_public_host`.
  Die Web-Schicht stellt `GetCertificate`: holt Kette und Schlüssel über `certKey`,
  hält das geparste Zertifikat im Speicher und prüft höchstens alle 60 Sekunden, ob sich
  der Fingerabdruck in `le_certs` geändert hat. Ein erneuertes Zertifikat gilt damit
  für neue Verbindungen ohne Neustart; bestehende Verbindungen bleiben.
- Gibt es (noch) kein Zertifikat, startet der Broker trotzdem; der TLS-Handshake scheitert,
  bis eines da ist. Die Broker-Seite sagt das in der Prüfliste. Kein Zertifikat ist kein
  Startfehler des Brokers.
- Status der Broker-Seite nennt den TLS-Port, wenn er läuft.

### WebSocket über 443 (nginx) – neue Rolle `mqtt`

- `certRole` liefert für einen Namen, der `int_public_host` einer **aktiven**
  Broker-Verbindung mit WebSocket-Port ist, die Rolle `mqtt`; der Upstream ist
  `http://127.0.0.1:<int_websocket_port>`.
- Der Update-Agent nimmt die Rolle `mqtt` an und schreibt wie bei `pdh` eine nginx-Seite
  (`certSiteConf`, 443 mit Zertifikat, Weiterreichen mit WebSocket-Kopfzeilen, **ohne**
  Umleitung von Port 80). Entfernen eines Namens räumt sie wie bei `pdh` weg.
- Ändert sich Name, WebSocket-Port oder Aktiv-Zustand der Verbindung, wird die Seite neu
  geschrieben bzw. entfernt (gleicher Weg wie die vorhandene Reparatur „Seite fehlt“ im
  täglichen Lauf, zusätzlich gleich nach dem Speichern).
- Ein älterer Agent lehnt die Rolle ab: Das Zertifikat bleibt gespeichert, die Zeile
  zeigt wie heute „noch nicht eingerichtet: …“. Kein Absturz, kein Zurückfallen auf `pdh`.
- Kein WebSocket-Port oder WebSocket selbst mit Datei-Zertifikat verschlüsselt:
  Rolle `""` (nur Dateien), keine Seite; die Prüfliste nennt den Grund.

### Tunnel (Cloudflare)

- `cfWantedTargets` liefert je aktiver Broker-Verbindung mit Name **in der Zone** und
  WebSocket-Port eine Zeile: Label „MQTT“ (bei mehreren „MQTT · <Verbindungsname>“),
  Host = Name, Service = `http://localhost:<int_websocket_port>`. `cfStrictWanted` stellt
  das Soll bei „Full (strict)“ wie bei den anderen geführten Namen auf
  `https://localhost:443` – das funktioniert, weil nginx den Namen bedient.
- „Einrichten“ ist die vorhandene Aktion (Tunnel-Ziel + DNS-Eintrag). Überschrift des
  Abschnitts: „PDH, Nextcloud und MQTT über den Tunnel“.

### Prüfliste auf der Broker-Seite

Nur bei gesetztem Namen und nur für Betrachter mit Schreibrecht an der Verbindung
(wie die Client-Einstellungen). Wird **nachgeladen** (eigener htmx-Abschnitt mit
Zeitgrenze), damit die Seite nicht auf UniFi oder Cloudflare wartet.

| Zeile | Zustände | Knopf (nur mit Recht) |
|---|---|---|
| Zertifikat | fehlt in der Liste / angefordert / gültig bis TT.MM.JJJJ / Fehler | „Zertifikat anfordern“ – vorhandene Aktion „Name hinzufügen“ der Zertifikatsliste, deren Recht |
| Name im Haus | eingetragen auf <Adresse> / fehlt / UniFi nicht eingerichtet (optional) | „Im Haus eintragen“ – vorhandene Aktion, `system.unifi` |
| Tunnel | eingerichtet / fehlt / zeigt auf … / Cloudflare nicht eingerichtet (optional) / Name nicht in der Zone | „Einrichten“ – vorhandene Aktion, `system.cloudflare` |
| TLS-Port | läuft auf 8883 / aus / wartet auf Zertifikat | – |
| WebSocket über 443 | eingerichtet / WebSocket-Port fehlt / Seite noch nicht geschrieben | – |

Die Knöpfe rufen die vorhandenen Routen auf und kehren zur Broker-Seite zurück. Ohne das
jeweilige Recht steht statt des Knopfs, wer es einrichten kann („Administrator mit Recht …“).
Die Zustände der Zeilen sieht, wer die Verbindung bearbeiten darf.

### Anzeige des Namens

- Broker-Seite „Client-Einstellungen“: mit Name stehen dort die fertigen Adressen –
  `mqtt://<name>:<Port>` (ohne TLS, im Haus), `mqtts://<name>:<TLS-Port>` (im Haus und über
  VPN), `wss://<name>/` (im Haus und von außen; Hinweis: Keep-Alive höchstens 60 s, weil
  Cloudflare stille Verbindungen nach 100 s trennt). Ohne Name wie bisher.
- TIA-Baustein (`tiaDefaults`): Broker-Adresse = Name, wenn gesetzt. Wählt der Anwender
  die TLS-Variante und ist ein TLS-Port gesetzt, steht der TLS-Port drin. Ohne Name wie bisher.
- Explorer und Wertübernahme verbinden sich weiter lokal über den Listen-Port.

## Rechte

Kein neues Recht: Die Felder gehören zur Verbindung (`import.write` über
`canConnectionWrite`), die Knöpfe der Prüfliste prüfen die Rechte der vorhandenen
Aktionen (`system.certs_admin` bzw. das Recht der Aktion „Name hinzufügen“,
`system.unifi`, `system.cloudflare`), die Tunnel-Zeile sieht nur, wer den Reiter
„Cloudflare-Tunnel“ sieht. Neue Routen (Prüfliste nachladen) kommen in `gateRules`.

## Handbuch, Version

- 0.80.20, CHANGELOG `### Neu` (Name des Brokers, TLS-Port mit Zertifikat aus der Liste,
  wss über 443, Zeile MQTT im Tunnel, Prüfliste), `### Geändert` (Broker-Seite und
  TIA-Baustein zeigen den Namen).
- Handbuch: Kapitel zum MQTT-Broker – „So geht's: Broker unter eigenem Namen erreichbar
  machen“ (Name eintragen → Zertifikat → im Haus eintragen → Tunnel einrichten → Adressen
  für Steuerung, Gerät, App), Hinweis „von außen ohne WebSocket nur über VPN“; Kapitel
  Cloudflare/Zertifikate um die Zeile MQTT ergänzen; Skizze der Broker-Seite anpassen.
- Der Update-Agent ändert sich (Rolle `mqtt`) – er wird mit dem Verbund-Update ausgeliefert.

## Prüfen

- Broker: TLS-Listener mit `GetCertificate` – Verbindung gelingt; nach Tausch des
  Zertifikats im Speicher sieht ein neuer Handshake das neue, die alte Verbindung bleibt;
  ohne Zertifikat scheitert nur der Handshake, der Broker läuft.
- Konfiguration: Regeln der beiden Felder (doppelter Name, Port-Kollision, TLS-Port ohne Name).
- `certRole`/`certInstallBody`: Rolle `mqtt` mit Upstream; Agent: Seite für `mqtt` ohne
  Port-80-Umleitung, unbekannte Rolle weiter abgelehnt.
- `cfWantedTargets`: Zeile MQTT (in/außerhalb der Zone, ohne WebSocket-Port, mit „Full (strict)“).
- Broker-Seite und Prüfliste ohne eingerichtetes Cloudflare/UniFi/Zertifikate; mit/ohne Rechte.
- `tiaDefaults` mit und ohne Namen; bestehende Tests bleiben grün.
- Browser: Broker-Seite, Verbindungsformular, Reiter Cloudflare in Fiori/Fluent, hell/dunkel,
  320–1366 px.

## Festlegungen nach der Gegenprüfung (16.11.2026, gehen dem Text oben vor)

Ein unabhängiger Leser hat den Entwurf gegen den Code geprüft. *Alle Punkte: Entscheidung Claude.*

1. **Mit Name ist der WebSocket-Port Pflicht** (Vorschlag im Feld: 8083), und die
   Dateipfade `int_tls_cert_path`/`int_tls_key_path` müssen leer sein – der Name ersetzt
   sie. Damit hat jeder MQTT-Name eine nginx-Seite, und „Full (strict)“ bleibt möglich
   (`certStrictOn` prüft jeden Namen der Liste auf 127.0.0.1:443, `certs_strict.go`).
2. **Rolle `mqtt` gilt für jeden Namen, den eine Broker-Verbindung trägt – aktiv oder
   nicht.** Ist der Broker aus, antwortet nginx mit 502; das Zertifikat auf 443 bleibt
   gültig. Trägt keine Verbindung mehr den Namen, steht er aber noch in der
   Zertifikatsliste, gilt wie bei jedem anderen Namen ohne Seite die Rolle `""`.
3. **Eigene nginx-Datei je Name:** `mqtt-<name>.conf` mit derselben Kopfzeile als
   Eigentumsnachweis. Die feste `pdh.conf` des Agenten bleibt unberührt
   (`cmd/update-agent/certs.go`: Anlegen, Kollisionsprüfung `certSitesWithName` und
   Entfernen brauchen einen eigenen Weg für `mqtt`).
4. **`certRole` ist heute eine reine Funktion ohne Datenbank** (Aufrufer in `certs.go`,
   `cloudflare_mtls.go`, `pki_status.go`). Der Bauende darf die Signatur ändern oder eine
   Abfrage davorsetzen; alle Aufrufer müssen dieselbe Antwort bekommen.
5. **Seite neu schreiben nach dem Speichern ist neu zu bauen.** Heute gibt es nur
   `certSiteStale` (fehlende IPv6-Zeile). Ändert sich Name oder WebSocket-Port, wird die
   Seite gleich nach dem Speichern neu geschrieben bzw. entfernt; scheitert das, holt es
   der tägliche Lauf nach.
6. **Knöpfe der Prüfliste:** htmx-Aufruf der vorhandenen Routen; die Antwort (Fragment des
   Reiters) wird verworfen, danach lädt die Prüfliste neu. Keine neuen Aktionsrouten.
7. **„Zertifikat anfordern“** führt „Name hinzufügen“ und „Ausstellen“ nacheinander aus.
   Recht wie dort: `certCanManage` (`system.certs` oder Administrator) – nicht
   `system.certs_admin` (das gilt nur fürs Entfernen). Ohne eingerichtetes Cloudflare
   (DNS-Prüfung) gibt es kein Zertifikat; die Zeile sagt „Cloudflare nicht eingerichtet“.
8. **Eigene `tls.Config` für den TLS-Port.** Die vorhandene Variable für Listen- und
   WebSocket-Port (Dateipfade) nicht wiederverwenden.
9. **TIA-Formular:** Beim Umschalten auf TLS setzt ein kleines Skript den TLS-Port ins
   Portfeld; die Vorbelegung ohne TLS bleibt wie bisher.
10. **Vor dem Plan zu klären:** Wie kommt die nginx-Seite auf den zweiten Server des
    Verbunds (wer ruft dort den Agenten auf, wann)? Der Weg muss für `mqtt` derselbe sein
    wie für `pdh`; wenn es für `pdh` keinen gibt, gehört das in den Plan.

## Dem Bauenden überlassen

Dateiaufteilung, Wortlaut der Meldungen, wie die Prüfliste technisch nachgeladen wird,
wie das Neuschreiben der nginx-Seite nach dem Speichern angestoßen wird, Zwischenspeicher
des Zertifikats (Aufbau), Übersetzungen der neuen Texte.
