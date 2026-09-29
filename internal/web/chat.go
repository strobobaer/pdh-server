package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Interner Nachrichtendienst im Teams-Stil (siehe migrations/068_chat).
// JSON-API unter /chat/api/*, Echtzeit per SSE unter /chat/stream,
// Oberflaeche unter /chat (web/templates/chat.gohtml).

const (
	chatFileDir        = "chat_files" // bewusst ausserhalb von /uploads (nicht oeffentlich)
	chatMaxBody        = 8000
	chatMaxFileSize    = 25 << 20
	chatMaxFiles       = 10
	chatPageSize       = 60
	chatMaxLinkPreview = 5
)

var chatQuickEmojis = []string{"👍", "❤️", "😂", "😮", "😢", "✅", "🙏", "🔥"}

var chatAllowedExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".pptx": true,
	".txt": true, ".csv": true, ".zip": true, ".mp4": true, ".dxf": true, ".step": true, ".stp": true,
}

// chat liefert den (einmalig initialisierten) Hub inkl. Online-Status-
// Weitergabe an alle verbundenen Clients.
func (h *Handler) chat() *chatHub {
	h.chatOnce.Do(func() {
		h.chatHubRef = newChatHub()
		hub := h.chatHubRef
		hub.onPresence = func(userID string, online bool) {
			hub.broadcast(chatEvent{Type: "presence", Data: map[string]interface{}{"user_id": userID, "online": online}})
		}
	})
	return h.chatHubRef
}

func (h *Handler) canChat(r *http.Request) bool {
	u := getUser(r)
	return u.ID != "" && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "chat.use")
}

func (h *Handler) isChatAdmin(r *http.Request) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_users")
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func chatError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

// chatGuard prueft Login + chat.use und liefert die Benutzer-ID.
func (h *Handler) chatGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.db == nil || !h.canChat(r) {
		chatError(w, http.StatusForbidden, "keine Berechtigung für den Chat")
		return "", false
	}
	return getUser(r).ID, true
}

// ── Modelle ──────────────────────────────────────────────────

type chatUser struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Initials   string `json:"initials"`
	Department string `json:"department"`
	Email      string `json:"email"`
	Online     bool   `json:"online"`
}

