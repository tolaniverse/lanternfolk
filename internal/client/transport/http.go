package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/tolaniverse/lanternfolk/protocol"
)

// HTTP is the real transport. Each request has a deadline from the caller or
// the default client's timeout; the websocket carries one envelope per frame.
type HTTP struct {
	Base   string
	Client *http.Client
}

func NewHTTP(base string) *HTTP {
	return &HTTP{Base: strings.TrimRight(base, "/"), Client: &http.Client{Timeout: 10 * time.Second}}
}

func (h *HTTP) request(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.Base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		var response struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&response)
		return &Error{Status: res.StatusCode, Message: response.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

func (h *HTTP) AuthDev(ctx context.Context, handle string) (protocol.AuthResponse, error) {
	var out protocol.AuthResponse
	err := h.request(ctx, http.MethodPost, "/v1/auth/dev", "", protocol.AuthDevRequest{Handle: handle}, &out)
	return out, err
}
func (h *HTTP) CreateWorkspace(ctx context.Context, token string, req protocol.CreateWorkspace) (protocol.Workspace, error) {
	var out protocol.Workspace
	err := h.request(ctx, http.MethodPost, "/v1/workspaces", token, req, &out)
	return out, err
}
func (h *HTTP) Gates(ctx context.Context, token, slug string) ([]protocol.GatePrompt, error) {
	var out []protocol.GatePrompt
	err := h.request(ctx, http.MethodGet, "/v1/workspaces/"+url.PathEscape(slug)+"/gates", token, nil, &out)
	return out, err
}
func (h *HTTP) Join(ctx context.Context, token, slug string, req protocol.JoinWorkspace) (protocol.Workspace, error) {
	var out protocol.Workspace
	err := h.request(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(slug)+"/join", token, req, &out)
	return out, err
}
func (h *HTTP) Dial(ctx context.Context, token, workspaceID string, since uint64) (Stream, error) {
	u, err := url.Parse(h.Base)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("transport: server must use http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/ws"
	q := u.Query()
	q.Set("workspace", workspaceID)
	q.Set("since", fmt.Sprint(since))
	u.RawQuery = q.Encode()
	c, res, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
	if err != nil {
		if res != nil {
			return nil, &Error{Status: res.StatusCode, Message: res.Status}
		}
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	return &wsStream{conn: c}, nil
}

type wsStream struct{ conn *websocket.Conn }

func (s *wsStream) Recv(ctx context.Context) (protocol.Envelope, error) {
	_, b, err := s.conn.Read(ctx)
	if err != nil {
		return protocol.Envelope{}, err
	}
	return protocol.Decode(b)
}
func (s *wsStream) Send(ctx context.Context, e protocol.Envelope) error {
	b, err := protocol.Encode(e)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}
func (s *wsStream) Close() error { return s.conn.Close(websocket.StatusNormalClosure, "") }
