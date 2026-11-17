# IT-Assets aus dem Netz – Paket 3: SNMP v3 als dritte Quelle (0.80.22)

Stand: 17.11.2026 · Entwurf von Claude, **vom Anwender am 17.11.2026 bestätigt**.
Baut auf Paket 1 UniFi (0.80.17, `2026-11-16-it-netzabgleich-design.md`) und
Paket 2 Omada (0.80.21) auf.

## Warum

UniFi und Omada decken nur Geräte ab, die an einem Controller hängen. Alles
andere im Netz – Switches anderer Hersteller, USV, Drucker, NAS, Firewalls,
Industrie-Switches in den Anlagen – kennt das PDH bisher nicht. Fast alle
diese Geräte sprechen SNMP. Mit SNMP v3 (angemeldet und verschlüsselt) kann
das PDH sie direkt fragen: bist du da, wie heißt du, welche Firmware, welche
Anschlüsse.

## Der Unterschied zu UniFi und Omada

Bei UniFi/Omada fragt das PDH **einen Controller**, der alle Geräte kennt.
Bei SNMP gibt es keinen Controller: Das PDH fragt **jedes Gerät einzeln**.
Daraus folgt:

- Es braucht eine **Geräteliste** (Adresse + Zugang), die der Anwender pflegt.
- „Quelle nicht erreichbar“ gibt es nicht mehr als Ganzes – jedes Gerät
  antwortet oder antwortet nicht. Der Schutz vor einem Alarm-Sturm muss
  deshalb neu gedacht werden (siehe „Zustand“).
- SNMP kennt keine Clients. **Vorschläge gibt es bei SNMP nicht.**

## Ziele

1. **Zugangsprofile:** Name, Benutzer, Anmelde-Verfahren + Kennwort,
   Verschlüsselungs-Verfahren + Kennwort. Ein Profil gilt für viele Geräte
   (üblich: ein SNMP-Benutzer für alle Switches).
2. **Geräteliste:** Adresse (IP oder Name), Port (161), Zugangsprofil.
   Knopf „Prüfen“ zeigt sofort, was das Gerät antwortet.
3. **Abgleich:** Jedes Gerät der Liste wird ein IT-Asset (Kategorie Netzwerk,
   vom Anwender änderbar) und laufend aktualisiert: Name (nur beim Anlegen),
   Modell, Seriennummer (nur wenn noch leer), MAC, IP, Firmware, Anschlüsse.
4. **Überwachung und Störmeldung** unverändert wie bei UniFi: Karenz,
   Alarm-Ticket mit Ursache-Pflicht, eigene Karenz je Kategorie/Asset (0.80.19).

## Nicht-Ziele

- Nichts an den Geräten ändern (nur lesen, kein SNMP SET).
- Kein SNMP v1/v2c (unverschlüsselt, „community“ im Klartext).
- Keine Traps/Informs empfangen (das PDH fragt, es lauscht nicht).
- Kein Netz-Suchlauf („alle Adressen durchprobieren“) – nur eingetragene Geräte.
- Keine Verbrauchswerte (Toner, USV-Akku, Temperatur), keine Bandbreiten,
  kein Verlauf. Kann später ein eigenes Paket werden.
- Kein Uplink über LLDP in diesem Paket (Feld bleibt leer).
- Kein Hinweis „Update verfügbar“ (SNMP kennt das nicht).

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)

- **Alarm-Sturm**, weil der PDH-Server selbst kurz kein Netz hat oder nach
  einer Umschaltung der zweite Server bei den Geräten nicht freigeschaltet ist.
- Ein falsches Kennwort im Profil erzeugt Ausfall-Tickets für alle Geräte.
- Ein Gerät, das schon aus UniFi/Omada bekannt ist, entsteht ein zweites Mal.
- Kennwörter stehen lesbar in der Datenbank, in Sicherungen oder im Protokoll.
- Ein hängendes Gerät bremst den Abgleich der anderen oder der anderen Quellen.
- Das PDH braucht SNMP, um zu laufen.

## Ansätze

