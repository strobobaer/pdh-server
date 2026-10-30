# Update im Verbund und Wartungsfenster

Stand: 2026-10-30 · Status: zur Prüfung ·
Gesamt-Architektur: `2026-10-24-ausfallschutz-design.md` (Abschnitt
„Rollende Updates“) · ersetzt den geplanten Teil 8 „Geplanter Rollentausch“ ·
Vorgänger: `2026-10-28-ausfallschutz-zugangsweg-design.md`

## 1. Ziel und Umfang

Heute aktualisiert „Update installieren“ (Core-Einstellungen) nur den Server,
an dem man klickt. Im Verbund bleiben Spiegel und Zeuge zurück und müssen von
Hand nachgezogen werden (`pdh-ha-join.sh --update`, `docker compose up -d
--build`). Künftig soll **ein Auslöser alle Rechner des Verbunds auf denselben
Stand bringen** – per Knopf oder automatisch in einem Wartungsfenster – und bei
jedem Fehler **immer auf den vorherigen Stand zurücksetzen**, ohne Datenverlust.

Entscheidungen des Nutzers:

- **Feste Einstellung** der Update-Art im Verbund: „ohne Unterbrechung“ (mit
  Rollentausch) oder „alle nacheinander“ (kurze Unterbrechung). Der Knopf folgt
  der Einstellung.
- **Zeitplan:** Wartungsfenster nach der täglichen Grundsicherung, wählbar vor
  oder nach dem nächsten Dateiabgleich; täglich oder wöchentlich.
- **Teil 8 geht hier auf:** Das Wartungsfenster führt je nach Einstellung
  „Update einspielen“ und/oder „Rollentausch“ aus.
- **Nicht grün:** Zeitplan bricht ab und meldet; der Knopf fragt nach
  („Trotzdem mit kurzer Unterbrechung?“).
- **Immer zurücksetzen**, auch wenn schon Datenbank-Änderungen liefen.
- **Wartungsbildschirm** mit Fortschrittsbalken in der kritischen Phase; jede
  Eingabe gesperrt, alle Benutzer abgemeldet. Vorher Hinweis-Band mit
  Countdown. Administratoren (mit Recht) sehen denselben Bildschirm und dürfen
  nur „Abbrechen und zurücksetzen“.
- **Höchstdauer der Sperre** 30 min (einstellbar), danach automatisch
  zurücksetzen und freigeben.
- **Zeuge aktualisiert sich selbst** (Helfer-Container mit Docker-Socket),
  Hinweis „Zeuge veraltet“ als Sicherheitsnetz.
- **Steuerung:** pdh-ha auf dem aktiven Server dirigiert, der Update-Agent
  jedes Rechners führt aus (Weg 1).

Nicht im Umfang: der „Neu in dieser Version“-Rundgang nach der Anmeldung
(eigener Teil, danach), fertige Images aus einer GitHub-Registry, Rückkehr zu
einer älteren Version auf Wunsch (Downgrade) außerhalb der Rückwege hier.

## 2. Begriffe

- **A** – zu Beginn aktiver Server, **B** – zu Beginn passiver Server (Spiegel).
- **Ziel X** – Commit, auf den alle Rechner gebracht werden; zu Beginn einmal
  festgelegt (neuester Stand des Update-Zweigs, Standard `main`).
- **Alt** – Commit, auf dem ein Rechner vor dem Update stand.
- **Auftrag** – der laufende Update-Vorgang mit Phase, Ziel, Alt-Ständen und
  Zeitstempeln; liegt in pdh-ha auf beiden Servern.
- **Sperre** – kritische Phase, in der nur der Wartungsbildschirm erreichbar
  ist und niemand schreibt.
- **Eingefrorene Rückfallkopie** – Datenbank des Servers, der die neue Version
  noch nicht ausführt; bleibt während der Sperre unverändert.
- **Fehlerhafte Version** – Ziel, das an einer Prüfung gescheitert ist; wird im
  Zeitplan übersprungen, bis eine neuere Version erscheint oder ein
  Administrator sie wieder zulässt.

## 3. Architektur

