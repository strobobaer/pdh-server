package web

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/tiamqtt"
)

// Seite „TIA-Baustein herunterladen“ an einer MQTT-Import-Verbindung: Die
// Person lädt die Quelle ihres Datenbausteins (.db) hoch, PDH erzeugt daraus
// die SCL-Quellen für das TIA Portal (Paket tiamqtt) und liefert sie als ZIP.
// Auf Wunsch legt PDH gleich die passenden Zuordnungen an – je Wert eine und
// eine für den Heartbeat (heartbeat_seconds).

const (
	permImportTIA = "import.tia"
	tiaMaxUpload  = 1 << 20  // Datei höchstens 1 MiB
	tiaFormExtra  = 64 << 10 // Platz für die übrigen Formularfelder
	tiaRefMax     = 500      // import_mappings.source_ref ist VARCHAR(500)
	tiaNameMax    = 150      // import_mappings.name ist VARCHAR(150)
)

// tiaForm sind die Eingaben der Seite (nie das Kennwort).
type tiaForm struct {
	Variant, Family, BrokerHost, ClientID, Username, Topic, InfraID     string
	BrokerPort, HeartbeatSeconds, KeepAliveSeconds, HwID, ConnID, Limit int
	UseTLS, Retain, IncludePassword, CreateMappings                     bool
}

// tiaTool: Werkzeug auf der Verbindungsseite (nur MQTT-Import mit import.tia).
func tiaTool(id string) connTool {
	return connTool{"/import/connections/" + id + "/tia", "ti-cpu", "TIA-Baustein herunterladen", "SCL-Baustein für Siemens-Steuerungen: Werte per Trigger, Heartbeat"}
}

// tiaCan: Recht für diese Verbindung (mit Abteilungsebene, wie efficioCan).
func (h *Handler) tiaCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	return h.hasPermRec(r, perm, "import_connection", id)
}

// tiaSlug: Verbindungsname klein, nur a-z, 0-9 und -.
func tiaSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	if out := strings.TrimRight(b.String(), "-"); out != "" {
		return out
	}
	return "pdh"
}

// tiaHost: Adresse, unter der die Steuerung den integrierten Broker erreicht –
// der Host der Anfrage (wie MqttBrokerPage.SuggestedHost), bei einem Namen die
// erste IPv4-Adresse (die eigenständige Variante braucht eine IPv4-Adresse).
func tiaHost(reqHost string) string {
	host := reqHost
	if hh, _, err := net.SplitHostPort(reqHost); err == nil {
		host = hh
	}
	host = strings.Trim(host, "[]")
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host); err == nil {
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return host
}

