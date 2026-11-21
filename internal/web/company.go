package web

// Firmenstammdaten (0.82.21): eine Firma, ein Datensatz (Tabelle company_profile).
// Gepflegt im Reiter „Firma“ an jedem obersten Infrastruktur-Knoten (company_web.go),
// verwendet in Mails, Druckkopf, Wartungsprotokoll und M365-Signaturen (company_format.go).

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type companyProfile struct {
	Name, NameAddition, Street, PostalCode, City, Country string
	Phone, Fax, Email, Website                           string
	RegisteredOffice, RegisterCourt, RegisterNo          string
	Management, SupervisoryChair, VatID, TaxNo           string
	BankName, IBAN, BIC                                  string
	UpdatedAt                                            time.Time
	UpdatedBy                                            string
}

// companyColumns: Spalten in der Reihenfolge von companyFields.
const companyColumns = `name, name_addition, street, postal_code, city, country, phone, fax, email, website,
	registered_office, register_court, register_no, management, supervisory_chair, vat_id, tax_no, bank_name, iban, bic`

// companyField: ein Feld mit Spaltenname (= Formularname), deutscher Bezeichnung und Höchstlänge.
type companyField struct {
	col, label string
	max        int
	ptr        func(p *companyProfile) *string
}

var companyFields = []companyField{
	{"name", "Firma", 200, func(p *companyProfile) *string { return &p.Name }},
	{"name_addition", "Zusatz", 200, func(p *companyProfile) *string { return &p.NameAddition }},
	{"street", "Straße", 200, func(p *companyProfile) *string { return &p.Street }},
	{"postal_code", "PLZ", 20, func(p *companyProfile) *string { return &p.PostalCode }},
	{"city", "Ort", 100, func(p *companyProfile) *string { return &p.City }},
	{"country", "Land", 100, func(p *companyProfile) *string { return &p.Country }},
	{"phone", "Telefon", 50, func(p *companyProfile) *string { return &p.Phone }},
	{"fax", "Fax", 50, func(p *companyProfile) *string { return &p.Fax }},
	{"email", "E-Mail", 200, func(p *companyProfile) *string { return &p.Email }},
	{"website", "Website", 200, func(p *companyProfile) *string { return &p.Website }},
	{"registered_office", "Sitz", 100, func(p *companyProfile) *string { return &p.RegisteredOffice }},
	{"register_court", "Registergericht", 100, func(p *companyProfile) *string { return &p.RegisterCourt }},
	{"register_no", "Registernummer", 50, func(p *companyProfile) *string { return &p.RegisterNo }},
	{"management", "Geschäftsführung", 300, func(p *companyProfile) *string { return &p.Management }},
	{"supervisory_chair", "Vorsitz Aufsichtsrat", 200, func(p *companyProfile) *string { return &p.SupervisoryChair }},
	{"vat_id", "USt-IdNr.", 30, func(p *companyProfile) *string { return &p.VatID }},
	{"tax_no", "Steuernummer", 30, func(p *companyProfile) *string { return &p.TaxNo }},
	{"bank_name", "Bank", 100, func(p *companyProfile) *string { return &p.BankName }},
	{"iban", "IBAN", 34, func(p *companyProfile) *string { return &p.IBAN }},
	{"bic", "BIC", 11, func(p *companyProfile) *string { return &p.BIC }},
}

// companyCacheState: Zwischenspeicher je Handler (nicht global – sonst sähen Tests und
// mehrere Handler im selben Prozess die Daten einer anderen Datenbank).
type companyCacheState struct {
	mu sync.RWMutex
	p  *companyProfile
}

func (h *Handler) resetCompanyCache() {
	h.companyC.mu.Lock()
	h.companyC.p = nil
	h.companyC.mu.Unlock()
}

// company liefert die Firmenstammdaten (zwischengespeichert); ohne Datenbank oder bei einem
// Lesefehler ein leeres Profil – dann sieht alles aus wie vor 0.82.21.
func (h *Handler) company(ctx context.Context) companyProfile {
	h.companyC.mu.RLock()
	c := h.companyC.p
	h.companyC.mu.RUnlock()
	if c != nil {
		return *c
	}
	if h.db == nil {
		return companyProfile{}
	}
	var p companyProfile
	dest := make([]any, 0, len(companyFields)+2)
	for _, f := range companyFields {
		dest = append(dest, f.ptr(&p))
	}
	var at *time.Time
	var by *string
	dest = append(dest, &at, &by)
	if err := h.db.QueryRow(ctx, `SELECT `+companyColumns+`, updated_at, updated_by::text FROM company_profile WHERE id`).Scan(dest...); err != nil {
		return companyProfile{}
	}
	if at != nil {
		p.UpdatedAt = *at
	}
	if by != nil {
		p.UpdatedBy = *by
	}
	h.companyC.mu.Lock()
	h.companyC.p = &p
	h.companyC.mu.Unlock()
	return p
}

