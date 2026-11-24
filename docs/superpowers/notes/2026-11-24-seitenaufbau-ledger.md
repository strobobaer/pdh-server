# SDD ledger — plan: docs/superpowers/plans/2026-11-24-seitenaufbau.md

Spec: docs/superpowers/specs/2026-11-24-seitenaufbau-design.md (bestätigt 24.11.2026). Ausführung: Subagenten (Anwender: „passst machn los“ auf die Empfehlung).
Branch feature/seitenaufbau, Basis origin/main f30cce0b (0.82.55).

## Pre-flight
| Aufgaben | geteilt | Befund |
|---|---|---|
| T1→T2 | `tablericons.Default/Names/Has/SVG` | passt |
| T2→T3 | `iconCSSHref`, Routen `/vendor/{ver}/icons-{key}.css`, `/vendor/{ver}/icons/{name}.svg` | passt |
| T2↔T4 | favicon.go, template_funcs.go, cmd/server/main.go (Routen unter `/vendor/{ver}/…`) | Ruling: T4 fügt `/vendor/{ver}/{lib}/{file}` hinzu; statischer Abschnitt `icons` muss vor `{lib}` greifen (chi bevorzugt statische Abschnitte) – T4 prüft per Test, dass `/vendor/<ver>/icons/ti-plus.svg` weiter die SVG liefert — kostet sonst kaputte Symbole |
| T3↔T4 | base.gohtml, global_dashboard.gohtml | nacheinander, passt |
| T4↔T5 | vendor_test.go (`TestNoExternalAssets`), THIRD_PARTY_LICENSES.md | T5 erweitert die Hosts, passt |
| T1↔T3 | `names.txt` aus der alten CSS, T3 löscht die alte CSS | Reihenfolge passt (T1 zuerst) |
| T6 | VERSION/CHANGELOG 0.82.56 | vor dem Merge `origin/main:VERSION` prüfen |
Je Aufgabe: Text in sich stimmig (Tests ↔ Code ↔ Dateien), keine Funde.

