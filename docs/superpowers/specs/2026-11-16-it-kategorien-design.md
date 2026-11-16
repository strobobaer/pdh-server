# IT-Kategorien pflegbar, Karenz je Kategorie und je Asset (0.80.19)

Stand: 16.11.2026 · Anwender: „baue es so wie du als Programmierer bauen würdest“ –
alle Entscheidungen unten sind von Claude getroffen und als solche markiert.

## Warum

Die Arten der IT-Assets (Server, Netzwerk, Workstation, Drucker, Telefon, Tablet,
Sonstiges) sind heute fest im Programm und in der Datenbank (Enum `it_asset_type`).
Der Betrieb braucht eigene Arten (z. B. Kamera, Steuerung, USV, Zugangskontrolle)
und je Art eine eigene Wartezeit bis zum Ausfall-Ticket des Netz-Abgleichs (0.80.17):
Eine Kamera darf kurz weg sein, ein Server nicht. Einzelne Geräte brauchen davon
abweichende Werte.

## Ziele

1. **Kategorien pflegen:** anlegen, umbenennen, Symbol, Reihenfolge, ausblenden,
   löschen (nur unbenutzt). Die bisherigen 7 Arten werden zu Kategorien.
2. **Karenz je Kategorie:** Minuten bis zum Ausfall-Ticket; leer = allgemeiner
   Wert von `/it/net`.
3. **Karenz je Asset:** auf der Karte „Netz“; leer = Wert der Kategorie.
   Wirksam: Asset → Kategorie → allgemein.
4. **„Neu übernommene überwachen“ je Kategorie:** ersetzt die feste Regel
   (Arbeitsplatz/Telefon/Tablet aus) aus 0.80.17; Vorgaben bleiben gleich.

## Nicht-Ziele

- Eine zweite Zeit (z. B. Erinnerung nach X weiteren Minuten) – *Entscheidung
  Claude: eine Zeit „Karenz bis zum Alarm“, wie auf `/it/net`.*
