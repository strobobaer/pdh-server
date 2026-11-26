# 0.90.0 – Einheitliche Oberfläche: Entwurf

Stand 25.11.2026. Grundlage: Vorgaben des Anwenders vom 24.11.2026 (AGENTS.md,
offene Punkte 24 (3)), Entscheidungen vom 25.11.2026 und die klickbare Vorschau
`docs/superpowers/specs/2026-11-25-oberflaeche-vorschau.html` (Anwender: „Das sieht
doch spitze aus“). Teil 1 „Schneller Seitenaufbau“ ist mit 0.82.57/0.82.58 erledigt.
Nicht Teil dieses Entwurfs: Folien wie PowerPoint (danach, 0.90.x), „Stammdaten
zusammenführen“ (eigenes Paket, parallel), Betriebssystem-Updates Teil B.

## 1. Absicht

Das PDH soll **überall gleich aussehen und sich gleich bedienen lassen**: was
zusammengehört, steht zusammen; gleiche Funktionen haben gleiche Knöpfe an der
gleichen Stelle. Es soll **schneller** werden und **nicht nach KI aussehen**,
„sondern einfach nur besser als SAP und Microsoft, aber trotzdem echt“. Die
Gestaltung wird so festgeschrieben, dass sich später alles neu aufbauen lässt.

Benutzer: Instandhalter und Schichtleiter im Betrieb (oft am Handy oder Tablet,
mit Handschuhen, schnell), Einkauf und Büro am breiten Bildschirm,
Administratoren. Erfolg heißt: Wer eine Seite kennt, kann jede andere bedienen;
niemand verliert eine Funktion; Seiten bauen spürbar schneller auf.

## 2. Ziele, Nicht-Ziele, Anti-Ziele

**Ziele**
- Ein Rahmen nach dem Fiori-Muster (Stil, nicht Marke): Bereichsleiste links,
  Reiter wie Edge, Menüband mit den Funktionen der Seite, Suchzeile mit Strg+K,
  Copilot rechts, Statusleiste unten.
- Vorgangslisten wie Outlook (Ordner links, Liste, Detail rechts; am Handy
  eigene Detailseite), **immer nach Infrastruktur gruppierbar**.
- Start- und Übersichtsseiten aus gleich gebauten, frei kombinierbaren Widgets,
  je Rolle vorbelegt.
- Zusatzdienste (Nextcloud, Grafana, weitere) im PDH-Rahmen.
- Eine Gestaltungsgrundlage im Repository, deren Einhaltung Tests erzwingen.
- Weniger Ballast je Seitenaufruf (CSS und JavaScript aus `base.gohtml` in
  versionierte, zwischenspeicherbare Dateien).

**Nicht-Ziele**
- Terminal, Kiosk-Terminal, Anlagenterminal, Easy-Modus, Leitstand-Bildschirm
  (`global_dashboard`), Anmeldung und Einrichtung bekommen **keinen** neuen
  Rahmen – nur die Gestaltungswerte (Farben, Schrift, Abstände) und weiter
  `data-style`.
- Keine neue Fachfunktion; Inhalte der Seiten ändern sich nur dort, wo sie auf
  die gemeinsamen Bausteine umgestellt werden.
- Adressen (URLs) bleiben; Lesezeichen und Verweise in Mails gelten weiter.

**Anti-Ziele** (wäre ein Fehlschlag, auch wenn es technisch läuft)
- KI-Look: Verläufe, Glas, bunte Schatten, Deko-Grafiken, Emojis, Karten mit
  farbigem Seitenstreifen, Fülltexte; Farbe ohne Bedeutung.
- Im Betrieb wochenlang eine halb alte, halb neue Oberfläche.
- Eine Funktion ist im neuen Rahmen nicht mehr erreichbar.
- Langsamer als heute (Messung wie in
  `docs/superpowers/notes/2026-11-24-seitenaufbau-abnahme.md`).

## 3. Rahmenbedingungen (aus AGENTS.md, gelten für jedes Teilpaket)

- Zwei Stile **Fiori** (Vorgabe) und **Fluent**, je hell und dunkel; Farben,
  Ecken, Schatten nur über Variablen; der Akzent kommt aus dem Farbschema.
- Kopfleiste: Uhr und Benutzermenü immer ganz sichtbar; erst kürzt sich der
  Seitentitel, dann blendet `pdhTopbarFit()` Knöpfe nach `data-tb-hide` aus;
  neue Knöpfe nur als Symbol mit `title`/`aria-label`; was ausgeblendet werden
  kann, bleibt anders erreichbar. Prüfen bei 320, 390, 768, 1024, 1366 px und
  120–160 % Vergrößerung.
- Alles neu Hinzugefügte braucht ein Recht (Migration, Rollen, die es bisher
  stillschweigend durften, bekommen es; `h.hasPermRec` bei Datensätzen).
- Handbuch (`help.gohtml`, Skizzen in `help_figures.gohtml`) und CHANGELOG im
  selben Schritt; jede Version ist die nächste Korrekturversion (0.82.61 …);
  **0.90.0 vergibt der Anwender für das Umschalten aller Benutzer**.
- Nichts aus dem Internet laden (seit 0.82.57 `web/static/vendor/`,
  `TestNoExternalAssets`).
- Mehrere Sitzungen arbeiten parallel auf `main`: kleine Pakete, vor jedem
  Merge die Prüfung aus AGENTS.md.

## 4. Ansätze und Entscheidung

**Übergang** (Frage an den Anwender, 25.11.2026):
- A – Schalter „Neue Oberfläche ausprobieren“; die neue Oberfläche wächst in
  Korrekturversionen dahinter, alle anderen behalten die alte. **Gewählt**
  („A und beides bleibt“ – „bis alles fertig ist“): alt und neu laufen
  nebeneinander, **bis alles fertig ist**; mit 0.90.0 bekommen alle die neue,
  der klassische Rahmen entfällt.
- B – jede Version ändert sofort für alle (keine Doppelpflege, aber
  wochenlang Mischoberfläche) – verworfen.
- C – alles auf einem Branch, dann auf einen Schlag – verworfen
  (Konflikte mit den parallelen Sitzungen).

