package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// ── Datensatz-Links ──────────────────────────────────────────

type chatLinkType struct {
	path, refType, table, titleExpr, label, icon string
}

// In Nachrichten erkannte PDH-Pfade -> Datensatz-Karte (Titel wird beim
// Senden als Momentaufnahme gespeichert).
var chatLinkTypes = []chatLinkType{
	{"tickets", "ticket", "tickets", "title", "Ticket", "ti-ticket"},
	{"faults", "fault", "faults", "title", "Störung", "ti-alert-triangle"},
	{"tasks", "task", "tasks", "title", "Aufgabe", "ti-checkbox"},
	{"projects", "project", "projects", "name", "Projekt", "ti-briefcase"},
	{"maintenance/tasks", "maintenance_task", "maintenance_tasks", "title", "Wartung", "ti-tool"},
	{"infrastructure", "infrastructure", "infrastructure", "name", "Anlage", "ti-hierarchy-2"},
	{"inventory", "spare_part", "spare_parts", "part_number || ' · ' || name", "Ersatzteil", "ti-package"},
	{"it", "it_asset", "it_assets", "name", "IT-Asset", "ti-server-2"},
	{"directory", "business_partner", "business_partners", "name", "Partner", "ti-building-factory-2"},
}

var chatLinkRe = regexp.MustCompile(`(?:https?://[^\s/]+)?/(tickets|faults|tasks|projects|maintenance/tasks|infrastructure|inventory|it|directory)/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

func chatLinkTypeByPath(p string) (chatLinkType, bool) {
	for _, t := range chatLinkTypes {
		if t.path == p {
			return t, true
		}
	}
	return chatLinkType{}, false
}

func chatLinkTypeByRef(ref string) (chatLinkType, bool) {
	for _, t := range chatLinkTypes {
		if t.refType == ref {
			return t, true
		}
	}
	return chatLinkType{}, false
}

// chatExtractLinks liefert die (eindeutigen) PDH-Datensatzverweise im Text.
func chatExtractLinks(body string) [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	for _, m := range chatLinkRe.FindAllStringSubmatch(body, -1) {
		key := m[1] + "/" + strings.ToLower(m[2])
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, [2]string{m[1], strings.ToLower(m[2])})
		if len(out) >= chatMaxLinkPreview {
			break
		}
	}
	return out
}

func (h *Handler) chatResolveLinks(ctx context.Context, msgID, body string) {
	_, _ = h.db.Exec(ctx, `DELETE FROM chat_message_links WHERE message_id = $1::uuid`, msgID)
	for _, l := range chatExtractLinks(body) {
		def, ok := chatLinkTypeByPath(l[0])
		if !ok {
			continue
		}
		var title string
		if err := h.db.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1::uuid`, def.titleExpr, def.table), l[1]).Scan(&title); err != nil {
			continue // unbekannter Datensatz -> bleibt normaler Link
		}
		_, _ = h.db.Exec(ctx, `
			INSERT INTO chat_message_links (message_id, ref_type, ref_id, title) VALUES ($1::uuid, $2, $3::uuid, LEFT($4, 300))
			ON CONFLICT DO NOTHING`, msgID, def.refType, l[1], title)
	}
}

// ── Senden ───────────────────────────────────────────────────

