package nextcloud

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Config struct {
	Enabled  bool
	BaseURL  string
	Username string
	Password string
	RootPath string
}

type Client struct {
	cfg        Config
	httpClient *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.cfg.Enabled && c.cfg.BaseURL != "" && c.cfg.Username != "" && c.cfg.Password != ""
}

func (c *Client) UploadAttachment(ctx context.Context, refType, refID, filename string, r io.Reader) (string, error) {
	if !c.Enabled() {
		return "", nil
	}
	folder := c.remoteFolder(refType, refID)
	if err := c.ensureFolder(ctx, folder); err != nil {
		return "", err
	}
	remotePath := path.Join(folder, safeName(filename))
	if err := c.put(ctx, remotePath, r); err != nil {
		return "", err
	}
	return remotePath, nil
}

func (c *Client) remoteFolder(refType, refID string) string {
	root := strings.Trim(c.cfg.RootPath, "/")
	if root == "" {
		root = "PDH"
	}
	module := map[string]string{
		"ticket":           "Tickets",
		"fault":            "Stoerungen",
		"maintenance_task": "Wartung",
	}[refType]
	if module == "" {
		module = safeName(refType)
	}
	return path.Join(root, module, safeName(refID))
}

func (c *Client) ensureFolder(ctx context.Context, folder string) error {
	parts := strings.Split(strings.Trim(folder, "/"), "/")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		if err := c.mkcol(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) mkcol(ctx context.Context, remotePath string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", c.webdavURL(remotePath), nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("nextcloud MKCOL %s: %s %s", remotePath, resp.Status, string(body))
}

func (c *Client) put(ctx context.Context, remotePath string, r io.Reader) error {
	var body io.Reader = r
	if seeker, ok := r.(io.Seeker); ok {
		_, _ = seeker.Seek(0, io.SeekStart)
	} else {
		buf, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.webdavURL(remotePath), body)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("nextcloud PUT %s: %s %s", remotePath, resp.Status, string(bodyBytes))
}

func (c *Client) webdavURL(remotePath string) string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	userPath := url.PathEscape(c.cfg.Username)
	segments := strings.Split(strings.Trim(remotePath, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return base + "/remote.php/dav/files/" + userPath + "/" + strings.Join(segments, "/")
}

func (c *Client) auth(req *http.Request) {
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
}

func safeName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	return replacer.Replace(v)
}