**Bauweise des Rahmens** (Entscheidung von Claude, der Anwender hat das „Wie“
übergeben): Der neue Rahmen ist eine **eigene Vorlage** neben `base.gohtml`
(z. B. `frame.gohtml`), die dieselben Inhaltsblöcke der Seiten einbettet. Der
Handler wählt je Anfrage den Rahmen (Benutzer-Schalter, sonst Firmen-Vorgabe).
So bleiben die Seiteninhalte für beide Rahmen gleich, und mit 0.90.0 lässt sich
der alte Rahmen ohne Umbau der Seiten löschen. Verworfen: ein Rahmen mit
Weichen in `base.gohtml` (die Datei ist schon 236 KB, das Löschen später wäre
fehleranfällig).

## 5. Was gebaut wird (vom Anwender entschieden bzw. in der Vorschau bestätigt)

### 5.1 Gestaltungsgrundlage

- **Gestaltungswerte als Datei im Repository**: Farben für Fiori/Fluent ×
  hell/dunkel (bestehende Variablen `--bg`, `--bg2`, `--text`, `--muted`,
  `--border`, `--accent`, `--shadow-card` und die nötigen weiteren für
  Statusfarben), Abstände im 4/8-px-Raster, **eine** Schrift (DM Sans,
  mitgeliefert) mit wenigen Größen (12/13/14/16/20), Rundungen, Schatten nur
  für Schwebendes (Menüs, Dialoge, Suchzeile). Die Werte der Vorschau sind der
  Ausgangspunkt.
- **CSS und JavaScript aus `base.gohtml` auslagern** (heute rund 70 KB CSS und
  über 100 KB JavaScript direkt in jeder Seite) in versionierte Dateien unter
  `/vendor/<Version>/…` bzw. einem gleichartigen Pfad, unveränderlich
  zwischengespeichert wie die Symbole seit 0.82.58. Nur was vor dem ersten
  Zeichnen nötig ist (Stil, hell/dunkel, Seitenleisten-Zustand), bleibt inline.
- **Bausteinkatalog** mit Regeln (Knopf, Schild, Liste, Gruppenkopf,
  Reiterleiste, Menüband-Gruppe, Detailkopf, Widget, Dialog, Statusmeldung) –
  als Seite im PDH für Administratoren und zum Ansehen im Design System des
  Anwenders in Claude.
- **Wächter-Tests**: keine neuen festen Farbwerte in Vorlagen (heute rund 1000;
  die Zahl darf nur sinken, mit 0.90.0 gleich 0 außerhalb der
  Gestaltungswerte), kein neues Inline-CSS in `base.gohtml`/`frame.gohtml`,
  neue Seiten nur aus Bausteinen.
- **Darstellung verfeinern** (Wunsch „Designauswahl verfeinern“): im Menüband
  unter „Darstellung“ Stil (Fiori/Fluent), Modus (hell/dunkel/wie System),
  Farbschema (die vorhandenen) und Dichte (kompakt/normal/Berührung).

### 5.2 Rahmen

> **Geändert durch Abschnitt 11 (Vorgaben des Anwenders vom 26.11.2026):**
> Kopfzeile nur noch Logo, Überschrift, Uhr; Benutzermenü und Anmelden links
> unten; keine Reiterleiste über dem Menüband – die Unterseiten sind Bänder
> des Menübands; Reiter des Menübands Start | Bänder des Bereichs | Ansicht |
> Einstellungen | Hilfe; Copilot und Suche aus „Start“. Wo die folgenden
> Punkte abweichen, gilt Abschnitt 11.

- **Kopfleiste**: „PDH“, Suchzeile „Was möchtest du tun? (Strg+K)“,
  Benachrichtigungen, Copilot-Knopf, Uhr, Benutzermenü (mit dem Schalter „Neue
  Oberfläche“ bis 0.90.0).
- **Bereichsleiste links** mit 9 Bereichen; die Unterseiten eines Bereichs sind
  feste Reiter. Einteilung (Vorschlag von Claude, in der Vorschau bestätigt):

  | Bereich | Unterseiten |
  |---|---|
  | Start | Startseite, Leitstand, Zuweisung |
  | Vorgänge | Störungen, Tickets, Aufgaben, Wartung, Rüstzeiten, Stillstände, KVP, Projekte |
  | Planung | Planer, Kalender, Zeiterfassung, Anwesenheit |
  | Anlagen | Infrastruktur, Schnellübersicht, IT-Assets, VPN, ESPPDH, Messstellen |
  | Material | Ersatzteile, Lager, Beschaffung, Bestellvorgänge, Hersteller & Lieferanten |
  | Wissen | Dokumente (Handbücher, Prozesse, Arbeitsanweisungen, Formblätter), Vorlagen & Signaturen, Schulungen |
  | Daten | Statistik, Import, Export, Abgleich, Datennetz |
  | Verwaltung | Benutzer, Abteilungen, Organigramm, Rollen & Rechte, Einstellungen (Core, Server), Netzwerk, Datensicherung, Bereinigung, Drucker, Geräte (App, Kiosk), Leitstände, Infobildschirm, Kostenstellen, Kategorien, Symbole |
  | Dienste | Nextcloud, Grafana, weitere Zusatzdienste |

  „Neu anlegen“ wird der Knopf „Neu“ im Menüband und ein Eintrag in Strg+K;
  Chat und Handbuch (Hilfe) stehen in der rechten Leiste. Sichtbar ist nur, was
  das Recht erlaubt (wie heute über `navDefs`). Jeder kann Reihenfolge und
  Anheften unter „Anpassen“ ändern (heute: eigene Menü-Anordnung), die Firma
  legt die Vorgabe fest.
- **Reiterleiste wie Edge**: über dem Menüband; feste Reiter der Unterseiten
  und je geöffnetem Vorgang ein eigener, schließbarer Reiter (× schließt).
- **Menüband** mit den Reitern Start, Ansicht, Anpassen, Darstellung und
  beschrifteten Gruppen (Beispiel Störungen: Vorgang – Neu, Speichern, Öffnen;
  Bearbeiten – Zuweisen, Warten, Erledigt; Ansichten; Teilen – Exportieren,
  Drucken; Einstellungen – Spalten, Filter merken). Die Aktionen einer Seite
  stehen **nur** dort, nicht verstreut in der Seite. Jede Seite meldet ihre
  Aktionen an einer Stelle im Code, damit gleiche Funktionen überall gleich
  heißen und gleich aussehen.