func tiaAtoi(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

// tiaDefaults: Vorbelegung aus der Verbindung. Client-ID bleibt leer
// (pdh-sps-<DB>), das Topic endet auf „/“ und bekommt den Namen des
// Datenbausteins erst nach dem Hochladen.
func tiaDefaults(config map[string]string, connName, reqHost string) tiaForm {
	f := tiaForm{
		Variant: string(tiamqtt.VariantStandalone), Family: string(tiamqtt.FamilyS71500),
		Topic:            "sps/" + tiaSlug(connName) + "/",
		HeartbeatSeconds: 60, KeepAliveSeconds: 60, HwID: 64, ConnID: 1, Limit: tiamqtt.DefaultLimit,
	}
	if config["broker_mode"] == "integrated" {
		f.BrokerHost = tiaHost(reqHost)
		f.BrokerPort = tiaAtoi(config["int_listen_port"], 1883)
		f.Username = strings.TrimSpace(config["int_broker_username"])
		f.UseTLS = strings.TrimSpace(config["int_tls_cert_path"]) != "" && strings.TrimSpace(config["int_tls_key_path"]) != ""
	} else {
		f.BrokerHost = strings.TrimSpace(config["ext_broker_host"])
		f.BrokerPort = tiaAtoi(config["ext_broker_port"], 1883)
		f.Username = strings.TrimSpace(config["ext_username"])
		f.UseTLS = config["ext_use_tls"] == "true"
	}
	if f.UseTLS {
		f.Variant = string(tiamqtt.VariantLMQTT) // die eigenständige Variante kann kein TLS
	}
	return f
}

// tiaFormFromRequest liest die Eingaben (Formular schon gelesen oder
// urlencoded). Bereiche, die Generate prüft (Heartbeat, Keep-Alive, hwId,
// connId, TLS bei eigenständig), meldet Generate selbst.
func tiaFormFromRequest(r *http.Request) (tiaForm, []string) {
	var errs []string
	num := func(key, label string) int {
		v := strings.TrimSpace(r.FormValue(key))
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, "„"+label+"“ muss eine ganze Zahl sein")
		}
		return n
	}
	f := tiaForm{
		Variant:          strings.TrimSpace(r.FormValue("variant")),
		Family:           strings.TrimSpace(r.FormValue("family")),
		BrokerHost:       strings.TrimSpace(r.FormValue("broker_host")),
		ClientID:         strings.TrimSpace(r.FormValue("client_id")),
		Username:         strings.TrimSpace(r.FormValue("username")),
		Topic:            strings.TrimSpace(r.FormValue("topic")),
		InfraID:          strings.TrimSpace(r.FormValue("infrastructure_id")),
		BrokerPort:       num("broker_port", "Port"),
		HeartbeatSeconds: num("heartbeat_seconds", "Heartbeat"),
		KeepAliveSeconds: num("keepalive_seconds", "Keep-Alive"),
		HwID:             num("hw_id", "Hardware-Kennung"),
		ConnID:           num("conn_id", "Verbindungsnummer"),
		Limit:            num("limit", "Größengrenze"),
		UseTLS:           r.FormValue("use_tls") == "on",
		Retain:           r.FormValue("retain") == "on",
		IncludePassword:  r.FormValue("include_password") == "on",
		CreateMappings:   r.FormValue("create_mappings") == "on",
	}
	if f.Variant != string(tiamqtt.VariantStandalone) && f.Variant != string(tiamqtt.VariantLMQTT) {
		errs = append(errs, "Bitte eine Variante wählen: eigenständig oder LMQTT")
	}
	if f.Family != string(tiamqtt.FamilyS71500) && f.Family != string(tiamqtt.FamilyS71200) {
		errs = append(errs, "Bitte die Steuerung wählen: S7-1500 oder S7-1200")
	}
	if f.BrokerHost == "" {
		errs = append(errs, "Bitte die Adresse des Brokers angeben")
	}
	if f.BrokerPort < 1 || f.BrokerPort > 65535 {
		errs = append(errs, "Der Port muss zwischen 1 und 65535 liegen")
	}
	if err := mqttValidPublishTopic(f.Topic); err != nil {
		errs = append(errs, err.Error())
	}
	if f.Limit < tiamqtt.MinLimit || f.Limit > tiamqtt.MaxLimit {
		errs = append(errs, "Die Größengrenze muss zwischen 1024 und 16384 Byte liegen")
	}
	if f.CreateMappings && !meterUUIDRe.MatchString(f.InfraID) {
		errs = append(errs, "Bitte eine Anlage für die Zuordnungen wählen")
	}
	return f, tiaDedupe(errs)
}

func tiaDedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// tiaTopicPart macht aus dem Namen des Datenbausteins eine Topic-Ebene.
func tiaTopicPart(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '+' || r == '#' || r == '/' || r < 0x20 || r == ' ' {
			return '_'
		}
		return r
	}, s)
}

// tiaTopic ergänzt ein Topic, das auf „/“ endet, um den Namen des Datenbausteins.
func tiaTopic(topic, dbName string) string {
	if strings.HasSuffix(topic, "/") {
		return topic + tiaTopicPart(dbName)
	}
	return topic
}

// tiaResult ist der gelesene, aufgeteilte und erzeugte Baustein.
type tiaResult struct {
	DB     *tiamqtt.DBSource
	Layout *tiamqtt.Layout
	Files  []tiamqtt.File
	Topic  string
}

