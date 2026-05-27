package nextcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type DeckConfig struct {
	Enabled            bool
	BaseURL            string
	Username           string
	Password           string
	BoardID            string
	TicketStackID      string
	FaultStackID       string
	MaintenanceStackID string
	PublicPDHURL       string
}

type DeckClient struct {
	cfg        DeckConfig
	httpClient *http.Client
}

type DeckCardInput struct {
	RefType     string
	RefID       string
	Title       string
	Description string
	Priority    string
	DueDate     string
	Checklist   []string
}

type DeckCard struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

func NewDeckClient(cfg DeckConfig) *DeckClient {
	return &DeckClient{cfg: cfg, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c *DeckClient) Enabled() bool {
	return c != nil && c.cfg.Enabled && c.cfg.BaseURL != "" && c.cfg.Username != "" && c.cfg.Password != "" && c.cfg.BoardID != ""
}

func (c *DeckClient) CreateTicketCard(ctx context.Context, in DeckCardInput) (*DeckCard, error) {
	if !c.Enabled() || c.cfg.TicketStackID == "" {
		return nil, nil
	}
	return c.createCard(ctx, c.cfg.TicketStackID, in)
}

func (c *DeckClient) CreateFaultCard(ctx context.Context, in DeckCardInput) (*DeckCard, error) {
	if !c.Enabled() || c.cfg.FaultStackID == "" {
		return nil, nil
	}
	return c.createCard(ctx, c.cfg.FaultStackID, in)
}

func (c *DeckClient) CreateMaintenanceCard(ctx context.Context, in DeckCardInput) (*DeckCard, error) {
	if !c.Enabled() || c.cfg.MaintenanceStackID == "" {
		return nil, nil
	}
	return c.createCard(ctx, c.cfg.MaintenanceStackID, in)
}

func (c *DeckClient) createCard(ctx context.Context, stackID string, in DeckCardInput) (*DeckCard, error) {
	desc := strings.TrimSpace(in.Description)
	if link := c.pdhLink(in.RefType, in.RefID); link != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "PDH: " + link
	}
	if in.Priority != "" {
		desc += "\n\nPriorität: " + in.Priority
	}
	if in.DueDate != "" {
		desc += "\nFällig: " + in.DueDate
	}
	if len(in.Checklist) > 0 {
		desc += "\n\nCheckliste:\n"
		for _, item := range in.Checklist {
			item = strings.TrimSpace(item)
			if item != "" {
				desc += "- [ ] " + item + "\n"
			}
		}
	}

	payload := map[string]any{
		"title":       in.Title,
		"description": desc,
	}
	if in.DueDate != "" {
		payload["duedate"] = in.DueDate
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/index.php/apps/deck/api/v1.0/boards/" + c.cfg.BoardID + "/stacks/" + stackID + "/cards"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("deck create card: %s %s", resp.Status, string(respBody))
	}

	card := &DeckCard{}
	if err := json.Unmarshal(respBody, card); err != nil {
		return &DeckCard{Title: in.Title}, nil
	}
	return card, nil
}

func (c *DeckClient) pdhLink(refType, refID string) string {
	base := strings.TrimRight(c.cfg.PublicPDHURL, "/")
	if base == "" || refID == "" {
		return ""
	}
	switch refType {
	case "ticket":
		return base + "/tickets/" + refID
	case "fault":
		return base + "/faults/" + refID
	case "maintenance_task":
		return base + "/maintenance/tasks/" + refID
	default:
		return base
	}
}
