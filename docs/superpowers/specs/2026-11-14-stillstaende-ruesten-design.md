# Rüstzeiten erfassen (Zusatzfunktion)

Stand: 14.11.2026 · Zweig `feature/stillstaende-ruesten`

## 1. Ziel und Zweck

Rüstzeiten sind **Stillstände einer Anlage, die zur Produktion gehören** und
keine Defekte sind. Der Anwender nennt drei Gründe:

- **Produktumstellung** (z. B. Formatwechsel),
- **produktionsbedingter Tausch von Hilfsmitteln** (z. B. Werkzeuge, Düsen),
- **Reinigung**.

Sie sollen **gesondert gekennzeichnet** werden, damit sie später in
**Verfügbarkeitsanalysen** getrennt von Störungen eingehen. Eine Rüstzeit löst
**weder Ticket noch Störmeldung** aus und benachrichtigt niemanden.

**Umfang dieses Pakets (Vorgabe des Anwenders, 14.11.2026):** Es ist eine
Zusatzfunktion. Zuerst kommt nur die **Eingabe**:
- im **PDH** so, wie man Tickets erfasst (anlegen, Liste, Detailseite,
  bearbeiten),
- im **Easy-Modus** an der Anlage mit eigenen Rüsttasten.

**Zurückgestellt (eigene spätere Vorhaben):**
- Übernahme aus dem Fremdsystem über den Abgleich.
- Auswertung der Verfügbarkeit (Datenansichten ohne Doppelzählung).
- Die Analyse, die Stillstände aus MQTT-Zuständen, Produktionsplan, Schicht-
  und Pausenzeiten mit Toleranzen und Karenzzeiten erkennt und dem
  Produktionsverantwortlichen zur Begründung vorlegt (begründen oder an die
  Instandhaltung weitergeben).

Damit diese Vorhaben später ohne Umbau aufsetzen, wird die Rüstzeit als erste
Art eines allgemeinen **Stillstands mit Ursache** gespeichert (siehe 5 und 6).

## 2. Entscheidungen des Anwenders (14.11.2026)

1. **Mehrere Tasten je Anlage**, eine je Rüstart, jede **eigens benannt**
   (z. B. „Formatwechsel“, „Düsen tauschen“, „Reinigung“).
2. **Ein Tipp startet sofort**; die Seite zeigt „… läuft seit 10:42“ und die
   Taste **„Fertig“**. Eine Notiz ist freiwillig.
3. Im PDH Eingabe „wie Tickets“.
4. Das Wie überlässt der Anwender („so, wie ein Analyst es bauen würde“).
   Festlegungen ohne diesen Vermerk sind Entscheidungen des Entwurfs.

## 3. Ziele, Nicht-Ziele, Fehlschläge

**Ziele**
- Rüstzeiten einer Anlage mit Beginn, Ende und Grund erfassen – an der Anlage
  in einem Tipp, im PDH per Formular.
- Korrigieren und nachtragen im PDH, jede Änderung im Verlauf.
- Rüsttasten je Anlage frei benennen und zuordnen.
- Daten so ablegen, dass Fremdsystem, Auswertung und Analyse später ohne
  Umbau dazukommen.

**Nicht-Ziele (dieses Paket):** Fremdsystem, Auswertungsansichten, Analyse,
Stillstandszeiten von Störungen.

**Fehlschläge (auch wenn alles technisch läuft)**
- Eine Rüstzeit löst eine Benachrichtigung, ein Ticket oder eine Störmeldung
  aus.
- An einer Anlage laufen zwei Rüstzeiten gleichzeitig.
- Ein vergessenes „Fertig“ läuft unbemerkt über Tage weiter.
- Das Erfassen an der Anlage braucht mehr als einen Tipp.
- Ohne eingerichtete Rüsttasten ändert sich am Easy-Modus oder an der
  Anlagenseite etwas.

## 4. Rahmen

- Go, html/template, htmx, Postgres. Regeln aus `AGENTS.md`: Handbuch,
  Änderungsprotokoll, beide Design-Stile, Rechte mit Abteilungsebene und
  Bereichsadministrator, Kopfleiste unverändert.
- Der Easy-Modus ist öffentlich und hat einen Missbrauchsschutz
  (`easyLimiter`).