| | Ansatz | Abwägung |
|---|---|---|
| **A (gewählt)** | Dritte `netsync.Source` „snmp“; Bibliothek `github.com/gosnmp/gosnmp` (v1.47.0, reines Go); Profile + Geräteliste in eigenen Tabellen | Passt in den vorhandenen Abgleich, Wächter, die Seite `/it/net` und die Asset-Ansicht. Keine Fremdsoftware auf dem Server. |
| B | `snmpget`/`snmpwalk` (net-snmp) als Programm aufrufen | Zusätzliche Installation auf beiden Servern und im Serverbau, Kennwörter in Befehlszeilen, Ausgabe zerlegen. |
| C | Über den Explorer/Daten-Client | Der Explorer ist für Prozessdaten; Gerätelisten passen dort nicht (wie in Paket 1 entschieden). |

## Sicherheit

- Nur **authPriv** (angemeldet **und** verschlüsselt).
  Anmeldung: SHA-1, SHA-224, SHA-256, SHA-384, SHA-512 (Vorschlag SHA-256).
  Verschlüsselung: AES-128, AES-192, AES-256 (Vorschlag AES-128, das kann jedes Gerät).
  **Nicht angeboten: MD5 und DES** – gelten als gebrochen. SHA-1 bleibt wählbar,
  weil viele ältere Switches nichts anderes können; die Oberfläche zeigt dann
  einen Hinweis „veraltet“.
- Kennwörter mindestens 8 Zeichen (Vorgabe des Protokolls), in der Datenbank
  nur verschlüsselt (`encryptSecret`/`pkiKey` wie das Omada-Secret), nie im
  Protokoll, nie zurück ins Formular.
- Adressen nur im Hausnetz (private Adressen, wie bei UniFi/Omada); UDP.
- Grenzen: höchstens 200 Geräte, 2 s Wartezeit, 2 Versuche je Anfrage,
  höchstens 8 Geräte gleichzeitig, Zeitlimit der Quelle wie bisher 75 s,
  Antwortgröße und Zahl der Anschlüsse begrenzt (512 je Gerät).

## Was gelesen wird (Form der Schnittstelle)

Bei **jedem Lauf** (billig, eine Anfrage): `sysUpTime` – Antwort = online.

Höchstens **alle 15 min** je Gerät (wie die UniFi-Details):

| Angabe | Herkunft |
|---|---|
| Name | `sysName` (SNMPv2-MIB) |
| Modell | `entPhysicalModelName` des Gehäuses (ENTITY-MIB), sonst gekürztes `sysDescr` |
| Seriennummer | `entPhysicalSerialNum` des Gehäuses |
| Firmware | `entPhysicalSoftwareRev`, sonst `entPhysicalFirmwareRev` |
| MAC | `dot1dBaseBridgeAddress` (BRIDGE-MIB), sonst kleinste `ifPhysAddress` eines Ethernet-Anschlusses |
| Anschlüsse | `ifTable`/`ifXTable` (IF-MIB): nur `ifType` = Ethernet (6); Nummer `ifIndex`, Name `ifName`, Zustand `ifOperStatus` (auf/ab), Geschwindigkeit `ifHighSpeed` |
| Art | Bridge-Adresse vorhanden und ≥ 4 Ethernet-Anschlüsse → `switch`, sonst `other` |

Fehlt eine Tabelle (viele Drucker/USV haben keine ENTITY-MIB), bleibt das
Feld leer – kein Fehler. PoE wird nicht gelesen.

Paket `internal/integrations/snmp`: `Client` mit `Ping(ctx)`, `Detail(ctx)`;
die Schnittstelle zu gosnmp ist schmal gehalten, damit Tests ohne Netz laufen.

## Zustand – Schutz vor dem Alarm-Sturm

| Was passiert | Zustand |
|---|---|
| Gerät antwortet | online |
| Keine Antwort (Zeit abgelaufen, 2 Versuche) | offline |
| Antwort „Anmeldung falsch“ / „unbekannter Benutzer“ / Zeitfenster-Fehler | **unbekannt** + Fehlertext am Gerät („Zugang abgelehnt – Profil prüfen“). Das Gerät lebt ja. |
| Name lässt sich nicht auflösen | unbekannt + Fehlertext |
| **Kein einziges** von mindestens 3 Geräten antwortet in einem Lauf | **alle unbekannt**, Fehler an der Quelle: „Kein Gerät antwortet – Netz des PDH-Servers oder Freischaltung prüfen“ |