- **Auswahlfelder erweitern**: jede Auswahlliste (Kategorie, Lieferant,
  Hersteller, Kostenstelle …) lässt sich über einen Menüband-Eintrag der Gruppe
  „Stammdaten“ erweitern („… hinzufügen“, kleiner Dialog, der neue Eintrag
  steht sofort in der Auswahl). Ohne das passende Recht ist der Eintrag
  ausgeblendet. Gilt erst für die Listen, die „Stammdaten zusammenführen“
  vereinheitlicht hat (Lieferant/Hersteller nur einmal, Kostenstelle nur
  einmal).
- **Strg+K**: Befehlszeile für Aktionen („Störung melden“, „Ticket anlegen“,
  „Wartung planen“), Seiten, Vorgänge (Nummer oder Titel) und Anlagen; Pfeile,
  Enter, Esc; nutzt die vorhandene Suche (`/api/search`) und die Menütabelle;
  zeigt nur Erlaubtes.
- **Copilot rechts wie bei Microsoft**: die vorhandene rechte Leiste mit den
  Reitern Copilot, Suche, Hilfe, Chat, ein- und ausblendbar über die
  Kopfleiste; „kennt diese Seite“.
- **Statusleiste** (die vorhandene `global-status-footer` mit
  Fortschrittsbalken): zeigt den **Update-Stand** (Einzelserver und Verbund,
  Neustart ohne „Netzwerkfehler“), jede Aktion in Klartext („Gespeichert:
  Störung ST-10482“) mit echtem Fortschritt bei langen Läufen und dauerhafte
  Warnungen (Wartung, Spiegelserver – nur lesen, Verbindung). Auch am Handy
  als Text; Tippen zeigt die letzten Meldungen.
- **Handy**: Bereichsleiste wird zur unteren Leiste (4 Bereiche + „Mehr“),
  Menüband auf die 3 wichtigsten Aktionen + „…“, Reiterleiste scrollt seitlich,
  Liste → Detail als eigene Seite mit Zurück. Bedienflächen ≥ 44 px.

### 5.3 Gleiche Bausteine

- **Liste** (für alle Vorgangslisten; Grundlage ist das gemeinsame
  `WorkBoardPage`): Filter-Reiter mit Anzahl (Alle, Offen, In Arbeit, Wartet,
  Erledigt), Ordner links mit dem Infrastrukturbaum (Auswahl filtert **immer
  mit Unteranlagen**), „Gruppieren: Infrastruktur | keine“. Gruppiert wird in
  der **Reihenfolge des Anlagenbaums**, Unteranlagen unter ihrer Anlage,
  Gruppenköpfe mit Anlagenpfad und Anzahl, aufklappbar. Jede Zeile: Titel,
  Nummer, Anlage mit Pfad (am Handy vorn gekürzt), Priorität und Status als
  Schild, Zeit. Am breiten Bildschirm Detail rechts daneben, sonst eigene
  Seite.
- **Detail**: Kopf (Nummer · Art, Titel, Schilder, Eckdaten Anlage, Gemeldet,
  Zuständig, Fällig), Reiter Übersicht, Arbeit, Zeit, Kommentare, Felder,
  Dokumente, Verknüpfungen, Verlauf (die vorhandenen `record-*`-Bausteine),
  Aktionen im Menüband.
- Weitere Listen (Ersatzteile, IT-Assets, Benutzer, Bestellungen …) nutzen
  denselben Baustein, wo es passt; die Infrastruktur-Gruppierung gilt überall,
  wo ein Eintrag an einer Anlage hängt.

### 5.4 Schnellstart und Widgets

- Startseite: oben Schnellstart-Kacheln **je Rolle**, darunter frei
  kombinierbare Widgets im Raster (1–4 Spalten breit), wie heute
  (`dashboard_widgets.go`, 25 Arten, je Benutzer gespeichert).
- Neu: **Vorgabe je Rolle** (Kacheln und Widgets), die ein Administrator
  festlegt; der Benutzer kann anpassen und „auf die Vorgabe meiner Rolle
  zurücksetzen“. Anpassen über das Menüband (Hinzufügen, Breite, Entfernen,
  Verschieben).
- Übersichtsseiten der Bereiche dürfen dieselben Widgets nutzen.

### 5.5 Ingress (Zusatzdienste im PDH-Rahmen)

- Bereich „Dienste“: jeder Zusatzdienst ist ein Eintrag (Name, Adresse,
  Symbol, wer ihn sieht) und erscheint **im Mittelrahmen** – Kopfleiste,
  Bereichsleiste, Reiter und Statusleiste bleiben vom PDH. Menüband: „Neu
  laden“, „In neuem Fenster öffnen“.
- Grafana läuft schon hinter der PDH-Anmeldung (`/grafana`, Rollen über
  `grafanaRole`) und wird so eingebunden.
- Nextcloud hat einen eigenen Namen und eine eigene Anmeldung (Einmalanmeldung
  über das PDH gibt es schon, `nextcloud_sso`). Damit sie im Rahmen erscheint,
  muss Nextcloud das Einbetten durch die PDH-Adresse erlauben. Verweigert ein
  Dienst das Einbetten, öffnet der Eintrag in einem neuen Fenster (wie heute bei
  Partner-Links).

### 5.6 Schalter und Umschalten

- Bis 0.90.0: „Neue Oberfläche ausprobieren“ im Benutzermenü (je Benutzer) und
  eine Firmen-Vorgabe in den Core-Einstellungen. Alle neuen Teile sind nur im
  neuen Rahmen zu sehen; Inhalte, die auf Bausteine umgestellt sind, sehen in
  beiden Rahmen gleich aus.
- 0.90.0: alle bekommen den neuen Rahmen, der Schalter und der klassische
  Rahmen entfallen.

## 6. Teilpakete (Reihenfolge)

Jedes Teilpaket ist eine eigene Korrekturversion mit eigenem Abschnitt im Plan,
Handbuch, CHANGELOG, Rechten und Prüfung in beiden Stilen, hell/dunkel und allen
Breiten.

1. **Gestaltungsgrundlage** – Werte-Datei, Auslagern von CSS/JS, Wächter-Tests,
   Bausteinkatalog. Für Benutzer unsichtbar bis auf den schnelleren Aufbau.
