# PDH Widget Templates

Dieser Ordner enthaelt wiederverwendbare UI-Templates fuer PDH. Ziel ist eine einheitliche Bedienung in allen Modulen und weniger Copy-Paste in `tickets`, `faults`, `maintenance`, `inventory`, `users`, `it` und spaeteren Widgets.

## Infrastruktur-Tree-Picker

Datei:

```text
web/templates/widgets/infra_tree_picker.gohtml
```

Zweck:

- Auswahl eines Infrastruktur-Objekts, z. B. Gebaeude, Linie, Anlage oder Geraet
- verwendbar in Tickets, Stoerungen, Wartungsplaenen und weiteren Formularen
- schreibt die ausgewaehlte UUID in ein verstecktes Feld `infrastructure_id`

Konzept:

```gotemplate
{{template "infra-tree-picker" .InfraPicker}}
```

Erwartete Datenstruktur:

```go
type InfraTreePickerData struct {
    TargetID        string
    SelectedLabelID string
    Label           string
    Value           string
    SelectedLabel   string
}
```

Beispiel:

```go
InfraPicker: InfraTreePickerData{
    TargetID:        "ticket-infra-id",
    SelectedLabelID: "ticket-infra-selected",
    Label:           "Infrastruktur",
}
```

Das Formular braucht danach nur noch:

```html
<input type="hidden" id="ticket-infra-id" name="infrastructure_id">
```

Der Picker setzt dieses Feld automatisch.

## Action Buttons

Datei:

```text
web/templates/widgets/action_buttons.gohtml
```

Zweck:

Einheitliche Buttons fuer Standardaktionen:

- `new` - Neu
- `refresh` - Aktualisieren
- `edit` - Bearbeiten
- `copy` - Kopieren
- `delete` - Loeschen
- `delete-mark` - Loeschvormerkung
- `activate` - Aktiv
- `deactivate` - Inaktiv
- `done` - Fertig
- `cancel` - Abbrechen
- `save` - Speichern

Basis-Aufruf:

```gotemplate
{{template "action-button" .Button}}
```

Erwartete Datenstruktur:

```go
type ActionButtonData struct {
    Action    string
    Label     string
    Href      string
    Type      string
    Class     string
    Style     string
    Confirm   string
    HxGet     string
    HxPost    string
    HxPut     string
    HxDelete  string
    HxTarget  string
    HxSwap    string
    HxTrigger string
    Disabled  bool
}
```

Beispiele:

```gotemplate
{{template "action-button" .NewButton}}
{{template "action-button" .SaveButton}}
{{template "action-button" .DeleteMarkButton}}
```

Oder mit HTMX:

```go
ActionButtonData{
    Action:   "delete-mark",
    HxPut:    "/tickets/123/delete-mark",
    HxTarget: "#ticket-row-123",
    HxSwap:   "outerHTML",
    Confirm:  "Ticket wirklich zur Loeschung vormerken?",
}
```

Icon-only Varianten:

```gotemplate
{{template "icon-button" .Button}}
```

Unterstuetzte Aktionen:

- `new` - Plus
- `edit` - Stift
- `copy` - Kopieren
- `save` - Diskette
- `activate` - Pfeil im Kreis nach oben
- `deactivate` - Pfeil im Kreis nach unten
- `done` - gruener Haken
- `cancel` - rotes X

## Integration

Die Widgets werden beim Serverstart geladen:

```go
t.ParseGlob("web/templates/widgets/*.gohtml")
```

Seiten-Templates koennen die Widgets direkt mit `{{template "..." .}}` einbinden.

## Design-Regel

Alle wiederverwendbaren UI-Elemente sollen kuenftig hier liegen:

```text
web/templates/widgets/
```

Modul-Templates sollen nur noch fachliche Struktur enthalten, nicht mehr jeden Button oder Picker selbst definieren.