- Verantwortliche je Kategorie (bleibt allgemein auf `/it/net`).
- Unterkategorien / Baum.
- Die übergreifenden Schlagworte „Kategorien (#)“ – das ist ein anderes Konzept
  und bleibt unverändert.

## Anti-Ziele

- Bestehende Assets, Filter, Zähler, QR-Etiketten, Verweise oder die API
  verändern ihr Verhalten nach dem Update.
- Eine Kategorie lässt sich löschen, obwohl Assets sie nutzen.
- „Netzwerk“ verschwindet und der Abgleich kann Netzgeräte nicht mehr anlegen.

## Ansätze

| | Ansatz | Abwägung |
|---|---|---|
| **A (gewählt)** | Tabelle `it_asset_categories` mit festem Schlüssel; `it_assets.type` wird von Enum zu Text mit Fremdschlüssel auf den Schlüssel | Bestehender Code arbeitet weiter mit Schlüsseln (`'network'`, `'server'` …), Daten bleiben unverändert, neue Kategorien bekommen einen erzeugten Schlüssel. |
| B | Enum behalten, Unterkategorie daneben | weniger Umbau, aber „Kamera“ bliebe „Sonstiges › Kamera“; Karenz/Überwachung müsste an zwei Stellen gesucht werden. |
| C | Enum per Migration erweitern | jede neue Art bräuchte ein Update – widerspricht dem Ziel. |

*Entscheidung Claude: A.*

## Datenmodell (Migration 165)

```sql
it_asset_categories(
  key             TEXT PRIMARY KEY,          -- 'server', 'network', … ; neue: aus dem Namen erzeugt
  name            TEXT NOT NULL,
  icon            TEXT NOT NULL DEFAULT '',  -- Tabler-Klasse 'ti-…'; leer = Vorgabe
  sort_order      INT  NOT NULL DEFAULT 0,
  active          BOOLEAN NOT NULL DEFAULT true,   -- ausgeblendet: nicht mehr wählbar, bestehende behalten sie
  monitor_default BOOLEAN NOT NULL DEFAULT true,   -- neu übernommene Clients überwachen
  grace_minutes   INT CHECK (grace_minutes BETWEEN 1 AND 1440), -- NULL = allgemein
  builtin         BOOLEAN NOT NULL DEFAULT false,  -- network, other: nicht löschbar/ausblendbar
  created_at, updated_at
)
it_assets.type            enum → TEXT NOT NULL DEFAULT 'other' REFERENCES it_asset_categories(key)
it_assets.net_grace_minutes INT NULL CHECK 1..1440  -- Karenz dieses Assets
```

Vorbelegung: server (Server, überwachen ja), network (Netzwerk, ja, builtin),
workstation (Workstation, nein), printer (Drucker, ja), phone (Telefon, nein),
tablet (Tablet, nein), other (Sonstiges, ja, builtin); Reihenfolge wie heute.
Der Enum-Typ `it_asset_type` wird nach der Umstellung gelöscht (`DROP TYPE IF EXISTS`).
Die Migration muss idempotent sein (Spalte schon Text → überspringen).

Schlüssel neuer Kategorien: Kleinbuchstaben a–z, 0–9, `-` aus dem Namen
(Umlaute ä→ae …), bei Kollision `-2`, `-3`; höchstens 40 Zeichen; ändert sich
beim Umbenennen nicht.

## Verhalten

- **Formulare** (Anlegen/Bearbeiten auf `/it`, `/it/{id}`), **Filterknöpfe** auf
  `/it` und **Bezeichnung/Symbol** in Liste, Detail, Neu-Zeile kommen aus der
  Tabelle (aktive Kategorien; ein Asset in einer ausgeblendeten Kategorie zeigt
  sie weiter an). Filterknöpfe: alle aktiven Kategorien, die mindestens ein
  Asset haben, in Reihenfolge. Zählerkarten Server/Netzwerk bleiben.
- **Symbol:** `icon` der Kategorie; leer → bei den 7 bisherigen Arten das
  Symbol aus der Symbolverwaltung (`it.<key>`, `/admin/icons`), sonst `ti-box`.
- **Abgleich (netsync):** Netzgeräte → Kategorie `network` (unverändert).
  `Adopt` setzt `net_monitored` aus `monitor_default` der gewählten Kategorie;
  die Typauswahl beim Übernehmen bietet alle aktiven Kategorien außer `network`.
  „Wieder verbinden“ nutzt dieselbe Regel. `MonitoredByTypeSQL` entfällt.
- **Wächter:** Karenz je Asset = `COALESCE(a.net_grace_minutes, c.grace_minutes, allgemein)`;
  Alarm-Ticket nennt die wirksame Karenz („offline seit …, Karenz 10 min“).
- **Karte „Netz“:** Zeile „Karenz bis zum Alarm: 10 min (Kategorie)“ /
  „… (allgemein)“ / „… (eigener Wert)“; Feld zum Setzen/Leeren (Recht `it.edit`
  per `hasPermRec`).
- **Seite `/it/categories` „IT-Kategorien“** (Knopf auf `/it`): Tabelle mit
  Name, Symbol, Reihenfolge, überwachen, Karenz, Anzahl Assets; Neu, Bearbeiten,
  Ausblenden/Einblenden, Löschen (nur bei 0 Assets und nicht builtin).
  Symbolauswahl wie auf `/admin/icons` (vorhandene Auswahl wiederverwenden).

## Rechte (Migration 165)

- `it.categories` „IT-Kategorien pflegen“ (Kategorie IT) – bekommen alle Rollen mit
  `it.admin` (Abteilung übernehmen); Bereichsadmins haben es ohnehin.
- Ansehen der Seite: `it.view`. Löschen einer Kategorie: `it.categories`
  (keine Datensätze betroffen, nur unbenutzte).
- Karenz am Asset: `it.edit` per `hasPermRec`.

## Handbuch, Version

- 0.80.19, CHANGELOG `### Neu` (Kategorien, Karenz je Kategorie/Asset),
  `### Geändert` (Überwachung neu übernommener Geräte kommt aus der Kategorie),
  `### Sicherheit` (Recht).
- Handbuch Kapitel `infrastructure`: Abschnitt „IT-Kategorien“ (So geht's:
  Kategorie anlegen, Karenz setzen, Karenz am Gerät); Netz-Abgleich-Text zur
  Karenz anpassen; `data-pages` um `it-categories`; Kapitel Administration (Recht).

## Dem Bauenden überlassen

Dateiaufteilung, Darstellung der Symbolauswahl, Texte der Meldungen.
