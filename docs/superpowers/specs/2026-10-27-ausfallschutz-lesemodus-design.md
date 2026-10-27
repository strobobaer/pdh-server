# Ausfallschutz Teil 4 – Lesemodus des PDH auf dem Spiegel

Stand: 2026-10-27 · Status: Entwurf zur Freigabe ·
Gesamt-Architektur: `2026-10-24-ausfallschutz-design.md` (Abschnitte 7, 13, 15) ·
Vorgänger: `2026-10-25-ausfallschutz-db-cluster-design.md`

## 1. Ziel und Umfang

Seit Teil 3 ist der Dienst `pdh` auf dem passiven Server gestoppt. Teil 4 lässt
ihn dort im **Spiegelmodus** laufen: anmelden, ansehen, nichts ändern.

Zwecke, nach Vorrang:

1. **Admin-Steuerung am Spiegel** – Status des Verbunds sehen und „Übernahme
   erzwingen“ auslösen, ohne SSH, auch wenn der aktive Server weg ist.
2. **Notfall-Ansicht für alle** – fällt der aktive Server aus, können alle
   Benutzer am Spiegel nachsehen (Anlagen, Störungen, Dokumente).
3. *(nicht in Teil 4)* Berichte/Grafana auf den Spiegel auslagern – eigener,
   kleiner Schritt danach; der Grafana-Lesebenutzer kommt über die Replikation
   bereits mit.

Im Umfang:

- Modus-Erkennung im PDH und Wächter, der bei einem Wechsel neu startet.
- Startpfad ohne Schreibzugriffe (keine Sicherung vor dem Update, keine
  Migrationen, keine Hintergrundjobs).
- Zentrale Schreibsperre mit freundlicher Meldung.
- Anmelderegel C (siehe 5).
- Band „Spiegel – nur lesen“ mit Datenstand.
- pdh-ha: `pdh` und `cloudflared` getrennt steuern, `pdh` auch am passiven
  Server betreiben.
- Labortest mit echtem Standby (findet versteckte Schreibzugriffe), Handbuch,
  neue Nebenversion.

Nicht im Umfang: Zugangsweg (Teil 5), Datei-Spiegelung (Teil 6), Samba (Teil 7),
Lastverteilung von Berichten (siehe oben).

## 2. Modus-Erkennung

Direkt nach dem Verbinden mit der Datenbank fragt das PDH:

```sql
SELECT pg_is_in_recovery(), current_setting('transaction_read_only') = 'on'
```

- Eines von beiden wahr → **Spiegelmodus** (`readonly`). Das zweite deckt eine
  abgeschottete Datenbank ab (`default_transaction_read_only = on`), falls das
  PDH dort doch einmal läuft.
- Beides falsch → normaler Betrieb wie bisher.
- Einzelserver ohne Verbund: die Datenbank ist nie Standby → keine Änderung.

Der Modus wird einmal beim Start festgelegt und gilt für die ganze Laufzeit
des Prozesses (eine Variable im `Handler`, keine Umschaltung zur Laufzeit).

**Wächter:** alle 5 s dieselbe Abfrage. Weicht das Ergebnis vom Startmodus ab
(Spiegel → schreibbar nach einer Übernahme, schreibbar → nur lesen nach einer
Abschottung), schreibt das PDH einen Protokolleintrag und beendet sich mit
Exit-Code ≠ 0; systemd (`Restart=always`) startet es im richtigen Modus neu.
Ist die Datenbank nicht erreichbar, zählt das nicht als Wechsel (kurze
Aussetzer beim Rollentausch); nach 60 s ohne Datenbank beendet sich das PDH
ebenfalls.

## 3. Startpfad im Spiegelmodus

`cmd/server/main.go` überspringt im Spiegelmodus alles, was schreibt:

| Schritt | Spiegel |
|---|---|
| `BackupBeforeUpdate` | entfällt |
| `RunMigrations` | entfällt (Versionsprüfung, siehe unten) |
| `.env`-Werte in die Datenbank übernehmen (Teil von `loadServerSettings`) | nur lesen |
| `grafanadb.EnsureReader` | entfällt |
| Einrichtungsassistent (keine Benutzer) | entfällt – Spiegel ohne Benutzer startet nicht |
| Alle `Start*`-Hintergrundjobs (Sicherung, DR-Wächter, Update-Prüfung, Laufwerks-, Abfrage-, Export-, Import-Abrufe, Erinnerungen, KVP, MQTT, Nextcloud-Abgleich, Änderungs-, Einkaufs-, Push-Benachrichtigung, Schulungen) | entfallen |
| `haWatch` (Ausfallschutz-Meldungen) | läuft – liest nur pdh-ha |

**Versionsprüfung:** Höchste Migration der Datenbank (`schema_migrations`) mit
der höchsten mitgelieferten vergleichen. Fehlen Migrationen (der Spiegel hat
schon das neue Programm, der aktive Server noch nicht), startet das PDH
**eingeschränkt**: nur Admin-Anmeldung und Reiter Ausfallschutz, Band „Spiegel
wartet auf das Update des aktiven Servers“. Ist die Datenbank neuer als das
Programm, normaler Spiegelmodus (wie im Normalbetrieb nach einem Update des
aktiven Servers).

## 4. Schreibsperre

Ein Middleware-Schritt in `Routes()` direkt nach `authMiddleware`:

- Im Spiegelmodus nur `GET`, `HEAD`, `OPTIONS` durchlassen.
- **Ausnahmen:** `/login…`, `/logout`, `/lang` (setzt im Spiegelmodus nur das
  Cookie, kein `UPDATE users`), die Ausfallschutz-Befehle unter
  `/admin/server-config` (gehen an den pdh-ha-Socket, nicht an die Datenbank).
- **Abweisung:** HTTP 409 mit „Dieser Server ist der Spiegel – nur lesen.
  Änderungen bitte am aktiven Server.“ und Link auf den aktiven Server; bei
  htmx-Anfragen als Hinweis-Toast (wie bestehende Fehlermeldungen), sonst als
  eigene Seite.
- Gesperrt sind damit auch Easy-Mode-Meldungen (`/e/…`), Fertigmeldungen am
  Leitstand und Uploads.
- **Versteckte Schreibzugriffe bei GET:** `presenceTouch` im Spiegelmodus
  überspringen; weitere findet der Labortest (Abschnitt 9) und werden einzeln
  mit `if h.readonly` umgangen.
- **Netz:** Liefert die Datenbank `cannot execute … in a read-only transaction`
  (SQLSTATE `25006`), wird daraus dieselbe freundliche Meldung statt 500 –
  an der zentralen Stelle, an der Handler-Fehler zur Antwort werden.
- Knöpfe bleiben sichtbar; die Sperre sitzt am Server.

## 5. Anmeldung (Regel C)

Das PDH liest den Status über den pdh-ha-Socket (`/run/pdh-ha/api.sock`, wie
der Reiter Ausfallschutz; kurz zwischengespeichert, ca. 5 s).

| Lage | Wer darf hinein |
|---|---|
| aktiver Partner gesehen | nur Admins |
| aktiver Partner nicht gesehen | alle (nur lesen) |
| Status nicht lesbar | nur Admins |

- Nicht-Admins sehen bei der Anmeldung „Bitte den aktiven Server verwenden“
  mit Link; eine schon laufende Sitzung eines Nicht-Admins wird bei der
  nächsten Anfrage mit demselben Hinweis beendet.
- Admin = Berechtigung, die heute den Reiter Ausfallschutz öffnet.
- Sitzungen bleiben über eine Übernahme hinweg gültig (gleicher JWT-Schlüssel
  aus der übertragenen `.env`).

## 6. Anzeige

- Band oben auf jeder Seite, orange: „Spiegel – nur lesen · Datenstand vor 2 s ·
  Aktiver Server: 10.10.0.17 [öffnen]“. Datenstand aus
  `now() - pg_last_xact_replay_timestamp()`; die Adresse aus dem pdh-ha-Status.
- Aktiver Server nicht gesehen: Band rot, „Aktiver Server nicht erreichbar –
  Daten vom TT.MM. HH:MM“, für Admins mit Knopf zum Reiter Ausfallschutz.
