# Ausfallschutz Teil 5 – Zugangsweg

Stand: 2026-10-28 · Status: freigegeben ·
Gesamt-Architektur: `2026-10-24-ausfallschutz-design.md` (Abschnitte 3, 7, 8) ·
Vorgänger: `2026-10-27-ausfallschutz-lesemodus-design.md`

## 1. Ziel und Umfang

Nach einem Rollentausch oder Ausfall sollen alle Benutzer und Geräte den
aktiven Server automatisch wieder erreichen – ohne dass jemand an Geräten,
Lesezeichen oder Laufwerken etwas umstellt.

Zugriffswege heute (vom Nutzer bestätigt): Cloudflare-Adresse, LAN direkt über
IP/Namen, Geräte mit fester Adresse (Leitstand, Terminals, Drucker, Scanner),
Samba/Netzlaufwerke.

Entscheidungen des Nutzers:

- Beide Server stehen im **gleichen Netz** (heute 10.10.0.x).
- Die heutige Adresse der Produktion **10.10.0.202 wird die mitwandernde
  Adresse**; der Produktionsserver bekommt beim Einstieg in den Verbund einmalig
  eine neue eigene Adresse (z. B. .203).
- DNS: **für alle Wege offen** – kein fester Anschluss an einen DNS-Server,
  sondern Skripte beim Rollenwechsel.
- Server können **beliebig virtualisiert oder Hardware** sein (Hyper-V,
  VMware, Proxmox/KVM, XenServer, Blech).
- Ansatz A: **pdh-ha setzt die Adresse selbst** (kein keepalived), damit
  Adresse, Datenbank und Amtszeit nie auseinanderlaufen.

Im Umfang:

1. Mitwandernde Adresse in pdh-ha (Übernehmen, Abgeben, Abgleich im Takt,
   Duplikat-Schutz, Test).
2. Cloudflare-Tunnel auf beiden Servern mit demselben Token, automatisch
   eingerichtet.
3. Skripte beim Rollenwechsel (`/etc/pdh/ha-hooks/`).
4. Karte „Zugangsweg“ im Ausfallschutz-Reiter, `pdh-ha status`.
5. Angeleitete Umstellung einer bisherigen Server-Adresse zur mitwandernden
   Adresse (`scripts/pdh-ha-adresse.sh`).

Nicht im Umfang: Samba und dessen Umschalten auf „nur lesen“ (Teil 7);
rsync-Spiegelung von Skripten und pdhadmin-Daten (Teil 6); Adressumschaltung
über Cloud-Anbieter-APIs (dafür gibt es die Skripte).

Erfolgskriterien:

- Nach Rollentausch, Ausfall und Neustart sind PDH, Geräte und Tunnel innerhalb
  weniger Sekunden über die bisherigen Adressen wieder erreichbar.
- Die mitwandernde Adresse ist **nie** auf zwei Servern gleichzeitig aktiv.
- Eine Störung im Zugangsweg (Skript, Tunnel, Adresse) verhindert nie die
  Umschaltung der Datenbank.

## 2. Mitwandernde Adresse

### 2.1 Einstellung

- In der gemeinsamen Verbund-Konfiguration (`clusterConfig`) kommt ein Block
  `access` dazu:
  `vip` (Adresse mit Präfix, z. B. `10.10.0.202/24`), `vip_on` (Schalter),
  `hooks_on` (Schalter, siehe 4). Verteilung wie die übrigen Einstellungen
  (Version hochzählen, Konfigurationsübergabe über Port 7480).
- Knotenspezifisch in `/etc/pdh/ha/node.json` optional `vip_interface`.
  Ohne Eintrag wählt jeder Server die Netzwerkkarte, deren eigene Adresse im
  selben Netz wie die mitwandernde Adresse liegt. Keine oder mehrere passende
  Karten → Einschalten wird abgelehnt („Netzwerkkarte nicht eindeutig – in
  node.json `vip_interface` eintragen“).
- Prüfungen beim Speichern: gültige IPv4-Adresse mit Präfix; nicht gleich einer
  eigenen Adresse eines Knotens oder des Zeugen; im Netz einer eigenen Adresse
  beider Server. IPv6 ist nicht im Umfang.
- Der Zeuge bekommt die Adresse nie.

### 2.2 Wann sie wandert

Der Entscheidungskern (`internal/ha/step.go`) löst `ActVIPTake` /
`ActVIPRelease` bereits aus:

| Anlass | Aktion |
|---|---|
| Übernahme (`promote`, neue Amtszeit) | übernehmen |
| Rollentausch, alter aktiver Server (`swap`) | abgeben (zusammen mit `ActDemote`) |
| Rollentausch, neuer aktiver Server (`promote`) | übernehmen |
| Selbst-Abschaltung (`ActFence`) | abgeben |
| Rückkehr aus der Abschaltung (`unfence`) | übernehmen |

Neu: `pgActor.Do` führt beide Aktionen wirklich aus (heute nur
„würde jetzt“). Beobachtungsmodus: automatische Aktionen werden weiterhin nur
protokolliert; Handbefehle (Tausch, Erzwingen) wirken immer – wie bei allen
anderen Aktionen. Ist `vip_on` aus, sind beide Aktionen ohne Wirkung und ohne
Ereignis.

### 2.3 Ausführung

- Neues Bauteil `vipCtl` in `cmd/pdh-ha` über die vorhandene, vortäuschbare
  `runner`-Schnittstelle:
  - **Haben?** `ip -o addr show dev <karte>` enthält die Adresse.
  - **Übernehmen:** Duplikat-Prüfung (2.4), dann
    `ip addr add <vip> dev <karte>`, danach `arping -U -c 3 -I <karte> <ip>`
    sofort und noch einmal nach 5 s (Gratuitous ARP – Switch, Geräte und
    Windows-PCs lernen den neuen Ort sofort).
  - **Abgeben:** `ip addr del <vip> dev <karte>`; nicht vorhanden gilt als
    Erfolg.
- Es wird die eigene MAC-Adresse der Karte benutzt – keine Plattform braucht
  MAC-Spoofing oder Promiscuous-Modus.
- Benötigte Pakete `iproute2` und `arping` installiert `pdh-ha-join.sh`.

### 2.4 Duplikat-Schutz

- Vor jedem Übernehmen, sofern der Server die Adresse nicht schon hat:
  `arping -D -c 2 -w 3 -I <karte> <ip>`. Antwortet ein anderes Gerät →
  **nicht übernehmen**, Alarm „Adresse 10.10.0.202 wird noch von einem anderen
  Gerät benutzt (MAC …)“, neuer Versuch im Abgleich (2.5), Alarm höchstens
  einmal je Stunde (Ereignis-Entprellung wie in Teil 4).
- Die Datenbank-Übernahme bleibt davon unberührt; der neue aktive Server ist
  dann über Cloudflare und seine eigene Adresse erreichbar.
- Abgeben scheitert → Alarm. Der andere Server übernimmt erst, wenn seine
  Duplikat-Prüfung frei meldet.

### 2.5 Abgleich im Takt

Wie beim PDH-Dienst (`shouldRunPDH`) prüft pdh-ha in jedem Takt den
Ist-Zustand, unabhängig von einzelnen Aktionen:

- Soll: Adresse genau dann vorhanden, wenn `vip_on` und die eigene Rolle
  `aktiv` ist (nicht `abgeschaltet`, nicht `wiedereingliederung`). Wartung
  ändert daran nichts – sie sperrt nur automatische Umschaltungen, der aktive
  Server behält die Adresse. Sonst nicht vorhanden.
- Abweichung → übernehmen bzw. abgeben (mit 2.4). Damit sind abgedeckt:
  Neustart (die Adresse steht nie in der festen Netzwerkkonfiguration, nach
  einem Neustart hat sie kein Server, bis pdh-ha ihn als aktiv bestätigt),
  versehentliches Entfernen durch den Netzwerkdienst (z. B. `netplan apply`),
  verlorene Aktionen.
- Im Beobachtungsmodus wird nur auf „abgeben“ abgeglichen (eine Adresse auf
  einem nicht aktiven Server ist immer falsch); „übernehmen“ nur protokolliert,
  außer nach einem Handbefehl.

### 2.6 Test

- Auslösbar im PDH (Karte „Zugangsweg“ → „Testen“) und mit
  `sudo pdh-ha vip test [--adresse <ip/präfix>]`.
- Prüfadresse: die mitwandernde Adresse, wenn sie im Netz frei ist; sonst
  (Umstellungsfall, sie gehört noch einem Server) eine einmalig einzugebende,
  freie Testadresse.
