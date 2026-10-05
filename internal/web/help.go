package web

import (
	"crypto/md5"
	"fmt"
	"html/template"
	"net/http"
	"sort"

	"pdh"
)

// Handbuch (/help): redaktionelle Kapitel in web/templates/help.gohtml plus
// automatisch aktuelle Abschnitte, die bei jedem Aufruf aus Code und
// Datenbank erzeugt werden (Berechtigungen, Feldsatz-Module,
// Etikettenformate, Versionsstand, Aenderungsprotokoll) und daher nicht veralten koennen.
// /help?fragment=1 liefert nur die Kapitel - fuer die Kontexthilfe im
// Hilfe-Reiter der Seitenleiste.
//
// Pflege: Jede neue Seite (BaseData.Page) braucht ein Kapitel mit passendem
// data-pages-Eintrag - TestHelpCoversAllPages prueft das.

type helpPermGroup struct {
	Category string
	Items    []helpPerm
}

type helpPerm struct{ Key, Label string }

type HelpPageData struct {
	BaseData
	PermGroups     []helpPermGroup
	FieldModules   []fieldModule
	LabelSizes     []labelSize
	Version        string
	CurrentVersion string             // Versionsnummer, z. B. "0.15.0"
	Releases       []ChangelogRelease // Aenderungsprotokoll (CHANGELOG.md)
	FieldTypes     []string
	TypeLabels     map[string]string
	Shots          map[string]string // Kapitel -> Anhang-Ref fuer eigene Bildschirmfotos
	CanShots       bool              // darf Bildschirmfotos hochladen
	SampleQR       template.HTML     // QR-Code fuer das Muster-Etikett
	SampleURL      string
}

// helpChapterIDs muss mit den section-IDs in help.gohtml uebereinstimmen
// (TestHelpChaptersMatch).
var helpChapterIDs = []string{"start", "records", "categories", "dashboard", "faults", "tickets", "tasks", "maintenance",
	"infrastructure", "inventory", "storage", "printers", "purchasing", "partners", "chat", "time", "users", "trainings", "fieldsets", "cleanup", "backup", "server", "versions", "admin", "connections", "faq"}

// helpChapterRef liefert eine feste UUID je Kapitel (Anhaenge brauchen eine UUID als ref_id).
func helpChapterRef(id string) string {
	b := md5.Sum([]byte("pdh-handbuch:" + id))
	b[6] = (b[6] & 0x0f) | 0x30 // Version 3 (namensbasiert)
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (h *Handler) helpData(r *http.Request) HelpPageData {
	d := HelpPageData{
		BaseData:       h.baseData(r, "help", "Handbuch", "Kapitel"),
		FieldModules:   fieldModules,
		LabelSizes:     labelSizes,
		Version:        versionLabel(h.buildCommit),
		CurrentVersion: pdh.Version(),
		Releases:       installedChangelog(),
		FieldTypes:     fieldTypeOrder,
		TypeLabels:     fieldTypeLabels,
	}
	d.Shots = map[string]string{}
	for _, id := range helpChapterIDs {
		d.Shots[id] = helpChapterRef(id)
	}
	d.CanShots = h.canManageFieldSets(r) || h.canManageRoles(r)
	d.SampleURL = h.publicBaseURL(r) + "/inventory/beispiel"
	if qr, err := qrSVG(d.SampleURL); err == nil {
		d.SampleQR = qr
	}
	if h.rbac != nil {
		if perms, err := h.rbac.ListPermissions(r.Context()); err == nil {
			byCat := map[string][]helpPerm{}
			for _, p := range perms {
				byCat[p.Category] = append(byCat[p.Category], helpPerm{p.Key, p.Label})
			}
			cats := make([]string, 0, len(byCat))
			for c := range byCat {
				cats = append(cats, c)
			}
			sort.Strings(cats)
			for _, c := range cats {
				d.PermGroups = append(d.PermGroups, helpPermGroup{Category: c, Items: byCat[c]})
			}
		}
	}
	return d
}

func (h *Handler) HelpPage(w http.ResponseWriter, r *http.Request) {
	d := h.helpData(r)
	if r.URL.Query().Get("fragment") == "1" {
		t, err := h.tmpl.Clone()
		if err == nil {
			_, err = t.ParseFiles("web/templates/help.gohtml")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err == nil {
			err = t.ExecuteTemplate(w, "help-chapters", d)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	h.render(w, "help", d)
}