2. **Neuer Rahmen hinter dem Schalter** – Kopfleiste, Bereichsleiste,
   Reiterleiste, Menüband (Grundfunktionen), rechte Leiste, Statusleiste mit
   Update-Stand und Meldungen, Strg+K, Handy. Alle Seiten laufen unverändert im
   neuen Rahmen.
   **2b. Rahmen-Umbau** nach den Vorgaben vom 26.11.2026 (Abschnitt 11,
   Einordnung in 11.8) – vor Teil 3.
3. **Vorgangslisten und Detail** – Störungen, Tickets, Aufgaben, Wartung,
   Rüstzeiten auf Liste und Detail nach 5.3, Vorgänge als Reiter, Aktionen ins
   Menüband.
4. **Startseite** – Schnellstart je Rolle, Rollen-Vorgaben, Anpassen im Menüband.
5. **Dienste (Ingress)**.
6. **Übrige Seiten** – Bereich für Bereich auf die Bausteine, feste Farben auf
   null; Auswahlfelder erweitern, sobald „Stammdaten zusammenführen“ da ist.
   Mehrere Korrekturversionen.
7. **0.90.0** – Umschalten für alle, klassischen Rahmen entfernen, Handbuch
   vollständig, Abnahme im Browser.

## 7. Rechte (Vorschlag von Claude)

- Schalter „Neue Oberfläche ausprobieren“: jeder Benutzer für sich, kein Recht.
- Firmen-Vorgabe des Rahmens, Vorgaben der Startseite je Rolle, Menü-Vorgabe
  der Firma: neues Recht `system.ui_defaults` („Oberfläche: Vorgaben für Firma
  und Rollen“), per Migration an alle Rollen, die heute `manage_roles` haben.
- Zusatzdienste: neue Rechte `services.view` (Bereich „Dienste“ sehen) und
  `services.admin` (Dienste anlegen, ändern, löschen; Bereichsadministrator);
  `services.view` per Migration an alle Rollen, die Nextcloud bzw. Grafana
  heute sehen.
- Strg+K, Menüband, Reiter zeigen nur, was die vorhandenen Rechte erlauben –
  keine neuen Rechte.

## 8. Messbare Ziele

- Seitengröße (HTML je Aufruf, angemeldet) sinkt um mindestens 150 KB.
- Erster Aufruf bei 4× gedrosselter CPU bleibt ≤ 1,5 s (Messskript
  `deploy/lab/seitenaufbau/messung.js`), wiederholte Aufrufe werden schneller.
- Feste Farbwerte in Vorlagen: nur sinkend, 0 bei 0.90.0.
- Jede Seite aus `navDefs` ist im neuen Rahmen erreichbar (Test).
- Keine Überläufe und Konsolenfehler in Fiori/Fluent × hell/dunkel × 320–1366 px.

## 9. Dem Bauenden überlassen

- Genaue Werte der Gestaltung (ausgehend von der Vorschau), Aufbau der
  Werte-Datei und wie daraus CSS wird.
- Dateinamen, Vorlagen-Aufteilung, wie Seiten ihre Menüband-Aktionen melden.
- Wo die geöffneten Vorgangs-Reiter gespeichert werden und wie viele es
  höchstens sind.
- Breite, ab der das Detail neben der Liste steht.
- Weitere Tastenkürzel neben Strg+K.

## 10. Festlegungen nach der Prüfung (Entscheidungen von Claude)

Ein unabhängiger Prüfer hat den Entwurf aus Sicht des Bauenden gelesen. Diese
Punkte hat Claude festgelegt; der Anwender kann sie beim Lesen korrigieren.

1. **Aktionen in der Übergangszeit:** Jede umgestellte Seite meldet ihre
   Aktionen **einmal** im Code (Name, Symbol, Recht, Ziel). Der neue Rahmen
   zeigt sie im Menüband, der alte Rahmen als Knopfreihe im Seitenkopf – aus
   derselben Meldung, also keine doppelte Pflege. Nicht umgestellte Seiten
   behalten im neuen Rahmen ihre Knöpfe im Inhalt; ihr Menüband zeigt nur die
   allgemeinen Reiter (Ansicht, Anpassen, Darstellung) und „Neu“.
2. **Navigation:** Jeder Seitenwechsel lädt die Seite wie heute ganz neu (kein
   Fragmenttausch des ganzen Rahmens – die vielen Seitenskripte würden sonst
   brechen). Schnell wird es durch die zwischengespeicherten CSS/JS-Dateien aus
   Teilpaket 1. Geöffnete Vorgangs-Reiter merkt sich der Browser je Benutzer
   (lokaler Speicher), Statusmeldungen und Update-Stand liefert der Server bei
   jedem Aufruf neu, der Copilot-Verlauf liegt wie heute am Server. Ein
   Dienst im Mittelrahmen lädt beim Zurückkehren neu.
3. **Eigene Menüs (Entscheidung des Anwenders, 25.11.2026: „Bereiche gleich,
   Favoriten“):** Die 9 Bereiche sind für alle gleich. Jeder kann die
   Reihenfolge der Bereiche ändern und Lieblingsseiten anheften; Favoriten
   stehen oben in der Bereichsleiste. Eigene Gruppen von heute entfallen im
   neuen Rahmen; ihre Seiten stehen in ihren Bereichen (im alten Rahmen bleibt
   das eigene Menü bis 0.90.0). Seiten aus `navDefs`, die die Tabelle in 5.2
   nicht nennt, ordnet der Bauende dem passenden Bereich zu; der Test „jede
   Seite erreichbar“ prüft die vollständige Zuordnung.
4. **Feste Farben – Geltungsbereich:** Der Wächter zählt alle Vorlagen. Ziel 0
   bei 0.90.0 gilt für alles, was in einem der beiden Rahmen oder mit eigenem
   Seitenkopf im Browser erscheint (auch Terminal, Easy, Leitstand, Anmeldung –
   dort über die Gestaltungswerte). **Ausnahmen** (feste Liste im Test):
   E-Mail-Vorlagen (Mailprogramme kennen keine Variablen), Druckvorlagen
   (Etiketten, Kanban-Druck, Wartungsprotokoll) und die Handbuch-Skizzen.
