//go:build integration

// App-Konfiguration (PDH-App): Gerät bestätigen, Benutzer zuordnen, freigeben oder ablehnen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestApp(Approve|Reject)' ./internal/web/
package web

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

type appAdmEnv struct {
	*appReqEnv
	adm http.Handler
}

func newAppAdmEnv(t *testing.T) *appAdmEnv {
	e := newAppReqEnv(t)
	tmpl := loadTestTemplates(t) // vor dem Wechsel des Arbeitsverzeichnisses
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	e.h.rbac = e.rb
	e.h.users = users.NewService(users.NewRepository(e.pool), "test-secret", 1)
	e.h.jwtSecret = "test-geheim"
	e.h.tmpl = tmpl
	r := chi.NewRouter()
	r.Get("/app/requests/{id}", e.h.AppRequestPage)
	r.Post("/app/requests/{id}/approve", e.h.AppRequestApproveWeb)
	r.Post("/app/requests/{id}/reject", e.h.AppRequestRejectWeb)
	r.Get("/app/devices", e.h.AppDevicesPage)
	r.Post("/app/devices/{id}/revoke", e.h.AppDeviceRevokeWeb)
	r.Post("/app/settings", e.h.AppSettingsSaveWeb)
	r.Get("/app/install", e.h.AppInstallPage)
	r.With(e.h.AppAuth).Get("/api/app/v1/me", e.h.AppMeAPI)
	return &appAdmEnv{appReqEnv: e, adm: r}
}

// au legt einen Benutzer an und räumt Historie und Tickets weg, die auf ihn zeigen
// (record_history.created_by und tickets.archived_by haben kein ON DELETE).
func (e *appAdmEnv) au(name, role string, dept string) *users.User {
	uid := e.user(name, role, false)
	if dept != "" {
		e.exec(`UPDATE users SET department_id = $2::uuid WHERE id = $1::uuid`, uid, dept)
	}
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM tickets WHERE app_request_id IN (SELECT id FROM app_requests WHERE name LIKE '%'||$1||'%')`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM record_history WHERE created_by = $1::uuid OR ref_id = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `UPDATE tickets SET archived_by = NULL WHERE archived_by = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `DELETE FROM app_devices WHERE user_id = $1::uuid OR approved_by = $1::uuid`, uid)
		_, _ = e.pool.Exec(bg, `UPDATE app_requests SET decided_by = NULL WHERE decided_by = $1::uuid`, uid)
	})
	return &users.User{ID: uid, Role: users.Role(role)}
}

type appReq struct {
	ID, Code, Ticket string
	Key              *ecdsa.PrivateKey
	IP               string
}

// request legt über die öffentliche Schnittstelle eine Anfrage an (mit Ticket).
func (e *appAdmEnv) request(dept string) appReq {
	e.t.Helper()
	ip := e.nextIP()
	k, pub := appTestKey(e.t)
	rec := e.post(ip, e.body("Erika "+e.sfx, dept, "android", pub))
	if rec.Code != 201 {
		e.t.Fatalf("anfrage: %d %s", rec.Code, rec.Body.String())
	}
	m := decodeAppReq(e.t, rec)
	return appReq{ID: m.RequestID, Code: m.ConfirmCode, Key: k, IP: ip,
		Ticket: e.id(`SELECT ticket_id::text FROM app_requests WHERE id = $1::uuid`, m.RequestID)}
}

func (e *appAdmEnv) do(method, target string, actor *users.User, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	e.adm.ServeHTTP(rec, withUser(req, actor))
	return rec
}

func (e *appAdmEnv) approve(rq appReq, actor *users.User, code, userID string, types []string, mayCreate bool) *httptest.ResponseRecorder {
	f := url.Values{"confirm_code": {code}, "user_id": {userID}, "types[]": types}
	if mayCreate {
		f.Set("may_create", "on")
	}
	return e.do("POST", "/app/requests/"+rq.ID+"/approve", actor, f)
}

var allAppTypes = []string{"fault", "ticket", "task", "maintenance"}

func (e *appAdmEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, q, args...).Scan(&n))
	return n
}