- Eingeschränkter Modus (3): eigenes Band „Spiegel wartet auf das Update des
  aktiven Servers“.
- `/health` meldet zusätzlich `"mode": "spiegel"`.

## 7. pdh-ha

**Dienste getrennt:** `serviceCtl` schaltet `pdh` und `cloudflared` einzeln
(heute immer gemeinsam).

| Lage | pdh | cloudflared |
|---|---|---|
| aktiv, Datenbank schreibbar | an | an |
| passiv, Datenbank läuft als Standby | an (Spiegelmodus) | aus |
| passiv während Neukopie / Rückführung / Abschottung / Datenbank gestoppt | aus | aus |
| Wartung | unverändert | unverändert |
| Einzel / ohne `replica.pass` | wie bisher | wie bisher |

- **Kein Autostart beim Booten:** `pdh` bleibt im Verbund `disabled`. Beim
  Hochfahren ist PostgreSQL ca. 1 s schreibbar (offener Punkt e aus Teil 3);
  ein selbst startendes PDH könnte dann Migrationen und Jobs anwerfen. pdh-ha
  startet es beim ersten Abgleich nach dem Booten, sobald der Zustand klar ist.
- **Übernahme:** nach `promote` `systemctl restart pdh` (nicht auf den Wächter
  warten), dann Tunnel starten.
- **Abgabe / Abschottung:** `pdh` und Tunnel stoppen wie bisher; nach
  erfolgreicher Wiedereingliederung startet `pdh` im Spiegelmodus.
- **Abgleich (`reconcile`)** am passiven Knoten: Tunnel immer stoppen; `pdh`
  starten, wenn die Datenbank Standby ist, sonst stoppen.
- **`should-run-pdh`:** auch „ja“ am passiven Knoten mit laufendem Standby, damit
  `install_update.sh` das PDH nach einem Update wieder startet.

## 8. Fehlerfälle

| Fall | Verhalten |
|---|---|
| Datenbank am Spiegel nicht erreichbar | PDH beendet sich nach 60 s; pdh-ha stoppt es beim nächsten Abgleich |
| pdh-ha-Status nicht lesbar | nur Admins (5) |
| Programm neuer als Datenbank | eingeschränkter Modus (3) |
| Schreibzugriff, der durch die Sperre rutscht | Datenbank lehnt ab (Standby), freundliche Meldung statt 500 (4) |
| Übernahme während Anfragen laufen | Wächter/Neustart, Anfragen scheitern kurz; Sitzungen bleiben |

## 9. Tests

- **Unit:** Modus-Erkennung; Schreibsperre (erlaubte/gesperrte Wege, htmx);
  Anmeldung als Tabelle (Admin/Nicht-Admin × gesehen/nicht gesehen/unbekannt);
  Versionsprüfung; pdh-ha-Abgleichsmatrix und getrennte Dienste;
  `should-run-pdh`.
- **Labor (Docker):** Primary + Streaming-Standby, PDH am Standby:
  - startet ohne Migrationen und ohne Hintergrundjobs;
  - ein Crawler meldet sich als Admin an und folgt allen Links per GET –
    jede 5xx-Antwort und jedes `25006` im Protokoll lässt den Test scheitern;
  - Beispiel-POSTs bekommen 409 mit der Sperrmeldung;
  - Nicht-Admin abgewiesen, solange der Primary läuft; nach Stopp des
    Primary zugelassen;
  - `pg_promote` → PDH innerhalb von 10 s schreibbar.
- **Testserver (.14/.17):** Band, Anmelderegeln, Übernahme und Rückkehr als
  Spiegel, Update-Reihenfolge (Spiegel zuerst → eingeschränkter Modus).

## 10. Handbuch und Version

- Kapitel Ausfallschutz: neuer Abschnitt „So arbeitest du am Spiegel“
  (Anmeldung, was geht, was nicht, Band); Regel „pdh läuft nur am aktiven
  Server“ anpassen; Skizze in `help_figures.gohtml` um das Band ergänzen.
- `CHANGELOG.md`/`VERSION`: neue Nebenversion (0.77.0).
