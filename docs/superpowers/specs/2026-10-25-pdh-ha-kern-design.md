# Ausfallschutz Teil 2 – pdh-ha-Kern (Beobachtungsmodus)

Stand: 2026-10-25 · Status: Entwurf zur Freigabe ·
Gesamt-Architektur: `2026-10-24-ausfallschutz-design.md` (Abschnitte 4–6, 12–15)

## 1. Ziel und Umfang

Teil 2 liefert den Dienst `pdh-ha`, der zwischen zwei Knoten und einem optionalen
Zeugen per Heartbeat die Rollen entscheidet – im **Beobachtungsmodus**: Heartbeat,
Amtszeit, Stimmen und Entscheidungen laufen echt, alle Aktionen (Postgres
hochstufen, abschalten, virtuelle IP …) werden nur als „würde jetzt: …“
protokolliert. Teile 3–7 hängen später echte Ausführungen an.

Im Umfang:

- Entscheidungskern `internal/ha` (rein, testbar), Dienst `cmd/pdh-ha` (Modi
  `node`, `witness`), mTLS-Heartbeat über mehrere Wege, Zeugenstimme.
- Einrichtung (Cluster-CA, Beitrittscode, `pdh-ha-join.sh`), Wechsel der
  Betriebsart Einzel ↔ Duo ↔ Trio, Knoten entfernen.
- Übersicht und Ereignisse im Reiter „Ausfallschutz“, Alarme.
- Docker-Testlabor mit Ausfall-Szenarien, Handbuch.

Nicht im Umfang: Replikation (Teil 3), Lesemodus (Teil 4), cloudflared/VIP/DNS
(Teil 5), rsync-Spiegelung (Teil 6), Samba (Teil 7). Der zweite Knoten bekommt
in Teil 2 **nur** `pdh-ha`, kein PDH und keine Datenbank.

Vorgaben des Nutzers: Zeuge-Ort offen → Programm für amd64 und arm64 plus
Container-Image; Standardwege LAN + WireGuard-VPN, Wegeliste frei; Beitritt
installiert nur `pdh-ha`; Produktion = systemd.

## 2. Bausteine

### 2.1 `internal/ha` – Entscheidungskern

Reines Go ohne Netz, Dateien oder Systemaufrufe.

```go
type Input struct {
    Now      time.Duration      // monotone Uhr des Knotens
    Self     NodeState          // Rolle, Amtszeit, Version, Gateway ok, Datenstand aktuell
    Peers    map[string]PeerObs // je Gegenstelle: zuletzt gesehen (monoton), Rolle, Amtszeit, Version, Wartung
    Vote     *VoteResult        // Antwort des Zeugen auf eine Stimmbitte (falls vorhanden)
    Command  *Command           // Handbefehl: swap, force_takeover, maintenance_on/off
    Settings Settings
    Persist  Persisted          // Amtszeit, letzte Umschaltung, Umschaltungen heute, Startzeit
}
type Output struct {
    Role      Role
    Term      uint64
    Persist   Persisted // muss vor Wirkung nach außen gespeichert werden
    Actions   []Action  // promote, demote, fence, unfence, vip_take, vip_release, request_vote, rejoin
    Events    []Event
}
func Step(in Input) Output
```

Rollen: `aktiv`, `passiv`, `abgeschaltet`, `wiedereingliederung`, `wartung`,
`zeuge`. Zusätzlich die Kennzeichnung **Einzel** (keine Gegenstelle eingetragen):
dann immer `aktiv`, keine Aktionen.

### 2.2 `cmd/pdh-ha` – Dienst

- systemd-Dienst `pdh-ha.service` (root), Modus aus `/etc/pdh/ha/node.json`.
- **Transport:** TCP-Port 7480, gegenseitiges TLS (TLS 1.3) mit der Cluster-CA.
  Je Weg und Gegenstelle eine dauerhafte Verbindung (der Knoten mit der
  kleineren Kennung baut sie auf, Neuaufbau mit Backoff 1–5 s). Über jede
  Verbindung senden beide Seiten im Takt eine JSON-Zeile (Abschnitt 3).
  Stimmbitten und -antworten laufen als eigene Nachrichtentypen auf denselben
  Verbindungen.
