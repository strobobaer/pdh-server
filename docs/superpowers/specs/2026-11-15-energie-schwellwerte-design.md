# Schwellwerte und Alarme an Messstellen – Entwurf

Stand 2026-11-15 · Version **0.80.15** · Grundlage: Efficio-Entwurf
(`2026-11-15-energie-efficio-design.md`, Abschnitt „Schwellwerte und Alarme“, vom
Anwender freigegeben) und Gespräch vom 15.11.2026. Was der Anwender gesagt hat, ist
als „(Anwender)“ markiert; alles andere ist ein Vorschlag des Entwurfs.

## Absicht

Stromspitzen und Zeichen für einen nötigen Werkzeugwechsel sollen nicht im
Datenstrom untergehen (Anwender). Je Messstelle legt man Schwellwerte fest. Wird
einer gerissen, entsteht ein Ticket für den Verantwortlichen, der den Grund des
Alarms beschreiben muss, bevor er das Ticket schließt. So bleibt nachvollziehbar,
warum es zu Spitzen kam.

## Ziele

- **Schwellwerte je Messstelle** (echt und virtuell, Anwender): beliebig viele je
  Messstelle. Je Schwellwert: Bezeichnung, Bedingung **über** oder **unter**,
  Grenzwert (in der Einheit der Messstelle), **Überwachungszeitraum** in Minuten
  (z. B. 1 oder 15, Anwender), **Rückschaltzeit** in Minuten (Vorgabe **15**,
  Anwender), Ticket-Tag (Anwender), Priorität (Vorgabe „mittel“), Verantwortlich,
  aktiv ja/nein.
- **Auswertung je Zeitraum** (Anwender, Efficio-Entwurf):
  - Momentanwert (`instant`): Mittelwert der Minutenwerte im Zeitraum.
  - Zählerstand (`counter`): Menge im Zeitraum = Wert am Zeitraumende minus Wert am
    Zeitraumanfang.
  - Verbrauch (`consumption`): Summe der Minutenwerte im Zeitraum.
  - Der Grenzwert gilt in der Einheit der Messstelle, z. B. „über 50 kWh in
    15 Minuten“.
- **Ablauf eines Alarms:**
  1. Ein Zeitraum erfüllt die Bedingung → Alarm beginnt (Zustand „Alarm“).
  2. Es entsteht **ein Ticket** (Anwender) mit dem eingestellten Tag, der Anlage der
     Messstelle, Priorität und Verantwortlichem; dazu eine Nachricht an den
     Verantwortlichen.
  3. Solange der Alarm läuft, entstehen keine weiteren Tickets (Anwender).
  4. **Rückschaltung** (Anwender): Der Alarm endet erst, wenn die Bedingung
     **15 Minuten am Stück** (Rückschaltzeit) nicht mehr erfüllt ist.
  5. Kommt danach eine neue Überschreitung, während das Ticket des letzten Alarms
     **noch offen** ist, bekommt dieses Ticket nur einen Eintrag („Erneut
     überschritten am 15.11.2026 10:42 – Wert 63 kWh, Grenze 50 kWh“) und der
     Verantwortliche eine Nachricht; es entsteht kein neues Ticket (Anwender).
     Ist das Ticket erledigt oder geschlossen, entsteht ein neues.
- **Ursache als Pflicht** (Anwender: „Eine Beschreibung des Grundes des Alarms“):
  Alarm-Tickets lassen sich nur erledigen, schließen oder archivieren, wenn das
  vorhandene Feld **„Ursache“** (`tickets.root_cause`) ausgefüllt ist. Das gilt auf
  allen Wegen: Erledigen-Dialog, Statusänderung, JSON-Schnittstelle, Schnell-
  Erledigen/Archivieren. Meldung: „Bitte beschreiben, warum der Alarm kam (Feld
  Ursache).“ Die vorhandene Maßnahmen-Pflicht beim Erledigen bleibt. Normale
  Tickets bleiben unverändert.
- **Verantwortlich** je Schwellwert: ein Benutzer **oder** eine Gruppe; leer = der
  Verantwortliche der Anlage (vererbt über `infrastructure_responsible`). Ist
  niemand ermittelbar, geht das Ticket ohne Zuständigen an die Anlage und die
  Nachricht an die Bereichsadministratoren der Energiedaten (`energy.admin`).
  Benutzer → `responsible_to`/`assigned_to` des Tickets; Gruppe →
  `assigned_group_id`, Nachricht an alle aktiven Mitglieder.
- **Nachricht** über den vorhandenen Weg (`systemNotify`: Chat vom „PDH-System“ und
  Mail für Benutzer mit Mail-Benachrichtigung). Keine doppelte Nachricht durch die
  allgemeine Benachrichtigung über neue Datensätze.
- **Oberfläche:**
  - Messstellen-Übersicht (`/energy/meters`): neue Spalte „Schwellwerte“ mit Anzahl
    und offenen Alarmen (Efficio-Entwurf); Filter „mit offenem Alarm“.
  - Je Messstelle eine Liste der Schwellwerte mit Anlegen, Bearbeiten, Aktiv-
    schalter, Zustand (normal / Alarm seit … / zurückschaltend seit …) und Link zum
    aktuellen Ticket. Löschen nur Bereichsadministrator.
  - Ticket-Ansicht: Alarm-Tickets zeigen einen Hinweis „Alarm-Ticket – Ursache
    nötig“ und das Feld Ursache gut sichtbar beim Erledigen/Schließen.
