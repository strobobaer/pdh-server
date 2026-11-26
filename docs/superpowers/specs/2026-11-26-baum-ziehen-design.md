# Entwurf: Bäume per Ziehen ordnen – Infrastruktur, Lager, IT-Netzplan

Stand 26.11.2026 · Wunsch des Anwenders: „Ich möchte auch im Infrastruktur, Lager
und IT Stamm per Drag and drop die Topologie verschieben.“ Entschieden (26.11.2026):
IT = **Netzwerk-Topologie**; Verschieben **sofort nach Nachfrage**; dürfen **wer
bearbeiten darf**. Mit „Vorschlag“ markierte Punkte hat Claude festgelegt.

## 1. Heute

- **Infrastruktur** (`/infrastructure`): Baum aus `infrastructure.parent_id`. Den
  Elternknoten ändert nur das Formular „Bearbeiten“ (`InfraUpdate`,
  `infra_web.go`); es verhindert schon, dass ein Knoten unter seine eigenen
  Unterknoten kommt. Reihenfolge der Geschwister fest nach Typ und Name
  (`sortInfraNodes`).
- **Lager** (`/storage`): Baum aus `storage_locations.parent_id`, Elternknoten über
  „Bearbeiten › Lage im Baum“ (`StorageEditWeb`, `storage_edit.go`).
- **IT** (`/it`): kein Baum. Ein Gerät hängt an einer Anlage (`infrastructure_id`).
  Der Netz-Abgleich mit UniFi/Omada schreibt den Uplink als Text
  (`it_assets.net_uplink`, die Kennung des Geräts beim Controller); angezeigt
  wird er nur als Text.
- Ziehen gibt es nur im Dashboard, bei den Leitständen und im Organigramm.

## 2. Ein Baustein für alle drei Bäume

Ein gemeinsames Skript `web/static/ui/pdh-tree-drag.js` (Regeln der
Gestaltungsgrundlage: keine Vorlagen-Syntax, Texte über `pdhT`), in beiden Rahmen:

- **Ziehen** mit Maus und Finger (Zeiger-Ereignisse; am Handy erst nach kurzem
  Halten, damit Blättern weiter geht). Beim Ziehen leuchtet der Zielknoten auf;
  ungültige Ziele (der Knoten selbst, seine Unterknoten, Knoten ohne Recht) sind
  ausgegraut.
- **Ablegen auf einem Knoten** = wird dessen Unterknoten. Eine Leiste „Oberste
  Ebene“ über dem Baum nimmt Knoten ohne Elternknoten auf. Die Reihenfolge der
  Geschwister bleibt wie heute (nach Typ und Name) – kein Sortieren von Hand
  (*Frage A*).
- **Nachfrage** (entschieden): „„Presse 3“ unter „Halle 2“ verschieben?“ mit dem
  bisherigen und dem neuen Pfad, „Verschieben“ und „Abbrechen“. Erst „Verschieben“
  speichert. Esc oder daneben klicken bricht ab.
- **Ohne Ziehen** (Tastatur, Bildschirmleser, kleines Handy): im Menü jedes Knotens
  „Verschieben nach …“ mit Suche im Baum – dieselbe Nachfrage.
- **Rückgängig**: Im neuen Rahmen setzt „Rückgängig“ im Band Start die letzte
  Verschiebung zurück (eine neue Verschiebung zum alten Ort, mit Nachfrage).
  Verschiebungen sind keine ungespeicherten Änderungen – die Nachfrage beim
  Verlassen der Seite (11.12) erscheint dafür nicht.
- **Verlauf**: jede Verschiebung steht im Verlauf des Knotens („verschoben von …
  nach … durch …“).

Der Server prüft alles selbst neu – das Skript ist nur die Bedienung:

- eine Route je Bereich: `POST /infrastructure/{id}/move`, `POST /storage/{id}/move`,
  `POST /it/{id}/uplink`, Felder `parent` (bzw. `uplink`) und `port` (nur IT);
- **Rechte** (entschieden): das Bearbeiten-Recht des Bereichs am Knoten **und** am
  neuen Elternknoten, je mit Abteilung (`hasPermRec`): `infrastructure.edit`,
  `inventory.edit`, `it.edit`. Wer ein Recht nur in einer Abteilung hat, kann nur
  innerhalb dieser Abteilung verschieben. Die oberste Ebene verlangt das Recht
  überall. Kein neues Recht.