```
 PDH (aktiv) ──Knopf/Einstellungen──► pdh-ha (aktiv) = Dirigent
      ▲  Wartungssperre, Bildschirm         │  Auftrag, Zustandsmaschine
      │  liest Auftrag über api.sock        │  Verbund-Nachrichten (mTLS, 7480)
      │                                     ▼
 Update-Agent (aktiv) ◄─ausführen─  pdh-ha (passiv) ─ausführen─► Update-Agent (passiv)
      │ Zeitplan (Wartungsfenster)
      └──Fenster fällig──► pdh-ha (aktiv)        Zeuge ◄── Helfer-Container (folgt den Servern)
```

### 3.1 Update-Agent (`cmd/update-agent`)

- **Ausführen mit Ziel:** `POST /v1/update` nimmt optional `{"commit":"<sha>"}`.
  `install_update.sh` bekommt den Schalter `--commit <sha>`: `fetch` des
  Update-Zweigs, Prüfung, dass `<sha>` darin enthalten ist, dann
  `merge --ff-only <sha>` statt `FETCH_HEAD`. Ohne Schalter Verhalten wie heute.
- **Zurückdrehen:** `install_update.sh --rollback <sha>` setzt den Checkout
  hart auf den Alt-Commit (`git reset --hard`), baut und startet neu. Nur auf
  Aufforderung des Auftrags; lokale Änderungen führen wie heute zum Abbruch.
- **Update-Zweig:** Einstellung `PDH_UPDATE_BRANCH` (Standard `main`), nur für
  Testsysteme; die Update-Prüfung in Core liest denselben Zweig.
- **Zeitplan:** Der Agent berechnet das nächste Wartungsfenster aus den
  Einstellungen (Abschnitt 4) und dem eigenen DR-Zustand (Grundsicherung,
  Dateiabgleich). Er handelt nur, wenn er auf dem aktiven Server läuft
  (vorhandenes `drHASelf`) bzw. auf einem Einzelserver.
  - Einzelserver: Agent führt das Update selbst aus (Abschnitt 5.3).
  - Verbund: Agent ruft `POST /v1/update/start` an pdh-ha (Socket) mit dem
    Ziel und „Quelle: Zeitplan“.
- Fehlerhafte Versionen und Ergebnis des letzten Fensters stehen im
  Agenten-Zustand (Einzelserver) bzw. im Auftrag von pdh-ha (Verbund).

### 3.2 pdh-ha (`internal/ha`, `cmd/pdh-ha`)

- **Zustandsmaschine `internal/ha/update.go`:** reine Funktion
  `UpdateStep(job, beobachtung) → (job', aktionen)` wie `Step` heute; alle
  Übergänge, Fristen und Rückwege dort, damit Tabellen- und Simulator-Tests
  ohne Netz möglich sind.
- **Auftrag** wird im vorhandenen Zustandsspeicher (`store.go`) abgelegt und
  per neuer Verbund-Nachricht `update_job` an den Partner gespiegelt. Wer aktiv
  ist, dirigiert; nach einem Rollentausch oder Neustart setzt der dann aktive
  Server den Auftrag fort.
- **Neue Verbund-Nachrichten** (`messages.go`): `update_job` (Auftrag
  spiegeln), `update_node` (Partner: „spiele X ein“ / „drehe auf Alt zurück“),
  `update_ack` (Ergebnis des Partners). Der Partner ruft daraufhin seinen
  lokalen Agenten (`/v1/update` bzw. Rückdrehen) mit dem Token aus
  `/opt/pdh-server/.env` auf.
- **Heartbeat** trägt zusätzlich den Commit (heute nur Version), damit „B ist
  auf X“ eindeutig prüfbar ist.
- **API** (`api.go`): `POST /v1/update/start {commit, quelle, art}`,
  `POST /v1/update/abort {von}`, `GET /v1/update` (Auftrag, Phase, Fortschritt
  in Prozent, Schritttext, Restzeit, Sperre an/aus, Ergebnis).
- **CLI:** `pdh-ha update status`, `pdh-ha update abort`.
- **Wartungsfenster-Einstellungen** werden Teil der Verbund-Einstellungen
  (`Settings`), änderbar wie die Umschaltzeiten.
- **Rollentausch im Fenster** (früher Teil 8) nutzt denselben Auftrag ohne
  Update-Schritte: Prüfung grün → Countdown → Sperre → `swap` → Prüfung →
  Freigabe.