Die letzte Zeile ersetzt den Schutz „Controller nicht erreichbar“ aus Paket 1.
Sie greift auch direkt nach einer Umschaltung im Verbund, wenn der zweite
Server bei den Geräten nicht als Abfrager erlaubt ist. Bei 1–2 Geräten gibt
es diesen Schutz nicht (dort wäre er nicht von einem echten Ausfall zu
unterscheiden) – die Karenz bleibt.

## Datenmodell (Migration 167)

- `it_snmp_profiles(id UUID PK, name TEXT UNIQUE, username, auth_proto, auth_secret_enc, priv_proto, priv_secret_enc, created_at, updated_at)`
- `it_snmp_targets(id UUID PK, host TEXT, port INT DEFAULT 161, profile_id → it_snmp_profiles ON DELETE RESTRICT, enabled BOOL, last_ok TIMESTAMPTZ, last_error TEXT, created_by, created_at, UNIQUE(lower(host), port))`
- IT-Asset: `net_source = 'snmp'`, `net_external_id` = ID des Eintrags der
  Geräteliste (stabil, auch wenn sich die Adresse ändert), `net_role = 'device'`.
- Einstellungen: `it.net.snmp.enabled`, `it.net.snmp.last_run`,
  `it.net.snmp.last_error`, `it.net.snmp.last_count` (wie die anderen Quellen).
- Sicherung/Wiederherstellung: beide Tabellen in `backup.go` aufnehmen
  (Reihenfolge Profile vor Geräten; Lehre aus 0.80.19 – Fremdschlüssel nicht doppelt).

**Doppelte vermeiden:** Zuordnung wie bisher über die MAC. Gehört die MAC
schon zu einem Asset einer **anderen** Quelle (z. B. UniFi-Switch, der auch per
SNMP abgefragt wird), entsteht kein zweites Asset; das Gerät steht in der
Liste mit dem Hinweis „schon über UniFi bekannt“ (Regel aus der
Omada-Schlussprüfung). Ein von Hand angelegtes Asset mit gleicher MAC wird verknüpft.

## Oberfläche

- **Server-Einstellungen → Reiter „SNMP“** (neu): Zugangsprofile anlegen,
  ändern, löschen. Kennwort-Felder leer = unverändert. Löschen nur, wenn kein
  Gerät das Profil nutzt.
- **`/it/net`:** dritte Karte „SNMP v3“ mit an/aus, letzter Lauf, Fehler,
  Zahl der Geräte. Darunter die **Geräteliste**: Adresse, Port, Profil,
  Zustand, letzter Fehler, Link zum Asset; „Gerät hinzufügen“, „Prüfen“
  (zeigt Name, Modell, Firmware, Zahl der Anschlüsse oder den Fehler in
  Klartext), an/aus je Gerät, „Entfernen“. Ohne Profil: Hinweis mit Link zum
  Reiter „SNMP“.
- **`/it/{id}`:** Karte „Netz“ wie bei UniFi mit Anschluss-Tabelle (Spalte PoE
  bleibt bei SNMP leer bzw. entfällt); Quelle „SNMP“.
- Beide Stile, hell/dunkel, 320–1366 px.

## Rechte (Migration 167)

- `system.snmp` – „SNMP-Zugangsprofile verwalten“ (Kategorie „System & Sicherheit“);
  bekommen alle, die heute `system.unifi` haben (Rollen und Einzelrechte,
  auch entzogene – wie bei `system.omada`).
- Geräteliste pflegen, prüfen, ein-/ausschalten: `it.netsync` (vorhanden).
- **Gerät aus der Liste entfernen: `it.admin`** (endgültiges Löschen nur durch
  den Bereichsadministrator; Regel in `gateRules`). Das Asset bleibt dabei
  bestehen und wird wie bei „Quelle trennen“ nicht mehr abgeglichen.
- Profil löschen: `system.snmp` reicht nicht – Bereichsadministrator des
  Bereichs System (so wie die übrigen Löschrouten dort).
- Ansehen am Asset: `it.view` wie bisher.

## Verbund (zwei Server)

Abgefragt wird nur vom aktiven Server. Die Anfragen kommen von der **eigenen
Adresse** des jeweils aktiven Servers, nicht von der gemeinsamen Adresse.
Geräte mit einer Liste erlaubter Abfrager müssen deshalb **beide Server**
eintragen. Das steht im Handbuch und als Hinweis auf der Seite; der
Sturm-Schutz oben fängt den Fall ab, falls es vergessen wurde.

