package web

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pdh/internal/integrations/snmp"
)

// SNMP v3 (Zusatz, 0.80.22): Zugangsprofile und Geräteliste in der Datenbank. Die Quelle
// des Netz-Abgleichs steht in it_net_snmp.go, die Abfrage selbst in internal/integrations/snmp.
// Kennwörter liegen nur verschlüsselt in der Datenbank, gehen nie zurück an die Oberfläche
// und stehen nie im Protokoll.

const (
	permSNMP = "system.snmp"

	snmpMaxTargets  = 200 // höchstens so viele Geräte in der Liste
	snmpMaxProfiles = 50
	snmpNameMax     = 80
)

// snmpInputErr: Fehler mit Klartext für Anwender (Eingabe oder Zustand, kein Programmfehler).
type snmpInputErr string

func (e snmpInputErr) Error() string { return string(e) }

var (
	errSNMPNotFound     = snmpInputErr("Eintrag nicht gefunden")
	errSNMPNameTaken    = snmpInputErr("Ein Profil mit diesem Namen gibt es schon")
	errSNMPProfileInUse = snmpInputErr("Das Profil wird noch von Geräten benutzt. Erst den Geräten ein anderes Profil geben oder sie aus der Liste entfernen")
	errSNMPProfileLimit = snmpInputErr("Höchstens 50 Zugangsprofile")
	errSNMPNoProfile    = snmpInputErr("Zugangsprofil nicht gefunden")
	errSNMPTargetTaken  = snmpInputErr("Dieses Gerät (Adresse und Port) steht schon in der Liste")
	errSNMPTargetLimit  = snmpInputErr("Höchstens 200 Geräte in der Liste")
	errSNMPSecretUnread = snmpInputErr("Die Kennwörter des Profils sind nicht lesbar – bitte beide Kennwörter neu eintragen")
	errSNMPNameMissing  = snmpInputErr("Name des Profils fehlt")
	errSNMPNameTooLong  = snmpInputErr("Name des Profils: höchstens 80 Zeichen")
	errSNMPNoDB         = errors.New("snmp: keine Datenbank")
)

// snmpValidateHostStub: Platzhalter-Adresse, um ein Profil ohne Gerät zu prüfen.
const snmpValidateHostStub = "10.0.0.1"

// snmpUserText: Klartext für Anwender, wenn der Fehler eine Eingabe oder die Antwort eines
// Geräts betrifft; ok = false bei einem Programm- oder Datenbankfehler (dann nur protokollieren).
func snmpUserText(err error) (text string, ok bool) {
	var in snmpInputErr
	if errors.As(err, &in) {
		return string(in), true
	}
	var se *snmp.Error
	if errors.As(err, &se) {
		return se.Msg, true
	}
	return "", false
}

func snmpPgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// ── Profile ──────────────────────────────────────────────────

// SNMPProfile: ein Zugangsprofil ohne Kennwörter.
type SNMPProfile struct {
	ID, Name, User       string
	AuthProto, PrivProto string // Schlüssel aus snmp.AuthProtos / snmp.PrivProtos
	Targets              int    // so viele Geräte benutzen das Profil
	UpdatedAt            time.Time
}

// SNMPProfileInput: Eingabe für Anlegen und Ändern. Beim Ändern heißt ein leeres
// Kennwort „unverändert“.
type SNMPProfileInput struct {
	Name, User          string
	AuthProto, AuthPass string
	PrivProto, PrivPass string
}

func (in *SNMPProfileInput) clean() error {
	in.Name, in.User = strings.TrimSpace(in.Name), strings.TrimSpace(in.User)
	if in.Name == "" {
		return errSNMPNameMissing
	}
	if utf8.RuneCountInString(in.Name) > snmpNameMax {
		return errSNMPNameTooLong
	}
	// Benutzer, Verfahren und Kennwörter prüft das Abfrage-Paket (ohne ein Gerät zu fragen).
	return snmp.Config{Host: snmpValidateHostStub, Port: snmp.DefaultPort, User: in.User,
		AuthProto: in.AuthProto, AuthPass: in.AuthPass, PrivProto: in.PrivProto, PrivPass: in.PrivPass}.Validate()
}