### 3.3 PDH (`internal/web`, `web/templates`)

- Kasten „Wartungsfenster“ im Ausfallschutz-Reiter, Knopf in Core,
  Fortschritt im Ausfallschutz-Reiter (Abschnitt 10).
- **Wartungssperre:** neue Middleware vor allen Routen. Sie liest den Auftrag
  über `haDo` (`GET /v1/update`, 1 s zwischengespeichert) bzw. beim
  Einzelserver über den Agenten. Ist die Sperre an, gilt Abschnitt 8.
- **Start im gesperrten Zustand:** pdh fragt beim Start den Auftrag ab, bevor
  es Anfragen annimmt. Läuft eine Sperre, startet es gesperrt (wichtig für B
  nach dem Tausch und den Einzelserver nach dem Neustart).
- Countdown-Band über den vorhandenen Weg für Live-Hinweise an alle
  angemeldeten Benutzer.

### 3.4 Zeuge (`deploy/ha-witness`)

Siehe Abschnitt 9.

## 4. Einstellungen und Wartungsfenster

Ort: Server-Einstellungen → Ausfallschutz → Kasten „Wartungsfenster“ (auch
auf dem Einzelserver). Speicherung: im Verbund in den Verbund-Einstellungen
von pdh-ha, auf dem Einzelserver im Update-Agenten.

| Einstellung | Auswahl | Standard |
|---|---|---|
| Wartungsfenster | aus / täglich / wöchentlich (Wochentag) | aus |
| Beginn | nach der Grundsicherung: vor / nach dem nächsten Dateiabgleich | nach dem Abgleich |
| Uhrzeit (nur ohne Ausfallschutz-Sicherung) | HH:MM | 02:00 |
| Spätestens beginnen bis | HH:MM | 05:00 |
| Neue Version einspielen | ja / nein | ja |
| Update-Art im Verbund | ohne Unterbrechung / alle nacheinander | ohne Unterbrechung |
| Rollentausch im Fenster (nur Verbund) | aus / in jedem Fenster | aus |
| Countdown vor der Sperre | 1–15 min | 2 min |
| Höchstdauer der Sperre | 10–120 min | 30 min |

Regeln:

- Das Fenster ist fällig, wenn die Grundsicherung des Tages erfolgreich
  fertig ist und – je nach Einstellung – der nächste Dateiabgleich ansteht
  („vor“: das Fenster startet, der Abgleich wartet bis zum Ende des Fensters)
  bzw. erfolgreich beendet ist („nach“).
- Ist „spätestens bis“ überschritten, wird das Fenster ausgelassen und
  gemeldet („Wartungsfenster ausgelassen: Grundsicherung erst um 05:40
  fertig“).
- Eingespielt wird nur, wenn der Update-Zweig einen neueren Commit hat, der
  nicht als fehlerhaft gemerkt ist.
- Ein Update „ohne Unterbrechung“ enthält einen Rollentausch; im selben Fenster
  wird nicht zusätzlich getauscht. Die Rollen bleiben vertauscht.
- Der Knopf „Update installieren“ nutzt dieselbe Update-Art, aber nicht das
  Fenster.

## 5. Ablauf

### 5.1 Ohne Unterbrechung (Verbund)

