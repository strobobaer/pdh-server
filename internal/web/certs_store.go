package web

import (
	"context"
	"errors"
	"time"

	"pdh/internal/integrations/acme"
)

// Let's-Encrypt-Zertifikate (optional): Rechte, Einstellungen und der
// verschlüsselte Speicher. Ohne Einrichtung bleibt die Tabelle le_certs leer.

const (
	permCerts = "system.certs"
	permUnifi = "system.unifi"
)

// Einstellungen (app_settings). Das Konto und der UniFi-Schlüssel liegen verschlüsselt.
const (
	keyCertEmail        = "certs.email"
	keyCertStaging      = "certs.staging"
	keyCertAccount      = "certs.account" // je Umgebung mit Anhang ".staging"
	keyCertStrictBefore = "certs.strict_before"

	keyUnifiURL  = "unifi.url"
	keyUnifiKey  = "unifi.key"
	keyUnifiSite = "unifi.site"
	keyUnifiPin  = "unifi.pin"
)

// Zustände eines Zertifikats.
const (
	certStateNeu     = "neu"
	certStateLaeuft  = "laeuft"
	certStateGueltig = "gueltig"
	certStateFehler  = "fehler"
)

type certRow struct {
	Name                string
	Staging             bool
	ChainPEM            string
	NotBefore, NotAfter time.Time
	Fingerprint         string
	State, Step         string
	LastError           string
	ChallengeID         string
	InstalledFP         string
	LANRecordID         string
	RetryAfter          time.Time
}

type certStore interface {
	certList(ctx context.Context) ([]certRow, error)
	certAdd(ctx context.Context, name string, staging bool) error
	certSetStep(ctx context.Context, name, state, step, lastErr string) error
	certSetChallenge(ctx context.Context, name, id string) error
	certSetInstalled(ctx context.Context, name, fp string) error
	certSetRetry(ctx context.Context, name string, until time.Time) error
	certSetLANRecord(ctx context.Context, name, id string) error
	certSaveIssued(ctx context.Context, name string, is *acme.Issued) error
	certKey(ctx context.Context, name string) (chainPEM, keyPEM []byte, err error)
	certDelete(ctx context.Context, name string) error
}

// certSealKey verschlüsselt ein Schlüssel-PEM für die Datenbank.
func certSealKey(key, pem []byte) (string, error) { return encryptSecret(key, string(pem)) }

// certOpenKey entschlüsselt ein gespeichertes Schlüssel-PEM.
func certOpenKey(key []byte, enc string) ([]byte, error) {
	s, err := decryptSecret(key, enc)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

func (h *Handler) certDB() error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	return nil
}

