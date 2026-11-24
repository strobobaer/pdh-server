# SDD ledger — plan: docs/superpowers/plans/2026-11-24-betriebssystem-updates-teil-a.md

Spec: docs/superpowers/specs/2026-11-24-betriebssystem-updates-design.md (bestätigt 24.11.2026 „Passt“). Worktree D:/pdh-os-update, Branch feature/os-update ab main a883ba02 (0.82.56). Bau durch Claude-Sonnet-Unteragenten (Anwender: „Claude baut, Codex prüft“ – Codex-Sandbox kann hier weder lesen noch schreiben), Schlussprüfung Claude Sonnet + Codex gpt-6.2-sol (Diff im Auftrag).

## Pre-flight
| Aufgaben | geteilt | Befund |
|---|---|---|
| T1→T2 | `scripts/pdh-os-update` | passt |
| T1→T3 | JSON von `status` ↔ `osScriptStatus` | Feldnamen gleich (name/old/new/security, script_version, checked_at, security_count, reboot_required, reboot_pkgs), passt |
| T3→T4/T5/T6 | `osExec`, `osStatus`, `lockAcquire`, Hooks `osRun`/`bootID`/`osCmd` | passt |
| T4→T5/T6 | `osSteps`, `osAdvance`, `jobRebootScript` | passt |
| T5→T6/T7 | `ha.UpdateOS`, `UpdateJob.OSStep`, `startOSJob`, `os-job.json` | passt |
| T3/T5→T7 | Agent-Endpunkte `/v1/os/status|check|config|job/start` | passt (Web nutzt `agentRaw`) |
Ruling: Wartungsfenster – ist das PDH-Update übersprungen, weil das Ziel als fehlerhaft gemerkt ist, gilt das wie „kein PDH-Update“: mit Haken und offenen Sicherheitsupdates startet der OS-Auftrag (Plan-Fakt 9 der Planerstellung sah das anders) — Entscheidung des Anwenders „mit Haken auch ohne PDH-Update“; Sicherheitsupdates sollen nicht an einer kaputten PDH-Version hängen — kostet: bei einer fehlerhaften Version läuft trotzdem ein Neustart im Fenster
Ruling: Speicherort des Hakens im Agenten (`os-state.json`) für Teil A — pdh-ha bleibt in Teil A unberührt; Teil B verlagert/erweitert — kostet: Umzug in Teil B
Ruling: `pdh` wird während apt gestoppt (Sperre, Stopp, Sicherung, apt) — Paket-Skripte von postgresql/docker starten Dienste selbst neu; Anwender sind in der Sperre ohnehin abgemeldet — kostet: Sperre etwas länger
Task 1: BASE e87e9aa3; Umsetzer gestartet (sonnet), Agent a8895f1f881424483, Bericht task-1-report.md
Task 1: Umsetzung fertig 5428dee1 (DONE; Skript LF, 100755, .gitattributes eol=lf; Protokollvariable PROTO statt LOG), Prüfer gestartet (sonnet)
Task 1: Prüfung (sonnet): Spezifikation ✅, Qualität freigegeben; 6 Kleinbefunde.
Task 1: Ruling: Befund 2 (apt list scheitert → status "ok":true mit leerer Liste) hochgestuft auf Important — verschluckter Fehler, Karte zeigte „aktuell“, Wartungsfenster sähe keine Sicherheitsupdates — kostet: eine kleine Korrekturrunde; Befund 1 (Testschalter bei root) und 5 (letzte Zeile ohne Zeilenumbruch) gehen mit, weil billig
Task 1: minor (deferred): PRETTY_NAME naiv entquotet; json_escape ohne Steuerzeichen; Tests für PDH_OS_TEST nicht gesetzt, Maskierung von system, NEUSTART bei check; TestOSScriptSyntax LookPath
Task 1: fix round 1 umgesetzt e53a2253 (status ok:false bei apt-Fehler, Testschalter nie als root, letzte Zeile), Nachprüfung gestartet
Task 1: fix round 1/5 (3 addressed, 0 open — status ok:false bei apt-Fehler, Testschalter nie als root, letzte Zeile; commits 5428dee..e53a225)
Task 1: minor (deferred): check-Zweig erkennt apt-list-Fehler nur über pipefail
Task 1: complete (commits e87e9aa..e53a225, review clean nach 1 Korrekturrunde)
Task 2: BASE e53a2253
Task 2: Umsetzer gestartet (sonnet), Agent a83f148405c9c7b6b, Bericht task-2-report.md
Task 2: Umsetzung fertig d467598b (DONE). Ruling: Fehlerfall im Test über eigenen Schalter PDH_TEST_OS_RC statt PDH_TEST_SD_RC=1 — SD_RC=1 lässt auch den späteren Agent-Neustart per systemd-run scheitern und verfälscht den Lauf — kostet nichts. Prüfer gestartet (sonnet)
Task 2: Prüfung (sonnet): Spezifikation ✅, Qualität freigegeben. minor (deferred): Warnung „siehe Ausgabe oben“ bei fehlender Quelle ohne Ausgabe
Task 2: complete (commits e53a225..d467598, review clean)
Task 3: BASE d467598b
Task 3: Umsetzer gestartet (sonnet), Agent a78cc1fcbe61ed7d4, Bericht task-3-report.md
Task 3: Umsetzung fertig 6d3b272e (DONE; Abweichungen: Testhilfe osScriptFile, job nur bei aktivem Auftrag, Skriptfehler 200 available:false bzw. 502 bei check, loadSingleJob nimmt Sperre wieder; Bedenken: ESPHome↔PDH-Update jetzt gegenseitig gesperrt, lockNet nur während Skriptlauf, Zeitlimit beendet nur systemd-run-Aufruf), Prüfer gestartet (sonnet)
Task 3: Prüfung (sonnet): Needs fixes – I1 wiederbetretbare Sperre (check neben install desselben Besitzers), I2 Zeitlimit gibt Sperre frei während die Einheit weiterläuft, I3 Netz-Bestätigungsfenster (Neustart würde automatische Rücknahme verlieren), I4 „not found“-Heuristik schluckt echte Check-Fehler
Task 3: Ruling: I3 wird Pflicht für Task 5 – OS-Start und jeder Neustart lehnen ab, solange netReadState() pending oder MaintOffPending meldet (Meldung „Eine Netz-Änderung wartet auf Bestätigung …“) — günstiger als lockNet bis zur Bestätigung zu halten — kostet: OS-Auftrag startet erst nach Bestätigung/Rücknahme
Task 3: Ruling: Minor „gleicher Einheitsname in derselben Sekunde → als veraltet gemeldet“ geht mit in die Korrekturrunde (gehört zu I4)
Task 3: fix round 1 umgesetzt b4d9d49e (lockTryExclusive für check, Sperre bis Einheit beendet, Klassifizierung veraltet nur Exit 2+Aufruf bzw. Skript fehlt, eindeutige Einheitsnamen; -race nicht möglich: kein gcc), Nachprüfung gestartet
Task 3: fix round 1/5 (4 addressed, 0 open — check exklusiv, Sperre bis Einheit endet, Klassifizierung veraltet, eindeutige Einheitsnamen; commits 6d3b272..b4d9d49)
Task 3: Ruling: Pflicht für Task 5 zusätzlich – startOSJob nimmt die Sperre exklusiv (lockTryExclusive), nicht wiedereintrittsfähig — sonst teilen sich zwei Starts die Sperre — kostet nichts
Task 3: minor (deferred): Hintergrund-Warten auf die Einheit ohne Obergrenze (endet mit der Einheit, fail-open bei systemctl-Fehler); osExitCode liest Code aus Text (nur Tests); WriteTimeout 60 s des Agenten kleiner als ein langer check
Task 3: complete (commits d467598..b4d9d49, review clean nach 1 Korrekturrunde)
Task 4: BASE b4d9d49e
Task 4: Ruling: osSteps bekommt Feld Unit (Name der install-Einheit, vor dem Start vergeben und gespeichert); Schritt install: läuft eine früher gestartete Einheit noch (osUnitActive) oder liefert osExec *osUnitRunning → warten (Schritt bleibt install, kein start-pdh, kein zweites install); ist sie beendet oder unbekannt → install neu (idempotent, dpkg --configure -a) — Folge aus Task-3-Korrektur, sonst startete pdh bzw. ein zweites apt während dpkg läuft — kostet: ein Feld und ein Wartezweig
Task 4: Umsetzer gestartet (sonnet), Agent a5fb39ff86b06a066, Bericht task-4-report.md
Task 4: Umsetzung fertig 9a597c58 (DONE; Zusatz: check/status-Fehler in Schritt "" → Failed ohne pdh-Stopp; status nach install unlesbar → Failed ohne Neustart; leere boot_id wartet bis osBootWait; fail() ersetzt frühere Msg), Prüfer gestartet (sonnet)
Task 4: Prüfung (sonnet): Needs fixes – I1 unbegrenztes Warten im Schritt install (pdh bliebe bei hängendem apt für immer gestoppt)
Task 4: Ruling: I1 – Obergrenze osInstallTimeout + 15 min ab StepAt (beim persist vor install gesetzt); danach Failed „Einspielen dauert zu lange – Einheit <unit> läuft noch“, Unit bleibt vermerkt, Step="start" (pdh startet) — PDH dauerhaft aus ist schlimmer als PDH neben einem laufenden apt — kostet: PDH läuft ggf. während apt noch Pakete einspielt
Task 4: Ruling: Pflicht für Task 5 – OS-Auftrag gibt lockOS erst frei, wenn eine vermerkte Einheit inaktiv ist (Muster osWaitUnit); beim Herunterfahren des Agenten gilt ein abgebrochener osExecUnit nicht als Fehler, sondern der Zustand bleibt install (Wiederaufnahme)
Task 4: minor in die Korrekturrunde: Paketmeldung bei fail() erhalten; Tests „Status nach install unlesbar“ und „Neustartbefehl scheitert“
Task 4: minor (deferred): start-pdh-Fehler ohne erneuten Versuch vor verify
Task 4: fix round 1 umgesetzt 6fea99bc (osInstallWait mit Obergrenze, failAfterInstall, 3 neue Tests). Pflicht für Task 5: wiederaufgenommener Zustand install braucht StepAt (null = abgelaufen); Nachprüfung gestartet
Task 4: fix round 1/5 (3 addressed, 0 open — Wartefrist install, Paketergebnis erhalten, 2 Tests; commits 9a597c5..6fea99b)
Task 4: Ruling: Null-StepAt-Härtung (StepAt null → jetzt setzen, persist, weiter warten) geht als Pflicht in Task 5 (Wiederaufnahme) — billig und fail-safe — kostet nichts
Task 4: complete (commits b4d9d49..6fea99b, review clean nach 1 Korrekturrunde)
Task 5: BASE 6fea99bc
Task 5: Umsetzer gestartet (sonnet), Agent a1e40af6fa73568dc, mit Pflichten Netz-Wartezustand, exklusiver Start, Sperre bis Einheit endet, Herunterfahren ≠ Fehler, Null-StepAt
Task 5: Umsetzung fertig 7dc0872c (DONE; Bedenken: Platzfehler normale Fehler; Netz-Warte-Text in View.Result; Abbruch zwischen Sicherung und install nicht ganz atomar; check-Fehler mit laufender Einheit hält Sperre nicht bis Einheitsende). Prüfung per Workflow (Ultracode an)
Task 5: Prüf-Workflow gestartet (wf_62328535-6ad: 5 Blickwinkel spec/lock/restart/integration/tests, je wichtiger Befund 3 Gegenprüfer, Mehrheit entscheidet)
Task 5: Prüf-Workflow (19 von 23 Agenten; 4 am Kostenlimit gescheitert: Blickwinkel tests ganz, Gegenprüfung integration): bestätigt Important – I1 check-Zeitüberschreitung im Auftrag gibt lockOS frei, während die check-Einheit läuft (spec+lock); I2 Wiederaufnahme von install nach Rechner-Neustart spielt Pakete bei laufendem pdh ein (restart); I3 nach Agent-Neustart ist lockOS frei, obwohl die install-Einheit eines beendeten/laufenden Auftrags läuft (restart, auch klein in lock/spec/integration). Widerlegt: „höchstens ein Neustart nicht abgesichert“ (3 Stimmen). UNGEPRÜFT (Gegenprüfer gescheitert): „Abbruch des OS-Auftrags über die Oberfläche praktisch nicht erreichbar, Abbruch-Knopf irreführend“. Klein: Abbruch kurz vor install bestätigt aber nicht ausgeführt; Ablehnungstexte („Ein PDH-Update läuft“ während OS-Update, Netzseite); „Keine Pakete“ endet als abgebrochen mit last_run ok:false; systemd-run-Fehler ohne Prüfung der Einheit; Restzeit beruht auf LockMax, das der OS-Auftrag nicht durchsetzt; Texte auf dem Wartungsbildschirm nicht übersetzt
Task 5: Ruling: Klein „Abbruch kurz vor install wird bestätigt, aber nicht ausgeführt“ hochgestuft auf Important — Anwender sieht „abgebrochen“, trotzdem werden Pakete eingespielt und neu gestartet — kostet: Teil der Korrekturrunde
Task 5: Ruling: I2 – vor jedem frischen install-Lauf (nicht beim Warten auf eine laufende Einheit) pdh stoppen, falls aktiv (jobStopPDH idempotent); Fehler → Failed → start — nach einem Rechner-Neustart startet pdh-ha/systemd pdh wieder — kostet: ein Stopp-Aufruf mehr
Task 5: Ruling: I3 – loadOSJob nimmt lockOS auch für einen beendeten Auftrag mit vermerkter, noch aktiver Einheit und gibt sie über osWaitUnit frei; I1 – osUnitRunning in Schritt "" (check/status) vermerkt die Einheit in OS.Unit
Task 5: Ruling: mit in die Runde (klein, nutzersichtbar): Sperr-Meldung nennt den tatsächlichen Besitzer (nicht „PDH-Update“ bei OS-Update); „Keine Pakete zu aktualisieren“ nach der Prüfung endet als fertig mit last_run ok:true, packages 0
Task 5: Workflow fortgesetzt (resume wf_62328535-6ad) für tests-Blickwinkel und Gegenprüfung des Abbruch-Befunds
Task 5: fix round 1 umgesetzt 58be5511 (6 Punkte; Test zu 5 war vor dem Test schon grün; Test zu 4 per Haken im Zeitfenster), Nachprüfung gestartet
Task 5: fix round 1/5 (6 addressed, 0 open; commits 7dc0872..58be551). minor (deferred): gofmt-Ausrichtung osBeforeInstall in main.go (mit Task 6 beheben); Wiederaufnahme install mit beendeter Unit startet neue Einheit (harmlos, ungetestet); osUnitIsActive-Fehler gilt als „nicht aktiv“ (Restrisiko)
Task 5: warte auf Ergebnis des fortgesetzten Prüf-Workflows (tests-Blickwinkel, Gegenprüfung Abbruch-Befund)
Task 5: fortgesetzter Prüf-Workflow (38 Agenten, ~2,4 Mio Token): integration bestätigt Important „Abbruch des OS-Auftrags am Einzelserver über die Oberfläche praktisch nicht erreichbar, Knopf auf dem Wartungsbildschirm irreführend (in der Sperre immer abgelehnt)“; tests bestätigt Important „osJobAbandon-Fehlerwege ohne Test (Gegenprobe: start-pdh/osJobUnlock entfernen bleibt grün)“ und „lockTryExclusive im Start durch keinen Test festgehalten“; „Wiederaufnahme install bei laufendem pdh“ = schon mit Runde 1 behoben. Widerlegt: Produktionsweiche ungetestet (nur minor), Ruling 3 nur einfachster Fall (= I1/I3, behoben)
Task 5: Ruling: Abbruch-Befund → Pflicht für Task 7 (Web): für Kind os am Einzelserver Abbruch in vorbereiten/countdown anbieten (Band und Karte, Recht wie heute system.maintenance_abort), auf dem Wartungsbildschirm in der Sperre für os nach stop kein Abbruch-Knopf — kostet: Task 7 etwas größer
Task 5: Runde 2 an den Umsetzer: Tabellentest osJobAbandon (psql-lock scheitert, pdh lässt sich nicht stoppen, pg_dump scheitert, Abbruch in UpLocked bei Step ""/stop) + Test lockTryExclusive im Start + gofmt main.go
Task 5: minor (deferred): TestOSJobInstallCancelledKeepsStep prüft nie genommenen Pfad; einige Zusicherungen grob; TestOSJobHandlers baut Routen nach; Goroutine bei Fehlschlag von TestOSJobLockHeldUntilUnitEnds; Restzeit beruht auf LockMax; Texte auf dem Wartungsbildschirm nicht übersetzt (Task 7)
Task 5: fix round 2 umgesetzt 6e12aa5f (TestOSJobAbandonPaths, TestOSJobStartNeedsExclusiveLock, Gegenproben RED belegt; gofmt main.go), Nachprüfung gestartet
Task 6: Ruling (Pflichten): (a) Netz-Wartezustand vor dem Neustart wandert in osAdvance (Schritt reboot, Frist osBootWait), damit auch der PDH-Auftrag mit Haken ihn hat; os_job.go nutzt dieselbe Stelle, Tests für beide Wege — sonst umgeht der Haken-Weg den Schutz; (b) PDH-Auftrag mit OS-Teil gibt die Sperre erst frei, wenn eine vermerkte Einheit inaktiv ist (osWaitUnit); (c) Wiederaufnahme des PDH-Auftrags (loadSingleJob) mit OS-Schritt install: Herunterfahren ≠ Fehler, Null-StepAt wartet, frisches install stoppt pdh – gleiche Regeln wie Task 5; (d) Wartungsfenster: ein als fehlerhaft gemerktes Ziel gilt wie „kein PDH-Update“ → mit Haken und Sicherheitsupdates startet der OS-Auftrag (Pre-flight-Ruling)
Task 5: fix round 2/5 (3 addressed, 0 open; commits 58be551..6e12aa5). minor (deferred): psql-unlock nur per Contains geprüft; Abbruchfall nur während pg_dump
Task 5: complete (commits 6fea99b..6e12aa5, review clean nach 2 Korrekturrunden)
Task 6: BASE 6e12aa5f
Task 6: Umsetzer gestartet (sonnet), Agent a7069bbe61d974a6a, Bericht task-6-report.md
Task 6: Umsetzung fertig 576c182d+aeb14793 (DONE; Netz-Wächter nur noch in osAdvance, Gegenproben RED belegt; Bedenken: osLastRun beim PDH-Auftrag nicht gesetzt; Agent-Ende im OS-Teil setzt jeden Schritt auf gesicherten Stand zurück; Wartegrund in Job.Result), Prüfer gestartet (sonnet)
Task 6: Prüfung (sonnet): alle 4 Rulings ✅; Important: PDH-Auftrag mit OS-Teil schreibt osLastRun nicht (Karte zeigte veralteten letzten Lauf)
Task 6: Ruling: last_run wird geschrieben, sobald der OS-Teil über die Prüfung hinaus lief; Pending==0 → {packages:0, ok:true, message „Keine Pakete zu aktualisieren“} wie im OS-Auftrag — Karte muss den echten letzten Lauf zeigen — kostet nichts
Task 6: Ruling: WithOS nur, wenn osAvailable (sonst kein OS-Teil, kein Text im Ergebnis) — vermeidet sinnlose Sperrzeit im Docker-/Verbundfall — kostet nichts
Task 6: minor (deferred): Agent-Ende nach fertigem apt → install läuft erneut (idempotent, stoppt pdh noch einmal); TestSingleJobAbortDuringOS stellt Zustand von Hand; DB-Sperrvermerk nach Rechner-Neustart nur im Test geprüft
Task 6: fix round 1 umgesetzt 561c9f91 (osRecordLastRun gemeinsam, osPlanned; nicht test-first, Gegenprobe RED belegt), Nachprüfung gestartet
Task 6: fix round 1/5 (2 addressed, 0 open; commits aeb1479..561c9f9). minor (deferred): Pfad „PDH scheitert vor dem OS-Teil“ ohne eigenen Test
Task 6: complete (commits 6e12aa5..561c9f9, review clean nach 1 Korrekturrunde)
Task 7: origin/main (0.82.58) in den Branch gemergt 566c26f1 (Konflikt .gitattributes: beide Seiten behalten), Build/vet/Tests update-agent, ha, web grün. BASE 566c26f1
Task 7: Umsetzer gestartet (sonnet), Agent ae70cd7b86e7b6a55. Rulings mitgegeben: Abbruch für Kind os am Einzelserver in vorbereiten/countdown (Band + Karte, Recht system.maintenance_abort), auf dem Wartungsbildschirm in der Sperre kein Abbruch-Knopf für os; alle neuen Texte übersetzt; Migration 222; Gestaltung nur über Variablen, lokal gerendert in 20 Ansichten
Task 7: Umsetzung fertig 72831b9e (DONE; 140 lokale Aufrufe ohne Überlauf/Konsolenfehler, selbst angesehen offen_fiori_light_1366 und auftrag_fluent_dark_320: stimmig; Bedenken: Abbrechen-Weiterleitung führt zu Server-Einstellungen→Ausfallschutz; Band nur im Countdown; 44 Übersetzungen nicht muttersprachlich geprüft), Prüfer gestartet (sonnet)
Task 7: Prüfung (sonnet): Needs fixes – I1 POST-Routen verlangen nur system.os_update, nicht system.manage_roles (Vorbild InstallUpdateWeb prüft canManageRoles)
Task 7: Ruling: I1 – osGuard verlangt manage_roles UND os_update (403 sonst), Gate-Regel bleibt — Brief: Seite verlangt weiter manage_roles, CanAct = os_update; Einzelvergabe von os_update ohne manage_roles darf nichts auslösen — kostet nichts
Task 7: Ruling: mit in die Runde (nutzersichtbar, billig): M3 Abbrechen von der Karte führt zurück zu den Core-Einstellungen (Rücksprung-Parameter oder eigene Route mit demselben maintAbort); M1 Zeitlimit für GET /v1/os/status auf 3 s; M5 eigenes Zeitlimit für den Start-Aufruf
Task 7: minor (deferred): M2 last_run-Protokolleintrag nicht atomar (doppelt bei gleichzeitigen Aufrufen möglich, kein Spam); M4 Band bietet Abbruch nur im Countdown (vorbereiten nur über die Karte) – Abweichung von Ruling 1 hingenommen; M6 Übersetzungen nicht muttersprachlich geprüft; M7 zwei Wartungsbildschirm-Tests in -run-Teilmengen rot (Bestand)
Task 7: fix round 1 umgesetzt d9fde512 (osGuard manage_roles+os_update, Rücksprung nach Abbruch nur für /core/settings, Status 3 s, Start 30 s). Eigene Prüfung: go test ./internal/web voll auf dem Branch grün; TestMaintScreenRender/MapsKnownTexts scheitern nur als -run-Teilmenge, auch auf main (Reihenfolge-Abhängigkeit, Bestand)
Task 7: minor (deferred): TestMaintScreenRender/TestMaintScreenMapsKnownTexts hängen vom Sprachkatalog eines anderen Tests ab (Bestand auf main)
Task 7: fix round 1/5 (4 addressed, 0 open; commits 72831b9..d9fde51)
Task 7: complete (commits 566c26f..d9fde51, review clean nach 1 Korrekturrunde)
Task 8: BASE d9fde512; origin/main VERSION 0.82.58
Task 8: Umsetzer gestartet (sonnet), Agent ad4e1410decef1b52; Ruling: Version 0.82.59 (main hat 0.82.58)
Task 8: Umsetzung fertig d63b89ab (DONE; Handbuch nur deutsch; neue Testdatei os_updates_docs_test.go wegen gemischter Zeilenenden in os_updates_test.go), Prüfer gestartet (sonnet)
Task 8: Prüfung (sonnet): Spezifikation ✅, freigegeben; Kleinbefunde Handbuch (Docker-Satz widersprüchlich, beide Rechte nennen, „Hilfsskript“/„Garantie“-Satz) → in die Schlusskorrektur
Task 8: complete (commits d9fde51..d63b89a, review clean)
Final: Codex (gpt-6.2-sol, Diff im Auftrag): „mergebereit: nein“ – 2 kritisch (apt-Frist mit --pipe wirkungslos; Speicherfehler vor install/reboot → doppelter Neustart), 6 wichtig, 1 klein. Schluss-Workflow gestartet (wf_04bb36c9-8d6): 4 Claude-Blickwinkel + je 3 Gegenprüfer (Sonnet) für alle Codex- und Claude-Befunde
Final: Codex #8 (Wartungsfenster installiert bei Sicherheitsupdates alles) = Abweichung vom bestätigten Entwurf („Andere offene Pakete ohne Sicherheitsupdate warten …“) → Entscheidung des Anwenders
Final: Anwender 24.11.2026 zu Codex #8: „Dann alle offenen Pakete“ – Wartungsfenster spielt bei offenen Sicherheitsupdates alle offenen Pakete ein (wie gebaut). In der Schlusskorrektur Entwurf (Abschnitt Bestätigung/Wartungsfenster), Handbuch, Karte und CHANGELOG daran anpassen; kein Code-Umbau
Final: Schluss-Workflow wf_04bb36c9-8d6 (73 Agenten, ~7,2 Mio Token Sonnet): bestätigt Codex #1 (--pipe, kritisch/wichtig), #2 (Speicherfehler, eher klein), #4 (lockPDH wiedereintrittsfähig), #6 (Netz/Platz im kombinierten Auftrag), #8 (Fenster alles – Anwender: so lassen); widerlegt Codex #3, #5, #7. Claude bestätigt: Recht in Abteilung reicht; ohne Internet check alle 5 min; --pipe koppelt apt an Agent (kritisch); Netz-Wartezustand erst nach PDH-Stopp; Ergebnistext kombinierter Auftrag widersprüchlich; Schreibfehler verschluckt; Verbund-Update irreführende Meldung bei fremder Sperre; Fenster fällt wegen kurzer Sperre aus; „aktuell“ ohne Auswertung; Karte „aktuell“ ohne Prüfung; „Teil B“ im Text; Rückfrage verschweigt Pakete/Neustart. Widerlegt: pdh-Start nie wiederholt; ESPHome-Ablehnung während PDH-Update
Final: Ruling: Klein „Neustart nötig ohne Weg dorthin“ hochgestuft (Sackgasse z. B. auf 202 nach manuellem apt upgrade) → F11 Neustart auch ohne Pakete
Final: Ruling: Codex #2 (Speicherfehler) trotz „klein“-Stimmen in die Runde (F2) — billig, schützt vor doppeltem Neustart — kostet nichts
Final: Korrekturliste final-fix-list.md (F1–F12, T1–T5), EIN Korrektur-Umsetzer (sonnet) gestartet, FIX_BASE d63b89ab
Final: Anwender: „Budget wird knapp“ – ab jetzt sparsam (keine Workflows, ein Sonnet-Prüfer je Korrektur, Codex für Zweitmeinung, Haiku für Einfaches)
Final: Korrektur fertig 03501388 (F1–F12, T1–T5; F7 mit kleiner Änderung in cmd/pdh-ha/agent_client.go, 423 klar gemeldet; F6 wartet bis 15 min synchron in windowTick). TestIconCSSChosen war seit 0.82.58 auf main veraltet (Verweise in der Restdatei) → Test angepasst, grün. Nachprüfung (sonnet, nur Korrektur-Diff) gestartet