- **Zeuge (`witness`):** nimmt Heartbeats an, beantwortet Stimmbitten, sendet
  selbst Heartbeats (Rolle `zeuge`). Entscheidet sonst nichts.
- **Ausführung:** Schnittstelle `Actor { Do(ctx, Action) error }`. Teil 2 liefert
  nur `observeActor`: schreibt `würde jetzt: <Aktion>` ins Ereignisprotokoll und
  meldet Erfolg.
- **Gateway-Prüfung:** ICMP-Ping im Takt an das Standard-Gateway und die
  zusätzlich eingetragenen Ziele; ok, wenn ein Ziel in den letzten 3 Takten
  antwortete.
- **Ablage:**
  - `/etc/pdh/ha/` (0700, nie gespiegelt): `node.json` (Kennung, Modus, eigene
    Adressen), `cluster.json` (Knotenliste mit Wegen, Zeiten, Gateway-Ziele,
    Sperrliste), `ca.crt`, `ca.key` (nur auf dem einrichtenden Knoten),
    `node.crt`, `node.key`.
  - `/var/lib/pdh-ha/state.json`: Amtszeit, Rolle, letzte Umschaltung,
    Umschaltungen je Tag, abgegebene Zeugenstimme (Amtszeit + Kandidat).
  - `/var/lib/pdh-ha/events.jsonl`: Ereignisse, Rotation bei 5 MiB (3 Dateien).
- **API:** HTTP über Unix-Socket `/run/pdh-ha/api.sock` (0660, Gruppe `pdh`):
  `GET /v1/status`, `GET /v1/events?since=`, `POST /v1/setup` (Einzel → Duo:
  CA + eigenes Zertifikat), `POST /v1/join-code {role}`,
  `POST /v1/maintenance {on}`, `POST /v1/command {swap|force_takeover}`,
  `POST /v1/nodes/remove {id, gone}`, `PUT /v1/settings`.
  Der Beitritt selbst (`POST /v1/join`) läuft nicht über den Socket, sondern
  über Port 7480 mit Code (4.2).

### 2.3 PDH – `internal/web/ha.go`

- Reiter „Ausfallschutz“ (nur `canServerConfig`) erhält oben den Bereich
  **Betriebsart**; der bestehende Bereich Netzlaufwerk-Sicherung bleibt darunter.
- Übersicht: Knoten (Kennung, Rolle, Amtszeit, Version, Heartbeat je Weg mit
  Alter, Gateway, Uhrabweichung), Zeuge, Betriebsart, Hinweis
  **„Beobachtungsmodus – es wird nichts umgeschaltet“**, im Duo dauerhaft
  „eingeschränkter Ausfallschutz (kein Zeuge)“.
- Knöpfe: „Ausfallschutz einrichten“, „Beitrittscode (Knoten/Zeuge)“, „Automatik
  an/aus“ (Wartung), „Rollen tauschen“, „Übernahme erzwingen“ (Warnung +
  Bestätigung), „Knoten entfernen“, Zeiten.
- Ereignisliste (neueste zuerst), Alarme über die PDH-Benachrichtigungen an
  Admins.
- Ohne installiertes `pdh-ha` bzw. ohne Socket: Betriebsart „Einzel“, Knopf
  „Ausfallschutz einrichten“.

### 2.4 Installation

- Erster Knoten: „Ausfallschutz einrichten“ → Update-Agent baut `pdh-ha` aus
  dem vorhandenen Repo (`go build ./cmd/pdh-ha`), installiert Binary nach
  `/usr/local/bin/pdh-ha`, die Unit, `ufw`-Regel, startet den Dienst und ruft
  `POST /v1/setup`.
- Weitere Knoten und Zeuge: `scripts/pdh-ha-join.sh [--witness] <Code>` auf
  frischem Ubuntu (amd64/arm64): installiert Go/git, klont das Repo auf den
  Commit aus dem Code, baut `pdh-ha`, erzeugt Schlüssel + CSR, tritt bei.
- Zeuge als Container: `deploy/ha-witness/Dockerfile` + `compose.yaml`, lokal
  gebaut, Code per Umgebungsvariable `PDH_HA_JOIN`, Daten im Volume.