type chatPreview struct {
	UserID    string    `json:"user_id"`
	Body      string    `json:"body"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

type chatConversation struct {
	ID            string               `json:"id"`
	Kind          string               `json:"kind"`
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	TeamID        string               `json:"team_id,omitempty"`
	Members       []string             `json:"members"`
	LastMessageAt time.Time            `json:"last_message_at"`
	LastReadAt    time.Time            `json:"last_read_at"`
	Unread        int                  `json:"unread"`
	Mentions      int                  `json:"mentions"`
	Muted         bool                 `json:"muted"`
	Preview       *chatPreview         `json:"preview,omitempty"`
	Reads         map[string]time.Time `json:"reads,omitempty"`
}

type chatTeam struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Color       string   `json:"color"`
	Role        string   `json:"role"`
	Members     []string `json:"members"`
	Owners      []string `json:"owners"`
}

type chatReaction struct {
	Emoji string   `json:"emoji"`
	Users []string `json:"users"`
}

type chatFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Mimetype string `json:"mimetype"`
	IsImage  bool   `json:"is_image"`
	URL      string `json:"url"`
}

type chatLink struct {
	RefType string `json:"ref_type"`
	RefID   string `json:"ref_id"`
	Title   string `json:"title"`
	Label   string `json:"label"`
	Icon    string `json:"icon"`
	URL     string `json:"url"`
}

type chatMessage struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	ParentID       string         `json:"parent_id,omitempty"`
	UserID         string         `json:"user_id"`
	Kind           string         `json:"kind"`
	Body           string         `json:"body"`
	CreatedAt      time.Time      `json:"created_at"`
	EditedAt       *time.Time     `json:"edited_at,omitempty"`
	Deleted        bool           `json:"deleted"`
	Mentions       []string       `json:"mentions"`
	Reactions      []chatReaction `json:"reactions"`
	Files          []chatFile     `json:"files"`
	Links          []chatLink     `json:"links"`
}

func initials(first, last string) string {
	var b strings.Builder
	for _, s := range []string{first, last} {
		if r, _ := utf8.DecodeRuneInString(strings.TrimSpace(s)); r != utf8.RuneError {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return strings.ToUpper(b.String())
}

// ── Mitgliedschaft ───────────────────────────────────────────

// chatMembership liefert Art der Unterhaltung und ob der Benutzer aktives
// Mitglied ist (archivierte Unterhaltungen sind nicht mehr beschreibbar).
func (h *Handler) chatMembership(ctx context.Context, convID, userID string) (kind, teamID string, archived, ok bool) {
	err := h.db.QueryRow(ctx, `
		SELECT c.kind, COALESCE(c.team_id::text, ''), c.archived_at IS NOT NULL
		FROM chat_conversations c
		JOIN chat_members m ON m.conversation_id = c.id AND m.user_id = $2::uuid AND m.left_at IS NULL
		WHERE c.id = $1::uuid`, convID, userID).Scan(&kind, &teamID, &archived)
	return kind, teamID, archived, err == nil
}

func (h *Handler) chatMemberIDs(ctx context.Context, convID string) []string {
	rows, err := h.db.Query(ctx, `SELECT user_id::text FROM chat_members WHERE conversation_id = $1::uuid AND left_at IS NULL`, convID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *Handler) chatTeamMemberIDs(ctx context.Context, teamID string) []string {
	rows, err := h.db.Query(ctx, `SELECT user_id::text FROM chat_team_members WHERE team_id = $1::uuid`, teamID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *Handler) chatTeamRole(ctx context.Context, teamID, userID string) string {
	var role string
	_ = h.db.QueryRow(ctx, `SELECT role FROM chat_team_members WHERE team_id = $1::uuid AND user_id = $2::uuid`, teamID, userID).Scan(&role)
	return role
}

// chatRefresh fordert Clients auf, Liste/Teams neu zu laden.
func (h *Handler) chatRefresh(userIDs []string) {
	h.chat().send(userIDs, chatEvent{Type: "refresh"})
}

// chatSystemMessage schreibt einen Hinweis ("X hat Y hinzugefuegt") und
// verteilt ihn.
func (h *Handler) chatSystemMessage(ctx context.Context, convID, text string) {
	var id string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO chat_messages (conversation_id, kind, body) VALUES ($1::uuid, 'system', $2) RETURNING id::text`,
		convID, text).Scan(&id); err != nil {
		return
	}
	_, _ = h.db.Exec(ctx, `UPDATE chat_conversations SET last_message_at = NOW() WHERE id = $1::uuid`, convID)
	if msgs, err := h.chatLoadMessagesByID(ctx, []string{id}); err == nil && len(msgs) == 1 {
		h.chat().send(h.chatMemberIDs(ctx, convID), chatEvent{Type: "message", Data: msgs[0]})
	}
}