- Vorbild für Katalog, Zuordnung zu Anlagen mit Vererbung, Reihenfolge und
  Übersetzung: die Meldetexte (`easy_report_texts`, `infra_easy_texts`,
  `internal/web/easy_texts.go`).
- Vorbild für Liste, Anlegen und Detailseite: Tickets.
- Zeiten als Zeitpunkt (`timestamptz`).

## 5. Wege, die betrachtet wurden

| Weg | Inhalt | Bewertung |
|---|---|---|
| **1 – Stillstand mit Ursache** (gewählt) | Eigene Tabelle Stillstände (Anlage, Beginn, Ende, Grund, Herkunft); Gründe in einem Katalog mit fester Gruppe; Rüsttasten als benannte Einträge mit Zuordnung zu Anlagen. Oberfläche wie Tickets. | Kaum Mehraufwand gegenüber einer reinen Rüstzeit; Fremdsystem, Auswertung und Analyse setzen später ohne Umbau auf. |
| 2 – Rüstzeit als Vorgangsart wie Ticket | Nutzt Status, Zuständige, Feldsätze, Archiv der Vorgänge. | Vorgänge sind Arbeitsaufträge mit Benachrichtigung; eine Rüstzeit ist eine Zeitspanne. Die Analyse bräuchte später doch ein Stillstandsmodell. Verworfen. |
| 3 – Tätigkeit „Rüsten“ in der Zeiterfassung | Gibt es schon. | Misst Arbeitszeit von Personen, nicht Stillstand der Anlage. Verworfen. |

„Wie Tickets“ heißt deshalb: **gleiche Bedienung** (Liste, Anlegen,
Detailseite, Bearbeiten, Verlauf), nicht gleiche Datenart.

## 6. Aufbau

### 6.1 Gründe

Jeder Grund gehört zu einer **Gruppe**, die fest im Code steht und später die
Wirkung auf die Verfügbarkeit bestimmt. In diesem Paket gibt es nur die
Gruppe **`setup` „Rüsten“**; weitere Gruppen (geplant, technisch,
unbegründet) kommen mit der Analyse.

Die Migration legt drei Gründe an: **Produktumstellung**,
**Hilfsmittel-Tausch**, **Reinigung**. Gründe lassen sich umbenennen,
ergänzen und deaktivieren; löschen nur, solange keine Rüstzeit daran hängt.

### 6.2 Rüsttasten

Eine **Rüsttaste** hat
- **Name** (deutsch, frei, bis 60 Zeichen),
- **Grund** (Pflicht),
- **Höchstdauer** in Minuten (Vorgabe 480, siehe 6.5),
- **Reihenfolge** (für alle Anlagen gleich),
- aktiv/inaktiv.

Rüsttasten werden **Anlagen zugeordnet**, mit derselben Vererbung wie die
Meldetexte: an einer Halle oder Linie zugeordnet, gelten sie für alle
Anlagen darunter. Der **Katalog** liegt im Infrastruktur-Stamm neben den
Meldetexten, die **Zuordnung** in einem Reiter der Anlage neben „Meldetexte“.
Im Easy-Modus erscheint der Name übersetzt in der Sprache des Handys, wie bei
den Meldetexten.

Begründung (Entwurf): Der Grund ist fest an der Taste, damit eine spätere
Auswertung sauber nach Grund trennt, egal wie die Tasten heißen.

### 6.3 Rüstzeit (Datensatz)

| Feld | Inhalt |
|---|---|
| Anlage | Pflicht |
| Beginn | Pflicht |
| Ende | leer, solange sie läuft |
| Grund | Pflicht (daraus die Gruppe) |
| Rüsttaste | mit welcher Taste gestartet; der Name wird mitgespeichert und bleibt, auch wenn die Taste später umbenannt oder gelöscht wird |
| Herkunft | `easy` (an der Anlage), `manual` (im PDH); später `foreign`, `analysis` |
| Zustand | `running` (läuft), `done` (beendet), `open_end` (Höchstdauer überschritten, Ende fehlt) |
| Notiz | freiwillig, bis 1000 Zeichen |
| angelegt / geändert von | angemeldete Person oder „Service“ (Easy-Modus) |
| Verlauf | jede Änderung im Verlauf am Datensatz |