func (h *Handler) certList(ctx context.Context) ([]certRow, error) {
	if err := h.certDB(); err != nil {
		return nil, err
	}
	rows, err := h.db.Query(ctx, `
		SELECT name, staging, COALESCE(chain_pem,''), not_before, not_after,
		       COALESCE(fingerprint,''), state, step, last_error, retry_after,
		       challenge_id, installed_fp, lan_record_id
		FROM le_certs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []certRow
	for rows.Next() {
		var c certRow
		var nb, na, ra *time.Time
		if err := rows.Scan(&c.Name, &c.Staging, &c.ChainPEM, &nb, &na, &c.Fingerprint,
			&c.State, &c.Step, &c.LastError, &ra, &c.ChallengeID, &c.InstalledFP, &c.LANRecordID); err != nil {
			return nil, err
		}
		if nb != nil {
			c.NotBefore = *nb
		}
		if na != nil {
			c.NotAfter = *na
		}
		if ra != nil {
			c.RetryAfter = *ra
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *Handler) certAdd(ctx context.Context, name string, staging bool) error {
	if err := h.certDB(); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `INSERT INTO le_certs (name, staging) VALUES ($1, $2)`, name, staging)
	return err
}

func (h *Handler) certSetStep(ctx context.Context, name, state, step, lastErr string) error {
	if err := h.certDB(); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `UPDATE le_certs SET state=$2, step=$3, last_error=$4, updated_at=now() WHERE name=$1`,
		name, state, step, lastErr)
	return err
}

func (h *Handler) certSetChallenge(ctx context.Context, name, id string) error {
	if err := h.certDB(); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `UPDATE le_certs SET challenge_id=$2, updated_at=now() WHERE name=$1`, name, id)
	return err
}

func (h *Handler) certSetInstalled(ctx context.Context, name, fp string) error {
	if err := h.certDB(); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `UPDATE le_certs SET installed_fp=$2, updated_at=now() WHERE name=$1`, name, fp)
	return err
}

func (h *Handler) certSetLANRecord(ctx context.Context, name, id string) error {
	if err := h.certDB(); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `UPDATE le_certs SET lan_record_id=$2, updated_at=now() WHERE name=$1`, name, id)
	return err
}

func (h *Handler) certSetRetry(ctx context.Context, name string, until time.Time) error {
	if err := h.certDB(); err != nil {
		return err
	}
	var v *time.Time
	if !until.IsZero() {
		v = &until
	}
	_, err := h.db.Exec(ctx, `UPDATE le_certs SET retry_after=$2, updated_at=now() WHERE name=$1`, name, v)
	return err
}

// certSaveIssued speichert das neue Zertifikat; der bisherige Stand wandert nach prev_*.
func (h *Handler) certSaveIssued(ctx context.Context, name string, is *acme.Issued) error {
	if err := h.certDB(); err != nil {
		return err
	}
	if is == nil {
		return errors.New("kein Zertifikat")
	}
	enc, err := certSealKey(h.pkiKey(), is.KeyPEM)
	if err != nil {
		return err
	}
	tag, err := h.db.Exec(ctx, `
		UPDATE le_certs SET
		    prev_chain_pem=chain_pem, prev_key_enc=key_enc,
		    chain_pem=$2, key_enc=$3, not_before=$4, not_after=$5, fingerprint=$6,
		    state='gueltig', step='', last_error='', retry_after=NULL, challenge_id='',
		    updated_at=now()
		WHERE name=$1`, name, string(is.ChainPEM), enc, is.NotBefore, is.NotAfter, is.Fingerprint)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("Zertifikat nicht gefunden")
	}
	return nil
}

// certKey liefert Kette und (entschlüsselten) Schlüssel. Nie protokollieren.
func (h *Handler) certKey(ctx context.Context, name string) (chainPEM, keyPEM []byte, err error) {
	if err := h.certDB(); err != nil {
		return nil, nil, err
	}
	var chain, enc string
	if err := h.db.QueryRow(ctx, `SELECT COALESCE(chain_pem,''), COALESCE(key_enc,'') FROM le_certs WHERE name=$1`,
		name).Scan(&chain, &enc); err != nil {
		return nil, nil, err
	}
	if chain == "" || enc == "" {
		return nil, nil, errors.New("noch kein Zertifikat ausgestellt")
	}
	k, err := certOpenKey(h.pkiKey(), enc)
	if err != nil {
		return nil, nil, err
	}
	return []byte(chain), k, nil
}

func (h *Handler) certDelete(ctx context.Context, name string) error {
	if err := h.certDB(); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `DELETE FROM le_certs WHERE name=$1`, name)
	return err
}

// certAccountKey liefert den Konto-Schlüssel für Let's Encrypt (je Umgebung). Er wird
// erst beim ersten Ausstellen angelegt, nie beim Start.
func (h *Handler) certAccountKey(ctx context.Context, staging bool) ([]byte, error) {
	k := keyCertAccount
	if staging {
		k += ".staging"
	}
	if enc := h.getUpdateSetting(ctx, k, ""); enc != "" {
		return certOpenKey(h.pkiKey(), enc)
	}
	pem, err := acme.NewAccountKey()
	if err != nil {
		return nil, err
	}
	enc, err := certSealKey(h.pkiKey(), pem)
	if err != nil {
		return nil, err
	}
	if err := h.setUpdateSetting(ctx, k, enc); err != nil {
		return nil, err
	}
	return pem, nil
}