- Updates: Der Update-Agent baut `pdh-ha` beim PDH-Update auf dem ersten Knoten
  mit. Auf anderen Knoten aktualisiert `pdh-ha-join.sh --update <Commit>`.
  Unterschiedliche Versionen blockieren nur die automatische Übernahme.

## 3. Heartbeat

Nachricht je Takt (Standard 1 s) und Weg:

```json
{"t":"hb","id":"node1","role":"aktiv","term":4,"ver":"0.75.0","commit":"abc123",
 "data_current":true,"data_assumed":true,"gateway":true,"maint":false,
 "pg":"unknown","pdh":"ok","disk":"ok","seq":1234,"wall":"2026-10-25T10:00:00Z"}
```

- Weg **lebendig**: in den letzten 3 Takten eine Nachricht empfangen.
  Gegenstelle **gesehen**: mindestens ein Weg lebendig; „zuletzt gesehen“ =
  jüngster Empfang über alle Wege (monotone Uhr).
- `data_current`: in Teil 2 immer `true` mit `data_assumed: true` („angenommen
  aktuell, keine Replikation“) – Ereignisse vermerken das. Teil 3 liefert den
  echten Wert.
- `wall` dient nur der Anzeige; Abweichung > 5 s → Hinweis „Uhrzeit prüfen“.
- Standardwege: LAN-Adresse und WireGuard-Adresse je Knoten; frei erweiterbar.

## 4. Betriebsarten und Einrichtung

### 4.1 Einzel

Standard. `pdh-ha` nicht nötig; Teil 1 (Update-Agent) läuft unverändert. Ist
`pdh-ha` installiert, aber keine Gegenstelle eingetragen: fest `aktiv`, kein
Heartbeat, keine Entscheidungen.

### 4.2 Einzel → Duo

1. „Ausfallschutz einrichten“: Installation (2.4), Cluster-CA (ECDSA P-256,
   10 Jahre), Knotenzertifikat (2 Jahre, Erneuerung automatisch ab 30 Tagen
   Restlaufzeit), Rolle `aktiv`, Amtszeit 1, Cluster in `wartung`.
2. „Beitrittscode erzeugen“: 32 Byte zufällig, 30 min gültig, einmalig; Code
   (Base64-Text) enthält Adressen des Knotens, CA-Fingerabdruck (SHA-256),
   Rolle (Knoten/Zeuge), Commit.
3. `sudo bash pdh-ha-join.sh <Code>` auf dem neuen Server: Schlüssel lokal
   erzeugen, `POST /v1/join` an Port 7480 mit Code + CSR + eigenen Adressen;
   der neue Knoten prüft das Server-Zertifikat gegen den Fingerabdruck. Antwort:
   signiertes Zertifikat, `ca.crt`, `cluster.json`. Rolle `passiv`.
4. Beide Knoten übernehmen die neue Knotenliste; `ufw` öffnet 7480 nur für die
   eingetragenen Adressen (für den Beitritt solange der Code gilt für alle).
5. Cluster bleibt in `wartung`, bis „Automatik an“ gewählt wird (auch dann nur
   Beobachtung). Die Pflicht-Testumschaltung folgt mit Teil 3.

### 4.3 Weitere Wechsel

- **Duo → Trio:** Beitrittscode Rolle Zeuge; `pdh-ha-join.sh --witness <Code>`
  oder Container.
- **Trio → Duo:** „Knoten entfernen“ beim Zeugen → Seriennummer auf die
  Sperrliste, Verbindungen werden getrennt.
- **Duo → Einzel:** „Knoten entfernen“ beim passiven Knoten (erreichbar, oder
  Bestätigung „dauerhaft weg“). Verbleibender Knoten fest `aktiv`.
- Ein aktiver Knoten kann nicht entfernt werden (vorher Rollen tauschen).

### 4.4 Netzlaufwerk-Sicherung und Zeugen-Warnung

- Update-Agent fragt vor WAL-Verschieben, Grundsicherung und Dateiabgleich
  `GET /v1/status`; läuft nur, wenn `aktiv` oder kein `pdh-ha` vorhanden.
