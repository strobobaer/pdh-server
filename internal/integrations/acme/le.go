package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	xacme "golang.org/x/crypto/acme"
)

const (
	urlProduktiv = "https://acme-v02.api.letsencrypt.org/directory"
	urlStaging   = "https://acme-staging-v02.api.letsencrypt.org/directory"

	// Ohne Retry-After-Angabe versuchen wir es frühestens nach einer Stunde wieder.
	rateLimitStandard = time.Hour

	// Platzhalter, falls Let's Encrypt den Namen schon kürzlich bestätigt hat und keine
	// neue Prüfung verlangt: Der Ablauf legt dann einen harmlosen Eintrag an.
	bereitsBestaetigt = "bereits-bestaetigt"
)

// NewAccountKey erzeugt einen neuen Kontoschlüssel (ECDSA P-256) als PEM.
func NewAccountKey() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("Kontoschlüssel erzeugen: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("Kontoschlüssel kodieren: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

type letsEncrypt struct {
	client *xacme.Client
	email  string

	mu         sync.Mutex
	registered bool
}

// NewLetsEncrypt baut den Zugang zu Let's Encrypt (bei staging: zur Testumgebung).
// Das Konto wird beim ersten Auftrag angelegt.
func NewLetsEncrypt(accountKeyPEM []byte, email string, staging bool) (CA, error) {
	blk, _ := pem.Decode(accountKeyPEM)
	if blk == nil {
		return nil, errors.New("Kontoschlüssel ist kein gültiges PEM")
	}
	key, err := x509.ParseECPrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("Kontoschlüssel ist nicht lesbar")
	}
	dir := urlProduktiv
	if staging {
		dir = urlStaging
	}
	return &letsEncrypt{
		client: &xacme.Client{Key: key, DirectoryURL: dir, UserAgent: "pdh-server"},
		email:  strings.TrimSpace(email),
	}, nil
}

// ensureAccount legt das Konto an, falls es noch nicht existiert.
func (l *letsEncrypt) ensureAccount(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.registered {
		return nil
	}
	acct := &xacme.Account{}
	if l.email != "" {
		acct.Contact = []string{"mailto:" + l.email}
	}
	if _, err := l.client.Register(ctx, acct, xacme.AcceptTOS); err != nil && !errors.Is(err, xacme.ErrAccountAlreadyExists) {
		return mapErr(err)
	}
	l.registered = true
	return nil
}

func (l *letsEncrypt) NewOrder(ctx context.Context, name string) (Order, error) {
	if err := l.ensureAccount(ctx); err != nil {
		return nil, err
	}
	o, err := l.client.AuthorizeOrder(ctx, xacme.DomainIDs(name))
	if err != nil {
		return nil, mapErr(err)
	}
	if len(o.AuthzURLs) == 0 {
		return nil, errors.New("Let's Encrypt verlangt keine Prüfung für diesen Namen")
	}
	ord := &leOrder{client: l.client, order: o, name: name, value: bereitsBestaetigt}
	for _, u := range o.AuthzURLs {
		az, err := l.client.GetAuthorization(ctx, u)
		if err != nil {
			return nil, mapErr(err)
		}
		if az.Status == xacme.StatusValid {
			continue // schon bestätigt, keine neue Prüfung nötig
		}
		for _, ch := range az.Challenges {
			if ch.Type != "dns-01" {
				continue
			}
			v, err := l.client.DNS01ChallengeRecord(ch.Token)
			if err != nil {
				return nil, fmt.Errorf("Prüfwert berechnen: %w", err)
			}
			ord.challenge, ord.value, ord.authzURL = ch, v, u
		}
		if ord.challenge == nil {
			return nil, errors.New("Let's Encrypt bietet keine DNS-Prüfung an")
		}
	}
	return ord, nil
}

func (l *letsEncrypt) Revoke(ctx context.Context, certPEM []byte) error {
	blk, _ := pem.Decode(certPEM)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return errors.New("Zertifikat ist kein gültiges PEM")
	}
	if err := l.ensureAccount(ctx); err != nil {
		return err
	}
	// Mit Kontoschlüssel signiert (key == nil).
	return mapErr(l.client.RevokeCert(ctx, nil, blk.Bytes, xacme.CRLReasonUnspecified))
}

type leOrder struct {
	client    *xacme.Client
	order     *xacme.Order
	name      string
	challenge *xacme.Challenge // nil, wenn nichts mehr zu prüfen ist
	authzURL  string
	value     string
}

func (o *leOrder) Challenge() (string, string) {
	return "_acme-challenge." + o.name, o.value
}

func (o *leOrder) Accept(ctx context.Context) error {
	if o.challenge != nil {
		if _, err := o.client.Accept(ctx, o.challenge); err != nil {
			return mapErr(err)
		}
		if _, err := o.client.WaitAuthorization(ctx, o.authzURL); err != nil {
			return mapErr(err)
		}
	}
	if _, err := o.client.WaitOrder(ctx, o.order.URI); err != nil {
		return mapErr(err)
	}
	return nil
}

func (o *leOrder) Finalize(ctx context.Context, csrDER []byte) ([]byte, error) {
	der, _, err := o.client.CreateOrderCert(ctx, o.order.FinalizeURL, csrDER, true)
	if err != nil {
		return nil, mapErr(err)
	}
	var out []byte
	for _, c := range der {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c})...)
	}
	return out, nil
}

// mapErr macht aus Ratenbegrenzungen von Let's Encrypt einen RateLimited und reicht
// alles andere unverändert weiter.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *xacme.Error
	if !errors.As(err, &ae) {
		return err
	}
	if ae.StatusCode != http.StatusTooManyRequests && ae.ProblemType != "urn:ietf:params:acme:error:rateLimited" {
		return err
	}
	return &RateLimited{Until: time.Now().Add(retryAfter(ae.Header)), Msg: ae.Detail}
}

// retryAfter liest Retry-After (Sekunden oder Datum); fehlt es, gilt eine Stunde.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return rateLimitStandard
	}
	if s, err := strconv.Atoi(v); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return rateLimitStandard
}