5. **Zusatzdienste technisch:** Ein Dienst-Eintrag ist eine https-Adresse, die
   ein Inhaber von `services.admin` einträgt; das PDH lädt sie nur im Browser
   (iframe), es holt nichts selbst ab (kein Abrufen fremder Adressen durch den
   Server). Die erlaubten Einbett-Ziele (`frame-src`) ergeben sich aus den
   eingetragenen Diensten. Grafana bleibt hinter dem vorhandenen Weg
   `/grafana`. Nextcloud braucht dafür ihre Erlaubnis zum Einbetten
   (`frame-ancestors` mit der PDH-Adresse); kommt die Anmeldung im Rahmen nicht
   an (fremde Adresse, Cookies), bleibt „In neuem Fenster öffnen“. Ohne
   eingetragenen Dienst gibt es den Bereich „Dienste“ nicht – Zusatzdienste
   bleiben eine Option.
6. **Startseite je Rolle:** Es gilt die Vorgabe der Rolle des Benutzers; hat
   die Rolle keine, die Firmen-Vorgabe von heute. Das PDH bringt für die
   vorhandenen Rollen Vorschläge mit (Kacheln wie in der Vorschau:
   Instandhalter, Schichtleiter, Einkauf, Administrator), die ein Administrator
   ändern kann. Wer seine Startseite schon angepasst hat, behält sie;
   „Zurücksetzen“ wirkt nur, wenn man es drückt.
7. **Schalter:** dreiwertig je Benutzer (nicht gesetzt → Firmen-Vorgabe, alt,
   neu). Die Firma kann die Vorgabe setzen, aber bis 0.90.0 nichts erzwingen;
   jeder kann zurück auf alt.
8. **Detail:** Jede Vorgangsart zeigt nur die Reiter, für die sie Inhalt hat –
   keine Angleichung der Fachfelder. Das Detail neben der Liste ist dieselbe
   Detailseite, als Ausschnitt geladen.
9. **Statusleiste beim Update:** Alle sehen, dass ein Update läuft und das PDH
   gleich kurz weg ist, samt Fortschrittsbalken; Einzelheiten (Server, Schritt,
   Versionen) nur mit dem Recht für Updates. Während des Neustarts fragt der
   Browser nur die eigene Adresse (`/health`) ab, keinen anderen Server.
10. **Untere Leiste am Handy:** die ersten vier Bereiche in der Reihenfolge des
    Benutzers (die aus der Vorgabe kommt; Vorgabe Start, Vorgänge, Anlagen,
    Material), der Rest unter „Mehr“.
11. **Assistenten (Vorgabe des Anwenders, 25.11.2026):** Der Neu-anlegen- und
    der Fertigstellen-Assistent (dazu der KVP-Assistent) dürfen in keinem Teil
    fehlen. Heute über `ui-overlays`; „Neu“ im Menüband und Strg+K öffnen den
    Neu-anlegen-Assistenten. Wandern die Seitenaktionen mit Teil 3 ins
    Menüband, bekommt die Detailseite dort „Fertigstellen“ (`pdhComplete`) –
    am Terminal und in den Listen bleiben die vorhandenen Knöpfe. Jeder Teil
    hält das mit einem Test fest (`TestFrameHasWizards`), und jede
    Browserprüfung öffnet beide Assistenten.

## 11. Nachtrag 26.11.2026 – Vorgaben des Anwenders zum Rahmen

Der Anwender hat am 26.11.2026 festgelegt, wie der neue Rahmen aufgebaut sein
muss („Was unbedingt in die neue Optik rein muss“). Diese Vorgaben gelten für
die ganze neue Oberfläche und **gehen Abschnitt 5.2 vor**, wo sie abweichen
(Kopfleiste, Reiterleiste, Reiter des Menübands, Ort des Benutzermenüs,
Copilot-Knopf). Der mit 0.82.65 gebaute Rahmen wird danach umgebaut
(Teilpaket 2b, siehe 11.8). Mit „Vorschlag“ markierte Punkte hat Claude
ergänzt; der Anwender kann sie korrigieren.

### 11.1 Kopfzeile

- In der Kopfzeile stehen **nur noch Logo, Überschrift (Seitentitel) und Uhr**.
- Alles andere zieht um: Suchzeile und Copilot in das Menüband „Start“,
  Benutzermenü nach links unten (11.2), Stil und hell/dunkel nach
  „Ansicht › Darstellung“.
- Vorschlag: Benachrichtigungen, Chat und „Im Chat teilen“ stehen im Menüband
  „Start“, die Sprache im Benutzermenü.
- Die Uhr bleibt immer ganz sichtbar; wird es eng, kürzt sich nur der Titel.
  Das Ausblenden von Knöpfen (`pdhTopbarFit`, `data-tb-hide`) braucht der neue
  Rahmen dann nicht mehr. Der klassische Rahmen bleibt unverändert.

### 11.2 Benutzermenü und Anmelden

- **Immer fest links unten** (Fuß der Bereichsleiste): angemeldet das Bild mit
  Namen, das Menü öffnet nach oben; nicht angemeldet der Knopf „Anmelden“.
- Vorschlag: Bei eingeklappter Leiste nur das Bild bzw. das Symbol. Am Handy
  ist es der letzte Platz der unteren Leiste.

### 11.3 Menüband statt Reiterleiste

- **Über dem Menüband gibt es keine Reiterleiste mehr.** Die Unterseiten eines
  Bereichs sind Reiter des Menübands – eigene Menübänder mit den Funktionen
  der Unterseite.
- **Aufteilung von links nach rechts:**
  `Start` | Bänder des Bereichs | `Ansicht` | `Einstellungen` | `Hilfe`.
- **Start** steht immer ganz links, in jedem Bereich. Dort stehen die
  Standardfunktionen, die jeder darf – als Taste oder als Dropdown: „Neu“
  (neuer Vorgang, öffnet den Neu-anlegen-Assistenten), Suche (Strg+K),
  Copilot (Fenster rechts ein und aus), Zeiterfassung („Zeit starten“,
  „Manuell erfassen“, siehe 11.9). Dazu die Grundaktionen der Seite.
- **Bänder des Bereichs** (Beispiele des Anwenders):
  - Bereich Start: Bänder **Dashboard** und **Leitstand**, je nach Recht mit
    „Bearbeiten“, „Speichern“ und „Als Standard für alle setzen“.
  - Der Reiter **„Zuweisung“ entfällt**: Im Band „Start“ steht neben „Neu“ das
    Zuweisen („… zuweisen“, die offenen Vorgänge ohne Zuweisung) – sichtbar
    nur für die, die heute die Seite „Zuweisung“ sehen.
  - Bereich Vorgänge: Störungen, Tickets, Aufgaben, Wartung, Rüstzeiten,
    Stillstände, KVP und Projekte sind Bänder mit ihren Funktionen.
  - Die übrigen Bereiche entsprechend: Die Unterseiten der Tabelle in 5.2
    werden Bänder.
  - Ein Klick auf ein Band wechselt zur Unterseite und zeigt deren Funktionen
    (Seitenwechsel wie in 10.2).