| # | Schritt | Anwender | Rückweg bei Fehler |
|---|---|---|---|
| 1 | Prüfung „alles grün“ (Abschnitt 6); Ziel X und Alt-Stände festlegen, Auftrag anlegen und spiegeln | arbeiten auf A | Zeitplan: abbrechen, melden. Knopf: Rückfrage |
| 2 | `update_node X` an B; Agent auf B spielt X ein (pdh, pdh-ha, Agent); pdh auf B startet im Spiegelmodus („wartet auf das Update des aktiven Servers“) | arbeiten auf A | B auf Alt zurückdrehen |
| 3 | B prüfen, Frist 10 min: Heartbeat mit Commit X, Spiegel synchron, pdh auf B antwortet (`/health`) | arbeiten auf A | B auf Alt zurückdrehen, X fehlerhaft |
| 4 | Countdown-Band an alle | Hinweis-Band | – |
| 5 | **Sperre an:** Abmeldung, Wartungsbildschirm, Schnittstellen 503 | gesperrt | – |
| 6 | Tausch mit Einfrieren: A gibt ab, **stoppt seine Datenbank und bleibt stehen** (wird noch kein Standby von B); B wird aktiv, Spiegelung bis zur Freigabe asynchron (kein synchroner Standby); pdh auf B schreibt die Sicherung vor dem Update und spielt die Datenbank-Änderungen ein; Adresse und Tunnel wandern | gesperrt | Zurücksetzen (5.4) |
| 7 | B prüfen, Frist 10 min: pdh antwortet, Version X, keine offenen Datenbank-Änderungen | gesperrt | Zurücksetzen (5.4), X fehlerhaft |
| 8 | **Freigabe:** A wird Standby von B (vorhandene Wiedereingliederung), Spiegelung wieder synchron, Sperre aus | arbeiten auf B | – |
| 9 | `update_node X` an A (jetzt passiv); A spielt X ein, pdh im Spiegelmodus | arbeiten auf B | – |
| 10 | A prüfen wie Schritt 3 | arbeiten auf B | 2. Versuch; danach A neu von B aufsetzen (`replica-init` mit Ziel X); Alarm, falls auch das scheitert |
| 11 | Fertig: Meldung „Update auf 0.x.y abgeschlossen“, Auftrag abgeschlossen | arbeiten auf B | – |

### 5.2 Alle nacheinander (Verbund)

1. Prüfung grün, Auftrag anlegen.
2. A baut X im Hintergrund (neue Programmdateien liegen bereit, laufendes pdh
   unverändert); B bleibt auf Alt.
3. Countdown, **Sperre an**.
4. B wird eingefroren (Datenbank-Wiedergabe angehalten und Stand gemerkt).
5. A startet mit X neu, Sicherung vor dem Update, Datenbank-Änderungen.
6. A prüfen (Frist 10 min). Fehler → Zurücksetzen: B wird mit seinem
   eingefrorenen Stand aktiv (Wiedergabe bis zum gemerkten Stand, dann
   Hochstufen), A wird per Wiedereingliederung Standby von B und auf Alt
   gedreht.
7. **Freigabe**, B setzt die Wiedergabe fort.
8. B spielt X ein und wird geprüft wie 5.1 Schritt 9–10.

### 5.3 Einzelserver

1. Prüfung: letzte Grundsicherung ok (falls eingerichtet), ≥ 2 GB frei.
2. Bauen im Hintergrund, Countdown, **Sperre an**.
3. Sicherung vor dem Update (vorhanden, `BackupBeforeUpdate`), Neustart mit X,
   Datenbank-Änderungen.
4. Prüfen (Frist 10 min). Fehler → Datenbank aus der Sicherung vor dem Update
   zurückspielen, Alt-Commit, Neustart, prüfen; X fehlerhaft.
5. Freigabe.

### 5.4 Zurücksetzen (Verbund, „ohne Unterbrechung“)

1. B stoppt pdh und seine Datenbank, gibt Adresse und Tunnel ab.
2. A startet seine eingefrorene Datenbank wieder als Hauptserver (alter
   Stand, alte Version) und wird aktiv; Amtszeit wird erhöht.
3. B wird per Wiedereingliederung (pg_rewind, Teil 3) Standby von A und auf
   Alt zurückgedreht.
4. Prüfen: A antwortet, Spiegel synchron.
5. Freigabe, Meldung mit Grund.

Datenverlust ist ausgeschlossen, weil in der Sperre niemand schreibt
(Schnittstellen eingeschlossen). Scheitert das Zurücksetzen selbst, bleibt die
Sperre bis zur Höchstdauer, dann dringender Alarm mit den Befehlen aus dem
Handbuch.

## 6. Prüfung „alles grün“

Vor Schritt 1 (und für den Rollentausch im Fenster):

- Beide Server sehen sich; genau einer aktiv, einer passiv; gleiche Amtszeit.
- Spiegel synchron, Verzögerung unter `LagLimit`.
- Wartung aus, kein anderer Vorgang (`busy`), kein offener Auftrag.
- Beide Server auf demselben Commit (Ausnahme: Fortsetzen eines Auftrags).
- Letzte Grundsicherung und letzter Dateiabgleich auf A erfolgreich (falls
  eingerichtet).