Regeln:
- **Höchstens eine laufende Rüstzeit je Anlage** (die Datenbank erzwingt es).
  Wird eine andere Rüsttaste gedrückt, während eine läuft, endet die laufende
  in diesem Moment und die neue beginnt im selben Moment – ohne Lücke, ohne
  Überlappung.
- Ende liegt nach Beginn; Beginn nicht in der Zukunft (5 Minuten Spielraum).
- Eine Rüstzeit gilt nur für **diese Anlage**, nicht für über- oder
  untergeordnete (Entscheidung des Entwurfs).

### 6.4 An der Anlage (Easy-Modus und Anlagenseite)

- Hat eine Anlage mindestens eine aktive Rüsttaste (direkt oder geerbt), zeigt
  der Easy-Modus unter „Störung +“ und „Ticket +“ einen Abschnitt
  **„Rüsten“** mit je einer Taste. Ohne Rüsttaste ändert sich nichts.

  ```
   Abfüllanlage 3
   ┌──────────────┐ ┌──────────────┐
   │  Störung +   │ │   Ticket +   │
   └──────────────┘ └──────────────┘
   Rüsten
   ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
   │ Formatwechsel│ │ Düsen tausch.│ │  Reinigung   │
   └──────────────┘ └──────────────┘ └──────────────┘
  ```

- **Ein Tipp startet** die Rüstzeit sofort (Herkunft `easy`, Ersteller
  „Service“). Keine Nachfrage.
- Läuft eine, steht oben **„Formatwechsel läuft seit 10:42“** mit
  **„Fertig“** und einem freiwilligen Notizfeld. „Fertig“ setzt das Ende auf
  jetzt. Eine andere Rüsttaste wechselt nahtlos (6.3).
- Kein Ticket, keine Störmeldung, keine Benachrichtigung.
- **Missbrauchsschutz** (Entscheidung des Entwurfs): eigene Grenze, die nicht
  gegen die Meldungen zählt – je Anlage höchstens 30 Rüst-Vorgänge (Start,
  Wechsel, Fertig) in 10 Minuten, unabhängig von der Adresse, weil sich Handys
  im Werks-WLAN oft eine Adresse teilen. Doppeltes Antippen derselben Taste
  innerhalb von 10 Sekunden startet nicht neu.
- **Angemeldete** landen beim QR-Code nicht im Easy-Modus, sondern auf der
  Seite der Anlage. Dort erscheint derselbe Abschnitt „Rüsten“ für jeden, der
  die Anlage sehen darf (Entscheidung des Entwurfs: Rüsten ist wie Melden kein
  Vorrecht); Ersteller ist dann die angemeldete Person. Ist der Easy-Modus
  abgeschaltet, gibt es Rüsten an der Anlage nur auf dieser Seite.

### 6.5 Vergessenes „Fertig“

- Jede Rüsttaste hat eine **Höchstdauer** (Vorgabe 480 Minuten); für im PDH
  ohne Ende angelegte Rüstzeiten gilt eine allgemeine Höchstdauer (Vorgabe
  480 Minuten).
- Ein Hintergrunddienst (nur am aktiven Server, ruht in der Wartungssperre,
  Takt etwa jede Minute) setzt laufende Rüstzeiten über der Höchstdauer auf
  **„Ende fehlt“** (`open_end`), das Ende bleibt leer (Entscheidung des
  Entwurfs: lieber eine sichtbare Lücke als eine erfundene Dauer).
- In der Liste stehen sie oben mit dem Hinweis „Ende fehlt“. An der Anlage
  kann trotzdem neu gerüstet werden.

### 6.6 Im PDH (wie Tickets)

- **Liste „Rüstzeiten“** (Navigation, Gruppe der Anlagen): Filter nach
  Anlage (mit Unteranlagen), Grund, Zustand und Zeitraum; Spalten Anlage,
  Grund, Taste, Beginn, Ende, Dauer, Herkunft, Zustand; Summe der Dauer über
  die Auswahl. Oben die Rüstzeiten mit „Ende fehlt“ und die laufenden.