func (e *appAdmEnv) requestStatus(id string) string {
	return e.id(`SELECT status FROM app_requests WHERE id = $1::uuid`, id)
}

func (e *appAdmEnv) devices(reqID string) int {
	return e.count(`SELECT count(*) FROM app_devices WHERE request_id = $1::uuid`, reqID)
}

func TestAppApprove(t *testing.T) {
	e := newAppAdmEnv(t)
	deptA, deptB := e.dept("A"), e.dept("B")
	admin := e.au("aadm", "admin", "")
	mgrA := e.au("amgr", "manager", deptA)
	e.grant(mgrA.ID, permAppAdmin, deptA, true) // Einzelrecht nur in A ersetzt das globale Rollenrecht
	viewer := e.au("avw", "viewer", "")
	tgtA := e.au("atA", "viewer", deptA)
	tgtB := e.au("atB", "viewer", deptB)
	tgtNone := e.au("atN", "viewer", "")
	tgtAdmin := e.au("atAd", "admin", deptA)
	tgtMgr := e.au("atMg", "manager", deptA)
	e.warm()

	t.Run("Freigeben", func(t *testing.T) {
		rq := e.request(deptA)
		rec := e.approve(rq, admin, rq.Code, tgtA.ID, []string{"fault", "task"}, false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/devices?revoked=1" {
			t.Fatalf("freigeben: %d %s %.200s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
		var uid, types, fp, approvedBy string
		var tokenNull, mayCreate bool
		e.must(e.pool.QueryRow(e.ctx, `SELECT user_id::text, array_to_string(allowed_types, ','), key_fingerprint, approved_by::text, token_sha256 IS NULL, may_create
			FROM app_devices WHERE request_id = $1::uuid`, rq.ID).Scan(&uid, &types, &fp, &approvedBy, &tokenNull, &mayCreate))
		if uid != tgtA.ID || types != "fault,task" || fp != appKeyFingerprint(e.id(`SELECT public_key FROM app_requests WHERE id=$1::uuid`, rq.ID)) ||
			approvedBy != admin.ID || !tokenNull || mayCreate {
			t.Errorf("gerät: %s %s %s %s %v %v", uid, types, fp, approvedBy, tokenNull, mayCreate)
		}
		if s := e.requestStatus(rq.ID); s != "approved" {
			t.Errorf("anfrage: %s", s)
		}
		if n := e.count(`SELECT count(*) FROM app_requests WHERE id = $1::uuid AND decided_at IS NOT NULL AND decided_by = $2::uuid AND device_id IS NOT NULL`, rq.ID, admin.ID); n != 1 {
			t.Errorf("decided_*/device_id nicht gesetzt")
		}
		if s := e.id(`SELECT status FROM tickets WHERE id = $1::uuid`, rq.Ticket); s != "closed" {
			t.Errorf("ticket: %s", s)
		}
		if n := e.count(`SELECT count(*) FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND action = 'app_device_approved' AND created_by = $2::uuid`, tgtA.ID, admin.ID); n != 1 {
			t.Errorf("historie am Benutzer: %d", n)
		}
		// Abholen liefert jetzt das Token (Zusammenspiel mit der Zugangs-Schnittstelle).
		got := e.get(rq.IP, rq.ID, rq.Key, e.nonce("a1"))
		if m := decodeAppReq(t, got); got.Code != 200 || m.Status != "approved" || !strings.HasPrefix(m.Token, "pdha_") {
			t.Errorf("abholen: %d %+v", got.Code, m)
		}
	})

	t.Run("Seite", func(t *testing.T) {
		rq := e.request(deptA)
		rec := e.do("GET", "/app/requests/"+rq.ID, admin, nil)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, "Erika "+e.sfx) || !strings.Contains(body, "Freigeben") ||
			!strings.Contains(body, "Ablehnen") || !strings.Contains(body, `name="confirm_code"`) || !strings.Contains(body, tgtA.ID) {
			t.Fatalf("seite: %d %.600s", rec.Code, body)
		}
		if strings.Contains(body, appSystemUserID) {
			t.Error("Systembenutzer in der Auswahl")
		}
		if rec = e.do("GET", "/app/requests/"+rq.ID, viewer, nil); rec.Code != 403 {
			t.Errorf("Betrachter ohne app.admin: %d", rec.Code)
		}
		if rec = e.do("GET", "/app/requests/"+e.id(`SELECT pdh_uuid_v7()::text`), admin, nil); rec.Code != 404 {
			t.Errorf("unbekannte Anfrage: %d", rec.Code)
		}
		if rec = e.do("GET", "/app/requests/kein-uuid", admin, nil); rec.Code != 404 {
			t.Errorf("ungültige Kennung: %d", rec.Code)
		}
	})

	t.Run("falscher Code", func(t *testing.T) {
		rq := e.request(deptA)
		wrong := "000000"
		if rq.Code == wrong {
			wrong = "111111"
		}
		rec := e.approve(rq, admin, wrong, tgtA.ID, allAppTypes, false)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Der Bestätigungscode stimmt nicht mit dem Telefon überein.") {
			t.Fatalf("falscher code: %d %.400s", rec.Code, rec.Body.String())
		}
		if e.devices(rq.ID) != 0 || e.requestStatus(rq.ID) != "pending" || e.id(`SELECT status FROM tickets WHERE id=$1::uuid`, rq.Ticket) == "closed" {
			t.Error("trotz falschem Code gespeichert")
		}
		if rec = e.approve(rq, admin, "", tgtA.ID, allAppTypes, false); rec.Code != 200 || e.devices(rq.ID) != 0 {
			t.Errorf("leerer code: %d", rec.Code)
		}
		// Eingabe mit Leerzeichen und Trennstrich gilt als dieselbe Zahl.
		spaced := rq.Code[:3] + " " + rq.Code[3:]
		if rec = e.approve(rq, admin, spaced, tgtA.ID, allAppTypes, false); rec.Code != 303 {
			t.Errorf("code mit Leerzeichen: %d", rec.Code)
		}
	})

	t.Run("Abteilung und Rang", func(t *testing.T) {
		// Anfrage aus B: Manager (nur A) darf nicht.
		rqB := e.request(deptB)
		if rec := e.do("GET", "/app/requests/"+rqB.ID, mgrA, nil); rec.Code != 403 {
			t.Errorf("Seite fremde Abteilung: %d", rec.Code)
		}
		if rec := e.approve(rqB, mgrA, rqB.Code, tgtA.ID, allAppTypes, false); rec.Code != 403 || e.devices(rqB.ID) != 0 {
			t.Errorf("Anfrage aus B: %d", rec.Code)
		}
		if rec := e.do("POST", "/app/requests/"+rqB.ID+"/reject", mgrA, url.Values{}); rec.Code != 403 || e.requestStatus(rqB.ID) != "pending" {
			t.Errorf("ablehnen aus B: %d", rec.Code)
		}
		// Anfrage ohne Abteilung: nur globales app.admin.
		rqN := e.request("")
		if rec := e.approve(rqN, mgrA, rqN.Code, tgtA.ID, allAppTypes, false); rec.Code != 403 || e.devices(rqN.ID) != 0 {
			t.Errorf("Anfrage ohne Abteilung: %d", rec.Code)
		}
		// Anfrage aus A, aber Zielbenutzer in B / ohne Abteilung / Rolle admin / gleicher Rang.
		rqA := e.request(deptA)
		for name, tgt := range map[string]*users.User{"Zielbenutzer in B": tgtB, "ohne Abteilung": tgtNone, "Rolle admin": tgtAdmin, "gleicher Rang": tgtMgr} {
			if rec := e.approve(rqA, mgrA, rqA.Code, tgt.ID, allAppTypes, false); rec.Code != 403 || e.devices(rqA.ID) != 0 {
				t.Errorf("%s: %d", name, rec.Code)
			}
		}
		// Kontrolle: Zielbenutzer in A klappt.
		if rec := e.approve(rqA, mgrA, rqA.Code, tgtA.ID, allAppTypes, false); rec.Code != 303 {
			t.Errorf("Manager in A: %d %.300s", rec.Code, rec.Body.String())
		}
		// Administrator (überall) darf auch Anfrage ohne Abteilung und Zielbenutzer ohne Abteilung.
		if rec := e.approve(rqN, admin, rqN.Code, tgtNone.ID, allAppTypes, false); rec.Code != 303 {
			t.Errorf("Administrator ohne Abteilung: %d %.300s", rec.Code, rec.Body.String())
		}
	})

	t.Run("Unterabteilung", func(t *testing.T) {
		child := e.id(`INSERT INTO departments (name, parent_id) VALUES ($1, $2::uuid) RETURNING id::text`, "AC"+e.sfx, deptA)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1::uuid`, child) })
		tgtChild := e.au("atC", "viewer", child)
		e.warm() // Rbac kennt die Abteilungsstruktur erst nach dem Neuladen
		rq := e.request(child)
		if rec := e.approve(rq, mgrA, rq.Code, tgtChild.ID, allAppTypes, false); rec.Code != 303 || e.devices(rq.ID) != 1 {
			t.Errorf("Manager in A, Anfrage und Person in Unterabteilung: %d %.300s", rec.Code, rec.Body.String())
		}
	})

	t.Run("Benutzer", func(t *testing.T) {
		sys := e.au("asys", "viewer", "")
		e.exec(`UPDATE users SET is_system_user = true WHERE id = $1::uuid`, sys.ID)
		bot := e.au("abot", "viewer", "")
		e.exec(`UPDATE users SET is_bot = true WHERE id = $1::uuid`, bot.ID)
		off := e.au("aoff", "viewer", "")
		e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, off.ID)
		rq := e.request(deptA)
		for name, id := range map[string]string{"Systembenutzer": sys.ID, "Bot": bot.ID, "inaktiv": off.ID, "unbekannt": e.id(`SELECT pdh_uuid_v7()::text`), "leer": "", "keine Kennung": "x"} {
			if rec := e.approve(rq, admin, rq.Code, id, allAppTypes, false); rec.Code != 422 || e.devices(rq.ID) != 0 {
				t.Errorf("%s: %d", name, rec.Code)
			}
		}
		// Auswahlliste führt sie nicht auf.
		body := e.do("GET", "/app/requests/"+rq.ID, admin, nil).Body.String()
		for _, id := range []string{sys.ID, bot.ID, off.ID} {
			if strings.Contains(body, id) {
				t.Errorf("nicht wählbarer Benutzer %s in der Liste", id)
			}
		}
	})

	t.Run("Vorgangsarten", func(t *testing.T) {
		rq := e.request(deptA)
		rec := e.approve(rq, admin, rq.Code, tgtA.ID, nil, false)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Bitte mindestens eine Vorgangsart wählen.") || e.devices(rq.ID) != 0 {
			t.Errorf("leer: %d %.300s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "checked") && strings.Contains(rec.Body.String(), `name="types[]" value="fault" checked`) {
			t.Error("leere Auswahl wurde beim Neuaufbau wieder vorgewählt")
		}
		if rec = e.approve(rq, admin, rq.Code, tgtA.ID, []string{"fault", "kaffee"}, false); rec.Code != 422 || e.devices(rq.ID) != 0 {
			t.Errorf("unbekannte Art: %d", rec.Code)
		}
		if rec = e.approve(rq, admin, rq.Code, tgtA.ID, []string{"maintenance", "maintenance", "ticket"}, false); rec.Code != 303 {
			t.Fatalf("doppelte Art: %d", rec.Code)
		}
		if got := e.id(`SELECT array_to_string(allowed_types, ',') FROM app_devices WHERE request_id = $1::uuid`, rq.ID); got != "maintenance,ticket" && got != "ticket,maintenance" {
			t.Errorf("allowed_types: %s", got)
		}
	})

	t.Run("may_create", func(t *testing.T) {
		rq := e.request(deptA)
		if rec := e.approve(rq, admin, rq.Code, tgtA.ID, allAppTypes, true); rec.Code != 303 {
			t.Fatalf("freigeben: %d", rec.Code)
		}
		if e.count(`SELECT count(*) FROM app_devices WHERE request_id = $1::uuid AND may_create`, rq.ID) != 0 {
			t.Error("may_create für Benutzer ohne app.create gespeichert")
		}
		withCreate := e.au("acr", "manager", deptA) // Rolle manager hat app.create
		rq2 := e.request(deptA)
		if rec := e.approve(rq2, admin, rq2.Code, withCreate.ID, allAppTypes, true); rec.Code != 303 {
			t.Fatalf("freigeben 2: %d", rec.Code)
		}
		if e.count(`SELECT count(*) FROM app_devices WHERE request_id = $1::uuid AND may_create`, rq2.ID) != 1 {
			t.Error("may_create bei Benutzer mit app.create nicht gespeichert")
		}
	})

	t.Run("bereits entschieden", func(t *testing.T) {
		rq := e.request(deptA)
		if rec := e.approve(rq, admin, rq.Code, tgtA.ID, allAppTypes, false); rec.Code != 303 {
			t.Fatalf("erste: %d", rec.Code)
		}
		before := e.id(`SELECT decided_at::text || device_id::text FROM app_requests WHERE id = $1::uuid`, rq.ID)
		if rec := e.approve(rq, admin, rq.Code, tgtB.ID, []string{"fault"}, false); rec.Code != 409 {
			t.Errorf("zweite Freigabe: %d", rec.Code)
		}
		if rec := e.do("POST", "/app/requests/"+rq.ID+"/reject", admin, url.Values{}); rec.Code != 409 {
			t.Errorf("Ablehnen nach Freigabe: %d", rec.Code)
		}
		if e.devices(rq.ID) != 1 || e.requestStatus(rq.ID) != "approved" ||
			e.id(`SELECT decided_at::text || device_id::text FROM app_requests WHERE id = $1::uuid`, rq.ID) != before {
			t.Error("entschiedene Anfrage verändert")
		}
		if rec := e.do("GET", "/app/requests/"+rq.ID, admin, nil); rec.Code != 200 || strings.Contains(rec.Body.String(), `name="confirm_code"`) {
			t.Errorf("Seite einer entschiedenen Anfrage: %d", rec.Code)
		}
	})

	t.Run("gleichzeitig", func(t *testing.T) {
		rq := e.request(deptA)
		var wg sync.WaitGroup
		codes := make([]int, 6)
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = e.approve(rq, admin, rq.Code, tgtA.ID, allAppTypes, false).Code
			}(i)
		}
		wg.Wait()
		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case 303:
				ok++
			case 409:
				conflict++
			}
		}
		if ok != 1 || conflict != len(codes)-1 || e.devices(rq.ID) != 1 {
			t.Errorf("parallel: %v, Geräte %d", codes, e.devices(rq.ID))
		}
	})
}

func TestAppReject(t *testing.T) {
	e := newAppAdmEnv(t)
	deptA := e.dept("A")
	admin := e.au("radm", "admin", "")
	viewer := e.au("rvw", "viewer", "")
	e.warm()

	rq := e.request(deptA)
	if rec := e.do("POST", "/app/requests/"+rq.ID+"/reject", viewer, url.Values{}); rec.Code != 403 || e.requestStatus(rq.ID) != "pending" {
		t.Errorf("Betrachter: %d", rec.Code)
	}
	rec := e.do("POST", "/app/requests/"+rq.ID+"/reject", admin, url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/app/devices?revoked=1" {
		t.Fatalf("ablehnen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if e.requestStatus(rq.ID) != "rejected" || e.devices(rq.ID) != 0 {
		t.Error("Anfrage nicht abgelehnt")
	}
	if n := e.count(`SELECT count(*) FROM app_requests WHERE id = $1::uuid AND decided_at IS NOT NULL AND decided_by = $2::uuid`, rq.ID, admin.ID); n != 1 {
		t.Error("decided_* fehlt")
	}
	if s := e.id(`SELECT status FROM tickets WHERE id = $1::uuid`, rq.Ticket); s != "closed" {
		t.Errorf("ticket: %s", s)
	}
	if n := e.count(`SELECT count(*) FROM record_history WHERE ref_type = 'app_request' AND ref_id = $1::uuid AND action = 'app_request_rejected' AND created_by = $2::uuid`, rq.ID, admin.ID); n != 1 {
		t.Errorf("historie: %d", n)
	}
	got := e.get(rq.IP, rq.ID, rq.Key, e.nonce("r1"))
	if m := decodeAppReq(t, got); got.Code != 200 || m.Status != "rejected" || m.Token != "" {
		t.Errorf("abholen: %d %+v", got.Code, m)
	}
	if rec := e.do("POST", "/app/requests/"+rq.ID+"/reject", admin, url.Values{}); rec.Code != 409 {
		t.Errorf("zweites Ablehnen: %d", rec.Code)
	}
}

// device legt ein freigegebenes Gerät mit Token an (für Sperr-Tests).
func (e *appAdmEnv) device(user *users.User) (devID, token string, key *ecdsa.PrivateKey) {
	e.t.Helper()
	k, pub := appTestKey(e.t)
	tok, err := newAppToken()
	e.must(err)
	devID = e.id(`INSERT INTO app_devices (user_id, public_key, key_fingerprint, token_sha256, platform, app_version, label)
		VALUES ($1::uuid, $2, $3, $4, 'android', '1.0.0', $5) RETURNING id::text`, user.ID, pub, appKeyFingerprint(pub), hashAppToken(tok), "Gerät "+e.sfx)
	e.t.Cleanup(func() { appDeviceCache.forget(devID) })
	return devID, tok, k
}

func (e *appAdmEnv) me(tok string, k *ecdsa.PrivateKey, n string) *httptest.ResponseRecorder {
	req := appSignedRequest(e.t, k, "GET", "/api/app/v1/me", nil, time.Now(), "rv-"+n+"-"+e.sfx+"00000000")
	req.Header.Set("Authorization", "PDHApp "+tok)
	req.RemoteAddr = "192.0.2.88:5000"
	rec := httptest.NewRecorder()
	e.adm.ServeHTTP(rec, req)
	return rec
}

func TestAppRevokeAndSettings(t *testing.T) {
	e := newAppAdmEnv(t)
	oldCache := appDeviceCache
	t.Cleanup(func() { appDeviceCache = oldCache })
	appDeviceCache = newDeviceCache(30 * time.Second) // 30 s: nur forget() lässt die Sperre sofort wirken
	deptA, deptB := e.dept("A"), e.dept("B")
	admin := e.au("vadm", "admin", "")
	mgrA := e.au("vmgrA", "manager", deptA)
	e.grant(mgrA.ID, permAppAdmin, deptA, true)
	viewer := e.au("vvw", "viewer", "")
	e.grant(viewer.ID, permAppUse, "", false) // Rolle darf app.use, dieser Benutzer ausdrücklich nicht
	tgtA := e.au("vtA", "worker", deptA)
	tgtB := e.au("vtB", "worker", deptB)
	tgtMgr := e.au("vtMg", "manager", deptA)
	e.grant(tgtA.ID, permAppUse, "", true)
	e.grant(tgtB.ID, permAppUse, "", true)
	e.warm()
	devA, tokA, keyA := e.device(tgtA)
	devB, _, _ := e.device(tgtB)
	devM, _, _ := e.device(tgtMgr)

	t.Run("Sperren wirkt sofort", func(t *testing.T) {
		if rec := e.me(tokA, keyA, "1"); rec.Code != 200 {
			t.Fatalf("vor Sperre: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.do("POST", "/app/devices/"+devA+"/revoke", viewer, url.Values{}); rec.Code != 403 {
			t.Errorf("Betrachter: %d", rec.Code)
		}
		rec := e.do("POST", "/app/devices/"+devA+"/revoke", mgrA, url.Values{})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/devices?revoked=1" {
			t.Fatalf("sperren: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.me(tokA, keyA, "2"); rec.Code != 401 || !strings.Contains(rec.Body.String(), "app_revoked") {
			t.Errorf("nach Sperre: %d %s", rec.Code, rec.Body.String())
		}
		if n := e.count(`SELECT count(*) FROM app_devices WHERE id = $1::uuid AND revoked_at IS NOT NULL AND revoked_by = $2::uuid`, devA, mgrA.ID); n != 1 {
			t.Error("revoked_* fehlt")
		}
		if n := e.count(`SELECT count(*) FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND action = 'app_device_revoked' AND created_by = $2::uuid`, tgtA.ID, mgrA.ID); n != 1 {
			t.Errorf("historie: %d", n)
		}
		if rec := e.do("POST", "/app/devices/"+devA+"/revoke", mgrA, url.Values{}); rec.Code != 409 {
			t.Errorf("zweites Sperren: %d", rec.Code)
		}
		if n := e.count(`SELECT count(*) FROM record_history WHERE action = 'app_device_revoked' AND ref_id = $1::uuid`, tgtA.ID); n != 1 {
			t.Errorf("Historie doppelt: %d", n)
		}
		if rec := e.do("POST", "/app/devices/00000000-0000-4000-8000-00000000dead/revoke", admin, url.Values{}); rec.Code != 404 {
			t.Errorf("unbekannt: %d", rec.Code)
		}
	})

	t.Run("Abteilung und Rang", func(t *testing.T) {
		list := e.do("GET", "/app/devices", mgrA, nil)
		if list.Code != 200 {
			t.Fatalf("liste: %d %.200s", list.Code, list.Body.String())
		}
		if strings.Contains(list.Body.String(), devB) {
			t.Error("Gerät aus Abteilung B sichtbar")
		}
		if !strings.Contains(list.Body.String(), "Gerät "+e.sfx) {
			t.Error("Gerät aus A fehlt")
		}
		if rec := e.do("POST", "/app/devices/"+devB+"/revoke", mgrA, url.Values{}); rec.Code != 403 {
			t.Errorf("fremde Abteilung: %d", rec.Code)
		}
		if rec := e.do("POST", "/app/devices/"+devM+"/revoke", mgrA, url.Values{}); rec.Code != 403 { // gleicher Rang
			t.Errorf("gleicher Rang: %d", rec.Code)
		}
		if n := e.count(`SELECT count(*) FROM app_devices WHERE id = ANY($1::uuid[]) AND revoked_at IS NOT NULL`, []string{devB, devM}); n != 0 {
			t.Errorf("trotz 403 gesperrt: %d", n)
		}
		if rec := e.do("GET", "/app/devices", viewer, nil); rec.Code != 403 {
			t.Errorf("Liste ohne Recht: %d", rec.Code)
		}
	})

	t.Run("Einstellungen", func(t *testing.T) {
		for _, k := range []string{keyAppAddresses, keyAppAllowPrevious, keyAppLatestPrefix + "android", keyAppPrevPrefix + "android", keyAppStorePrefix + "android", keyAppLatestPrefix + "ios", keyAppPrevPrefix + "ios", keyAppStorePrefix + "ios"} {
			k := k
			_, _ = e.pool.Exec(e.ctx, `DELETE FROM app_settings WHERE key = $1`, k)
			e.t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, k) })
		}
		form := func(latest string) url.Values {
			return url.Values{"addresses": {"https://pdh.example.org/\nhttp://192.0.2.5:8090"}, "allow_previous": {"0"},
				"latest_android": {latest}, "latest_ios": {""}, "store_android": {"https://play.example.org/x"}, "store_ios": {""}}
		}
		get := func(k string) string {
			var v string
			_ = e.pool.QueryRow(e.ctx, `SELECT value FROM app_settings WHERE key = $1`, k).Scan(&v)
			return v
		}
		if rec := e.do("POST", "/app/settings", mgrA, form("1.2.0")); rec.Code != 403 {
			t.Errorf("Abteilungs-Admin: %d", rec.Code)
		}
		if get(keyAppLatestPrefix+"android") != "" {
			t.Error("trotz 403 gespeichert")
		}
		if rec := e.do("POST", "/app/settings", admin, form("1.2.0")); rec.Code != 303 {
			t.Fatalf("speichern: %d %.300s", rec.Code, rec.Body.String())
		}
		if get(keyAppLatestPrefix+"android") != "1.2.0" || get(keyAppAllowPrevious) != "0" || get(keyAppStorePrefix+"android") != "https://play.example.org/x" ||
			get(keyAppAddresses) != "https://pdh.example.org\nhttp://192.0.2.5:8090" {
			t.Errorf("Werte: %q %q %q %q", get(keyAppLatestPrefix+"android"), get(keyAppAllowPrevious), get(keyAppStorePrefix+"android"), get(keyAppAddresses))
		}
		if get(keyAppPrevPrefix+"android") != "" {
			t.Error("erste Version darf keine vorige setzen")
		}
		if rec := e.do("POST", "/app/settings", admin, form("1.2.0")); rec.Code != 303 || get(keyAppPrevPrefix+"android") != "" {
			t.Errorf("unverändert: %d prev=%q", rec.Code, get(keyAppPrevPrefix+"android"))
		}
		if rec := e.do("POST", "/app/settings", admin, form("1.3.0")); rec.Code != 303 {
			t.Fatalf("neue Version: %d", rec.Code)
		}
		if get(keyAppLatestPrefix+"android") != "1.3.0" || get(keyAppPrevPrefix+"android") != "1.2.0" {
			t.Errorf("Version: %q vorige %q", get(keyAppLatestPrefix+"android"), get(keyAppPrevPrefix+"android"))
		}
		bad := form("1.4.0")
		bad.Set("addresses", "ftp://x")
		rec := e.do("POST", "/app/settings", admin, bad)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "http://") {
			t.Errorf("ftp: %d %.200s", rec.Code, rec.Body.String())
		}
		if get(keyAppLatestPrefix+"android") != "1.3.0" {
			t.Error("bei Fehler wurde gespeichert")
		}
		for _, a := range []string{"https://user:pw@pdh.example.org", "https://pdh.example.org/?x=1", "https://pdh.example.org/#frag"} {
			b := form("1.4.0")
			b.Set("addresses", a)
			if rec := e.do("POST", "/app/settings", admin, b); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("Adresse %q: %d", a, rec.Code)
			}
		}
		// 1.3 ist zahlenmäßig 1.3.0: die vorige Version bleibt
		if rec := e.do("POST", "/app/settings", admin, form("1.3")); rec.Code != 303 || get(keyAppPrevPrefix+"android") != "1.2.0" {
			t.Errorf("gleiche Version anders geschrieben: %d prev=%q", rec.Code, get(keyAppPrevPrefix+"android"))
		}
		if rec := e.do("POST", "/app/settings", admin, form("abc")); rec.Code != http.StatusUnprocessableEntity || get(keyAppLatestPrefix+"android") != "1.3" {
			t.Errorf("Version abc: %d", rec.Code)
		}
		if rec := e.do("POST", "/app/settings", admin, form("")); rec.Code != 303 || get(keyAppPrevPrefix+"android") != "" {
			t.Errorf("neueste Version geleert: %d prev=%q", rec.Code, get(keyAppPrevPrefix+"android"))
		}
		page := e.do("GET", "/app/devices", admin, nil)
		if !strings.Contains(page.Body.String(), `name="latest_android"`) {
			t.Error("Formular fehlt für globalen Admin")
		}
		if strings.Contains(e.do("GET", "/app/devices", mgrA, nil).Body.String(), `name="latest_android"`) {
			t.Error("Formular für Abteilungs-Admin sichtbar")
		}
	})

	t.Run("App installieren", func(t *testing.T) {
		if rec := e.do("GET", "/app/install", viewer, nil); rec.Code != 403 {
			t.Errorf("ohne app.use: %d", rec.Code)
		}
		rec := e.do("GET", "/app/install", tgtA, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<svg") || !strings.Contains(rec.Body.String(), "/app/setup") {
			t.Errorf("mit app.use: %d %.200s", rec.Code, rec.Body.String())
		}
	})

	t.Run("Benutzermenü", func(t *testing.T) {
		req := withUser(httptest.NewRequest("GET", "/", nil), viewer)
		if b := e.h.baseData(req, "dashboard", "Dashboard", ""); b.CanAppUse {
			t.Error("CanAppUse ohne app.use")
		}
		req = withUser(httptest.NewRequest("GET", "/", nil), tgtA)
		if b := e.h.baseData(req, "dashboard", "Dashboard", ""); !b.CanAppUse {
			t.Error("CanAppUse mit app.use fehlt")
		}
	})
}