// snmpProfiles: alle Profile nach Name.
func (h *Handler) snmpProfiles(ctx context.Context) ([]SNMPProfile, error) {
	if h.db == nil {
		return nil, errSNMPNoDB
	}
	rows, err := h.db.Query(ctx, `SELECT p.id::text, p.name, p.username, p.auth_proto, p.priv_proto, p.updated_at,
			(SELECT count(*) FROM it_snmp_targets t WHERE t.profile_id = p.id)
		FROM it_snmp_profiles p ORDER BY lower(p.name), p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SNMPProfile
	for rows.Next() {
		var p SNMPProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.User, &p.AuthProto, &p.PrivProto, &p.UpdatedAt, &p.Targets); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// snmpProfileCount: Zahl der Profile (0 auch bei einem Fehler).
func (h *Handler) snmpProfileCount(ctx context.Context) int {
	if h.db == nil {
		return 0
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM it_snmp_profiles`).Scan(&n)
	return n
}

// snmpProfileCreate legt ein Profil an und liefert seine ID.
func (h *Handler) snmpProfileCreate(ctx context.Context, in SNMPProfileInput) (string, error) {
	if h.db == nil {
		return "", errSNMPNoDB
	}
	if err := in.clean(); err != nil {
		return "", err
	}
	auth, err := encryptSecret(h.pkiKey(), in.AuthPass)
	if err != nil {
		return "", err
	}
	priv, err := encryptSecret(h.pkiKey(), in.PrivPass)
	if err != nil {
		return "", err
	}
	var id string
	err = h.db.QueryRow(ctx, `INSERT INTO it_snmp_profiles (name, username, auth_proto, auth_secret_enc, priv_proto, priv_secret_enc)
		SELECT $1, $2, $3, $4, $5, $6 WHERE (SELECT count(*) FROM it_snmp_profiles) < $7
		RETURNING id::text`, in.Name, in.User, in.AuthProto, auth, in.PrivProto, priv, snmpMaxProfiles).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", errSNMPProfileLimit
	case snmpPgCode(err) == "23505":
		return "", errSNMPNameTaken
	}
	return id, err
}

// snmpProfileUpdate ändert ein Profil. Ein leeres Kennwort bleibt, wie es ist.
func (h *Handler) snmpProfileUpdate(ctx context.Context, id string, in SNMPProfileInput) error {
	if h.db == nil {
		return errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return errSNMPNotFound
	}
	var authEnc, privEnc string
	err := h.db.QueryRow(ctx, `SELECT auth_secret_enc, priv_secret_enc FROM it_snmp_profiles WHERE id = $1::uuid`, id).Scan(&authEnc, &privEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSNMPNotFound
	}
	if err != nil {
		return err
	}
	// Leer = unverändert: zum Prüfen das gespeicherte Kennwort einsetzen.
	check := in
	for _, f := range []struct {
		pass, enc *string
	}{{&check.AuthPass, &authEnc}, {&check.PrivPass, &privEnc}} {
		if *f.pass != "" {
			if *f.enc, err = encryptSecret(h.pkiKey(), *f.pass); err != nil {
				return err
			}
			continue
		}
		old, derr := decryptSecret(h.pkiKey(), *f.enc)
		if derr != nil || old == "" {
			return errSNMPSecretUnread
		}
		*f.pass = old
	}
	if err := check.clean(); err != nil {
		return err
	}
	tag, err := h.db.Exec(ctx, `UPDATE it_snmp_profiles SET name = $2, username = $3, auth_proto = $4, auth_secret_enc = $5,
			priv_proto = $6, priv_secret_enc = $7, updated_at = now() WHERE id = $1::uuid`,
		id, check.Name, check.User, check.AuthProto, authEnc, check.PrivProto, privEnc)
	switch {
	case snmpPgCode(err) == "23505":
		return errSNMPNameTaken
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return errSNMPNotFound
	}
	return nil
}