- **Anlegen** (Herkunft `manual`): Anlage, Grund, Beginn, Ende oder Dauer,
  Notiz. Ohne Ende ist sie **laufend** – sie zählt für „höchstens eine
  laufende je Anlage“, eine Rüsttaste an der Anlage beendet sie nahtlos.
- **Detailseite** mit allen Feldern, Bearbeiten (Beginn, Ende, Grund, Notiz),
  „Beenden“ für laufende, Verlauf.
- **Reiter „Rüstzeiten“ an der Anlage** mit derselben Liste, auf die Anlage
  und ihre Unteranlagen eingeschränkt.
- **Löschen** nur durch den Bereichsadministrator.
- **Katalog** (Gründe, Rüsttasten) und **Zuordnung** wie in 6.1/6.2.

### 6.7 Rechte

Kategorie „Infrastruktur & Anlagen“.

| Schlüssel | Erlaubt |
|---|---|
| `downtime.view` | Rüstzeiten sehen (Liste, Reiter, Detail, Verlauf) |
| `downtime.edit` | anlegen, bearbeiten, beenden; Gründe und Rüsttasten pflegen und zuordnen |
| `downtime.admin` | Bereichsadministrator; löscht als Einziger Rüstzeiten, Gründe und Tasten |

Migration: Rollen mit `infrastructure.view` bekommen `downtime.view`; Rollen
mit `infrastructure.edit` zusätzlich `downtime.edit`; Rollen mit
Administratorrechten `downtime.admin`. Abteilung einer Rüstzeit ist die
Abteilung ihrer Anlage. Rüsten an der Anlage (6.4) braucht kein Recht.

(Die Schlüssel heißen `downtime.*`, weil sie später alle Stillstände
abdecken; in der Oberfläche heißen sie „Rüstzeiten“.)

### 6.8 Verbund

Der Dienst aus 6.5 läuft nur am aktiven Server. Alle Daten liegen in der
PDH-Datenbank und sind nach einem Rollentausch vorhanden.

## 7. Fehlerbilder

| Lage | Verhalten |
|---|---|
| „Fertig“ vergessen | nach Höchstdauer „Ende fehlt“, oben in der Liste |
| Zwei Tasten kurz nacheinander | nahtloser Wechsel, keine Überlappung |
| Gleichzeitige Tipps an derselben Anlage | genau eine Rüstzeit läuft (Datenbank) |
| Doppeltes Antippen | innerhalb 10 s kein Neustart |
| Anlage ohne Rüsttaste | Easy-Modus und Anlagenseite unverändert |
| Rüsttaste umbenannt oder gelöscht | vorhandene Rüstzeiten behalten Namen und Grund |
| Viele Handys hinter einer Adresse | eigene Grenze je Anlage, Meldungen unberührt |

## 8. Prüfung

- Tests: höchstens eine laufende je Anlage (auch gleichzeitig), nahtloser
  Wechsel, doppeltes Antippen, Grenze des Missbrauchsschutzes getrennt von
  Meldungen, Höchstdauer → „Ende fehlt“, Anlegen laufend/beendet,
  Bearbeiten mit Verlauf, Rechte mit Abteilungsebene, keine Benachrichtigung,
  Easy-Modus und Anlagenseite unverändert ohne Rüsttasten, Vererbung der
  Zuordnung.
- Oberfläche in Fiori und Fluent, hell und dunkel, 390 und 1366 px; Easy-
  Modus auf dem Handy.

## 9. Handbuch und Version

- Neues Kapitel „Rüstzeiten“ (So geht's: Rüsttasten anlegen und zuordnen, an
  der Anlage rüsten, im PDH erfassen und korrigieren). Abschnitt im
  Easy-Modus-Kapitel und im Kapitel der Anlage ergänzen.
- Änderungsprotokoll: nächste Korrekturversion über `main` (erwartet
  0.80.3) mit „Neu“ und „Sicherheit“ (neue Rechte).

## 10. Dem Bauenden überlassen

- Tabellen- und Spaltennamen, Migrationsnummer (nächste freie über `main`).
- Platz und Gestaltung von Liste, Detailseite und Reitern nach vorhandenen
  Mustern (Tickets, Reiter der Anlage, Meldetexte).
- Genauer Takt des Dienstes aus 6.5.