- Ablauf je Server nacheinander: Prüfadresse annehmen (mit 2.4), der andere
  Server prüft über die Verbund-Schnittstelle (Port 7480, neuer Aufruf
  „Adresse prüfen“) Ping und `http://<ip>:8090/health` (dort antwortet das PDH
  – am passiven Server als Spiegel, Teil 4), der Zeuge prüft nur HTTP (im
  Container ohne Ping-Recht); Prüfadresse wieder abgeben. Höchstdauer je
  Server 20 s.
- Ergebnis je Richtung im PDH und in `pdh-ha status`. `vip_on` lässt sich erst
  einschalten, wenn ein Test in beide Richtungen erfolgreich war. Gespeichert
  werden Zeitpunkt, Netz und Karten; der Test gilt für jede Adresse im selben
  Netz auf denselben Karten (so zählt im Umstellungsfall der Test mit der
  Testadresse auch für .202). Anderes Netz oder andere Karte → neuer Test.
- Scheitert der Test, nennt das PDH die typischen Ursachen: Proxmox-Firewall
  mit IP-Filter, Switch mit ARP-Prüfung/Port-Security, Cloud-Server (dort
  grundsätzlich nicht möglich) – und empfiehlt die Skripte (4) als Ersatz.

## 3. Cloudflare-Tunnel auf beiden Servern

- Heute: pdh-ha startet/stoppt `cloudflared` nach Rolle, sofern installiert
  (Teil 3/4); auf dem zweiten Server ist die Einrichtung Handarbeit.
- Neu: Das Token liegt verschlüsselt in der Datenbank
  (`PDH_CLOUDFLARE_TUNNEL_TOKEN`) und ist auf beiden Servern lesbar – das PDH
  am Spiegel liest die Server-Einstellungen schon (Teil 4). Das **PDH** reicht
  das Token an den lokalen pdh-ha weiter (Unix-Socket, neuer Aufruf
  `PUT /v1/tunnel`); pdh-ha muss nichts entschlüsseln.
  - `pdh-ha` meldet im Status die Prüfsumme (SHA-256) des abgelegten Tokens.
    Das PDH vergleicht beim Start und danach alle 60 s mit der Prüfsumme des
    Tokens aus der Datenbank und schickt das Token nur bei Abweichung
    (`PUT /v1/tunnel` mit Token; leeres Token = Tunnel entfernen).
  - pdh-ha legt das Token in `/etc/pdh/ha/cloudflared.env` (root, 0600) ab
    und eine systemd-Ergänzung für `cloudflared.service`, die es von dort
    liest. **Autostart bleibt aus**; pdh-ha startet den Dienst nur auf dem
    aktiven Server. Ein laufender Tunnel wird nach Token-Änderung neu
    gestartet.
  - Ist `cloudflared` nicht installiert, installiert `pdh-ha-join.sh` das
    Paket (Paketquelle von Cloudflare). Fehlt es trotzdem → Hinweis in der
    Karte, kein Alarm.
- Prüfungen in der Karte „Zugangsweg“:
  - je Server: eingerichtet, Prüfsumme des Tokens gleich, läuft/läuft nicht;
  - Fehler, wenn der Tunnel auf dem nicht aktiven Server läuft;
  - Warnung, wenn das Ziel (`PDH_CLOUDFLARE_ORIGIN`) nicht `localhost`/
    `127.0.0.1` ist – eine feste Server-Adresse zeigt nach dem Wechsel ins
    Leere;
  - Hinweis bis Teil 6: pdhadmin-Seiten (n8n, pgAdmin, code-server) laufen nach
    dem Wechsel erst, wenn ihre Daten gespiegelt werden.
- Erwartete Unterbrechung beim Wechsel: 5–15 s Neuverbindung bei Cloudflare;
  Sitzungen bleiben (liegen in der Datenbank).

## 4. Skripte beim Rollenwechsel

- Ablage: `/etc/pdh/ha-hooks/` (root, nur root schreibbar). Ausgeführt werden
  alle ausführbaren regulären Dateien darin in alphabetischer Reihenfolge
  (z. B. `10-dns`). Dateien, die nicht root gehören oder für andere schreibbar
  sind, werden übersprungen (Ereignis „Skript … unsicher – übersprungen“).
- **Nicht im PDH bearbeitbar** – ein im Browser eingegebenes Skript, das als
  root läuft, wäre ein Einfallstor. Das PDH zeigt nur Liste, Gleichheit und
  letzten Lauf und schaltet `hooks_on` ein/aus.