- **Ansicht** hat zwei Dropdown-Unterpunkte: **Darstellung** (Stil, Modus,
  Farbschema, Dichte) und **Anpassen** (Bereiche ordnen, Favoriten, Startseite,
  Spalten). Die bisherigen eigenen Reiter „Anpassen“ und „Darstellung“
  entfallen.
- **Einstellungen**: die Einstellungen des jeweiligen Bereichs. Die heutigen
  Core- und Server-Einstellungen werden dem passenden Bereich zugeordnet;
  sichtbar ist nur, wofür das Recht da ist. Vorschlag: Der Bereich Verwaltung
  behält die Gesamtübersicht aller Einstellungen.
- **Hilfe**: Handbuch, Hilfe zu dieser Seite, Versionen & Updates.

### 11.4 Leitstand

- Der Leitstand ist ein **globales Fenster, das immer ohne Anmeldung
  erscheint**.
- Im Band „Leitstand“ bearbeiten die Berechtigten ihn **im mittleren Feld**
  (dem Listenfeld), nicht auf einer getrennten Verwaltungsseite.
- Vorschlag: Ohne Anmeldung zeigt der Rahmen den Leitstand im Mittelfeld,
  dazu Kopfzeile und links unten „Anmelden“. Für Wandbildschirme bleibt die
  Vollbild-Ansicht ohne Rahmen.

### 11.5 Copilot und Suche

- Das Copilot-Fenster (rechte Leiste) wird **aus dem Menüband „Start“**
  ein- und ausgeschaltet, ebenso die Suche. Strg+K bleibt.

### 11.6 Fußzeile

- **Rechts neben der Versionsnummer steht die Zahl der gerade angemeldeten
  Benutzer** (Form `V0.82.66 · 12/40`, siehe 11.10). Die Versionsnummer kommt damit auch in den neuen Rahmen (bisher
  fehlte sie dort).
- Vorschlag: Die Zahl sehen alle Angemeldeten; gezählt werden Personen mit
  Aktivität in den letzten Minuten (die vorhandene Anwesenheit,
  `presence.go`), ohne Terminals. Wer das ist, zeigt weiter die Seitenleiste
  „Aktivität“ mit ihrem Recht.

### 11.7 Was bleibt

Bereichsleiste mit 9 Bereichen und Favoriten (10.3), Strg+K, Statusleiste mit
Meldungen und Update-Stand, beide Assistenten (10.11), der Schalter
klassisch/neu bis 0.90.0, die Rechte aus Abschnitt 7.

### 11.8 Einordnung und offene Fragen

- **Teilpaket 2b „Rahmen-Umbau“** vor Teil 3: Kopfzeile, Benutzermenü links
  unten, Menüband mit den Bändern der Bereiche statt der Reiterleiste,
  Ansicht/Einstellungen/Hilfe, Copilot und Suche aus „Start“, Fußzeile mit
  Version und Benutzerzahl, Leitstand im Rahmen. Vorher die klickbare Vorschau
  anpassen und vom Anwender bestätigen lassen.
- Das Band „Einstellungen“ verweist in 2b auf die vorhandenen Abschnitte der
  Core- und Server-Einstellungen; das eigentliche Aufteilen der Seiten folgt
  Bereich für Bereich mit Teil 6.
- **Entschieden (Anwender 26.11.2026: „die Punkte 1–5 sehen vernünftig aus“):**
  Es gelten die Vorschläge – 1. geöffnete Vorgänge als Dropdown „Geöffnet“ im
  Band „Start“; 2. Leitstand an Wandbildschirmen weiter als Vollbild ohne
  Rahmen; 3. Benachrichtigungen, Chat und „Im Chat teilen“ im Band „Start“,
  Sprache im Benutzermenü; 4. die Zahl der angemeldeten Benutzer sehen alle
  Angemeldeten, wer es ist nur mit dem vorhandenen Recht; 5. „Zeit starten“
  und „Manuell erfassen“ im Band „Start“ in jedem Bereich.
- Ursprüngliche Fragen:
  1. Geöffnete Vorgänge als eigene, schließbare Reiter (Entscheidung „Beides“
     vom 24.11.2026): Wohin damit ohne Reiterleiste – Dropdown „Geöffnet“ im
     Band „Start“ (Vorschlag) oder eine schmale Zeile unter dem Menüband?
  2. Leitstand an Wandbildschirmen weiter als Vollbild ohne Rahmen?
  3. Benachrichtigungen, Chat und Sprache wie in 11.1 vorgeschlagen?
  4. Zahl der angemeldeten Benutzer für alle sichtbar?
  5. Zeiterfassung (11.9): Tasten „Zeit starten“ und „Manuell erfassen“ im
     Band „Start“ in jedem Bereich (Vorschlag, weil „immer zugänglich“) oder
     nur im Bereich Start?

### 11.9 Zeiterfassung (Ergänzung des Anwenders vom 26.11.2026)

- Heute öffnet ein Klick auf die Uhr die Zeiterfassung (`/time`).
- Neu: Die Zeiterfassung ist **immer zugänglich über Tasten im Band „Start“**:
  **„Zeit starten“** und **„Manuell erfassen“**.
- Die **Übersicht** der Zeiterfassung ist ein **neuer Menüpunkt direkt rechts
  neben „Start“**, sichtbar **nur in den Bereichen Start und Planung** –
  gegebenenfalls nur als Verweis auf die Seite Zeiterfassung im Bereich
  Planung.
- Läuft eine Zeit, zeigt die Taste stattdessen „Zeit stoppen“ mit der
  laufenden Dauer. Die Uhr in der Kopfzeile ist **nicht mehr anklickbar**
  (Anwender 26.11.2026: „muss nicht mehr über die Uhr zu öffnen sein“). Sichtbar ist das alles nur mit den vorhandenen Rechten der
  Zeiterfassung; kein neues Recht.

