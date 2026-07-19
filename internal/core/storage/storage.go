package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Modelle ──────────────────────────────────────────────────

// NodeType ist die Ebene eines Lagerort-Knotens im Baum.
// Ein Knoten muss immer eine "tiefere" Ebene sein als sein Elternknoten,
// siehe typeDepth weiter unten - Schachtelung darf aber Ebenen überspringen
// (z.B. ein Fach direkt unter einem Lagerort, ohne Regal dazwischen).
type NodeType string

const (
	TypeLagerort NodeType = "lagerort"
	TypeRegal    NodeType = "regal"
	TypeFach     NodeType = "fach"
	TypePlatz    NodeType = "platz"
)

// typeDepth definiert die erlaubte Reihenfolge der Ebenen.
var typeDepth = map[NodeType]int{
	TypeLagerort: 0,
	TypeRegal:    1,
	TypeFach:     2,
	TypePlatz:    3,
}

func validType(t string) (NodeType, bool) {
	switch NodeType(t) {
	case TypeLagerort, TypeRegal, TypeFach, TypePlatz:
		return NodeType(t), true
	}
	return "", false
}

// Node ist ein einzelner Knoten im Lagerort-Baum (Lagerort, Regal, Fach oder Platz).
// Children wird nur beim Baum-Abruf (GetTree/GetSubtree) befüllt.
// Location ist nur beim Typ "lagerort" sinnvoll befüllt (entspricht dem
// früheren separaten "Standort"-Feld des Warehouse-Modells).
type Node struct {
	ID           string    `json:"id"`
	ParentID     *string   `json:"parent_id,omitempty"`
	Name         string    `json:"name"`
	Type         NodeType  `json:"type"`
	Description  string    `json:"description,omitempty"`
	Location     string    `json:"location,omitempty"`
	Capacity     string    `json:"capacity,omitempty"`
	CurrentParts int       `json:"current_parts"`
	Active       bool      `json:"active"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Children     []*Node   `json:"children,omitempty"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, n *Node) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO storage_nodes (id, parent_id, name, type, description, location, capacity, created_by)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, current_parts, active, created_at, updated_at`,
		n.ParentID, n.Name, n.Type, n.Description, n.Location, n.Capacity, n.CreatedBy,
	).Scan(&n.ID, &n.CurrentParts, &n.Active, &n.CreatedAt, &n.UpdatedAt)
}

func (r *Repository) Update(ctx context.Context, id string, n *Node) error {
	_, err := r.db.Exec(ctx,
		`UPDATE storage_nodes SET name=$1, description=$2, location=$3, capacity=$4, updated_at=NOW()
		 WHERE id=$5 AND active=true`,
		n.Name, n.Description, n.Location, n.Capacity, id)
	return err
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Node, error) {
	n := &Node{}
	err := r.db.QueryRow(ctx,
		`SELECT id, parent_id, name, type, COALESCE(description,''), COALESCE(location,''), COALESCE(capacity,''),
		        current_parts, active, created_by, created_at, updated_at
		 FROM storage_nodes WHERE id=$1`, id).
		Scan(&n.ID, &n.ParentID, &n.Name, &n.Type, &n.Description, &n.Location, &n.Capacity,
			&n.CurrentParts, &n.Active, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListAll holt den kompletten aktiven Baum als flache Liste.
// Der Aufbau der Eltern-Kind-Beziehungen passiert danach in Go (buildTree),
// weil rekursive Baum-Queries in Go schwerer zu debuggen sind als eine
// einfache Liste + anschließende Verknüpfung über parent_id.
func (r *Repository) ListAll(ctx context.Context) ([]*Node, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, parent_id, name, type, COALESCE(description,''), COALESCE(location,''), COALESCE(capacity,''),
		        current_parts, active, created_by, created_at, updated_at
		 FROM storage_nodes WHERE active=true ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []*Node
	for rows.Next() {
		n := &Node{}
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Name, &n.Type, &n.Description, &n.Location, &n.Capacity,
			&n.CurrentParts, &n.Active, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// Delete deaktiviert den Knoten UND alle Nachfahren (rekursiv über parent_id),
// damit z.B. beim Löschen eines Regals nicht "verwaiste" Fächer/Plätze übrig bleiben.
func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id FROM storage_nodes WHERE id = $1
			UNION ALL
			SELECT sn.id FROM storage_nodes sn
			JOIN subtree s ON sn.parent_id = s.id
		)
		UPDATE storage_nodes SET active=false, updated_at=NOW()
		WHERE id IN (SELECT id FROM subtree)`, id)
	return err
}