// snmpProfileDelete löscht ein Profil – nur, solange kein Gerät es benutzt (errSNMPProfileInUse).
func (h *Handler) snmpProfileDelete(ctx context.Context, id string) error {
	if h.db == nil {
		return errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return errSNMPNotFound
	}
	tag, err := h.db.Exec(ctx, `DELETE FROM it_snmp_profiles WHERE id = $1::uuid`, id)
	switch {
	case snmpPgCode(err) == "23503":
		return errSNMPProfileInUse
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return errSNMPNotFound
	}
	return nil
}

// snmpProfileSecrets: Zugang eines Profils im Klartext – nur für die Abfrage, nie für die Anzeige.
type snmpProfileSecrets struct {
	Name, User           string
	AuthProto, PrivProto string
	authPass, privPass   string
}

func (h *Handler) snmpDecryptProfile(name, user, authProto, authEnc, privProto, privEnc string) (snmpProfileSecrets, error) {
	p := snmpProfileSecrets{Name: name, User: user, AuthProto: authProto, PrivProto: privProto}
	var err error
	if p.authPass, err = decryptSecret(h.pkiKey(), authEnc); err != nil || p.authPass == "" {
		return p, errSNMPSecretUnread
	}
	if p.privPass, err = decryptSecret(h.pkiKey(), privEnc); err != nil || p.privPass == "" {
		return p, errSNMPSecretUnread
	}
	return p, nil
}

func (p snmpProfileSecrets) config(host string, port int) snmp.Config {
	return snmp.Config{Host: host, Port: port, User: p.User, AuthProto: p.AuthProto, AuthPass: p.authPass,
		PrivProto: p.PrivProto, PrivPass: p.privPass}
}