- Gleichheit: pdh-ha bildet eine Prüfsumme über Namen und Inhalte und schickt
  sie im Heartbeat mit (neues, optionales Feld – ältere Versionen ignorieren
  es). Verschieden → Warnung in der Karte. Ab Teil 6 spiegelt rsync den Ordner.
- Auslöser: jeder Rollenwechsel des eigenen Knotens zu `aktiv`, `passiv` oder
  `abgeschaltet` – auf dem aktiv werdenden Server nach dem Übernehmen der
  Adresse. Im Beobachtungsmodus bei automatischen Entscheidungen nur
  protokolliert. Nicht beim Zeugen.
- Umgebung:

  | Variable | Inhalt |
  |---|---|
  | `PDH_HA_ROLLE` | `aktiv` · `passiv` · `abgeschaltet` |
  | `PDH_HA_AMTSZEIT` | Amtszeit |
  | `PDH_HA_KNOTEN` | eigene Kennung |
  | `PDH_HA_EIGENE_ADRESSE` | erste eigene Adresse |
  | `PDH_HA_PARTNER_ADRESSE` | erste Adresse des Partners |
  | `PDH_HA_VIP` | mitwandernde Adresse ohne Präfix, leer wenn keine |

- Lauf im Hintergrund, blockiert nie die Umschaltung; je Skript Abbruch nach
  30 s; Ende mit Fehler oder Zeitüberschreitung → Alarm mit den letzten Zeilen
  der Ausgabe (höchstens 2 KB) im Ereignisprotokoll. Ein neuer Rollenwechsel
  während eines Laufs wartet, bis der vorige fertig ist (Reihenfolge bleibt
  erhalten).
- `sudo pdh-ha hook run` führt die Skripte mit der aktuellen Rolle aus und
  zeigt die Ausgabe (Testen, Nachholen).

## 5. Oberfläche und Status

- Ausfallschutz-Reiter, neue Karte **„Zugangsweg“** mit drei Zeilen, je Server:
  1. Cloudflare-Tunnel (3),
  2. Mitwandernde Adresse: Adresse, wer sie hat, letzter Test, „Testen“,
     Schalter, Eingabefeld (nur Admin mit Ausfallschutz-Recht),
  3. Skripte beim Rollenwechsel: Namen, gleich auf beiden, letzter Lauf mit
     Ergebnis, Schalter.
- Speichern läuft über die vorhandenen Routen unter
  `/admin/server-config/ha/` (am Spiegel bereits erlaubt).
- `pdh-ha status` (Text und `--json`): Zeilen „Adresse 10.10.0.202: hier“ bzw.
  „bei pdh-server“, „Tunnel: läuft hier / aus“, „Skripte: 2, gleich, letzter
  Lauf ok“.
- Spiegel-Hinweis aus Teil 4 („Bitte den aktiven Server verwenden“): Link auf
  die mitwandernde Adresse, sobald `vip_on`; sonst wie bisher.
- Design: beide Stile (Fiori/Fluent), hell/dunkel, nur Farbvariablen.

## 6. Umstellung einer bisherigen Adresse (z. B. .202)

Netzwerkänderungen macht kein Knopf im Browser, sondern
`sudo bash scripts/pdh-ha-adresse.sh` in Schritten; jeder Schritt einzeln
startbar und zurücknehmbar, Änderungen an netplan immer mit `netplan try`
(automatische Rücknahme nach 120 s ohne Bestätigung):

1. **Neue eigene Adresse dazu** (z. B. .203) als zusätzliche feste Adresse;
   .202 bleibt, nichts wird unterbrochen.
2. **Verbund umstellen:** eigene Adressen des Knotens (`node.json` `addrs`,
   Verbund-Konfiguration) auf .203; Heartbeat und Replikation laufen danach
   über .203. Die mitwandernde Adresse darf nie als Verbindungsweg eingetragen
   sein (wird beim Speichern abgelehnt).
3. **Test** (2.6) mit freier Testadresse.
4. **Übergabe:** .202 aus der festen Netzwerkkonfiguration entfernen,
   gleichzeitig `vip=.202`, `vip_on` setzen. Die Adresse bleibt dabei auf
   demselben Server (pdh-ha setzt sie im selben Takt) – für Benutzer keine
   Unterbrechung; eine SSH-Sitzung über .202 kann abreißen, daher fordert das
   Skript die Anmeldung über .203.