func (h *Handler) ChatSend(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	kind, _, archived, member := h.chatMembership(ctx, convID, me)
	if !member {
		chatError(w, http.StatusNotFound, "Unterhaltung nicht gefunden")
		return
	}
	if archived {
		chatError(w, http.StatusConflict, "Unterhaltung ist archiviert")
		return
	}
	// groessere Uploads: Lese-/Schreibfrist nur fuer diese Anfrage verlaengern
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, chatMaxFiles*chatMaxFileSize+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if !errors.Is(err, http.ErrNotMultipart) {
			chatError(w, http.StatusBadRequest, "Formular ungültig oder Dateien zu groß")
			return
		}
		_ = r.ParseForm()
	}
	body := strings.TrimSpace(r.FormValue("body"))
	var files []*multipart.FileHeader
	if r.MultipartForm != nil {
		files = r.MultipartForm.File["files"]
	}
	switch {
	case body == "" && len(files) == 0:
		chatError(w, http.StatusBadRequest, "Nachricht ist leer")
		return
	case utf8.RuneCountInString(body) > chatMaxBody:
		chatError(w, http.StatusBadRequest, fmt.Sprintf("Nachricht zu lang (max. %d Zeichen)", chatMaxBody))
		return
	case len(files) > chatMaxFiles:
		chatError(w, http.StatusBadRequest, fmt.Sprintf("Maximal %d Dateien je Nachricht", chatMaxFiles))
		return
	}
	var parent interface{}
	if pid := strings.TrimSpace(r.FormValue("parent_id")); pid != "" {
		var okParent bool
		_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat_messages WHERE id = $1::uuid AND conversation_id = $2::uuid AND parent_id IS NULL AND kind = 'text')`,
			pid, convID).Scan(&okParent)
		if kind != "channel" || !okParent {
			chatError(w, http.StatusBadRequest, "Beitrag für Antwort nicht gefunden")
			return
		}
		parent = pid
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var msgID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO chat_messages (conversation_id, parent_id, user_id, body) VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		RETURNING id::text`, convID, parent, me, body).Scan(&msgID); err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ids := r.Form["mentions"]; len(ids) > 0 {
		// nur aktive Mitglieder, nicht man selbst
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat_mentions (message_id, user_id)
			SELECT $1::uuid, m.user_id FROM chat_members m
			WHERE m.conversation_id = $2::uuid AND m.left_at IS NULL
			  AND m.user_id::text = ANY($3::text[]) AND m.user_id <> $4::uuid
			ON CONFLICT DO NOTHING`, msgID, convID, ids, me); err != nil {
			chatError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	var saved []string
	cleanup := func() {
		for _, p := range saved {
			_ = os.Remove(p)
		}
	}
	for _, fh := range files {
		path, err := chatStoreFile(ctx, tx, convID, msgID, me, fh)
		if err != nil {
			cleanup()
			chatError(w, http.StatusBadRequest, err.Error())
			return
		}
		saved = append(saved, path)
	}
	if _, err := tx.Exec(ctx, `UPDATE chat_conversations SET last_message_at = NOW() WHERE id = $1::uuid`, convID); err == nil {
		_, err = tx.Exec(ctx, `UPDATE chat_members SET last_read_at = NOW() WHERE conversation_id = $1::uuid AND user_id = $2::uuid`, convID, me)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			cleanup()
			chatError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		cleanup()
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.chatResolveLinks(ctx, msgID, body)

	msgs, err := h.chatLoadMessagesByID(ctx, []string{msgID})
	if err != nil || len(msgs) != 1 {
		chatError(w, http.StatusInternalServerError, "Nachricht gespeichert, aber nicht ladbar")
		return
	}
	h.chat().send(h.chatMemberIDs(ctx, convID), chatEvent{Type: "message", Data: msgs[0]})
	writeJSON(w, http.StatusCreated, msgs[0])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// chatStoreFile speichert eine Datei unter chat_files/<conv>/<zufall>.<ext>
// (nicht oeffentlich) und traegt sie in chat_files ein.
func chatStoreFile(ctx context.Context, tx pgx.Tx, convID, msgID, userID string, fh *multipart.FileHeader) (string, error) {
	name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
	ext := strings.ToLower(filepath.Ext(name))
	if !chatAllowedExt[ext] {
		return "", fmt.Errorf("Dateityp %s ist nicht erlaubt", ext)
	}
	if fh.Size > chatMaxFileSize {
		return "", fmt.Errorf("%s ist größer als %d MB", name, chatMaxFileSize>>20)
	}
	src, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(src, head)
	mimetype := http.DetectContentType(head[:n])
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	dir := filepath.Join(chatFileDir, convID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	rel := filepath.Join(convID, randomHex(16)+ext)
	abs := filepath.Join(chatFileDir, rel)
	dst, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	written, err := io.Copy(dst, io.LimitReader(src, chatMaxFileSize+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && written > chatMaxFileSize {
		err = fmt.Errorf("%s ist größer als %d MB", name, chatMaxFileSize>>20)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO chat_files (message_id, conversation_id, filename, storage_path, mimetype, size_bytes, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid)`,
			msgID, convID, name, filepath.ToSlash(rel), mimetype, written, userID)
	}
	if err != nil {
		_ = os.Remove(abs)
		return "", err
	}
	return abs, nil
}

