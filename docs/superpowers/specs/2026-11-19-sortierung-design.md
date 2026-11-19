# Sortieren in Historie, Maßnahmen und Kommentaren – Entwurf

Stand 19.11.2026. Auftrag des Anwenders: „in der Datenhistorie und Maßnahmen
wie auch den Kommentaren eine Sortierungsmöglichkeit einbauen – nach Datum,
nach Häufigkeit, nach Gleichung usw., komplett über das System durchgezogen“.
Geplant als eigene Version nach 0.82.1.

**Vom Anwender am 19.11.2026 freigegeben („3. Freigegeben“).** Die vier
Fragen am Ende blieben unbeantwortet und sind mit den Vorschlägen dieses
Entwurfs entschieden (siehe „Festlegungen“). Version 0.82.2.

## Bestand (am Code geprüft)

Alle diese Listen sind heute fest nach Datum sortiert, ohne Auswahl:

| Liste | Wo | Heute |
|---|---|---|
| **Datensatzhistorie** (Reiter „Historie“/„Änderungen“) | 13 Detailseiten: Störung, Ticket, Aufgabe, Wartung, Projekt, Rüstzeit, KVP, Lager, Ersatzteil, Partner, Bestellung, Benutzer, Anlage | neueste zuerst, nur die letzten 30, kein Blättern |
| **Anlagen-Historie** (Zeitleiste über alles an einer Anlage) | Anlagen-Detailseite | neueste zuerst, 200 Einträge; Filter nach Text, Zeitraum, Art gibt es schon |
| **Maßnahmen** | Störung, Ticket, Aufgabe, Wartung (je eine einfache Liste: Text, wer, wann); KVP (mit Verantwortlichem und Termin) | älteste zuerst; KVP nach Termin |
| **Kommentare** | Ticket, KVP, Anlage (in der Anlagen-Historie) | älteste zuerst; Anlage neueste zuerst |

Dazu etwa zwölf weitere Protokoll-Listen (Lagerbewegungen, Prüfprotokoll,
VPN-Ereignisse, Bau-Läufe, Sicherungen …), ebenfalls fest nach Datum.

Es gibt **keine** gemeinsame Sortier-Hilfe; nur die Messstellen-Tabelle
(Energie) hat anklickbare Spaltenköpfe. Die Listen sind auf vier Arten gebaut
(Vorlage, nachgeladener Teil, drei fast gleiche Skripte im Browser, im
Programm zusammengesetztes HTML). Für „gleiche Texte erkennen“ gibt es schon
eine Hilfe (`textmatch.Normalize`, vom Copilot und der Störungssuche genutzt:
Groß/Klein, Umlaute, Satzzeichen zählen nicht).

## Ziel

Überall dieselbe kleine Leiste über der Liste:

**Sortieren:** `Neueste zuerst` · `Älteste zuerst` · `Häufigste zuerst` · `Gleiches zusammen` – und, wo es die Angabe gibt, `Nach Person` und `Nach Art`.

- **Neueste / Älteste zuerst:** nach Zeitpunkt.
- **Häufigste zuerst:** Einträge mit gleichem Inhalt werden zu **einer Zeile
  mit Zähler** zusammengefasst („12× Filter gereinigt – zuletzt 14.11.2026“),
  die häufigste oben. Ein Klick klappt die einzelnen Einträge auf.
- **Gleiches zusammen:** dieselbe Zusammenfassung, aber alphabetisch nach dem
  Inhalt geordnet – zum Durchsehen, was es alles gibt.
- **Nach Person:** nach dem, der eingetragen hat (Name, darin neueste zuerst).
- **Nach Art:** in der Historie nach geändertem Feld bzw. Vorgang, in der
  Anlagen-Historie nach Art des Eintrags (Störung, Wartung, Maßnahme …).

Was „gleich“ heißt:

- Maßnahmen und Kommentare: gleicher Text nach der vorhandenen Angleichung
  (Groß/Klein, Umlaute, Satz- und Leerzeichen egal).
- Datensatzhistorie: gleicher Vorgang am gleichen Feld („Status geändert“,
  „Verantwortlicher geändert“) – so sieht man, was an einem Datensatz am
  häufigsten geändert wird.

Die gewählte Sortierung **merkt sich das PDH je Benutzer und Listenart**
(z. B. „Maßnahmen: Häufigste zuerst“) und nimmt sie auf allen Seiten dieser
Art wieder. Standard bleibt wie heute.

## Was sich sonst ändert

- Die Datensatzhistorie zeigt nicht mehr nur 30 Einträge: „Weitere laden“.
  Häufigkeit und Zusammenfassen rechnen immer über **alle** Einträge des
  Datensatzes, nicht nur die sichtbaren.
- Maßnahmen an Störung, Ticket und Aufgabe kommen künftig wie bei der Wartung
  fertig vom Server (ein gemeinsamer Baustein statt drei Skripten). Aussehen
  und Eingabe bleiben gleich.
- Kommentare an Ticket und KVP nutzen denselben Baustein.
- Gezählt wird nur, was der Benutzer sehen darf (Abteilungsebene der Rechte).

## Rechte, Handbuch, Version

- Kein neues Recht: Es werden nur Einträge anders geordnet, die der Benutzer
  ohnehin sieht. Die gemerkte Sortierung ist eine persönliche Einstellung.
- Eine Migration: Spalte für die gemerkten Sortierungen am Benutzer.
- Handbuch: ein kurzer Abschnitt „Listen sortieren“ im Kapitel zur Bedienung,
  Verweis in den Kapiteln Störungen, Tickets, Aufgaben, Wartung, KVP, Anlagen.
- Beide Stile, hell/dunkel; auf dem Handy wird die Leiste zu einem Auswahlfeld.

## Paketschnitt

1. **Paket A:** gemeinsame Sortier-Leiste und -Logik, gemerkte Auswahl;
   Datensatzhistorie (alle 13 Seiten) mit „Weitere laden“; Anlagen-Historie.
2. **Paket B:** Maßnahmen (Störung, Ticket, Aufgabe, Wartung, KVP) und
   Kommentare (Ticket, KVP) auf den gemeinsamen Baustein.
3. **Paket C:** die übrigen Protokoll-Listen (Lagerbewegungen, Prüfprotokoll,
   VPN-Ereignisse, Bau-Läufe, Sicherungen, Bereinigungsläufe …) – dort nur
   Datum, Person, Art; Häufigkeit ergibt dort wenig Sinn.

## Festlegungen (19.11.2026)

Der Anwender hat den Entwurf freigegeben, ohne die vier Fragen einzeln zu
beantworten. Es gelten daher die Vorschläge des Entwurfs; der Anwender kann
jede Festlegung noch ändern.

1. **„Nach Gleichung“ = „Gleiches zusammen“:** gleiche Einträge als eine Zeile
   mit Zähler, gleich nach der vorhandenen Angleichung. **Ähnliche** Texte
   („Filter gereinigt“ / „Filter gesäubert“) werden nicht zusammengelegt.
2. **Häufigkeit nur innerhalb der jeweiligen Liste.** Keine Zählung über die
   ganze Anlage, keine Auswertung „häufigste Maßnahmen und Ursachen“ – das
   wäre ein eigenes Paket.
3. **Umfang: Pakete A, B und C** („komplett über das System durchgezogen“).
4. **Keine weiteren Sortierungen** über Datum, Häufigkeit, Gleiches, Person
   und Art hinaus.