// tiaBuild liest die Quelle, teilt sie auf und erzeugt die Dateien. Die
// Fehler sind die Meldungen des Pakets tiamqtt in Anwendersprache.
func tiaBuild(f tiaForm, src []byte, password, by string) (*tiaResult, error) {
	db, err := tiamqtt.ParseDBSource(src)
	if err != nil {
		return nil, err
	}
	l, err := tiamqtt.Plan(db, tiamqtt.Family(f.Family), f.Limit)
	if err != nil {
		return nil, err
	}
	res := &tiaResult{DB: db, Layout: l, Topic: tiaTopic(f.Topic, db.Name)}
	res.Files, err = tiamqtt.Generate(db, l, tiamqtt.Options{
		Variant: tiamqtt.Variant(f.Variant), Family: tiamqtt.Family(f.Family),
		BrokerHost: f.BrokerHost, BrokerPort: f.BrokerPort, UseTLS: f.UseTLS,
		ClientID: f.ClientID, Username: f.Username, Password: password, IncludePassword: f.IncludePassword,
		Topic: res.Topic, HeartbeatSeconds: f.HeartbeatSeconds, KeepAliveSeconds: f.KeepAliveSeconds,
		Retain: f.Retain, HwID: f.HwID, ConnID: f.ConnID, GeneratedBy: by,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func tiaTruncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// tiaMappingRows: je gesendeten Wert eine Zuordnung, dazu eine für den
// Heartbeat (nur wenn heartbeat > 0; nur sie hat heartbeat_seconds).
func tiaMappingRows(l *tiamqtt.Layout, dbName, topic string, heartbeat int) []importMappingCreateInput {
	var out []importMappingCreateInput
	for _, lf := range l.Leaves {
		out = append(out, importMappingCreateInput{
			SourceRef: mqttSourceRef(topic, lf.JSONPath),
			Name:      tiaTruncate(dbName+"."+lf.Path, tiaNameMax),
			Note:      tiaTruncate("Aus dem TIA-Baustein, Typ "+tiamqtt.TypeName(lf.Kind, lf.StrLen), 500),
		})
	}
	if heartbeat > 0 {
		out = append(out, importMappingCreateInput{
			SourceRef:        mqttSourceRef(topic+"/heartbeat", "count"),
			Name:             tiaTruncate(dbName+".Heartbeat", tiaNameMax),
			Note:             "Heartbeat der Steuerung alle " + strconv.Itoa(heartbeat) + " s",
			HeartbeatSeconds: heartbeat,
		})
	}
	return out
}

// ── Prüfergebnis (htmx-Fragment) ────────────────────────────

type tiaCheckValue struct{ Path, JSONPath, Type string }

type tiaCheckPart struct {
	No, MaxBytes int
	Values       []tiaCheckValue
}

type tiaCheckView struct {
	Errors           []string
	DBName, Topic    string
	Parts            []tiaCheckPart
	Skipped          []tiamqtt.Skipped
	Values, Mappings int
	HasRetain        bool
}

func tiaCheck(f tiaForm, src []byte, errs []string) tiaCheckView {
	if len(errs) > 0 {
		return tiaCheckView{Errors: errs}
	}
	res, err := tiaBuild(f, src, "", "")
	if err != nil {
		return tiaCheckView{Errors: []string{err.Error()}}
	}
	v := tiaCheckView{DBName: res.DB.Name, Topic: res.Topic, Skipped: res.Layout.Skipped, Values: len(res.Layout.Leaves), HasRetain: res.DB.HasRetain}
	for _, p := range res.Layout.Parts {
		cp := tiaCheckPart{No: p.No, MaxBytes: p.MaxBytes}
		for _, lf := range p.Leaves {
			cp.Values = append(cp.Values, tiaCheckValue{lf.Path, lf.JSONPath, tiamqtt.TypeName(lf.Kind, lf.StrLen)})
		}
		v.Parts = append(v.Parts, cp)
	}
	v.Mappings = len(tiaMappingRows(res.Layout, res.DB.Name, res.Topic, f.HeartbeatSeconds))
	return v
}

// ── Seite ───────────────────────────────────────────────────

// TIABlockPageData ist das Datenmodell der Seite.
type TIABlockPageData struct {
	BaseData
	ID, Name           string
	Form               tiaForm
	Integrated         bool
	CanWrite           bool // Kennwort mitliefern, Zuordnungen anlegen
	HasPassword        bool // die Verbindung hat ein Kennwort
	Errors             []string
	InfraLabel         string
	InfoURL            string
	MinLimit, MaxLimit int
}

var errTIANotMQTT = errors.New("keine MQTT-Import-Verbindung")

// tiaConn liest die Verbindung; nur MQTT-Import, sonst errTIANotMQTT.
// Ein nicht lesbares Kennwort ist kein Fehler (secretErr).
func (h *Handler) tiaConn(ctx context.Context, id string) (name string, config map[string]string, secretErr error, err error) {
	if h.db == nil || !uuidInPathRe.MatchString(id) {
		return "", nil, nil, errTIANotMQTT
	}
	name, kind, _, config, err := h.connectionRow(ctx, id, "import")
	if isConnSecretErr(err) {
		secretErr, err = err, nil
	}
	if err != nil || kind != "mqtt" {
		return "", nil, nil, errTIANotMQTT
	}
	return name, config, secretErr, nil
}

func tiaPassword(config map[string]string) string {
	if config["broker_mode"] == "integrated" {
		return config["int_broker_password"]
	}
	return config["ext_password"]
}

func (h *Handler) tiaPageData(r *http.Request, id, name string, config map[string]string, f tiaForm, errs []string) TIABlockPageData {
	d := TIABlockPageData{
		BaseData: h.baseData(r, "import", "TIA-Baustein herunterladen", name),
		ID:       id, Name: name, Form: f, Errors: errs,
		Integrated:  config["broker_mode"] == "integrated",
		CanWrite:    h.tiaCan(r, "import.write", id),
		HasPassword: tiaPassword(config) != "",
		InfoURL:     tiamqtt.LMQTTInfoURL, MinLimit: tiamqtt.MinLimit, MaxLimit: tiamqtt.MaxLimit,
	}
	if meterUUIDRe.MatchString(f.InfraID) {
		d.InfraLabel = h.infraPaths(r.Context(), []string{f.InfraID})[f.InfraID]
	}
	return d
}

// TIABlockPage: GET /import/connections/{id}/tia.
func (h *Handler) TIABlockPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.tiaCan(r, permImportTIA, id) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	name, config, _, err := h.tiaConn(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.render(w, "tia_block", h.tiaPageData(r, id, name, config, tiaDefaults(config, name, r.Host), nil))
}

// tiaReadRequest liest das Formular samt hochgeladener Datei (höchstens 1 MiB).
func tiaReadRequest(w http.ResponseWriter, r *http.Request) (tiaForm, []byte, []string) {
	r.Body = http.MaxBytesReader(w, r.Body, tiaMaxUpload+tiaFormExtra)
	if err := r.ParseMultipartForm(tiaMaxUpload + tiaFormExtra); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return tiaForm{}, nil, []string{"Die Datei ist größer als 1 MiB"}
		}
		return tiaForm{}, nil, []string{"Das Formular konnte nicht gelesen werden"}
	}
	f, errs := tiaFormFromRequest(r)
	file, _, err := r.FormFile("db_file")
	if err != nil {
		return f, nil, append(errs, "Bitte die Quelle des Datenbausteins (.db) hochladen")
	}
	defer file.Close()
	src, err := io.ReadAll(io.LimitReader(file, tiaMaxUpload+1))
	if err != nil {
		return f, nil, append(errs, "Die Datei konnte nicht gelesen werden")
	}
	if len(src) > tiaMaxUpload {
		return f, nil, append(errs, "Die Datei ist größer als 1 MiB")
	}
	if len(src) == 0 {
		return f, nil, append(errs, "Die Datei ist leer")
	}
	return f, src, errs
}

// TIABlockCheckWeb: POST /import/connections/{id}/tia/check – Fragment mit den
// erkannten Werten je Nachricht und den übersprungenen Variablen.
func (h *Handler) TIABlockCheckWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.tiaCan(r, permImportTIA, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	f, src, errs := tiaReadRequest(w, r)
	if _, _, _, err := h.tiaConn(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	h.renderFragment(w, "tia-block-check", tiaCheck(f, src, errs))
}

// TIABlockDownload: POST /import/connections/{id}/tia/download – ZIP mit den
// SCL-Quellen; legt auf Wunsch die Zuordnungen an. Mit Kennwort oder
// Zuordnungen zusätzlich import.write.
func (h *Handler) TIABlockDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.tiaCan(r, permImportTIA, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	f, src, errs := tiaReadRequest(w, r)
	if (f.IncludePassword || f.CreateMappings) && !h.tiaCan(r, "import.write", id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	name, config, secretErr, err := h.tiaConn(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fail := func(msgs ...string) {
		shown := f
		if shown == (tiaForm{}) { // Formular nicht lesbar (zu groß): Vorgaben zeigen
			shown = tiaDefaults(config, name, r.Host)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		h.render(w, "tia_block", h.tiaPageData(r, id, name, config, shown, msgs))
	}
	if len(errs) > 0 {
		fail(errs...)
		return
	}
	password := ""
	if f.IncludePassword {
		if secretErr != nil {
			fail("Das Kennwort der Verbindung ist nicht lesbar – bitte ohne Kennwort herunterladen oder das Kennwort an der Verbindung neu eingeben")
			return
		}
		password = tiaPassword(config)
	}
	u := getUser(r)
	res, err := tiaBuild(f, src, password, u.Username)
	if err != nil {
		fail(err.Error())
		return
	}
	zipped, err := tiamqtt.Zip(res.Files)
	if err != nil {
		fail("Die ZIP-Datei konnte nicht erstellt werden")
		return
	}
	created := 0
	if f.CreateMappings {
		rows := tiaMappingRows(res.Layout, res.DB.Name, res.Topic, f.HeartbeatSeconds)
		hbOffRef := ""
		if f.HeartbeatSeconds == 0 {
			hbOffRef = mqttSourceRef(res.Topic+"/heartbeat", "count")
		}
		if created, err = h.tiaCreateMappings(ctx, id, f.InfraID, u.ID, rows, hbOffRef); err != nil {
			componentLog("import").Warn().Err(err).Str("verbindung", id).Msg("tia-baustein: zuordnungen anlegen")
			msg := "Die Zuordnungen konnten nicht angelegt werden – existiert die gewählte Anlage noch?"
			if errors.Is(err, errTIARefTooLong) {
				msg = err.Error()
			}
			fail(msg)
			return
		}
		h.reconcileMqttConsumer(id)
	}
	if f.IncludePassword || f.CreateMappings {
		msg := "TIA-Baustein heruntergeladen"
		if f.IncludePassword {
			msg += " (mit Kennwort)"
		}
		if f.CreateMappings {
			msg += ", " + strconv.Itoa(created) + " Zuordnung(en) angelegt"
		}
		h.addHistory(ctx, "connection", id, "tia_download", "", "", res.DB.Name, msg, u.ID)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": tiamqtt.ZipName(res.DB.Name, tiamqtt.Variant(f.Variant))}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(zipped)
}

var errTIARefTooLong = errors.New("Ein Pfad im Datenbaustein ist zu lang für eine Zuordnung (höchstens 500 Zeichen samt Topic) – bitte ohne Zuordnungen herunterladen oder kürzere Namen verwenden")

// tiaInsertMappingSQL legt eine Zuordnung nur an, wenn es für diese
// Verbindung noch keine mit demselben Quellverweis gibt (vorhandene bleiben
// unverändert, auch mit anderem Namen). Nur bei der Heartbeat-Zuordnung
// wird ein geändertes Intervall nachgezogen (tiaUpdateHeartbeatSQL).
const tiaInsertMappingSQL = `
	INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by, heartbeat_seconds)
	SELECT $1::uuid, $2::text, $3::uuid, $4::text, $5::text, $6::uuid, NULLIF($7::int, 0)
	WHERE NOT EXISTS (SELECT 1 FROM import_mappings WHERE connection_id = $1::uuid AND source_ref = $2::text)`

// tiaUpdateHeartbeatSQL: vorhandene Heartbeat-Zuordnung bekommt das neue
// Intervall (0 = aus, NULL) – nur diese Spalte, Name, Anlage und Notiz bleiben.
const tiaUpdateHeartbeatSQL = `
	UPDATE import_mappings SET heartbeat_seconds = NULLIF($3::int, 0)
	WHERE connection_id = $1::uuid AND source_ref = $2::text AND heartbeat_seconds IS DISTINCT FROM NULLIF($3::int, 0)`

// tiaCreateMappings legt die Zuordnungen in einer Transaktion an und liefert,
// wie viele neu sind. Heartbeat 0 erzeugt keine Heartbeat-Zeile; eine vorhandene
// Heartbeat-Zuordnung (hbOffRef) verliert ihr Intervall, wird also nicht mehr
// überwacht.
func (h *Handler) tiaCreateMappings(ctx context.Context, connID, infraID, userID string, rows []importMappingCreateInput, hbOffRef string) (int, error) {
	for _, m := range rows {
		if len([]rune(m.SourceRef)) > tiaRefMax {
			return 0, errTIARefTooLong
		}
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// zwei gleichzeitige Downloads legen nichts doppelt an
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('import_mappings:' || $1::text))`, connID); err != nil {
		return 0, err
	}
	n := 0
	for _, m := range rows {
		tag, err := tx.Exec(ctx, tiaInsertMappingSQL, connID, m.SourceRef, infraID, m.Name, m.Note, nullID(userID), m.HeartbeatSeconds)
		if err != nil {
			return 0, err
		}
		n += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 && m.HeartbeatSeconds > 0 {
			if _, err := tx.Exec(ctx, tiaUpdateHeartbeatSQL, connID, m.SourceRef, m.HeartbeatSeconds); err != nil {
				return 0, err
			}
		}
	}
	if hbOffRef != "" {
		if _, err := tx.Exec(ctx, tiaUpdateHeartbeatSQL, connID, hbOffRef, 0); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit(ctx)
}
