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