### 11.10 Rückmeldung zur Vorschau (Anwender 26.11.2026)

Zur Vorschau `2026-11-26-rahmen-start-vorschau.html`; bestätigt („ja“):
Benutzermenü links unten, Bänder des Bereichs Start, Ansicht, Einstellungen
und Hilfe. Geändert:

- **Uhr** ohne Funktion (siehe 11.9).
- **Band „Start“ ist nicht überall gleich**, sondern wächst mit Bereich, Seite
  und Recht: Neben den Standardfunktionen stehen dort die Hauptaktionen der
  offenen Seite – z. B. „Speichern“ und „Abbrechen“, während man in einer Liste
  bearbeitet, oder „Buchen“ im Materialbeleg. Die Aktionen kommen aus der
  einen Meldung je Seite (10.1); das Band der Seite zeigt weiter alle.
- **Ohne Anmeldung** ist nur der Leitstand zu sehen und links unten
  „Anmelden“ – kein Menüband, keine Bereiche, keine Benutzerzahl.
- **Vorgangslisten:** In jeder Zeile stehen **ganz oben Anlage
  (Infrastruktur) und Priorität**, darunter Titel und Nummer; innerhalb einer
  Anlage nach Priorität sortiert (Lesart von Claude).
- **Hinweis auf die Vorlage** sehen nur Gestaltungs-Administratoren (Recht
  `system.ui_defaults`) und nur beim Darüberfahren über den Seitentitel: welche
  Vorlage, wer sie zuletzt wann angepasst hat. Dafür wird bei jeder Zuordnung
  und jeder Firmen-Vorgabe gespeichert, wer und wann.
- **Fußzeile:** `V0.82.66 · 12/40` – angemeldet / aktive Benutzer (Lesart von
  Claude für „12/xx“), Erklärung im Tooltip.
- Die graue Leiste oben gehört nicht zum PDH: Sie steht für das Fenster
  (Minimieren, Maximieren, Schließen); die Schalter der Vorschau stecken dort
  im Knopf „Vorschau“.

### 11.11 PDH ohne Browserleisten (Anwender 26.11.2026)

- „Die graue Leiste ist der Fensterrahmen, ansonsten ist nichts mehr – PDH soll
  ohne Browserleisten starten.“ Über dem PDH steht nur noch der Rahmen des
  Fensters mit Minimieren, Maximieren und Schließen – keine Adresszeile,
  keine Browser-Reiter.
- Weg (Vorschlag von Claude, vom Anwender am 26.11.2026 für das erste Paket
  bestätigt): das PDH als **installierbare Web-App**. App-Beschreibung
  (`web/static/site.webmanifest`, `display: standalone`) und Service Worker
  (`web/static/sw.js`, für Push) gibt es schon; es fehlen `id`, `scope` und
  `lang`, ein Knopf „Als App installieren“ und die Anleitung im Handbuch
  (Plan `docs/superpowers/plans/2026-11-26-rahmen-start.md`, Task 9). In Edge und Chrome dann
  „App installieren“; das PDH startet aus dem Startmenü bzw. vom Desktop als
  eigenes Fenster. Kein eigenes Programm, das gepflegt werden müsste.
- **Voraussetzung:** HTTPS mit gültigem Zertifikat (oder `localhost`). Über
  `http://<Adresse>:8090` bietet der Browser die Installation nicht an – im
  Testverbund heute nur so erreichbar, auf 10.10.0.202 über den Namen mit
  Let's-Encrypt-Zertifikat möglich.
- Terminals bleiben im Kioskbetrieb wie heute.
- Das Handbuch bekommt „So installierst du das PDH als App“; der Eintrag
  „App installieren“ im Benutzermenü (heute die Handy-App) nennt beides.

### 11.12 Rückgängig, Speichern und Verwerfen (Anwender 26.11.2026)

- **„Rückgängig“ im Band „Start“** nimmt den letzten Bearbeitungsschritt
  zurück – Schritt für Schritt, bis zum endgültigen Speichern. Die Taste zeigt
  die Zahl der Schritte; nach dem Speichern ist sie leer. Strg+Z wie gewohnt.
- Ungespeicherte Änderungen werden **angezeigt** (Statusleiste „● 2 nicht
  gespeichert“, „Speichern“ hervorgehoben).
- **Beim Wechseln** (anderes Band, anderer Bereich, anderer Vorgang) **oder
  Verlassen** (Abmelden, Fenster schließen) fragt das PDH „Änderungen
  speichern?“ mit „Speichern“ und „Verwerfen“. **Wird nicht bestätigt**
  (Esc, daneben geklickt, Fenster geschlossen), **werden die Eingaben
  verworfen.** Lesart von Claude für „automatisches Speichern wird
  signalisiert“: Es wird nichts still im Hintergrund übernommen; das PDH
  meldet nur, dass etwas offen ist, und übernimmt erst mit Bestätigung.
- Gilt für alle Vorlagen, in denen man bearbeitet (Cockpit, Liste beim
  Bearbeiten in der Liste, Detail, Einstellungen, Tabelle/Zuordnung, Aktion).
  Schritte, die sofort wirken müssen (Zeit starten/stoppen, Buchen,
  Fertigstellen), sind keine Bearbeitungsschritte und bleiben wie heute.

## 12. Nachtrag 26.11.2026 – Seitenvorlagen und Zuordnung

Vorgabe des Anwenders (26.11.2026): Es gibt **wenige Seitenvorlagen, die immer
wieder verwendet werden** („ein Template für die Infocockpit-Seite, eine
Listen-Seite, eine Einstellungs-Seite, eine Planungs-/Kalender-Seite …“). Jede
Seite des PDH wird einer Vorlage zugeordnet. Zugeordnet wird **unter
„Anpassen“ wie bei Microsoft**, dazu gibt es **unter „Ansicht“ eine
Verwaltungsseite**, die die Zuordnung festlegt. Gebaut wird **mit dem Bereich
Start zuerst**, nicht jede alte Seite einzeln vorab.

### 12.1 Die Vorlagen

Eine Vorlage ist das ganze mittlere Feld samt ihrem Menüband: gleiche Stellen,
gleiche Tasten, gleiche Namen. Die Bausteine aus 5.3 (Liste, Detail) gehen
darin auf.