- **keine Schleifen**: Ziel darf nicht der Knoten selbst oder einer seiner
  Unterknoten sein (rekursive Abfrage in derselben Transaktion, Zeile gesperrt);
- **gleichzeitig verschoben**: Hat jemand den Knoten inzwischen anders gehängt, kommt
  „Inzwischen geändert – Baum neu geladen“ statt einer stillen Verschiebung
  (mitgeschickter bisheriger Elternknoten).
- Spiegelserver und Wartung: wie jede schreibende Route gesperrt.

## 3. Infrastruktur und Lager

- Ziehen im Baum der Seite; die Detailseite bekommt im Menüband „Verschieben nach …“.
- Was mitwandert: alle Unterknoten. Bei Anlagen bleiben Vorgänge, Wartungspläne,
  IT-Geräte und Zuordnungen an der Anlage; bei Lagerorten der Bestand. Die
  Abteilung eines Knotens ändert sich durch das Verschieben nicht (sie hängt an
  `infrastructure_department`) – die Nachfrage weist darauf hin, wenn alter und
  neuer Elternknoten verschiedene Abteilungen haben (*Vorschlag*).
- Der oberste Knoten mit den Firmenstammdaten bleibt oben (nicht verschiebbar).

## 4. IT: Netzplan

Neuer Reiter **„Netzplan“** auf der Seite IT (Vorlage „Baum“, 12.1):

- **Baum nach Uplink**: oben Geräte ohne Uplink (Router, Firewall, Internet),
  darunter, was an ihnen hängt (Switch → Access Point → Gerät). Jede Zeile: Name,
  Art, IP, Port am Uplink, Zustand (online/offline aus dem Netz-Abgleich), Anlage.
- **Neu gespeichert** (Migration): `it_assets.uplink_id` (Verweis auf ein anderes
  Gerät, beim Löschen des Uplinks leer) und `uplink_port` (Text, z. B. „Port 12“).
- **Ziehen** eines Geräts auf ein anderes = hängt jetzt dort an. Die Nachfrage fragt
  zusätzlich den **Port** (freiwillig, vorbelegt mit dem bisherigen).
- **Geräte aus UniFi/Omada** (*Vorschlag, Frage B*): Ihr Uplink kommt vom Controller
  (`net_uplink`, über die Kennung dem Gerät im PDH zugeordnet) und wird beim
  nächsten Abgleich wieder gesetzt. Deshalb sind sie **nicht ziehbar**, mit Hinweis
  „Uplink kommt aus UniFi“. Wer sie trotzdem von Hand hängen will, löst sie vorher
  vom Abgleich (vorhandener Knopf). Als Ziel (Uplink anderer Geräte) taugen sie.
- **Störungsmeldungen** des Netz-Abgleichs bleiben in diesem Paket unverändert
  (sie lesen weiter `net_uplink`); „Folgestörung unterdrücken, wenn der Uplink weg
  ist“ für Geräte mit Uplink von Hand ist ein späterer Schritt.

## 5. Schnitt und Version

- **Teil A** (nächste Korrekturversion): Baustein, Infrastruktur und Lager – keine
  Migration, kein neues Recht.
- **Teil B** (danach): IT-Netzplan mit Migration (`uplink_id`, `uplink_port`).
- Je Teil: Handbuch („So verschiebst du …“, Kapitel Infrastruktur, Lager, IT),
  CHANGELOG, Tests (Rechte je Abteilung, Schleifen, gleichzeitige Änderung,
  Spiegel), Browserprüfung mit Maus und Touch in Fiori/Fluent, hell/dunkel,
  320–1366 px, Nachfrage und „Verschieben nach …“ per Tastatur.

## 6. Fragen an den Anwender

- **A.** Reicht „unter einen anderen Knoten hängen“, oder sollen Geschwister auch
  von Hand sortiert werden (dann statt Typ und Name eine eigene Reihenfolge)?
  Vorschlag: nur umhängen.
- **B.** Geräte aus UniFi/Omada im Netzplan nicht ziehbar (Vorschlag), oder darf der
  Uplink von Hand den Controller überstimmen?
- **C.** Reihenfolge: erst Teil A (Infrastruktur, Lager), dann Teil B (Netzplan) –
  einverstanden?