Task 1: BASE 8a9bdf9a
Task 1: Umsetzer gestartet (sonnet), Agent ad4231179c09dc9db, Bericht task-1-report.md
Task 1: Ruling: 56 Altnamen ohne gleichnamige SVG über denselben Codepunkt der Schrift zugeordnet (aliases.txt) — die Schrift zeigte genau dieses Zeichen, so bleibt das Aussehen gleich — kostet: zwei auffällige Zuordnungen (ti-box-seam→package, ti-hand-rock→hand-love-you) sind dennoch das bisherige Zeichen
Task 1: Umsetzung fertig 9698bd26 (DONE_WITH_CONCERNS: Aliase), Prüfer gestartet (sonnet), Paket review-task1-code.diff (ohne Datendateien)
Task 1: Prüfung: Spezifikation ✅, Qualität freigegeben (sonnet). Kleinbefunde: M1 drei -filled-Aliase nach Name statt Codepunkt (Bericht ungenau, inhaltlich passend), kein Test für den Alias-Pfad; M2 Lesefehler von aliases.txt still verschluckt, Einträge nicht geprüft; M3 Default() ohne sync.Once; M4 SVG() baut jedes Mal neu; M5 TestSVGWellFormed nur 50 Stichproben; M6 CRLF ungetestet
Task 1: Ruling: M3 (Default ohne sync.Once) geht mit Aufgabe 2 mit, weil Aufgabe 2 Default() aus Handlern aufruft und es dann ein Datenwettlauf wäre — kostet sonst einen Wettlauf unter Last; M1/M2/M5 (Alias-Test, Lesefehler unterscheiden, Alias-Einträge prüfen) ebenfalls in Aufgabe 2 mitnehmen, da klein und im selben Paket; M4/M6 zur Schlussprüfung
Task 1: complete (commits 8a9bdf9..9698bd2, review clean)
Task 2: BASE 9698bd26
Task 2: Umsetzer gestartet (sonnet), Agent a06d1575252ada916, Bericht task-2-report.md; mitgegeben: sync.Once in Default(), Alias-Test, Lesefehler aliases.txt unterscheiden und Einträge prüfen
Task 2: Ruling: Zwischenspeicher der gewählten Symbole wird zusätzlich beim Speichern unter /admin/icons geleert — sonst erscheint ein frisch gewähltes Symbol bis zu 60 s als Verweis statt eingebettet (Spezifikation: kein Kästchen nach einer Symbolwahl) — kostet: ein weiterer Aufrufort im Bestand
Task 2: Umsetzung fertig 49dd04d0 (DONE; Hinweise: kein sauberes RED vor dem Code, iconCSSHref nutzt den zwischengespeicherten key – veralteter key nach 60 s wird von der Route mit no-store aufgefangen, Zwischenspeicher paketweit), Prüfer gestartet (sonnet), Paket review-9698bd26..49dd04d0.diff
Task 2: Prüfung: Spezifikation ✅, Qualität freigegeben (sonnet). ⚠️ geklärt: iconGroups() enthält navDefs (icons.go:97-103); Browser → Task 6; Test-Router nutzt dieselben Muster wie main.go (Task 4 prüft mit {lib} in gleicher Reihenfolge). Kleinbefunde (zur Schlussprüfung): M1 Sperre iconCSSMu über DB-Abfragen und gzip bei jedem Ablauf, auch ohne Änderung (besser: key zuerst, bei gleichem key nur at erneuern); M2 Scan-Fehler von app_settings (kaputtes JSON) still ignoriert; M3 Glob web/static/*.js ohne Unterordner, zusammengesetzte Klassen nicht erfasst; M4 SHA-256 je SVG-Anfrage, Vary ohne gzip; M5 Default() nach Panik nil; M6 Tests: anderer Lesefehler als ErrNotExist, Cache-Leeren beim Speichern, nur Test-Router
Task 2: complete (commits 9698bd2..49dd04d, review clean)
Task 3: BASE 49dd04d0
Task 3: Ruling: Plan-Lücke – work_board.gohtml:423-429 (.w11-infobar::before) zeichnet 4 Symbole über font-family 'tabler-icons' mit Codepunkt; ohne Schrift wären sie weg. Aufgabe 3 stellt sie auf die Masken-Bauweise um (gleiches Aussehen, Symbole in icons_used.txt) und ein Test verbietet font-family tabler/Codepunkt-content außerhalb von vendor — Spezifikation: alle Symbole bleiben überall nutzbar — kostet: eine weitere Vorlage im Umfang
Task 3: Ruling: unter /vendor/tabler-icons/ bleibt nur tabler-icons.min.css als 302 (no-store) auf die aktuelle Stildatei; alles andere dort 404 no-store, sofern der Browser es nicht braucht (Datendateien sind Quelle für den Server) — kostet: falls ein Skript names.txt o. ä. abruft, muss es die Admin-Route nutzen
Task 3: Umsetzer gestartet (sonnet), Agent a60673eab9f68f0c8, Bericht task-3-report.md
Task 3: Umsetzung fertig 0821edaf (DONE_WITH_CONCERNS: Infoleiste nicht im Browser gesehen → Task 6; Seitenköpfe im Test mit leeren Daten; volle Integrationsreihe nicht gelaufen → vor Merge), Prüfer gestartet (sonnet), Paket review-task3.diff (ohne gelöschte 1,4-MB-CSS)
Task 3: Prüfung: Spezifikation ✅, Qualität freigegeben (sonnet). ⚠️ Aussehen → Task 6; CRLF → in der Korrekturrunde prüfen lassen.
Task 3: eigene Prüfung (Edge, forcedColors emuliert, scratchpad/forced): .ti::before mit CanvasText sichtbar (hell/dunkel) – Befund M6 für .ti unbegründet; .w11-infobar::before (Akzentfarbe, keine Kontrastregel) wird auf Canvas gezwungen → Symbol unsichtbar. Hochgestuft auf Important (Review Focus 4, mit der Schrift war es sichtbar).
Task 3: Ruling: Befund M1 (TestNoIconFontUse verschluckt WalkDir/ReadFile-Fehler, kann leer bestehen) hochgestuft auf Important — Wächtertest, der nichts prüft, ist nach Rubrik Important — kostet: eine Zeile Zählung
Task 3: minor (deferred): renderStandalone verwirft den Ausführungsfehler (bei leerem Puffer t.Fatal)
Task 3: minor (deferred): TestInfobarUsesIconMask prüft nur Teilstrings, nicht die gerenderte Adresse mit Version
Task 3: minor (deferred): TestOldIconCSSRedirects baut die Stildatei vorher nicht (hängt am Cache-Zustand)
Task 3: minor (deferred): LegacyIconVendorWeb leitet bei jeder Methode um (r.Get genauer)
Task 3: fix round 1 umgesetzt 485a2df0 (Kontrastregel Infoleiste, TestNoIconFontUse mit Fehlern und Mindestzahl; CRLF bestätigt), Nachprüfung gestartet (sonnet). Eigene Edge-Prüfung (forcedColors hell/dunkel): Infoleiste und success in CanvasText sichtbar.
Task 3: minor (deferred): Kontrastregel der Infoleiste (work_board.gohtml:430) gewinnt nur über die Reihenfolge (gleiche Spezifität) – eine später angefügte Variantenregel dahinter würde sie aushebeln
Task 3: fix round 1/5 (2 addressed, 0 open — Kontrastregel Infoleiste, TestNoIconFontUse Fehler/Mindestzahl; commits 0821eda..485a2df)
Task 3: minor (deferred): Regex in TestInfobarUsesIconMask locker ([^@]* erlaubt }); Mindestzahl 50 fängt nur leere Suche
Task 3: complete (commits 49dd04d..485a2df, review clean nach 1 Korrekturrunde)
Task 4: BASE 485a2df0
Task 4: Umsetzer gestartet (sonnet), Agent a63e4502a80cbc8f9, Bericht task-4-report.md
Task 4: Ruling: Dateien aus genau der heute verwendeten Adresse laden (gleiche Bytes wie jetzt im Betrieb), Quelle und SHA-256 im Bericht — Spezifikation „unverändert“ — kostet: falls CDN-Datei von npm abweicht, Hinweis im Bericht
Task 4: Ruling: /vendor/{ver}/{lib}/{file} nur für eine feste Liste (htmx, sortablejs, frappe-gantt, chartjs, xlsx, mammoth; Task 5 fonts) — sonst wären Datendateien der Symbole (2 MB JSON) öffentlich abrufbar — kostet: neue Bibliothek muss in die Liste
Task 4: Ruling: alle /vendor-Routen in einer Funktion (RegisterVendorRoutes), von main.go und Tests genutzt — Tests prüfen so die echte Reihenfolge (Pre-flight T2↔T4) — kostet: kleiner Umbau in main.go
Task 4: Umsetzung fertig 990750d6 (DONE_WITH_CONCERNS: Chart.js npm-Datei nicht minifiziert → von cdnjs, nennt 4.4.0; frappe-gantt LICENSE aus npm-Tarball; htmx/../../x → 404 ohne no-store (Router-404); Lib-Route nur .js/.css → Task 5 ergänzt .woff2; timeline_style.go unverändert)
Task 4: Befund außerhalb: TestITNetRunOnceNotConfigured scheitert mit Labor-DB („online“ statt unbekannt) – datenabhängig, it_netsync auf dem Branch unverändert; im Gesamtlauf vor dem Merge gegen origin/main vergleichen
Task 4: Prüfer gestartet (sonnet), Paket review-task4.diff (ohne heruntergeladene .js/.css)
Task 4: Prüfung: Spezifikation ✅, Qualität freigegeben (sonnet; SHA-256 auf der Platte nachgeprüft). ⚠️ Chart.js von cdnjs → gedeckt durch Ruling; Browser und CHANGELOG → Task 6.
Task 4: minor (deferred): Pin-Test prüft mammoth nur auf "mammoth", frappe-gantt gar nicht (Version/Hash)
Task 4: minor (deferred): Router-404 für /vendor/* ohne no-store (htmx/../../x); einheitliches NotFound wäre sauberer
Task 4: minor (deferred): kein 304/HEAD-Test für die Lib-Route; „..“-Prüfung neben Regex ohne Kommentar; THIRD_PARTY_LICENSES Quelle uneinheitlich (cdnjs/npm)
Task 4: complete (commits 485a2df..990750d, review clean)
Task 5: BASE 990750d6
Task 5: Umsetzer gestartet (sonnet), Agent a6e1da04bd6af3c2b, Bericht task-5-report.md
Task 5: Ruling: Allow-Liste um fonts, Endungen um .woff2 (font/woff2); url() in der Schrift-CSS relativ, damit die Version im Pfad automatisch gilt — Spezifikation Version im Pfad — kostet: nichts Bekanntes
Task 5: Umsetzung fertig eb2b6736 (DONE; 5 CSS, 29 WOFF2 unverändert, 5 OFL-Texte, 852 KB; Hinweis: offene Seite über ein Update → Schriftteil der alten Version 404 bis zum Neuladen)
Task 5: Prüfung: Spezifikation ✅, Qualität freigegeben (sonnet; alle url() auf der Platte, OFL-Texte echt, alle Verbraucher von Href über FontHref/FontsJS). ⚠️ voller ./internal/web-Lauf nach letzter Korrektur fehlt → Task 6.
Task 5: minor (deferred): vendorTypes nicht gofmt-konform (Task 6 lässt gofmt -l über geänderte Go-Dateien laufen); .woff2-Test in fonts_test.go statt vendor_test.go; appearance_test Teilstring schwach; veralteter Kommentar „(Schrift-Hosts: Task 5.)“
Task 5: complete (commits 990750d..eb2b673, review clean)
Task 6: BASE eb2b6736
Task 6: origin/main stand bei 0.82.56 (Terminal-Verbindungsweg, Migration 221) → in den Branch gemergt (ohne Konflikte), Build und Wächtertests grün. Ruling: Version dieses Pakets 0.82.57 statt 0.82.56 — AGENTS: nächste Korrekturversion über main — kostet nichts
Task 6: Ruling: Schlussprüfung (Step 4) und Freigabe (Step 5) macht der Controller nach SDD final-review; der Umsetzer macht Step 1–3 und den Commit
Task 6: BASE (nach Merge) 1542b6b7; Umsetzer gestartet (sonnet), Agent ae22042ea52585a5c, Bericht task-6-report.md
Task 6: Umsetzung fertig 807c1ac1 (DONE; Tests grün bis auf bekannte; 40 Vergleichsbilder ≤1 px Versatz, selbst angesehen fluent_dark_390 alt/neu: gleich; messung.js nur ohne Server geprüft), Prüfer gestartet (sonnet)
Task 6: Prüfung: Spezifikation ✅, Qualität freigegeben (sonnet; CRLF erhalten, VERSION/CHANGELOG 0.82.57, Kennwort nie ausgegeben). ⚠️ Messskript nie gegen Server → nach dem Einspielen am Testverbund.
Task 6: minor (deferred): messung.js pending() praktisch immer 0 – „alle SVG fertig“ nur über 2 stabile Frames, README beschreibt mehr
Task 6: minor (deferred): messung.js Resource-Timing-Puffer 250 Einträge – count() kann zu früh stabil wirken (setResourceTimingBufferSize)
Task 6: minor (deferred): messung.js icons-Zeit leicht pessimistisch (≥2 Frames nach load), Parameter cdp ungenutzt
Task 6: minor (deferred): README.md nennt interne Adresse 192.0.2.15 – Punkt 18: echte Adressen nur in AGENTS.md (192.0.2.15)
Task 6: complete (commits 1542b6b..807c1ac, review clean)
Final: Schlussprüfung (opus): Ready to merge „With fixes“; 0 Critical, 3 Important (I1 unbekannter/leerer Name = gefülltes Quadrat, ti-tool-off am Terminal; I2 gewählte Symbole aus Kategorien/Grafana/Watch-Boards/IT-Kategorien nach Speichern nicht eingebettet; I3 fremde Version 404 → offene Seiten verlieren Nachgeladenes), 13 Minor (M-a … M-m).
Final: Ruling: I3 – bekannte Datei/bekannter Name einer fremden Version → 302 mit Cache-Control: no-store auf die laufende Version (Stildatei: auf iconCSSHref), Unbekanntes weiter 404 no-store; Abweichung vom Plan (404), Spezifikation verlangt nur no-store und kein immutable für fremde Versionen — offene Seiten (Terminal, Leitstand, Hintergrund-Tabs) behalten Ordnen, Vorschau, Schriften, Symbole — kostet: im HA-Mix bekommt eine neue Seite kurz die Dateien der alten Version (bei Bibliotheken gleich, bei Symbolen evtl. ein fehlendes Symbol, dank I1-Rückfall unsichtbar statt Quadrat)
Final: Ruling: I1 ohne Ablehnen unbekannter Namen beim Speichern — der Masken-Rückfall macht Unbekanntes unsichtbar wie früher mit der Schrift; Prüfung der Speicherwege wäre neues Verhalten — kostet: Tippfehler bleiben unbemerkt (wie bisher)
Final: Ruling: in die Korrekturrunde zusätzlich M-c (/vendor/ in quietPath – sonst Dutzende Info-Zeilen je Seitenaufruf im Server-Protokoll, das der Anwender auf 202 liest), M-d (losgelöster Kontext, Kennung zuerst vergleichen – gehört zu I2), M-f (toter ?v=-immutable-Zweig = Muster aus 0.44.0), M-h (.ti[hidden]), M-j (CHANGELOG-Wortlaut), M-k (Kommentar), T6 README-Adresse und messung.js pending()/Puffer (laut Triage vor dem Merge) — alle klein und nutzerwirksam bzw. vom Prüfer als „vor dem Merge“ eingestuft — kostet: etwas größere Korrekturrunde
Final: minor (deferred): M-a Infoleiste lädt Einzel-SVGs statt data:-Adresse (mit I3 über Updates hinweg funktionsfähig)
Final: minor (deferred): M-b alte 0.82.56-Seiten nach dem 302: ensureIconFont setzt icons-fallback – einmaliger Übergang, beim Einspielen „Seiten neu laden“ empfehlen
Final: minor (deferred): M-e Kennung nur aus Namen (Inhalt hängt auch an Programm/SVG-Daten) – sicher, solange jede Änderung die Version anhebt (AGENTS-Regel)
Final: minor (deferred): M-g .woff2 unnötig mit BestCompression gepackt; M-i CanvasText auch auf Highlight-/Knopfflächen; M-l HEAD → 405 (r.Get); M-m Vergleich nur künstliche Seite → Browserprüfung nach dem Einspielen
Final: Korrektur-Umsetzer (sonnet) mit vollständiger Liste gestartet, FIX_BASE 807c1ac1
Final: Anwender 24.11.2026: Dauerfreigabe Testverbund (Merge/Push/Verbund-Update nach grüner Schlussprüfung und Tests; 202 ausgenommen), ein Entwurf für 0.90.0-Teile 2+4+5, zweite Sitzung parallel (Stammdaten, OS-Updates), Folien nach 0.90.0 – nach der Korrekturrunde in AGENTS.md eintragen
Final: Anwender: Codex als zusätzlicher Prüfer (Teil 1 vor dem Merge, adversarial-review) und als zweite Sitzung (OS-Updates, dann Stammdaten, eigener Worktree)
Final: Anwender: sparsames Modell – Codex --model spark; Claude-Schlussprüfungen künftig Sonnet
Final: Korrekturrunde fertig a18fd462 (DONE_WITH_CONCERNS: messung.js nur node --check; Rückfall-Maske nicht neu in Edge; 'ti '+Eingabe nur über Rückfall; Symbol-Netz startet nicht am Spiegel; Handbuch unverändert). Nachprüfung (sonnet) + Codex-Prüfung (spark) gestartet
Final: Nachprüfung der Korrekturrunde (sonnet): alle Befunde ADDRESSED, keine neuen Critical/Important. Eigene Edge-Prüfung der Rückfall-Maske: unbekannt/leer unsichtbar, hidden ausgeblendet, bekannt unverändert.
Final: minor (deferred): adoptGrafana (leitstand_grafana.go:98) ruft iconChoicesChanged nicht – vom 60-s-Takt aufgefangen; messung.js hängt CDP-Listener je measure() neu an (je Lauf neue Sitzung, folgenlos); Symbol-Takt läuft nicht am Spiegel (nach Rollentausch Auffrischung bei Anfrage); iconCSSBuildMu bis 3 s über DB-Abfrage
Final: Codex-Prüfung mit spark gescheitert („spark nicht mit ChatGPT-Konto“) → neu mit gpt-6.2-sol gestartet
Final: Codex-Prüfung (gpt-6.2-sol, Diff im Auftrag): „mit Korrekturen“, 2 wichtig, 3 klein.
Final: Ruling: Codex #1 (Weiterleitung im Kreis beim rollierenden Update) verworfen — unter VIP 192.0.2.15 und Cloudflare-Tunnel antwortet nur der aktive Server (Tunnel „läuft hier“ nur am aktiven), der Spiegel nur unter eigener Adresse mit eigener Version; HTML und Dateien kommen nie von zwei Versionen über dieselbe Adresse — kostet falls falsch: in den Sekunden des Rollentauschs einzelne Abrufe über das Umleitungslimit, bis zum Neuladen
Final: Ruling: Codex #2 (Kontrastmodus ersetzt CanvasText) verworfen — in Edge mit emulierten forced-colors gemessen: background-color CanvasText bleibt (hell rgb(0,0,0), dunkel rgb(255,255,255)), Symbole sichtbar; Nicht-Systemfarbe wird ersetzt (darum Infoleisten-Regel) — kostet falls falsch: Symbole im echten Hochkontrast unsichtbar, Abhilfe forced-color-adjust:none
Final: minor (deferred): Codex #3 Router-404 ohne no-store für nicht passende /vendor-Pfade, HEAD → 405; #4 gzip;q=0 wird nicht beachtet (Bestand serveAsset); #5 messung.js zählt loadingFailed als fertig – bei der Messung Fehlerzahl mit ausgeben/Netzprotokoll prüfen