- Warnung bei erkennbar gemeinsamer Hardware zweier Stimmen: gleiche
  `/sys/class/dmi/id/product_uuid` bzw. bei VMs (`systemd-detect-virt`) gleiche
  Host-Kennung, soweit der Hypervisor sie preisgibt. Nur Hinweis, keine Sperre.

## 5. Entscheidungsregeln

Zeiten einstellbar, Standard in Klammern: Takt (1 s), Selbst-Abschaltung (10 s),
Übernahme Trio (30 s), Übernahme Duo (60 s), Wartezeit nach Umschaltung (15 min),
Umschaltungen je Tag (3). Abgelehnt: Übernahme < Selbst-Abschaltung + 2 × Takt
+ 5 s.

### 5.1 Trio

- **Aktiv** bleibt aktiv, solange Partner **oder** Zeuge gesehen; sonst nach der
  Selbst-Abschaltzeit `abgeschaltet` (Aktionen `fence`, `vip_release`).
- **Passiv** sendet `request_vote` (Amtszeit + 1), wenn der aktive Knoten für die
  Übernahmezeit auf allen Wegen fehlt, keine Wartung, gleiche Version, Wartezeit
  und Tageshöchstzahl erlauben und `data_current`.
- **Zeuge** gibt die Stimme nur, wenn er den aktiven Knoten selbst seit der
  Übernahmezeit nicht gesehen hat und für diese oder eine höhere Amtszeit noch
  nicht gestimmt hat. Stimme wird vor der Antwort gespeichert.
- Mit Stimme: neue Amtszeit speichern, Rolle `aktiv`, Aktionen `promote`,
  `vip_take`.
- `abgeschaltet` → `wiedereingliederung` → `passiv`, sobald ein Knoten mit
  höherer Amtszeit gesehen wird; sieht er wieder Partner oder Zeugen und keine
  höhere Amtszeit → `aktiv` (Aktion `unfence`).

### 5.2 Duo

- **Aktiv:** Partner **und** Gateway für die Selbst-Abschaltzeit nicht gesehen →
  `abgeschaltet`; Partner oder Gateway wieder da und keine höhere Amtszeit
  gesehen → `aktiv` (`unfence`).
- **Passiv:** Partner für die Übernahmezeit (Duo) nicht gesehen und eigenes
  Gateway ok, übrige Bedingungen wie Trio, ohne Stimme → neue Amtszeit, `aktiv`.
- Restrisiko (Direktwege weg, beide sehen Gateway) wird dauerhaft angezeigt.

### 5.3 Amtszeit, Rückkehr, Handbefehle

- Wer eine höhere Amtszeit sieht, übernimmt sie sofort. Ein bisher aktiver oder
  abgeschalteter Knoten → `wiedereingliederung` (Aktion `rejoin`, in Teil 2 nur
  protokolliert) → `passiv`.
- Gleiche Amtszeit und beide aktiv (darf nicht vorkommen): beide → `wartung`,
  Alarm.
- **Rollen tauschen:** aktiv gibt ab (`demote`), sendet `handover` mit Amtszeit
  + 1; passiv übernimmt. Auch in `wartung` erlaubt; zählt nicht zur
  Tageshöchstzahl.
- **Übernahme erzwingen:** passiv wird mit Amtszeit + 1 aktiv, auch ohne Stimme
  und mit `data_current=false`; Warnung + Bestätigung im PDH, Ereignis + Alarm.
- **Wartung** gilt clusterweit (Flag im Heartbeat, Stand der höchsten Amtszeit
  gewinnt); in Wartung keine automatischen Übernahmen und Abschaltungen.
- Kein automatisches Zurückschalten.

### 5.4 Start nach Neustart

- Vorher `passiv`: bleibt `passiv`.
- Vorher `aktiv`, Partner gesehen mit nicht höherer Amtszeit → `aktiv`.
- Vorher `aktiv`, kein Partner gesehen: `abgeschaltet`, bis die Übernahmezeit
  seit Start verstrichen ist; dann
  - Trio: `aktiv`, wenn der Zeuge gesehen wird und keine höhere Amtszeit meldet;
    sonst `abgeschaltet`.
  - Duo: `aktiv`, wenn das Gateway erreichbar ist (damit läuft ein Server nach
    Stromausfall des ganzen Standorts wieder an); sonst `abgeschaltet`.