## Prüfung ohne echte Geräte

- Automatische Tests gegen eine nachgebaute Antwort-Schicht (im Speicher).
- Zusätzlich im Labor ein echter SNMP-v3-Dienst als Docker-Container
  (`deploy/lab/snmp`, net-snmp `snmpd` mit einem v3-Benutzer) – damit werden
  Anmeldung, Verschlüsselung und das Lesen der Tabellen wirklich geprüft,
  auch der Fall „falsches Kennwort“.
- Abnahme an echten Geräten (UniFi-Switch mit eingeschaltetem SNMP v3,
  Drucker) macht der Anwender auf dem Testverbund bzw. später auf 202.

## Versionen, Handbuch

- 0.80.22; CHANGELOG `### Neu` (SNMP v3 als dritte Quelle) und `### Sicherheit`
  (Recht `system.snmp`, nur authPriv, kein MD5/DES).
- Handbuch: Abschnitt „Netz-Abgleich“ um SNMP erweitern (So geht's: Profil
  anlegen, Gerät hinzufügen, prüfen; Hinweis beide Server freischalten; was
  SNMP nicht kann), Kapitel Administration (Recht), Skizze in
  `help_figures.gohtml` um die dritte Karte ergänzen.

## Aufräumen im Zuge des Pakets

`itNetState`/`itNetSources` tragen heute je Quelle eigene Felder (UniFi,
Omada). Mit der dritten Quelle werden sie auf eine Liste umgestellt, damit
nicht ein dritter Satz Felder dazukommt. Verhalten bleibt gleich.

## Festlegungen nach Bau und Schlussprüfung

Was beim Bauen und in der unabhängigen Schlussprüfung anders oder genauer
festgelegt wurde als oben beschrieben. Bei Widerspruch gilt dieser Abschnitt.

- **Migration 168, Recht `system.snmp_admin`:** „SNMP: Zugangsprofile löschen
  (Bereichsadministrator)“. Nur damit lässt sich ein Profil endgültig löschen;
  das Recht allein genügt für den Reiter. Es geht an Rollen, die überall
  `system.snmp` **und** `system.certs_admin` haben (Einzelrechte entsprechend,
  Entzug bleibt Entzug).
- **Asset erst nach der ersten Antwort:** Ein Eintrag der Geräteliste wird erst
  ein IT-Asset, wenn das Gerät einmal seine Einzelheiten geliefert hat. Ohne
  MAC-Adresse ließe sich nicht erkennen, ob es schon über eine andere Quelle
  bekannt ist.
- **Doppelte Listeneinträge:** Steht dasselbe Gerät zweimal in der Liste (Name
  und IP-Adresse), wird nur ein Eintrag ein Asset. Vorrang hat der Eintrag, der
  schon ein Asset hat – dessen MAC zählt auch direkt nach einem Neustart –,
  sonst der ältere. Am anderen steht „doppelt in der Liste“.
- **Getrenntes Asset:** Ist das Asset eines Eintrags vom Abgleich getrennt
  („Quelle trennen“ am Asset, oder entfernt und neu aufgenommen), entsteht kein
  neues. Am Eintrag steht „Das IT-Asset ist vom Abgleich getrennt – am Asset
  ‚Wieder verbinden‘ wählen“ mit Link. Nach dem Neu-Aufnehmen bekommt das
  getrennte Asset die ID des neuen Eintrags (`net_external_id`), damit „Wieder
  verbinden“ genau diesen Eintrag weiterführt.
- **Offline erst nach erster Antwort an diesen Server:** „Keine Antwort“ heißt
  nur dann „offline“, wenn das Gerät diesem Server seit Beginn der laufenden
  Abfrage-Strecke mindestens einmal geantwortet hat (auch ablehnend). Vorher:
  „unbekannt“ mit dem Text „Hat diesem Server noch nie geantwortet – Adresse,
  Profil und Freischaltung für diesen Server prüfen“. Die Strecke lebt im
  Speicher des Programms und beginnt neu mit dem Programmstart sowie dann, wenn
  der letzte Lauf der Quelle auf diesem Server länger als 3 Intervalle
  (mindestens 5 min) zurückliegt – das erfasst Wartungssperre, ausgeschaltete
  Quelle und einen Rollenwechsel bei weiterlaufendem Programm. Ändert sich die
  Adresse eines Eintrags, muss das Gerät unter der neuen erst antworten. Ein
  laufender Alarm (`it_net_alarm_state`) bleibt bei „unbekannt“ bestehen.
  Das ersetzt die Aussage oben, der Sturm-Schutz fange die vergessene
  Freischaltung ab: Bei gemischter Freischaltung (5 von 20 antworten) greift er
  nicht, diese Regel schon. Der Sturm-Schutz „keines von ≥ 3 antwortet“ bleibt
  zusätzlich.