// saveCompany schreibt die Firmenstammdaten (vorher validateCompany) und protokolliert,
// welche Felder sich geändert haben.
func (h *Handler) saveCompany(ctx context.Context, p companyProfile, userID string) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	old := h.company(ctx)
	sets := make([]string, 0, len(companyFields))
	args := make([]any, 0, len(companyFields)+1)
	var changed []string
	for i, f := range companyFields {
		sets = append(sets, fmt.Sprintf("%s=$%d", f.col, i+1))
		args = append(args, *f.ptr(&p))
		if *f.ptr(&old) != *f.ptr(&p) {
			changed = append(changed, f.label)
		}
	}
	var by any
	if userID != "" {
		by = userID
	}
	args = append(args, by)
	q := `UPDATE company_profile SET ` + strings.Join(sets, ", ") + fmt.Sprintf(`, updated_at=NOW(), updated_by=$%d::uuid WHERE id`, len(args))
	tag, err := h.db.Exec(ctx, q, args...)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("Firmenstammdaten fehlen in der Datenbank (Migration 193)")
	}
	h.resetCompanyCache()
	if err != nil {
		return err
	}
	componentLog("stammdaten").Info().Str("von", userID).Str("felder", strings.Join(changed, ", ")).Msg("firmenstammdaten geändert")
	return nil
}

var (
	companyVatRe = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{2,12}$`)
	companyBICRe = regexp.MustCompile(`^[A-Z0-9]{8}([A-Z0-9]{3})?$`)
	companyIBANf = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{10,30}$`)
)

// validateCompany bereinigt die Eingaben (Leerzeichen, Großschreibung bei IBAN/BIC/USt-IdNr.)
// und prüft Längen und Form; leere Felder sind erlaubt.
func validateCompany(p *companyProfile) error {
	for _, f := range companyFields {
		v := f.ptr(p)
		*v = strings.TrimSpace(*v)
		if utf8.RuneCountInString(*v) > f.max {
			return fmt.Errorf("%s: höchstens %d Zeichen", f.label, f.max)
		}
	}
	if p.Email != "" {
		if a, err := mail.ParseAddress(p.Email); err != nil || a.Address != p.Email || !strings.Contains(a.Address, ".") {
			return errors.New("E-Mail: bitte eine gültige Adresse eingeben, z. B. info@firma.de")
		}
	}
	if p.Website != "" {
		u, err := url.Parse(p.Website)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("Website: bitte mit https:// eingeben, z. B. https://www.firma.de")
		}
	}
	compact := func(s string) string { return strings.ToUpper(strings.ReplaceAll(s, " ", "")) }
	if p.IBAN != "" {
		p.IBAN = compact(p.IBAN)
		if !companyIBANf.MatchString(p.IBAN) || !ibanValid(p.IBAN) {
			return errors.New("IBAN: die Prüfziffer stimmt nicht – bitte die IBAN prüfen")
		}
	}
	if p.BIC != "" {
		p.BIC = compact(p.BIC)
		if !companyBICRe.MatchString(p.BIC) {
			return errors.New("BIC: 8 oder 11 Zeichen, z. B. COBADEFFXXX")
		}
	}
	if p.VatID != "" {
		p.VatID = compact(p.VatID)
		if !companyVatRe.MatchString(p.VatID) {
			return errors.New("USt-IdNr.: zwei Buchstaben für das Land und die Nummer, z. B. DE123456789")
		}
	}
	return nil
}

// ibanValid: Prüfsumme nach ISO 13616 (mod 97 = 1).
func ibanValid(iban string) bool {
	r := iban[4:] + iban[:4]
	var digits strings.Builder
	for _, c := range r {
		switch {
		case c >= '0' && c <= '9':
			digits.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			digits.WriteString(fmt.Sprint(int(c-'A') + 10))
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}