func (h *Handler) chatUserName(ctx context.Context, userID string) string {
	var n string
	_ = h.db.QueryRow(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid`, userID).Scan(&n)
	return n
}

// ── Seite & Stream ───────────────────────────────────────────

type ChatPageData struct {
	BaseData
	MeID        string
	ShareURL    string
	ShareTitle  string
	QuickEmojis []string
}

func (h *Handler) ChatPage(w http.ResponseWriter, r *http.Request) {
	if !h.canChat(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	share := safeReturn(q.Get("share"))
	h.render(w, "chat", ChatPageData{
		BaseData:    h.baseData(r, "chat", "Chat", "Chat"),
		MeID:        getUser(r).ID,
		ShareURL:    share,
		ShareTitle:  strings.TrimSpace(q.Get("title")),
		QuickEmojis: chatQuickEmojis,
	})
}

// ChatStream haelt eine SSE-Verbindung fuer Echtzeit-Ereignisse offen.
func (h *Handler) ChatStream(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	flusher, fok := w.(http.Flusher)
	if !fok {
		http.Error(w, "Streaming nicht unterstützt", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Server-WriteTimeout (120 s) gilt nicht fuer den langlebigen Stream
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	hub := h.chat()
	ch := hub.subscribe(userID)
	defer hub.unsubscribe(userID, ch)

	fmt.Fprint(w, "retry: 3000\n\n")
	writeSSE(w, flusher, "chat", chatEvent{Type: "hello", Data: map[string]interface{}{"online": h.chatOnlineUsers()}})

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			writeSSE(w, flusher, "chat", ev)
		}
	}
}

func (h *Handler) chatOnlineUsers() []string {
	hub := h.chat()
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	ids := make([]string, 0, len(hub.clients)+len(hub.offline))
	for id := range hub.clients {
		ids = append(ids, id)
	}
	for id := range hub.offline {
		if _, ok := hub.clients[id]; !ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// ── Bootstrap / Listen ───────────────────────────────────────

func (h *Handler) ChatBootstrap(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	users, err := h.chatUsers(ctx)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	convs, err := h.chatConversations(ctx, me)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	teams, err := h.chatTeams(ctx, me)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"me": me, "users": users, "conversations": convs, "teams": teams,
		"is_admin": h.isChatAdmin(r), "emojis": chatQuickEmojis,
	})
}

func (h *Handler) chatUsers(ctx context.Context) ([]chatUser, error) {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, first_name, last_name, COALESCE(department, ''), email
		FROM users WHERE active ORDER BY first_name, last_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hub := h.chat()
	var list []chatUser
	for rows.Next() {
		var u chatUser
		var first, last string
		if rows.Scan(&u.ID, &first, &last, &u.Department, &u.Email) != nil {
			continue
		}
		u.Name = strings.TrimSpace(first + " " + last)
		u.Initials = initials(first, last)
		u.Online = hub.isOnline(u.ID)
		list = append(list, u)
	}
	return list, rows.Err()
}

func (h *Handler) chatConversations(ctx context.Context, me string) ([]*chatConversation, error) {
	rows, err := h.db.Query(ctx, `
		SELECT c.id::text, c.kind, c.name, c.description, COALESCE(c.team_id::text, ''),
		       c.last_message_at, m.last_read_at, m.muted,
		       (SELECT COUNT(*) FROM chat_messages x
		         WHERE x.conversation_id = c.id AND x.created_at > m.last_read_at AND x.kind = 'text'
		           AND x.user_id IS DISTINCT FROM m.user_id AND x.deleted_at IS NULL),
		       (SELECT COUNT(*) FROM chat_mentions mm JOIN chat_messages x ON x.id = mm.message_id
		         WHERE x.conversation_id = c.id AND mm.user_id = m.user_id AND x.created_at > m.last_read_at AND x.deleted_at IS NULL),
		       COALESCE(p.user_id::text, ''), COALESCE(p.body, ''), COALESCE(p.kind, ''), p.created_at,
		       ARRAY(SELECT mb.user_id::text FROM chat_members mb
		              WHERE mb.conversation_id = c.id AND mb.left_at IS NULL AND c.kind <> 'channel' ORDER BY mb.joined_at)
		FROM chat_members m
		JOIN chat_conversations c ON c.id = m.conversation_id AND c.archived_at IS NULL
		LEFT JOIN LATERAL (
			SELECT user_id, CASE WHEN deleted_at IS NULL THEN body ELSE '' END AS body, kind, created_at
			FROM chat_messages WHERE conversation_id = c.id
			ORDER BY created_at DESC LIMIT 1) p ON true
		WHERE m.user_id = $1::uuid AND m.left_at IS NULL
		ORDER BY c.last_message_at DESC`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*chatConversation
	var directIDs []string
	for rows.Next() {
		c := &chatConversation{}
		var pUser, pBody, pKind string
		var pAt *time.Time
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.Description, &c.TeamID, &c.LastMessageAt, &c.LastReadAt,
			&c.Muted, &c.Unread, &c.Mentions, &pUser, &pBody, &pKind, &pAt, &c.Members); err != nil {
			return nil, err
		}
		if pAt != nil {
			if len([]rune(pBody)) > 120 {
				pBody = string([]rune(pBody)[:120]) + "…"
			}
			c.Preview = &chatPreview{UserID: pUser, Body: pBody, Kind: pKind, CreatedAt: *pAt}
		}
		if c.Kind == "direct" {
			directIDs = append(directIDs, c.ID)
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Lesestand der Gegenseite fuer "Gesehen" in 1:1-Chats
	if len(directIDs) > 0 {
		reads := h.chatReads(ctx, directIDs)
		for _, c := range list {
			if c.Kind == "direct" {
				c.Reads = reads[c.ID]
			}
		}
	}
	return list, nil
}

func (h *Handler) chatReads(ctx context.Context, convIDs []string) map[string]map[string]time.Time {
	out := map[string]map[string]time.Time{}
	rows, err := h.db.Query(ctx, `
		SELECT conversation_id::text, user_id::text, last_read_at FROM chat_members
		WHERE conversation_id = ANY($1::uuid[]) AND left_at IS NULL`, convIDs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var cid, uid string
		var at time.Time
		if rows.Scan(&cid, &uid, &at) == nil {
			if out[cid] == nil {
				out[cid] = map[string]time.Time{}
			}
			out[cid][uid] = at
		}
	}
	return out
}

func (h *Handler) chatTeams(ctx context.Context, me string) ([]chatTeam, error) {
	rows, err := h.db.Query(ctx, `
		SELECT t.id::text, t.name, t.description, t.color, tm.role,
		       ARRAY(SELECT user_id::text FROM chat_team_members WHERE team_id = t.id ORDER BY joined_at),
		       ARRAY(SELECT user_id::text FROM chat_team_members WHERE team_id = t.id AND role = 'owner')
		FROM chat_team_members tm JOIN chat_teams t ON t.id = tm.team_id AND t.archived_at IS NULL
		WHERE tm.user_id = $1::uuid
		ORDER BY t.name`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []chatTeam
	for rows.Next() {
		var t chatTeam
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Color, &t.Role, &t.Members, &t.Owners); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// ChatUnread liefert die Summe ungelesener Nachrichten (nicht stummgeschaltet).
func (h *Handler) ChatUnread(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	var total, mentions int
	_ = h.db.QueryRow(r.Context(), `
		SELECT COUNT(*) FILTER (WHERE NOT m.muted),
		       COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM chat_mentions mm WHERE mm.message_id = x.id AND mm.user_id = m.user_id))
		FROM chat_members m
		JOIN chat_conversations c ON c.id = m.conversation_id AND c.archived_at IS NULL
		JOIN chat_messages x ON x.conversation_id = m.conversation_id AND x.created_at > m.last_read_at
		     AND x.kind = 'text' AND x.deleted_at IS NULL AND x.user_id IS DISTINCT FROM m.user_id
		WHERE m.user_id = $1::uuid AND m.left_at IS NULL`, me).Scan(&total, &mentions)
	writeJSON(w, http.StatusOK, map[string]int{"unread": total, "mentions": mentions})
}

// ── Nachrichten laden ────────────────────────────────────────

const chatMessageColumns = `m.id::text, m.conversation_id::text, COALESCE(m.parent_id::text, ''), COALESCE(m.user_id::text, ''),
	m.kind, CASE WHEN m.deleted_at IS NULL THEN m.body ELSE '' END, m.created_at, m.edited_at, m.deleted_at IS NOT NULL`

func scanChatMessages(rows interface {
	Next() bool
	Scan(...interface{}) error
	Close()
	Err() error
}) ([]*chatMessage, error) {
	defer rows.Close()
	var list []*chatMessage
	for rows.Next() {
		m := &chatMessage{}
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.ParentID, &m.UserID, &m.Kind, &m.Body, &m.CreatedAt, &m.EditedAt, &m.Deleted); err != nil {
			return nil, err
		}
		m.Mentions, m.Reactions, m.Files, m.Links = []string{}, []chatReaction{}, []chatFile{}, []chatLink{}
		list = append(list, m)
	}
	return list, rows.Err()
}

func (h *Handler) chatLoadMessagesByID(ctx context.Context, ids []string) ([]*chatMessage, error) {
	rows, err := h.db.Query(ctx, `SELECT `+chatMessageColumns+` FROM chat_messages m WHERE m.id = ANY($1::uuid[]) ORDER BY m.created_at`, ids)
	if err != nil {
		return nil, err
	}
	msgs, err := scanChatMessages(rows)
	if err != nil {
		return nil, err
	}
	h.chatEnrich(ctx, msgs)
	return msgs, nil
}

// chatEnrich ergaenzt Reaktionen, Erwaehnungen, Dateien und Datensatz-Links.
func (h *Handler) chatEnrich(ctx context.Context, msgs []*chatMessage) {
	if len(msgs) == 0 {
		return
	}
	byID := map[string]*chatMessage{}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
		ids = append(ids, m.ID)
	}
	if rows, err := h.db.Query(ctx, `
		SELECT message_id::text, emoji, array_agg(user_id::text ORDER BY created_at)
		FROM chat_reactions WHERE message_id = ANY($1::uuid[])
		GROUP BY message_id, emoji ORDER BY MIN(created_at)`, ids); err == nil {
		for rows.Next() {
			var mid string
			var rc chatReaction
			if rows.Scan(&mid, &rc.Emoji, &rc.Users) == nil && byID[mid] != nil {
				byID[mid].Reactions = append(byID[mid].Reactions, rc)
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT message_id::text, user_id::text FROM chat_mentions WHERE message_id = ANY($1::uuid[])`, ids); err == nil {
		for rows.Next() {
			var mid, uid string
			if rows.Scan(&mid, &uid) == nil && byID[mid] != nil {
				byID[mid].Mentions = append(byID[mid].Mentions, uid)
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `
		SELECT id::text, message_id::text, filename, size_bytes, mimetype FROM chat_files
		WHERE message_id = ANY($1::uuid[]) ORDER BY created_at`, ids); err == nil {
		for rows.Next() {
			var f chatFile
			var mid string
			if rows.Scan(&f.ID, &mid, &f.Name, &f.Size, &f.Mimetype) == nil && byID[mid] != nil {
				f.IsImage = strings.HasPrefix(f.Mimetype, "image/")
				f.URL = "/chat/files/" + f.ID
				byID[mid].Files = append(byID[mid].Files, f)
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT message_id::text, ref_type, ref_id::text, title FROM chat_message_links WHERE message_id = ANY($1::uuid[])`, ids); err == nil {
		for rows.Next() {
			var l chatLink
			var mid string
			if rows.Scan(&mid, &l.RefType, &l.RefID, &l.Title) == nil && byID[mid] != nil {
				if def, ok := chatLinkTypeByRef(l.RefType); ok {
					l.Label, l.Icon, l.URL = def.label, def.icon, "/"+def.path+"/"+l.RefID
				}
				byID[mid].Links = append(byID[mid].Links, l)
			}
		}
		rows.Close()
	}
	for _, m := range msgs {
		if m.Deleted {
			m.Reactions, m.Files, m.Links = []chatReaction{}, []chatFile{}, []chatLink{}
		}
	}
}

// ChatMessages liefert eine Seite Nachrichten (aelteste zuerst). Kanaele
// werden nach Beitraegen paginiert, Antworten kommen vollstaendig mit.
func (h *Handler) ChatMessages(w http.ResponseWriter, r *http.Request) {
	me, ok := h.chatGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convID := chi.URLParam(r, "id")
	kind, _, _, member := h.chatMembership(ctx, convID, me)
	if !member {
		chatError(w, http.StatusNotFound, "Unterhaltung nicht gefunden")
		return
	}
	var before interface{}
	if b := r.URL.Query().Get("before"); b != "" {
		if t, err := time.Parse(time.RFC3339Nano, b); err == nil {
			before = t
		}
	}
	topLevel := ""
	if kind == "channel" {
		topLevel = " AND m.parent_id IS NULL"
	}
	rows, err := h.db.Query(ctx, `
		SELECT `+chatMessageColumns+` FROM chat_messages m
		WHERE m.conversation_id = $1::uuid AND ($2::timestamptz IS NULL OR m.created_at < $2::timestamptz)`+topLevel+`
		ORDER BY m.created_at DESC LIMIT `+strconv.Itoa(chatPageSize+1), convID, before)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msgs, err := scanChatMessages(rows)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasMore := len(msgs) > chatPageSize
	if hasMore {
		msgs = msgs[:chatPageSize]
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].CreatedAt.Before(msgs[j].CreatedAt) })
	if kind == "channel" && len(msgs) > 0 {
		parentIDs := make([]string, len(msgs))
		for i, m := range msgs {
			parentIDs[i] = m.ID
		}
		if rrows, err := h.db.Query(ctx, `SELECT `+chatMessageColumns+` FROM chat_messages m
			WHERE m.parent_id = ANY($1::uuid[]) ORDER BY m.created_at`, parentIDs); err == nil {
			if replies, err := scanChatMessages(rrows); err == nil {
				msgs = append(msgs, replies...)
			}
		}
	}
	h.chatEnrich(ctx, msgs)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages": msgs, "has_more": hasMore, "reads": h.chatReads(ctx, []string{convID})[convID],
	})
}