- **Meldung bei anhaltendem Totalausfall:** Hält „kein Gerät antwortet“ so lange
  an wie die allgemeine Karenz, bekommen die Verantwortlichen des Netz-Abgleichs
  (dieselben wie bei Alarm-Tickets) genau eine Nachricht: „SNMP: Seit <Dauer>
  antwortet kein Gerät. Entweder ist das Netz des PDH-Servers gestört oder alle
  SNMP-Geräte sind ausgefallen.“ Kein Ticket je Gerät; die nächste Nachricht
  erst, nachdem wieder ein Gerät geantwortet hat. Der Merker liegt im Speicher:
  Nach einem Neustart mitten im Ausfall kann die Nachricht einmal erneut kommen.
- **Kein Gerät wirklich gefragt:** Sind alle Profile unlesbar oder alle Namen
  nicht auflösbar, steht an der Quelle „Kein Gerät konnte gefragt werden –
  Zugangsprofile und Adressen prüfen“ statt des Sturm-Texts; es geht keine
  Meldung über einen Totalausfall hinaus.
- **Port abgewiesen = offline:** Weist das Gerät die Anfrage ab (SNMP-Dienst
  aus) oder meldet das Netz „Gerät nicht erreichbar“, zählt das wie „keine
  Antwort“. Fehler am PDH-Server selbst (kein Weg ins Netz, Adresse nicht
  verfügbar, nicht erlaubt, zu viele offene Dateien, kein Puffer) sind
  „unbekannt“, nie offline.
- **Zeitbudget:** Der Ping-Teil eines Laufs hat höchstens 30 s. Geräte, die
  nicht mehr gefragt wurden, behalten Zustand und „seit“ (kein Wechsel auf
  „unbekannt“, kein „nicht mehr gemeldet“); der nächste Lauf beginnt bei ihnen.
- **MAC ohne Bridge-Adresse:** nicht die kleinste `ifPhysAddress`, sondern die
  des ersten Ethernet-Anschlusses nach `ifIndex`, der „up“ ist; lokal
  verwaltete Adressen (Bit 0x02) sowie Null- und Rundruf-Adressen zählen nicht.
  Gibt es nur lokal verwaltete, dann die mit dem kleinsten `ifIndex`.
- **Entfernen je Datensatz:** „Entfernen“ prüft `it.admin`; hat der Eintrag ein
  Asset, zusätzlich für genau dieses Asset (Abteilung), wie `/it/{id}/net-detach`.
- **PoE-Spalte nur bei PoE:** Am IT-Asset erscheint die Spalte PoE der
  Anschluss-Tabelle nur, wenn das Gerät PoE meldet – bei SNMP also nie.
- **Kennwörter:** Das Handbuch empfiehlt lange, zufällige Kennwörter (mindestens
  16 Zeichen), weil das Profil an jede eingetragene Hausadresse geschickt wird.

## Entscheidungen von Claude (bitte bestätigen oder ändern)

1. **Profile getrennt von Geräten** (Reiter „SNMP“ mit `system.snmp`), Geräteliste auf `/it/net` mit `it.netsync`.
2. **Nur authPriv; kein MD5, kein DES;** SHA-1 erlaubt mit Hinweis „veraltet“.
3. **Sturm-Schutz:** antwortet keines von mindestens 3 Geräten → alle „unbekannt“, kein Ticket.
4. **Falsches Kennwort = „unbekannt“**, nie offline.
5. **Keine Vorschläge, kein Suchlauf, keine Traps, kein LLDP-Uplink, keine Verbrauchswerte** in diesem Paket.
6. **Seriennummer** wird aus SNMP nur gefüllt, wenn das Feld leer ist.
7. **Entfernen aus der Geräteliste nur `it.admin`.**
8. Prüfung im Labor mit einem `snmpd`-Container; echte Geräte durch den Anwender.