- **Rechte:** neues Recht `energy.thresholds` „Schwellwerte pflegen“ (Kategorie wie
  die übrigen `energy.*`), per Migration an alle Rollen mit `energy.edit` (Abteilung
  übernehmen wie Migration 161). Ansehen der Schwellwerte mit `energy.view`.
  Löschen nur `energy.admin`. Tickets mit den vorhandenen Ticket-Rechten.
  Datensatzbezug über die Anlage der Messstelle (`h.hasPermRec`).
- **Verbund:** Nur der aktive Server prüft (Start im selben Block wie
  `StartEnergySampler`), bei Wartungssperre und auf dem Spiegel nichts. Der Zustand
  je Schwellwert liegt in der Datenbank; Neustart und Rollentausch setzen dort fort.
- Handbuch (Kapitel Energiedaten + Tickets), Änderungsprotokoll 0.80.15, `VERSION`,
  Fiori/Fluent hell/dunkel.

## Nicht-Ziele

- Rückschaltung über Prozent- oder Wertabstand (Hysterese in %); nur Zeit.
- Eskalation, wiederholender Push-Alarm bis zur Quittierung (gibt es für „Anlage
  steht“, hier nicht).
- Diagramme/Auswertung der Alarme; Schwellwerte an Rohwerten von Import-
  Zuordnungen (nur Messstellen).
- Änderungen an Export und Erfassung (bleiben unberührt; ein Fehler in der
  Alarm-Prüfung hält sie nicht auf – Efficio-Entwurf).

## Anti-Ziele

- Ticketflut: eine pendelnde Maschine darf nicht Dutzende Tickets erzeugen.
- Ein Alarm-Ticket, das ohne Begründung verschwindet.
- Fehlalarme durch fehlende Daten (Gerät offline) – fehlen Werte im Zeitraum, wird
  der Zeitraum nicht bewertet und der Zustand bleibt, wie er ist.

## Vorgehen (gewählt: B)

| | Weg | Bewertung |
|---|---|---|
| A | Prüfung im Minuten-Durchlauf des Samplers | wenig Code, aber Fehler beim Ticket/Benachrichtigen hängen an der Erfassung – widerspricht „getrennt, hält nichts auf“ |
| **B** | **Eigener Alarm-Wächter** jede Minute kurz nach dem Sampler, liest `energy_values`, Zustand je Schwellwert in der DB | **gewählt (Anwender)**: getrennt, robust bei Neustart/Rollentausch, holt verpasste Minuten nach; bis 1 min Verzögerung |
| C | Datenbank-Trigger auf `energy_values` | verworfen: keine Nachrichten aus der DB, Tickets umständlich |

## Festgelegtes zum Wie

- Neues Paket bzw. Datei im Paket `internal/modules/energie` (Wächter `alarms.go`
  o. ä.), Verdrahtung wie `energy_wire.go` (`StartEnergyAlarms`, Start in
  `cmd/server/main.go` neben `StartEnergySampler`, `Skip = h.MaintLocked`, nichts
  bei `Mirror().On`).
- Migration **163**: Tabelle der Schwellwerte (FK auf `energy_meters`, beim Löschen
  der Messstelle mit), Zustand je Schwellwert (Zustand, Alarm seit, unter Grenze
  seit, letzter geprüfter Zeitpunkt, Ticket), Kennzeichen am Ticket, dass es ein
  Alarm-Ticket ist (z. B. `tickets.requires_root_cause boolean` oder Herkunft),
  Recht `energy.thresholds`.
- Ticket anlegen über `tickets.Service.CreateInTx`/`AfterCreate` mit dem
  System-Benutzer; `CreateInput` um die Gruppe erweitern. Nur ein Ticket je
  Alarm auch bei parallelen Läufen (Zeilensperre wie Rüst-Überwachung).
- Der Wächter wertet jede noch nicht geprüfte Minute in Reihenfolge aus (Nachholen
  nach Ausfall, höchstens 24 h zurück); der Zeitraum endet jeweils an dieser Minute
  (gleitend).
- Festlegungen des Entwurfs (nach Prüfung, vom Anwender zu bestätigen):
  - Ein Zeitraum wird nur bewertet, wenn **jede Minute** darin einen Wert hat;
    sonst bleibt der Zustand unverändert (auch die Rückschaltzeit läuft dann nicht
    weiter).
  - Zählerstand mit negativer Menge im Zeitraum (Zählertausch, Rücksprung): Zeitraum
    nicht bewerten.
  - „Offen“ heißt Ticket-Status `open`, `in_progress` oder `pending` und nicht
    archiviert; `resolved`/`closed` gelten als erledigt.
- Ticket-Text: Titel „Schwellwert „<Bezeichnung>“ <überschritten|unterschritten>:
  <Messstelle>“, Beschreibung mit Wert, Grenze, Zeitraum, Zeitpunkt, Link zur
  Messstelle.

## Dem Umsetzer überlassen

Tabellen- und Spaltennamen, ob die Schwellwerte als eigene Seite oder Bereich der
Messstellen-Bearbeitung erscheinen (vorhandene Muster nutzen), wie die
Ursachen-Pflicht technisch in `UpdateStatus`/`Resolve`/`QuickResolve`/API eingebaut
wird (eine gemeinsame Prüfung), genaue Wortlaute der Oberfläche.