| Vorlage | Wofür | Tasten im Band (je nach Recht) |
|---|---|---|
| **Cockpit** (Infocockpit) | Kacheln und Widgets im Raster: Dashboard, Leitstand, Schnellübersicht, Statistik, Übersichten der Bereiche | Bearbeiten, Speichern, Als Standard für alle setzen, Widget hinzufügen |
| **Liste** | Alle Listen mit Filter, Gruppierung nach Infrastruktur und Detail daneben: Vorgänge, Ersatzteile, IT-Assets, Benutzer … | Neu, Öffnen, Zuweisen, Exportieren, Drucken, Spalten |
| **Detail** | Ein Vorgang oder Datensatz: Kopf und Reiter | Speichern, Zuweisen, Warten, Fertigstellen, Drucken |
| **Einstellungen** | Optionen in Abschnitten: Core- und Server-Einstellungen je Bereich, Regeln | Speichern, Zurücksetzen, Testen (wo es eine Verbindung gibt) |
| **Planung** (Kalender) | Kalender, Planer, Zeitstrahl, Jahresplan, Schichtplan | Heute, Tag/Woche/Monat, Neu, Besetzen |
| **Baum** (Vorschlag) | Infrastruktur, Lagerorte, Organigramm – Baum links, Detail rechts | Neu, Unterknoten, Verschieben |
| **Dienst** | Zusatzdienst im Mittelfeld (5.5) | Neu laden, In neuem Fenster öffnen |
| **Aktion** (agieren) | Eine Tätigkeit ausführen: Zeit eintragen, Material buchen, Rüstzeit, Störung melden – große Bedienflächen, wenige Felder, auch am Terminal | Buchen, Abbrechen, Nachtragen |
| **Druck** | Drucken mit Vorschau: Etiketten, Wartungsprotokoll, Kanban, Berichte | Drucken, Drucker wählen, Format, PDF |
| **Tabelle/Zuordnung** | Tabellen- und Zuordnungsvorschau bearbeiten: Import, Export, Abgleich, Feldzuordnungen | Vorschau, Spalte zuordnen, Prüfen, Übernehmen |

Weitere Vorlagen entstehen im Lauf der Umstellung (Anwender 26.11.2026);
jede neue kommt in diese Tabelle.

Die Assistenten (10.11) bleiben eigene Fenster über der Seite.

### 12.2 Erste Zuordnung der heutigen Seiten (Vorschlag von Claude)

| Bereich | Cockpit | Liste (+ Detail) | Planung | Einstellungen | Baum / Dienst |
|---|---|---|---|---|---|
| Start | Dashboard, Leitstand | Zuweisen, Zeiterfassung-Übersicht | – | – | – |
| Vorgänge | – | Störungen, Tickets, Aufgaben, Wartung, Rüstzeiten, Stillstände, KVP, Projekte | – | Regeln je Vorgangsart | – |
| Planung | – | Zeiterfassung, Anwesenheit | Planer, Kalender | – | – |
| Anlagen | Schnellübersicht | IT-Assets, VPN, ESPPDH, Messstellen | – | – | Infrastruktur (Baum) |
| Material | – | Ersatzteile, Beschaffung, Bestellvorgänge, Hersteller & Lieferanten | – | – | Lager (Baum) |
| Wissen | – | Dokumente, Vorlagen & Signaturen, Schulungen | – | – | – |
| Daten | Statistik | Datennetz (Import, Export, Abgleich: Vorlage Tabelle/Zuordnung) | – | – | – |
| Verwaltung | – | Benutzer, Abteilungen, Drucker, Geräte, Leitstände, Infobildschirm, Kostenstellen, Kategorien, Symbole | – | Core, Server, Netzwerk, Datensicherung, Bereinigung, Rollen & Rechte | Organigramm (Baum) |
| Dienste | – | – | – | – | Nextcloud, Grafana … (Dienst) |

Der Bauende prüft die Tabelle gegen `navDefs`; der Test „jede Seite
erreichbar“ (Abschnitt 8) verlangt zusätzlich, dass jede Seite genau eine
Vorlage hat.

### 12.3 Zuordnen (vom Anwender entschieden 26.11.2026: „wird durch die Firma festgelegt“)

- **Die Zuordnung legt die Firma fest**, nicht der einzelne Benutzer: auf der
  Verwaltungsseite unter „Ansicht“ (Recht `system.ui_defaults`) – ein Fenster
  wie „Menüband anpassen“ bei Microsoft: links alles, was es gibt (Seiten und
  Funktionen), rechts die Bereiche und ihre Bänder; mit Pfeilen oder Ziehen
  zuordnen, umbenennen, Reihenfolge ändern, „Zurücksetzen“ auf die Vorgabe des
  PDH.
- **Der einzelne Benutzer** ändert unter „Ansicht › Anpassen“ weiter nur
  Reihenfolge der Bereiche und Favoriten (Festlegung 10.3 bleibt).
- Rechte gehen immer vor: Jeder sieht nur, was er darf; niemand bekommt durch
  eine Zuordnung Zugriff.

### 12.4 Vorgehen

1. Klickbare Vorschau anpassen: Rahmen nach Abschnitt 11 und der Bereich
   Start mit der Vorlage Cockpit – vom Anwender bestätigen lassen. Erstellt
   am 26.11.2026: `docs/superpowers/specs/2026-11-26-rahmen-start-vorschau.html`
   (zeigt dazu je eine Seite jeder Vorlage, die Zuordnung der Firma und den
   Leitstand ohne Anmeldung; die untere Leiste am Handy fehlt noch).
2. **Bereich Start zuerst** (Teilpaket 2b): Rahmen-Umbau, Vorlage Cockpit
   mit Dashboard und Leitstand, Tasten Neu, Zuweisen, Zeiterfassung, Suche,
   Copilot.
3. Zuordnen („Anpassen“ und Verwaltungsseite).
4. Danach Bereich für Bereich, jeweils mit der Vorlage, die er braucht:
   Vorgänge (Liste und Detail – bisher Teil 3), Planung (Kalender),
   Verwaltung und die Einstellungen der Bereiche, dann der Rest. Die alten
   Seiten werden dabei bereichsweise umgestellt.

### 12.5 Offene Fragen (Anwender)

6. ~~Zuordnen~~ – entschieden: die Firma legt fest.
7. ~~Vorlagenliste~~ – ergänzt um Aktion, Druck, Tabelle/Zuordnung; weitere
   entstehen im Lauf der Umstellung.