- Mindestens 2 GB frei auf beiden Servern.
- Zeuge nicht erreichbar → nur Hinweis, kein Abbruch.

## 7. Fehlerfälle

| Fall | Verhalten |
|---|---|
| Nicht grün | Zeitplan: abbrechen, Meldung mit Grund, nächstes Fenster. Knopf: Rückfrage; bei „Ja“ Ablauf „alle nacheinander“ |
| B scheitert vor der Sperre | B auf Alt, X fehlerhaft, Meldung |
| Tausch scheitert | A bleibt bzw. wird wieder aktiv, B auf Alt; X **nicht** fehlerhaft |
| B scheitert nach dem Tausch | Zurücksetzen (5.4), X fehlerhaft – auch nach Datenbank-Änderungen |
| A scheitert nach der Freigabe | 2. Versuch, dann A neu von B aufsetzen, Meldung |
| Stromausfall/Neustart | Auftrag liegt auf beiden Servern; der aktive setzt fort: vor Schritt 6 → B auf Alt, abbrechen; Schritt 6–7 → Zurücksetzen; ab Schritt 8 → fortsetzen |
| Echter Ausfall oder Hand-Übernahme | Auftrag abbrechen, Meldung, kein weiterer automatischer Schritt |
| Sperre länger als Höchstdauer | Zurücksetzen, Freigabe, Alarm |
| Abbruch durch Administrator | Zurücksetzen, Freigabe; Protokoll mit Namen; X nicht fehlerhaft |

Meldungen laufen als pdh-ha-Ereignisse (`kind: alarm|info`) über den
vorhandenen Weg (`haWatch` → `notifyAdmins`); auf dem Einzelserver über den
Agenten-Zustand und dieselbe Benachrichtigung.

Zusätzlich wird eine bestehende Lücke geschlossen: Ein Spiegel, dessen
Datenbank **neuer** ist als sein Programm (eingespielte Änderungen, die das
Programm nicht kennt), zeigt „Programm zu alt für diese Datenbank“ und sperrt
sich (heute nur der umgekehrte Fall, `mirror_start.go`).

Während Server unterschiedliche Versionen haben, bleibt die automatische
Übernahme wie heute gesperrt („Versionen unterschiedlich“); vom Nutzer so
bestätigt.

## 8. Wartungssperre und Wartungsbildschirm

**Hinweis-Band** (Countdown): auf allen Seiten für angemeldete Benutzer
„Automatische Wartung in 2:00 min – bitte Eingaben speichern“, zählt herunter.

**Sperre an:**

- Alle Sitzungen werden ungültig, außer denen von Inhabern des Rechts
  „Automatische Wartung abbrechen“; offene Dauerverbindungen (Chat-SSE,
  Live-Anzeigen) werden geschlossen.
- Jede Route liefert den Wartungsbildschirm (HTML-Anfragen) bzw. HTTP 503 mit
  `Retry-After` (API, Webhooks, App, n8n, Copilot, HX-/fetch-Anfragen).
  Ausnahmen: der Fortschritt `GET /wartung/status`, die Administrator-Anmeldung
  und der Abbruch-Knopf.
- Abgeschickte Formulare werden nicht verarbeitet, sondern zeigen den
  Bildschirm.

**Bildschirm** (eigener Seitenkopf, setzt `data-style`, Fiori- und
Fluent-Variante, hell/dunkel, `--accent` aus dem Farbschema, übersetzt):

- „Automatische Wartung läuft“, Versionswechsel „0.77.7 → 0.77.8“.
- Fortschrittsbalken mit Schritten: Abmelden → Rollentausch → Datenbank
  aktualisieren → Prüfen → Freigabe; aktueller Schritt hervorgehoben, ein Satz
  zum Geschehen, voraussichtliche Restzeit.
- Beim Zurücksetzen: „Wartung wird rückgängig gemacht – die bisherige Version
  wird wiederhergestellt“.
- Ende: „Wartung beendet“, Weiterleitung zur Anmeldung.
- Antwortet der Server kurz nicht (Neustart, Tausch), bleibt der Bildschirm
  stehen („Server wird neu gestartet …“) und fragt alle 2 s erneut.