// snmpProfileConfig: Zugang zu einem Gerät über das Profil.
func (h *Handler) snmpProfileConfig(ctx context.Context, profileID, host string, port int) (snmp.Config, error) {
	if h.db == nil {
		return snmp.Config{}, errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(profileID) {
		return snmp.Config{}, errSNMPNoProfile
	}
	var name, user, ap, ae, pp, pe string
	err := h.db.QueryRow(ctx, `SELECT name, username, auth_proto, auth_secret_enc, priv_proto, priv_secret_enc
		FROM it_snmp_profiles WHERE id = $1::uuid`, profileID).Scan(&name, &user, &ap, &ae, &pp, &pe)
	if errors.Is(err, pgx.ErrNoRows) {
		return snmp.Config{}, errSNMPNoProfile
	}
	if err != nil {
		return snmp.Config{}, err
	}
	p, err := h.snmpDecryptProfile(name, user, ap, ae, pp, pe)
	if err != nil {
		return snmp.Config{}, err
	}
	return p.config(host, port), nil
}

// ── Geräte ───────────────────────────────────────────────────

// SNMPTarget: eine Zeile der Geräteliste mit Zustand und Link zum Asset.
type SNMPTarget struct {
	ID, Host    string
	Port        int
	ProfileID   string
	ProfileName string
	Enabled     bool
	LastOK      *time.Time // letzte Antwort des Geräts
	LastError   string     // Klartext des letzten Fehlers ("" = keiner)
	Note        string     // Hinweis des Abgleichs, z. B. „schon über UniFi bekannt“
	AssetID     string     // eigenes Asset der Quelle SNMP ("" = noch keines)
	AssetName   string
	NetState    string // online, offline, unknown ("" ohne Asset)
	// DetachedAssetID: Das Asset des Geräts ist vom Abgleich getrennt (AssetID bleibt dann leer).
	DetachedAssetID   string
	DetachedAssetName string
}

// anyAssetID: abgeglichenes oder getrenntes Asset des Eintrags ("" = keines).
func (t SNMPTarget) anyAssetID() string {
	if t.AssetID != "" {
		return t.AssetID
	}
	return t.DetachedAssetID
}

// snmpCleanTarget prüft Adresse und Port (0 → 161).
func snmpCleanTarget(host string, port int) (string, int, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if port == 0 {
		port = snmp.DefaultPort
	}
	if err := snmp.ValidateHost(host); err != nil {
		return "", 0, err
	}
	if port < 1 || port > 65535 {
		return "", 0, snmpInputErr("Port: bitte eine Zahl von 1 bis 65535 eingeben (üblich: 161)")
	}
	return host, port, nil
}

// snmpTargets: die Geräteliste (Adresse, Port).
func (h *Handler) snmpTargets(ctx context.Context) ([]SNMPTarget, error) {
	if h.db == nil {
		return nil, errSNMPNoDB
	}
	rows, err := h.db.Query(ctx, `SELECT t.id::text, t.host, t.port, t.profile_id::text, p.name, t.enabled, t.last_ok, t.last_error, t.note,
			COALESCE(a.id::text, ''), COALESCE(a.name, ''), COALESCE(a.net_state, ''), COALESCE(a.net_detached, false)
		FROM it_snmp_targets t
		JOIN it_snmp_profiles p ON p.id = t.profile_id
		LEFT JOIN it_assets a ON a.net_source = $1 AND a.net_external_id = t.id::text
		ORDER BY lower(t.host), t.port, t.id`, h.itNetSNMPName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SNMPTarget
	for rows.Next() {
		var t SNMPTarget
		var detached bool
		if err := rows.Scan(&t.ID, &t.Host, &t.Port, &t.ProfileID, &t.ProfileName, &t.Enabled, &t.LastOK, &t.LastError, &t.Note,
			&t.AssetID, &t.AssetName, &t.NetState, &detached); err != nil {
			return nil, err
		}
		if detached {
			t.DetachedAssetID, t.DetachedAssetName = t.AssetID, t.AssetName
			t.AssetID, t.AssetName, t.NetState = "", "", ""
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// snmpTargetCreate nimmt ein Gerät in die Liste auf (Port 0 → 161) und liefert die ID.
func (h *Handler) snmpTargetCreate(ctx context.Context, host string, port int, profileID, userID string) (string, error) {
	if h.db == nil {
		return "", errSNMPNoDB
	}
	host, port, err := snmpCleanTarget(host, port)
	if err != nil {
		return "", err
	}
	if !itNetUUIDRe.MatchString(profileID) {
		return "", errSNMPNoProfile
	}
	var by *string
	if itNetUUIDRe.MatchString(userID) {
		by = &userID
	}
	var id string
	err = h.db.QueryRow(ctx, `INSERT INTO it_snmp_targets (host, port, profile_id, created_by)
		SELECT $1, $2, $3::uuid, $4::uuid WHERE (SELECT count(*) FROM it_snmp_targets) < $5
		RETURNING id::text`, host, port, profileID, by, snmpMaxTargets).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", errSNMPTargetLimit
	case snmpPgCode(err) == "23505":
		return "", errSNMPTargetTaken
	case snmpPgCode(err) == "23503":
		return "", errSNMPNoProfile
	}
	return id, err
}

// snmpTargetUpdate ändert Adresse, Port und Profil eines Geräts. Das Asset bleibt dasselbe.
func (h *Handler) snmpTargetUpdate(ctx context.Context, id, host string, port int, profileID string) error {
	if h.db == nil {
		return errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return errSNMPNotFound
	}
	host, port, err := snmpCleanTarget(host, port)
	if err != nil {
		return err
	}
	if !itNetUUIDRe.MatchString(profileID) {
		return errSNMPNoProfile
	}
	tag, err := h.db.Exec(ctx, `UPDATE it_snmp_targets SET host = $2, port = $3, profile_id = $4::uuid WHERE id = $1::uuid`,
		id, host, port, profileID)
	switch {
	case snmpPgCode(err) == "23505":
		return errSNMPTargetTaken
	case snmpPgCode(err) == "23503":
		return errSNMPNoProfile
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return errSNMPNotFound
	}
	return nil
}

// snmpTargetSetEnabled schaltet die Abfrage eines Geräts ein oder aus. Ausgeschaltet wird
// es nicht mehr gefragt; sein Asset bleibt und steht ab dem nächsten Lauf auf „unbekannt“.
func (h *Handler) snmpTargetSetEnabled(ctx context.Context, id string, on bool) error {
	if h.db == nil {
		return errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return errSNMPNotFound
	}
	tag, err := h.db.Exec(ctx, `UPDATE it_snmp_targets SET enabled = $2,
			last_error = CASE WHEN $2 THEN last_error ELSE '' END WHERE id = $1::uuid`, id, on)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSNMPNotFound
	}
	return nil
}

// snmpTargetAssetID: Asset eines Eintrags der Liste – abgeglichen oder getrennt ("" = keines;
// errSNMPNotFound, wenn es den Eintrag nicht gibt).
func (h *Handler) snmpTargetAssetID(ctx context.Context, id string) (string, error) {
	if h.db == nil {
		return "", errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return "", errSNMPNotFound
	}
	var asset string
	err := h.db.QueryRow(ctx, `SELECT COALESCE((SELECT a.id::text FROM it_assets a
			WHERE a.net_source = $2 AND a.net_external_id = t.id::text), '')
		FROM it_snmp_targets t WHERE t.id = $1::uuid`, id, h.itNetSNMPName()).Scan(&asset)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errSNMPNotFound
	}
	return asset, err
}

// snmpTargetRemove entfernt ein Gerät aus der Liste. Sein Asset bleibt bestehen und wird wie
// bei „Quelle trennen“ nicht mehr abgeglichen und nicht mehr überwacht – es entsteht kein Alarm.
func (h *Handler) snmpTargetRemove(ctx context.Context, id string) error {
	if h.db == nil {
		return errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return errSNMPNotFound
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM it_snmp_targets WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSNMPNotFound
	}
	var asset string
	err = tx.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source = $1 AND net_external_id = $2 FOR UPDATE`,
		h.itNetSNMPName(), id).Scan(&asset)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := itNetDetachTx(ctx, tx, asset); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ── Prüfen ───────────────────────────────────────────────────

// snmpAPI: was der Abgleich von einem Gerät liest (snmp.Client; in Tests im Speicher).
type snmpAPI interface {
	Ping(context.Context) error
	Detail(context.Context) (snmp.Detail, error)
}

// snmpClient: Abfrage eines Geräts.
func (h *Handler) snmpClient(cfg snmp.Config) snmpAPI {
	if h.itNet.snmpDial != nil {
		return h.itNet.snmpDial(cfg)
	}
	return snmp.New(cfg)
}

// snmpCheck fragt ein Gerät sofort ab (Knopf „Prüfen“, auch vor dem Aufnehmen in die Liste):
// Name, Modell, Firmware, Anschlüsse – oder der Fehler; Klartext über snmpUserText.
// Gespeichert wird nichts.
func (h *Handler) snmpCheck(ctx context.Context, host string, port int, profileID string) (snmp.Detail, error) {
	host, port, err := snmpCleanTarget(host, port)
	if err != nil {
		return snmp.Detail{}, err
	}
	cfg, err := h.snmpProfileConfig(ctx, profileID, host, port)
	if err != nil {
		return snmp.Detail{}, err
	}
	return h.snmpClient(cfg).Detail(ctx)
}

// snmpTargetCheck: „Prüfen“ für ein Gerät der Liste.
func (h *Handler) snmpTargetCheck(ctx context.Context, id string) (snmp.Detail, error) {
	if h.db == nil {
		return snmp.Detail{}, errSNMPNoDB
	}
	if !itNetUUIDRe.MatchString(id) {
		return snmp.Detail{}, errSNMPNotFound
	}
	var host, profile string
	var port int
	err := h.db.QueryRow(ctx, `SELECT host, port, profile_id::text FROM it_snmp_targets WHERE id = $1::uuid`, id).Scan(&host, &port, &profile)
	if errors.Is(err, pgx.ErrNoRows) {
		return snmp.Detail{}, errSNMPNotFound
	}
	if err != nil {
		return snmp.Detail{}, err
	}
	return h.snmpCheck(ctx, host, port, profile)
}