Prüfliste vor Schritt 4 (im Skript und im Handbuch): alles, was .202 als
*genau diesen Server* meint, muss auf .203: SSH-Zugänge, Überwachung,
Firewall-Freigaben nur für .202, **die Netzlaufwerk-Freigabe `pdhnas` auf der
Produktion** (die Testserver sichern dorthin – ein Sicherungsziel darf nicht
mitwandern).

Reihenfolge: zuerst Testverbund .14/.17 mit der freien Testadresse
**10.10.0.15** (vom Nutzer genannt, außerhalb des DHCP-Bereichs) als
mitwandernder Adresse; Produktion erst, wenn dort ein
zweiter Server existiert – bis dahin ändert sich an .202 nichts.

## 7. Fehlerfälle

| Fall | Verhalten |
|---|---|
| Adresse antwortet beim Übernehmen noch | nicht übernehmen, Alarm (entprellt), im Takt neu prüfen; DB-Übernahme läuft |
| Abgeben scheitert | Alarm; Partner übernimmt erst nach freier Duplikat-Prüfung |
| Netzwerkdienst entfernt die Adresse | im nächsten Takt neu setzen, ARP melden |
| Karte nicht eindeutig / Adresse in fremdem Netz | Einschalten abgelehnt mit Meldung |
| Kein erfolgreicher Test | Einschalten nicht möglich |
| Skript hängt/scheitert/unsicher | Abbruch 30 s bzw. übersprungen, Alarm; Umschaltung unberührt |
| Skripte verschieden | Warnung in der Karte |
| Token geändert | neu einrichten, laufenden Tunnel neu starten; Fehler → Alarm |
| Tunnel läuft auf nicht aktivem Server | stoppen (wie bisher), Fehler in der Karte |
| `arping`/`ip` fehlt | Einschalten abgelehnt, Hinweis auf `pdh-ha-join.sh --update` |
| Zeuge | nie Adresse, Tunnel oder Skript |

## 8. Tests

- **Go-Tests** (`cmd/pdh-ha`, vorgetäuschter `runner`): Übernehmen/Abgeben,
  Duplikat blockiert, Abgleich im Takt (inkl. Neustart, entfernte Adresse,
  Beobachtungsmodus), Kartenwahl, Speicher-Prüfungen, Test-Ablauf,
  Skript-Lauf (Variablen, Reihenfolge, Zeitbegrenzung, Fehler, unsichere
  Datei), Prüfsumme und Heartbeat-Feld, Tunnel-Einrichtung bei geändertem
  Token, `vip_on` aus = keine Wirkung.
- **PDH** (`internal/web`): Karte, Speichern/Prüfen, Token-Weitergabe,
  Spiegel-Link auf die Adresse, Hilfe-Tests.
- **HA-Labor (Docker)**: Knoten mit `NET_ADMIN`, `arping` im Abbild, dritter
  Container ruft die mitwandernde Adresse ab. Szenarien: Tausch → Adresse
  wandert und ist sofort erreichbar; Ausfall → Übernahme mit Adresse;
  Selbst-Abschaltung gibt ab; Duplikat blockiert; Adresse entfernt → kommt
  zurück; Neustart des aktiven holt sie nicht vorzeitig; Skript läuft auf
  beiden Servern mit richtiger Rolle; Test 2.6 grün und mit blockiertem ARP rot.
- **Abnahme** Testverbund .14/.17 + Zeuge mit freier Testadresse: Tausch hin
  und zurück, Ausfall, Neustart, Windows-PC ruft während des Wechsels ab und
  hängt sich nach wenigen Sekunden wieder an; Tunnel auf .17 automatisch
  eingerichtet.

## 9. Handbuch und Version

- Handbuch, Kapitel Ausfallschutz, neuer Abschnitt „Zugangsweg“:
  „So richtest du die mitwandernde Adresse ein“, „So stellst du die bisherige
  Adresse um“, Skript-Beispiele (Windows-DNS per SSH/PowerShell
  `Set-DnsServerResourceRecord`, `nsupdate`, Pi-hole, Cloudflare-DNS per
  `curl`), typische Blocker (Proxmox-IP-Filter, Switch-ARP-Prüfung, Cloud).
- Skizze des Ausfallschutz-Reiters (`help_figures.gohtml`) um die Karte
  ergänzen.
- Version **0.78.0** (neue Funktionen), Einträge im Änderungsprotokoll.