- Einzel: sofort `aktiv`.

## 6. Fehlerbehandlung und Sicherheit

- Nur Zertifikate der Cluster-CA, Kennung und Rolle im Zertifikat (CN = Kennung,
  OU = `node`/`witness`), Sperrliste in `cluster.json`; abgelehnte Verbindungen
  werden protokolliert (gedrosselt).
- `ca.key` nur auf dem einrichtenden Knoten (0600). Neue Knoten erzeugen ihren
  Schlüssel selbst (nur die CSR verlässt den Knoten).
- Beitrittscode: einmalig, 30 min, höchstens 5 Fehlversuche je Minute und
  Quelladresse.
- `state.json` atomar (temp + fsync + rename + fsync Ordner) **vor** jeder
  Nachricht, die eine neue Amtszeit oder Stimme enthält.
- Entscheidungen nur nach der monotonen Uhr.
- Fehler oder Panic beim Ausführen einer Aktion → `wartung` + Alarm.
- Alarme (Zusatz „(Beobachtung)“ in Teil 2): Umschaltung, Selbst-Abschaltung,
  Weg > 1 min ausgefallen, Zeuge nicht erreichbar, Versionen unterschiedlich,
  Uhrabweichung, doppelt aktiv. Das PDH holt Ereignisse über den Socket und
  erzeugt die Benachrichtigungen (je Ereignisart höchstens eine je 15 min).

## 7. Testen

- **`internal/ha`** – Tabellentests mit simulierter Uhr: Trio (aktiv weg,
  Direktleitung weg, Zeuge weg, Zeuge + Partner weg, gleichzeitige Bitte),
  Duo (mit/ohne Gateway, beide sehen Gateway), Bremsen (Wartezeit,
  Tageshöchstzahl, Version, Wartung), Amtszeit/Rückkehr, Neustart (5.4),
  Handbefehle, Zeitprüfung. Simulation mit mehreren Knoten und zufälligen
  Wegeausfällen (Fuzz): nie zwei `aktiv` mit gleicher Amtszeit; eine Amtszeit
  wird nie zweimal vergeben.
- **`cmd/pdh-ha`** – Beitritt mit CSR und Fingerabdruck, fremdes/gesperrtes
  Zertifikat abgelehnt, abgelaufener/benutzter Code abgelehnt, atomares
  Speichern, Socket-API, Neuaufbau der Verbindung; echtes TLS auf `127.0.0.1`.
- **PDH** – Reiter rendert mit/ohne `pdh-ha` (Fake-Socket), Rechteprüfung,
  Zeitprüfung beim Speichern, Alarm-Drosselung; Handbuch-Tests.
- **Testlabor** `deploy/ha-lab/`: `compose.yaml` mit `node1`, `node2`,
  `witness`, Netze `lan` und `vpn`, Gateway-Container; `ha-lab.sh <Szenario>`
  mit `aktiv-weg`, `leitung-weg`, `zeuge-weg`, `hin-und-her`, `rueckkehr`,
  `neustart-aktiv`, `version-anders`, `alle`. Jedes Szenario prüft über die API
  Rollen und „höchstens ein aktiv“ und meldet bestanden/nicht bestanden.
  Nur Testumgebung.
- **Abschluss im echten Netz:** zweite VM als Knoten 2 neben `pdh-backup`,
  Zeuge (Container auf `pdh-server` oder Pi); Heartbeat über LAN und WireGuard
  im Reiter sichtbar; Weg trennen, VM stoppen → „würde übernehmen“ nach den
  eingestellten Zeiten.

## 8. Handbuch und Version

Neues Kapitel (Knoten/Zeuge hinzufügen und entfernen, Betriebsarten,
Beobachtungsmodus lesen, Zeugen-Regel), Skizze der Übersicht, `helpChapterIDs`,
Changelog unter „Neu“, Nebenversion erhöhen.

## 9. Offene Punkte

- Kopie von `ca.key` auf den zweiten Knoten (Beitritte auch nach Ausfall des
  ersten) – mit Teil 3.
- `pg`-Zustand im Heartbeat bleibt `unknown` bis Teil 3.