func (r *Repository) GetStats(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx,
		`SELECT type, COUNT(*) FROM storage_nodes WHERE active=true GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]int{
		string(TypeLagerort): 0,
		string(TypeRegal):    0,
		string(TypeFach):     0,
		string(TypePlatz):    0,
	}
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		stats[t] = c
	}
	return stats, rows.Err()
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// Create legt einen neuen Knoten an. parentID == nil bedeutet: neuer
// Wurzelknoten, muss dann vom Typ "lagerort" sein.
func (s *Service) Create(ctx context.Context, parentID *string, name, nodeType, description, location, capacity, userID string) (*Node, error) {
	t, ok := validType(nodeType)
	if !ok {
		return nil, fmt.Errorf("ungültiger typ %q (erlaubt: lagerort, regal, fach, platz)", nodeType)
	}

	if parentID == nil {
		if t != TypeLagerort {
			return nil, fmt.Errorf("ohne übergeordneten lagerort kann nur der typ 'lagerort' angelegt werden")
		}
	} else {
		parent, err := s.repo.GetByID(ctx, *parentID)
		if err != nil {
			return nil, fmt.Errorf("übergeordneter knoten nicht gefunden")
		}
		if typeDepth[t] <= typeDepth[parent.Type] {
			return nil, fmt.Errorf("%s kann nicht unter %s liegen (muss in der hierarchie weiter unten sein)", t, parent.Type)
		}
	}

	n := &Node{ParentID: parentID, Name: name, Type: t, Description: description, Location: location, Capacity: capacity, CreatedBy: userID}
	return n, s.repo.Create(ctx, n)
}

func (s *Service) Update(ctx context.Context, id, name, description, location, capacity string) error {
	return s.repo.Update(ctx, id, &Node{Name: name, Description: description, Location: location, Capacity: capacity})
}

func (s *Service) GetTree(ctx context.Context) ([]*Node, error) {
	flat, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return buildTree(flat), nil
}

func (s *Service) GetSubtree(ctx context.Context, id string) (*Node, error) {
	flat, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return findNode(buildTree(flat), id), nil
}

func (s *Service) GetStats(ctx context.Context) (map[string]int, error) { return s.repo.GetStats(ctx) }

func (s *Service) Delete(ctx context.Context, id string) error { return s.repo.Delete(ctx, id) }

// buildTree baut aus der flachen Liste (mit ParentID) eine verschachtelte
// Baumstruktur. Wurzeln sind Knoten, deren Elternknoten nicht (mehr) in der
// aktiven Liste vorkommt (z.B. weil er selbst kein parent_id hat).
func buildTree(flat []*Node) []*Node {
	byID := make(map[string]*Node, len(flat))
	for _, n := range flat {
		n.Children = nil
		byID[n.ID] = n
	}
	var roots []*Node
	for _, n := range flat {
		if n.ParentID != nil {
			if parent, ok := byID[*n.ParentID]; ok {
				parent.Children = append(parent.Children, n)
				continue
			}
		}
		roots = append(roots, n)
	}
	return roots
}

func findNode(nodes []*Node, id string) *Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
		if found := findNode(n.Children, id); found != nil {
			return found
		}
	}
	return nil
}

// ── Handler ──────────────────────────────────────────────────
// Diese JSON-API läuft unter /api/v1/storage (siehe main.go).
// Wichtig: alle Ebenen (lagerort/regal/fach/platz) teilen sich jetzt dieselben
// Endpunkte - es gibt keine separaten Pfade wie /locations/{id} oder
// /places/{id} mehr, weil intern alles ein "Node" ist.

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Get("/", h.GetTree)
	r.Get("/stats", h.Stats)
	r.Get("/{id}", h.GetSubtree)
	r.Post("/", h.Create)                   // neuen Wurzel-Lagerort anlegen
	r.Post("/{id}/children", h.CreateChild) // Kind-Knoten unter {id} anlegen
	r.Post("/{id}", h.Update)               // FIX: POST statt PUT, sonst von Cloudflare/Nginx blockiert
	r.Delete("/{id}", h.Delete)

	return r
}

func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func formOrJSON(r *http.Request, out interface{}, applyForm func()) error {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/x-www-form-urlencoded") || strings.Contains(ct, "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			return err
		}
		applyForm()
		return nil
	}
	return json.NewDecoder(r.Body).Decode(out)
}

type nodeInput struct {
	Name        string
	Type        string
	Description string
	Location    string
	Capacity    string
}

func bindNodeInput(r *http.Request) (nodeInput, error) {
	var in nodeInput
	err := formOrJSON(r, &in, func() {
		in.Name = r.FormValue("name")
		in.Type = r.FormValue("type")
		in.Description = r.FormValue("description")
		in.Location = r.FormValue("location")
		in.Capacity = r.FormValue("capacity")
	})
	return in, err
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	in, err := bindNodeInput(r)
	if err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	n, err := h.svc.Create(r.Context(), nil, in.Name, in.Type, in.Description, in.Location, in.Capacity, uid(r))
	if err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 201, n)
}

func (h *Handler) CreateChild(w http.ResponseWriter, r *http.Request) {
	parentID := chi.URLParam(r, "id")
	in, err := bindNodeInput(r)
	if err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	n, err := h.svc.Create(r.Context(), &parentID, in.Name, in.Type, in.Description, in.Location, in.Capacity, uid(r))
	if err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 201, n)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	in, err := bindNodeInput(r)
	if err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.Update(r.Context(), chi.URLParam(r, "id"), in.Name, in.Description, in.Location, in.Capacity); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GetTree(w http.ResponseWriter, r *http.Request) {
	tree, err := h.svc.GetTree(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, tree)
}

func (h *Handler) GetSubtree(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.GetSubtree(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	if n == nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, n)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.GetStats(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, stats)
}