**Administratoren:** Inhaber des Rechts sehen denselben Bildschirm mit aktivem
Knopf „Abbrechen und zurücksetzen“ (Rückfrage). Nicht angemeldete Inhaber
melden sich über „Als Administrator anmelden“ unten auf dem Bildschirm an;
andere erhalten „Anmeldung während der Wartung nur für Administratoren“.
Andere Seiten bleiben auch für sie gesperrt. Notbremse ohne PDH:
`pdh-ha update abort` auf der Konsole des aktiven Servers.

## 9. Zeuge

**Grundsatz:** Der Zeuge folgt den Servern: Er zieht nach, wenn beide Server
denselben Commit X haben und kein Auftrag läuft.

**Docker-Zeuge:** `deploy/ha-witness/compose.yaml` erhält den Dienst
`updater` (kleines Image mit Docker-CLI und git, Docker-Socket eingebunden,
eigenes Volume mit Quelltext-Kopie; der Arbeitsordner des Nutzers wird nicht
angefasst). Alle 5 min:

1. `docker exec <zeuge> pdh-ha status --json` → Commits aller Rechner.
2. Beide Server auf X, Zeuge nicht, kein Auftrag → `git fetch`, `checkout X`,
   `docker compose -p pdh-ha-witness up -d --build witness` mit
   `PDH_HA_COMMIT=X` (Projektname und Volumes bleiben → Schlüssel bleiben).
3. Prüfen: Zeuge sieht innerhalb von 2 min beide Server; sonst alten Stand
   wieder bauen, Meldung im Protokoll des Helfers.

Abschalten: `PDH_HA_AUTOUPDATE=0`. Einmalig nötig: neue `compose.yaml` von
Hand einspielen.

**Zeuge als Programm:** pdh-ha auf dem Zeugen erkennt denselben Fall und
startet `pdh-ha-join.sh --update X` per `systemd-run`; die Sperre
`--update` + `--witness` im Skript entfällt.

**Sicherheitsnetz:** Ist der Zeuge 30 min nach Abschluss eines Auftrags noch
auf einem anderen Commit, zeigt der Ausfallschutz-Reiter „Zeuge veraltet
(0.77.7, Server 0.77.8)“ mit dem Befehl zum Kopieren. Ein veralteter Zeuge
sperrt weder Update noch Umschaltung.

## 10. Oberfläche

- **Ausfallschutz → Kasten „Wartungsfenster“:** Einstellungen (Abschnitt 4),
  nächstes Fenster („Do 31.10., nach der Grundsicherung, ca. 02:30“), Ergebnis
  des letzten Fensters, gemerkte fehlerhafte Versionen mit „Wieder zulassen“.
- **Core → Update installieren:** im Verbund „Update im Verbund
  installieren“, darunter Update-Art und Verweis aufs Wartungsfenster;
  Rückfrage bei nicht grün; am Spiegel gesperrt mit „wird vom aktiven Server
  gesteuert“.
- **Ausfallschutz-Reiter:** Fortschritt eines laufenden Auftrags, Hinweis
  „Zeuge veraltet“; der bisher angezeigte Befehl
  `pdh-ha-join.sh --update <Commit>` wird zum Notweg (eingeklappt).
- Alles in Fiori und Fluent, hell/dunkel, übersetzt.

## 11. Rechte

Per Migration (Kategorie Administration, `permCategoryOrder`):

- **„Wartungsfenster einstellen“** – an alle Rollen, die heute die
  Ausfallschutz-Einstellungen ändern dürfen (`canServerConfig`); umfasst
  „Wieder zulassen“.
- **„Automatische Wartung abbrechen“** – an alle Rollen, die heute „Update
  installieren“ dürfen, und an die Bereichsadministratoren.

Die Schlüssel folgen dem Schema der vorhandenen Rechte und werden im Plan nach
Durchsicht von `permissions`/`perm_gate.go` benannt. Die Socket-Schnittstellen
von pdh-ha bleiben root/Agent vorbehalten.

## 12. Handbuch, Änderungsprotokoll, Version

- `help.gohtml`, Kapitel Ausfallschutz und „Versionen & Updates“: neue
  „So geht's“-Abschnitte „Wartungsfenster einrichten“, „Was bei einem Update im
  Verbund passiert“, „Wartungsbildschirm und Abbrechen“, „Zeuge aktualisiert
  sich selbst“ (mit Hinweis zum Docker-Socket), Notweg auf der Konsole. Der
  Satz „Update bringt pdh-ha nur auf dem ersten Server auf den neuen Stand“
  wird ersetzt, die Hand-Anleitung wird Notweg.