// ChatFileDownload liefert eine Chat-Datei nur an Mitglieder der Unterhaltung.
func (h *Handler) ChatFileDownload(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	var name, rel, mimetype string
	err := h.db.QueryRow(r.Context(), `
		SELECT f.filename, f.storage_path, f.mimetype FROM chat_files f
		JOIN chat_members m ON m.conversation_id = f.conversation_id AND m.user_id = $2::uuid AND m.left_at IS NULL
		JOIN chat_messages msg ON msg.id = f.message_id AND msg.deleted_at IS NULL
		WHERE f.id = $1::uuid`, chi.URLParam(r, "id"), me).Scan(&name, &rel, &mimetype)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	abs := filepath.Join(chatFileDir, filepath.FromSlash(rel))
	if !strings.HasPrefix(filepath.Clean(abs), filepath.Clean(chatFileDir)+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	// Nur Bilder/PDF inline, alles andere als Download (kein HTML/SVG im Origin)
	disposition := "attachment"
	if strings.HasPrefix(mimetype, "image/") && mimetype != "image/svg+xml" || mimetype == "application/pdf" {
		disposition = "inline"
	} else {
		mimetype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimetype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename*=UTF-8''%s`, disposition, urlPathEscape(name)))
	http.ServeFile(w, r, abs)
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (h *Handler) ChatFilesList(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	convID := chi.URLParam(r, "id")
	if _, _, _, member := h.chatMembership(r.Context(), convID, me); !member {
		chatError(w, http.StatusNotFound, "Unterhaltung nicht gefunden")
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT f.id::text, f.filename, f.size_bytes, f.mimetype, f.created_at, COALESCE(f.created_by::text, '')
		FROM chat_files f JOIN chat_messages m ON m.id = f.message_id AND m.deleted_at IS NULL
		WHERE f.conversation_id = $1::uuid ORDER BY f.created_at DESC LIMIT 300`, convID)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type fileRow struct {
		chatFile
		CreatedAt time.Time `json:"created_at"`
		UserID    string    `json:"user_id"`
	}
	list := []fileRow{}
	for rows.Next() {
		var f fileRow
		if rows.Scan(&f.ID, &f.Name, &f.Size, &f.Mimetype, &f.CreatedAt, &f.UserID) == nil {
			f.IsImage = strings.HasPrefix(f.Mimetype, "image/")
			f.URL = "/chat/files/" + f.ID
			list = append(list, f)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// ── Bearbeiten / Loeschen / Reagieren ────────────────────────

// chatOwnMessage laedt Unterhaltung und Autor einer Nachricht, sofern der
// Benutzer Mitglied ist.
func (h *Handler) chatMessageContext(ctx context.Context, msgID, me string) (convID, author string, deleted, ok bool) {
	err := h.db.QueryRow(ctx, `
		SELECT m.conversation_id::text, COALESCE(m.user_id::text, ''), m.deleted_at IS NOT NULL
		FROM chat_messages m
		JOIN chat_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = $2::uuid AND cm.left_at IS NULL
		WHERE m.id = $1::uuid AND m.kind = 'text'`, msgID, me).Scan(&convID, &author, &deleted)
	return convID, author, deleted, err == nil
}

func (h *Handler) chatPublishUpdate(ctx context.Context, convID, msgID string) (*chatMessage, error) {
	msgs, err := h.chatLoadMessagesByID(ctx, []string{msgID})
	if err != nil || len(msgs) != 1 {
		return nil, errors.New("Nachricht nicht ladbar")
	}
	h.chat().send(h.chatMemberIDs(ctx, convID), chatEvent{Type: "message_updated", Data: msgs[0]})
	return msgs[0], nil
}

func (h *Handler) ChatEdit(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	msgID := chi.URLParam(r, "id")
	convID, author, deleted, found := h.chatMessageContext(ctx, msgID, me)
	if !found || deleted || author != me {
		chatError(w, http.StatusForbidden, "Nur eigene Nachrichten können bearbeitet werden")
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" || utf8.RuneCountInString(in.Body) > chatMaxBody {
		chatError(w, http.StatusBadRequest, "Nachricht leer oder zu lang")
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE chat_messages SET body = $1, edited_at = NOW() WHERE id = $2::uuid`, in.Body, msgID); err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.chatResolveLinks(ctx, msgID, in.Body)
	msg, err := h.chatPublishUpdate(ctx, convID, msgID)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) ChatDelete(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	msgID := chi.URLParam(r, "id")
	convID, author, deleted, found := h.chatMessageContext(ctx, msgID, me)
	if !found || deleted || (author != me && !h.isChatAdmin(r)) {
		chatError(w, http.StatusForbidden, "Nachricht kann nicht gelöscht werden")
		return
	}
	// Dateien physisch entfernen, Nachricht als geloescht markieren
	if rows, err := h.db.Query(ctx, `DELETE FROM chat_files WHERE message_id = $1::uuid RETURNING storage_path`, msgID); err == nil {
		for rows.Next() {
			var rel string
			if rows.Scan(&rel) == nil {
				_ = os.Remove(filepath.Join(chatFileDir, filepath.FromSlash(rel)))
			}
		}
		rows.Close()
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE chat_messages SET deleted_at = NOW(), body = '' WHERE id = $1::uuid;`, msgID); err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _ = h.db.Exec(ctx, `DELETE FROM chat_reactions WHERE message_id = $1::uuid`, msgID)
	_, _ = h.db.Exec(ctx, `DELETE FROM chat_message_links WHERE message_id = $1::uuid`, msgID)
	msg, err := h.chatPublishUpdate(ctx, convID, msgID)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) ChatReact(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	msgID := chi.URLParam(r, "id")
	convID, _, deleted, found := h.chatMessageContext(ctx, msgID, me)
	if !found || deleted {
		chatError(w, http.StatusNotFound, "Nachricht nicht gefunden")
		return
	}
	var in struct {
		Emoji string `json:"emoji"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Emoji == "" || utf8.RuneCountInString(in.Emoji) > 4 || len(in.Emoji) > 16 {
		chatError(w, http.StatusBadRequest, "ungültige Reaktion")
		return
	}
	tag, err := h.db.Exec(ctx, `DELETE FROM chat_reactions WHERE message_id = $1::uuid AND user_id = $2::uuid AND emoji = $3`, msgID, me, in.Emoji)
	if err == nil && tag.RowsAffected() == 0 {
		_, err = h.db.Exec(ctx, `INSERT INTO chat_reactions (message_id, user_id, emoji) VALUES ($1::uuid, $2::uuid, $3) ON CONFLICT DO NOTHING`, msgID, me, in.Emoji)
	}
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg, err := h.chatPublishUpdate(ctx, convID, msgID)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// ── Lesestatus / Tippen / Stumm ──────────────────────────────

func (h *Handler) ChatRead(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	var at time.Time
	if err := h.db.QueryRow(ctx, `
		UPDATE chat_members SET last_read_at = GREATEST(last_read_at, NOW())
		WHERE conversation_id = $1::uuid AND user_id = $2::uuid AND left_at IS NULL
		RETURNING last_read_at`, convID, me).Scan(&at); err != nil {
		chatError(w, http.StatusNotFound, "Unterhaltung nicht gefunden")
		return
	}
	h.chat().send(h.chatMemberIDs(ctx, convID), chatEvent{Type: "read", Data: map[string]interface{}{
		"conversation_id": convID, "user_id": me, "at": at,
	}})
	writeJSON(w, http.StatusOK, map[string]interface{}{"at": at})
}

func (h *Handler) ChatTyping(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	if _, _, _, member := h.chatMembership(ctx, convID, me); !member {
		chatError(w, http.StatusNotFound, "Unterhaltung nicht gefunden")
		return
	}
	var others []string
	for _, id := range h.chatMemberIDs(ctx, convID) {
		if id != me {
			others = append(others, id)
		}
	}
	h.chat().send(others, chatEvent{Type: "typing", Data: map[string]string{"conversation_id": convID, "user_id": me}})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ChatMute(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	var muted bool
	if err := h.db.QueryRow(r.Context(), `
		UPDATE chat_members SET muted = NOT muted WHERE conversation_id = $1::uuid AND user_id = $2::uuid AND left_at IS NULL
		RETURNING muted`, chi.URLParam(r, "id"), me).Scan(&muted); err != nil {
		chatError(w, http.StatusNotFound, "Unterhaltung nicht gefunden")
		return
	}
	h.chatRefresh([]string{me})
	writeJSON(w, http.StatusOK, map[string]bool{"muted": muted})
}

// ── Chats & Gruppen ──────────────────────────────────────────

// ChatDirect oeffnet (oder legt an) den 1:1-Chat mit einem Benutzer.
func (h *Handler) ChatDirect(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(r, &in); err != nil || in.UserID == "" {
		chatError(w, http.StatusBadRequest, "Benutzer fehlt")
		return
	}
	ctx := r.Context()
	var active bool
	if err := h.db.QueryRow(ctx, `SELECT active FROM users WHERE id = $1::uuid`, in.UserID).Scan(&active); err != nil || !active {
		chatError(w, http.StatusBadRequest, "Benutzer nicht gefunden")
		return
	}
	convID, err := h.chatEnsureDirect(ctx, me, in.UserID)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.chatRefresh([]string{me})
	writeJSON(w, http.StatusOK, map[string]string{"id": convID})
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// chatActiveUserIDs filtert auf existierende, aktive Benutzer.
func (h *Handler) chatActiveUserIDs(ctx context.Context, ids []string) []string {
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT id::text FROM users WHERE active AND id::text = ANY($1::text[])`, ids)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func chatNameList(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " und " + names[len(names)-1]
}

func (h *Handler) chatNames(ctx context.Context, ids []string) []string {
	var out []string
	for _, id := range ids {
		if n := h.chatUserName(ctx, id); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (h *Handler) ChatCreateGroup(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	ctx := r.Context()
	members := h.chatActiveUserIDs(ctx, append(in.Members, me))
	if len(members) < 2 {
		chatError(w, http.StatusBadRequest, "Mindestens ein weiteres Mitglied auswählen")
		return
	}
	name := strings.TrimSpace(in.Name)
	if utf8.RuneCountInString(name) > 100 {
		chatError(w, http.StatusBadRequest, "Name zu lang")
		return
	}
	var convID string
	if err := h.db.QueryRow(ctx, `INSERT INTO chat_conversations (kind, name, created_by) VALUES ('group', $1, $2::uuid) RETURNING id::text`,
		name, me).Scan(&convID); err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, uid := range members {
		_, _ = h.db.Exec(ctx, `INSERT INTO chat_members (conversation_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, convID, uid)
	}
	h.chatSystemMessage(ctx, convID, h.chatUserName(ctx, me)+" hat den Gruppenchat erstellt.")
	h.chatRefresh(members)
	writeJSON(w, http.StatusCreated, map[string]string{"id": convID})
}

// ChatAddMembers fuegt einer Gruppe weitere Mitglieder hinzu.
func (h *Handler) ChatAddMembers(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	kind, _, archived, member := h.chatMembership(ctx, convID, me)
	if !member || archived || kind != "group" {
		chatError(w, http.StatusBadRequest, "Mitglieder können nur zu Gruppenchats hinzugefügt werden")
		return
	}
	var in struct {
		Members []string `json:"members"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	var added []string
	for _, uid := range h.chatActiveUserIDs(ctx, in.Members) {
		tag, err := h.db.Exec(ctx, `
			INSERT INTO chat_members (conversation_id, user_id, last_read_at) VALUES ($1::uuid, $2::uuid, NOW())
			ON CONFLICT (conversation_id, user_id) DO UPDATE SET left_at = NULL, joined_at = NOW()
			WHERE chat_members.left_at IS NOT NULL`, convID, uid)
		if err == nil && tag.RowsAffected() > 0 {
			added = append(added, uid)
		}
	}
	if len(added) > 0 {
		h.chatSystemMessage(ctx, convID, fmt.Sprintf("%s hat %s hinzugefügt.", h.chatUserName(ctx, me), chatNameList(h.chatNames(ctx, added))))
	}
	h.chatRefresh(h.chatMemberIDs(ctx, convID))
	writeJSON(w, http.StatusOK, map[string]int{"added": len(added)})
}

func (h *Handler) ChatLeave(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	kind, _, _, member := h.chatMembership(ctx, convID, me)
	if !member || kind != "group" {
		chatError(w, http.StatusBadRequest, "Nur Gruppenchats können verlassen werden")
		return
	}
	_, _ = h.db.Exec(ctx, `UPDATE chat_members SET left_at = NOW() WHERE conversation_id = $1::uuid AND user_id = $2::uuid`, convID, me)
	h.chatSystemMessage(ctx, convID, h.chatUserName(ctx, me)+" hat den Chat verlassen.")
	h.chatRefresh(append(h.chatMemberIDs(ctx, convID), me))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ChatRename(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	kind, teamID, archived, member := h.chatMembership(ctx, convID, me)
	canEdit := member && !archived && (kind == "group" || kind == "channel" &&
		(h.chatTeamRole(ctx, teamID, me) == "owner" || h.isChatAdmin(r)))
	if !canEdit {
		chatError(w, http.StatusForbidden, "Keine Berechtigung")
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if (kind == "channel" && in.Name == "") || utf8.RuneCountInString(in.Name) > 100 || utf8.RuneCountInString(in.Description) > 500 {
		chatError(w, http.StatusBadRequest, "Name fehlt oder ist zu lang")
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE chat_conversations SET name = $1, description = $2 WHERE id = $3::uuid`, in.Name, in.Description, convID); err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.chatSystemMessage(ctx, convID, fmt.Sprintf("%s hat den Namen in „%s“ geändert.", h.chatUserName(ctx, me), in.Name))
	h.chatRefresh(h.chatMemberIDs(ctx, convID))
	w.WriteHeader(http.StatusNoContent)
}

// ── Teams & Kanaele ──────────────────────────────────────────

// chatJoinTeamChannels macht Benutzer zu Mitgliedern aller aktiven
// Kanaele eines Teams (Lesestand = jetzt, keine Altlast an Ungelesenen).
func (h *Handler) chatJoinTeamChannels(ctx context.Context, teamID string, userIDs []string) {
	_, _ = h.db.Exec(ctx, `
		INSERT INTO chat_members (conversation_id, user_id, last_read_at)
		SELECT c.id, u.id, NOW() FROM chat_conversations c CROSS JOIN users u
		WHERE c.team_id = $1::uuid AND c.archived_at IS NULL AND u.id::text = ANY($2::text[])
		ON CONFLICT (conversation_id, user_id) DO UPDATE SET left_at = NULL, joined_at = NOW()`, teamID, userIDs)
}

func (h *Handler) chatLeaveTeamChannels(ctx context.Context, teamID, userID string) {
	_, _ = h.db.Exec(ctx, `
		UPDATE chat_members SET left_at = NOW()
		WHERE user_id = $2::uuid AND conversation_id IN (SELECT id FROM chat_conversations WHERE team_id = $1::uuid)`, teamID, userID)
}

func (h *Handler) chatGeneralChannel(ctx context.Context, teamID string) string {
	var id string
	_ = h.db.QueryRow(ctx, `SELECT id::text FROM chat_conversations WHERE team_id = $1::uuid AND archived_at IS NULL ORDER BY created_at LIMIT 1`, teamID).Scan(&id)
	return id
}

var chatTeamColors = []string{"#4f6ef7", "#10b981", "#f59e0b", "#ef4444", "#7c3aed", "#0ea5e9", "#ec4899", "#14b8a6"}

func (h *Handler) ChatCreateTeam(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Members     []string `json:"members"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 || utf8.RuneCountInString(in.Description) > 500 {
		chatError(w, http.StatusBadRequest, "Teamname fehlt oder ist zu lang")
		return
	}
	ctx := r.Context()
	members := h.chatActiveUserIDs(ctx, append(in.Members, me))
	color := chatTeamColors[int(time.Now().UnixNano()%int64(len(chatTeamColors)))]
	tx, err := h.db.Begin(ctx)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	var teamID, channelID string
	err = tx.QueryRow(ctx, `INSERT INTO chat_teams (name, description, color, created_by) VALUES ($1, $2, $3, $4::uuid) RETURNING id::text`,
		in.Name, in.Description, color, me).Scan(&teamID)
	if err == nil {
		for _, uid := range members {
			role := "member"
			if uid == me {
				role = "owner"
			}
			if _, err = tx.Exec(ctx, `INSERT INTO chat_team_members (team_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`, teamID, uid, role); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.QueryRow(ctx, `INSERT INTO chat_conversations (kind, name, team_id, created_by) VALUES ('channel', 'Allgemein', $1::uuid, $2::uuid) RETURNING id::text`,
			teamID, me).Scan(&channelID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.chatJoinTeamChannels(ctx, teamID, members)
	h.chatSystemMessage(ctx, channelID, fmt.Sprintf("%s hat das Team „%s“ erstellt.", h.chatUserName(ctx, me), in.Name))
	h.chatRefresh(members)
	writeJSON(w, http.StatusCreated, map[string]string{"id": teamID, "channel_id": channelID})
}

func (h *Handler) chatCanManageTeam(r *http.Request, teamID, me string) bool {
	return h.chatTeamRole(r.Context(), teamID, me) == "owner" || h.isChatAdmin(r)
}

func (h *Handler) ChatTeamAddMembers(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	teamID := chi.URLParam(r, "id")
	if !h.chatCanManageTeam(r, teamID, me) {
		chatError(w, http.StatusForbidden, "Nur Team-Besitzer können Mitglieder verwalten")
		return
	}
	var in struct {
		Members []string `json:"members"`
		Owner   bool     `json:"owner"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	role := "member"
	if in.Owner {
		role = "owner"
	}
	var added []string
	for _, uid := range h.chatActiveUserIDs(ctx, in.Members) {
		tag, err := h.db.Exec(ctx, `
			INSERT INTO chat_team_members (team_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)
			ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role WHERE chat_team_members.role <> EXCLUDED.role`, teamID, uid, role)
		if err == nil && tag.RowsAffected() > 0 {
			added = append(added, uid)
		}
	}
	if len(added) > 0 {
		h.chatJoinTeamChannels(ctx, teamID, added)
		if general := h.chatGeneralChannel(ctx, teamID); general != "" {
			h.chatSystemMessage(ctx, general, fmt.Sprintf("%s hat %s zum Team hinzugefügt.", h.chatUserName(ctx, me), chatNameList(h.chatNames(ctx, added))))
		}
	}
	h.chatRefresh(h.chatTeamMemberIDs(ctx, teamID))
	writeJSON(w, http.StatusOK, map[string]int{"added": len(added)})
}

func (h *Handler) chatRemoveTeamMember(ctx context.Context, teamID, userID, actorName string, self bool) error {
	before := h.chatTeamMemberIDs(ctx, teamID)
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var role string
	if err := tx.QueryRow(ctx, `DELETE FROM chat_team_members WHERE team_id = $1::uuid AND user_id = $2::uuid RETURNING role`, teamID, userID).Scan(&role); err != nil {
		return errors.New("Mitglied nicht gefunden")
	}
	// letzten Besitzer ersetzen: aeltestes Mitglied wird Besitzer
	if role == "owner" {
		if _, err := tx.Exec(ctx, `
			UPDATE chat_team_members SET role = 'owner'
			WHERE team_id = $1::uuid AND NOT EXISTS (SELECT 1 FROM chat_team_members WHERE team_id = $1::uuid AND role = 'owner')
			  AND user_id = (SELECT user_id FROM chat_team_members WHERE team_id = $1::uuid ORDER BY joined_at LIMIT 1)`, teamID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	h.chatLeaveTeamChannels(ctx, teamID, userID)
	if general := h.chatGeneralChannel(ctx, teamID); general != "" {
		text := fmt.Sprintf("%s hat das Team verlassen.", h.chatUserName(ctx, userID))
		if !self {
			text = fmt.Sprintf("%s hat %s aus dem Team entfernt.", actorName, h.chatUserName(ctx, userID))
		}
		h.chatSystemMessage(ctx, general, text)
	}
	h.chatRefresh(before)
	return nil
}

func (h *Handler) ChatTeamRemoveMember(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	teamID, userID := chi.URLParam(r, "id"), chi.URLParam(r, "userId")
	if !h.chatCanManageTeam(r, teamID, me) {
		chatError(w, http.StatusForbidden, "Nur Team-Besitzer können Mitglieder entfernen")
		return
	}
	if err := h.chatRemoveTeamMember(r.Context(), teamID, userID, h.chatUserName(r.Context(), me), userID == me); err != nil {
		chatError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ChatTeamLeave(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	if err := h.chatRemoveTeamMember(r.Context(), chi.URLParam(r, "id"), me, "", true); err != nil {
		chatError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ChatCreateChannel(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	teamID := chi.URLParam(r, "id")
	if h.chatTeamRole(ctx, teamID, me) == "" {
		chatError(w, http.StatusForbidden, "Nur Teammitglieder können Kanäle anlegen")
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		chatError(w, http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 || utf8.RuneCountInString(in.Description) > 500 {
		chatError(w, http.StatusBadRequest, "Kanalname fehlt oder ist zu lang")
		return
	}
	var channelID string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO chat_conversations (kind, name, description, team_id, created_by) VALUES ('channel', $1, $2, $3::uuid, $4::uuid)
		RETURNING id::text`, in.Name, in.Description, teamID, me).Scan(&channelID); err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	members := h.chatTeamMemberIDs(ctx, teamID)
	h.chatJoinTeamChannels(ctx, teamID, members)
	h.chatSystemMessage(ctx, channelID, fmt.Sprintf("%s hat den Kanal „%s“ erstellt.", h.chatUserName(ctx, me), in.Name))
	h.chatRefresh(members)
	writeJSON(w, http.StatusCreated, map[string]string{"id": channelID})
}

func (h *Handler) ChatArchiveChannel(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	kind, teamID, _, member := h.chatMembership(ctx, convID, me)
	if !member || kind != "channel" || !h.chatCanManageTeam(r, teamID, me) {
		chatError(w, http.StatusForbidden, "Nur Team-Besitzer können Kanäle archivieren")
		return
	}
	if h.chatGeneralChannel(ctx, teamID) == convID {
		chatError(w, http.StatusBadRequest, "Der Kanal „Allgemein“ kann nicht archiviert werden")
		return
	}
	members := h.chatMemberIDs(ctx, convID)
	_, _ = h.db.Exec(ctx, `UPDATE chat_conversations SET archived_at = NOW() WHERE id = $1::uuid`, convID)
	h.chatRefresh(members)
	w.WriteHeader(http.StatusNoContent)
}

// ── Suche ────────────────────────────────────────────────────

func (h *Handler) ChatSearch(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) < 2 {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT m.id::text, m.conversation_id::text, COALESCE(m.parent_id::text, ''), COALESCE(m.user_id::text, ''),
		       m.body, m.created_at
		FROM chat_messages m
		JOIN chat_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = $1::uuid AND cm.left_at IS NULL
		WHERE m.deleted_at IS NULL AND m.kind = 'text' AND m.body ILIKE $2
		ORDER BY m.created_at DESC LIMIT 50`, me, likePattern(q))
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type hit struct {
		ID             string    `json:"id"`
		ConversationID string    `json:"conversation_id"`
		ParentID       string    `json:"parent_id,omitempty"`
		UserID         string    `json:"user_id"`
		Body           string    `json:"body"`
		CreatedAt      time.Time `json:"created_at"`
	}
	list := []hit{}
	for rows.Next() {
		var x hit
		if rows.Scan(&x.ID, &x.ConversationID, &x.ParentID, &x.UserID, &x.Body, &x.CreatedAt) == nil {
			if len([]rune(x.Body)) > 200 {
				x.Body = string([]rune(x.Body)[:200]) + "…"
			}
			list = append(list, x)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// chatEnsureDirect liefert den 1:1-Chat zweier Benutzer und legt ihn bei
// Bedarf an (beide Mitglieder werden ggf. reaktiviert).
func (h *Handler) chatEnsureDirect(ctx context.Context, a, b string) (string, error) {
	ids := []string{a, b}
	sort.Strings(ids)
	var convID string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO chat_conversations (kind, direct_key, created_by) VALUES ('direct', $1, $2::uuid)
		ON CONFLICT (direct_key) DO UPDATE SET direct_key = EXCLUDED.direct_key
		RETURNING id::text`, ids[0]+":"+ids[1], a).Scan(&convID); err != nil {
		return "", err
	}
	for _, uid := range uniqueStrings(ids) {
		if _, err := h.db.Exec(ctx, `
			INSERT INTO chat_members (conversation_id, user_id) VALUES ($1::uuid, $2::uuid)
			ON CONFLICT (conversation_id, user_id) DO UPDATE SET left_at = NULL`, convID, uid); err != nil {
			return "", err
		}
	}
	return convID, nil
}

// chatPost schreibt eine Textnachricht im Namen eines Benutzers (z. B.
// automatische Broker-Meldung) und verteilt sie in Echtzeit.
func (h *Handler) chatPost(ctx context.Context, convID, userID, body string) error {
	var msgID string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO chat_messages (conversation_id, user_id, body) VALUES ($1::uuid, $2::uuid, $3) RETURNING id::text`,
		convID, userID, body).Scan(&msgID); err != nil {
		return err
	}
	_, _ = h.db.Exec(ctx, `UPDATE chat_conversations SET last_message_at = NOW() WHERE id = $1::uuid`, convID)
	_, _ = h.db.Exec(ctx, `UPDATE chat_members SET last_read_at = NOW() WHERE conversation_id = $1::uuid AND user_id = $2::uuid`, convID, userID)
	h.chatResolveLinks(ctx, msgID, body)
	if msgs, err := h.chatLoadMessagesByID(ctx, []string{msgID}); err == nil && len(msgs) == 1 {
		members := h.chatMemberIDs(ctx, convID)
		h.chatRefresh(members) // neue Unterhaltung in allen Listen sichtbar machen
		h.chat().send(members, chatEvent{Type: "message", Data: msgs[0]})
	}
	return nil
}