- Wartungsbildschirm als eigene Seite → Kapitel mit `data-pages`, Eintrag in
  `helpChapterIDs`, falls neues Kapitel.
- `help_figures.gohtml`: Skizze des Ausfallschutz-Reiters anpassen, Skizze
  der Update-Schritte ergänzen.
- `2026-10-24-ausfallschutz-design.md`: Vermerk „Teil 8 aufgegangen in
  `2026-10-30-verbund-update-design.md`“.
- `VERSION` 0.77.8 und `CHANGELOG.md` (Neu, Geändert, Sicherheit für die
  neuen Rechte); liegt beim Zusammenführen schon eine höhere Version auf
  main, die nächste Korrekturversion.

## 13. Tests und Abnahme

**Go-Tests:**

- `internal/ha`: `UpdateStep` tabellarisch (jeder Schritt, jeder Rückweg,
  Fristen, Neustart, Abbruch, Höchstdauer), Simulator-Szenarien, Prüfung
  „alles grün“.
- Agent: Fensterberechnung (täglich/wöchentlich, vor/nach Abgleich,
  „spätestens bis“, Uhrzeit ohne Grundsicherung, fehlerhafte Version),
  `--commit`/`--rollback` in den Skript-Bausteinen, Update-Zweig.
- PDH: Sperr-Middleware (HTML → Bildschirm, API → 503, Ausnahmen), Abmeldung
  außer Rechte-Inhabern, Administrator-Anmeldung nur mit Recht, Abbruch-Knopf,
  Countdown-Band, Start im gesperrten Zustand, Spiegel mit zu neuer Datenbank,
  Rechte-Migration, Handbuch-Tests, `TestChangelogMatchesVersion`.
- Race-Test für pdh-ha und Agent.

**Labor** (`deploy/ha-lab`; Platzhalter-pdh mit Schaltern „startet nicht“
und „Datenbank-Änderung scheitert“):

1. Ohne Unterbrechung erfolgreich (alle auf X, Rollen vertauscht, Schreibtest
   ohne Verlust).
2. B scheitert vor dem Tausch → B auf Alt, X fehlerhaft.
3. B scheitert nach dem Tausch mit eingespielter Datenbank-Änderung → A mit
   alter Datenbank aktiv, B zurückgesetzt, Daten unverändert.
4. Abbruch in der Sperre (Konsole und Knopf).
5. A hart beendet vor dem Tausch bzw. nach dem Tausch → abbrechen bzw.
   zurücksetzen.
6. Nicht grün (Spiegel getrennt) → Zeitplan bricht ab.
7. Alle nacheinander: erfolgreich und mit Fehler.
8. Höchstdauer überschritten → zurücksetzen, freigeben.
9. Zeugen-Helfer zieht nach; Rückdrehen des Helfers.

**Browser:** Wartungsbildschirm, Countdown-Band, Administrator-Ansicht,
Kasten „Wartungsfenster“ – Fiori und Fluent, hell und dunkel, 320/390/768/
1024/1366 px.

**Abnahme im Echtnetz** (pdh 10.10.0.14, pdh-server 10.10.0.17, Zeuge
10.10.0.231; nie die Produktion 10.10.0.202):

- Testserver mit `PDH_UPDATE_BRANCH=test/verbund-update-probe`; dieser Zweig
  kommt nie nach main und enthält zwei Proben-Commits: harmlose Version mit
  Test-Datenbank-Änderung, absichtlich kaputte Version.
- Prüfen: Update per Knopf (Countdown, Sperre, Fortschritt, Freigabe), Update
  im Wartungsfenster, kaputte Version wird zurückgesetzt und gemerkt, Abbruch
  durch Administrator, Zeuge zieht nach.
- Danach Testserver zurück auf `main`. Merge nach main nur mit Freigabe des
  Nutzers. In der Produktion (Einzelserver) bleibt das Wartungsfenster aus,
  bis es eingeschaltet wird; der Knopf nutzt dort den neuen Ablauf für den
  Einzelserver (5.3) mit Countdown und Sperre.
