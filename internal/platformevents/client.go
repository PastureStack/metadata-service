package platformevents

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxResponseBytes int64 = 4 << 20

type Event struct {
	Name         string                 `json:"name,omitempty"`
	ID           string                 `json:"id,omitempty"`
	ReplyTo      string                 `json:"replyTo,omitempty"`
	ResourceID   string                 `json:"resourceId,omitempty"`
	ResourceType string                 `json:"resourceType,omitempty"`
	Data         map[string]interface{} `json:"data,omitempty"`
}

type Publish struct {
	Name                 string                 `json:"name"`
	PreviousIDs          []string               `json:"previousIds"`
	Data                 map[string]interface{} `json:"data,omitempty"`
	Transitioning        string                 `json:"transitioning,omitempty"`
	TransitioningMessage string                 `json:"transitioningMessage,omitempty"`
}

type Handler func(*Event, *Client) error

type Client struct {
	eventBaseURL *url.URL
	apiBaseURL   *url.URL
	accessKey    string
	secretKey    string
	httpClient   *http.Client
	dialer       *websocket.Dialer
	pingInterval time.Duration
	writeMu      sync.Mutex
}

func NewClient(rawURL, accessKey, secretKey string) (*Client, error) {
	eventBaseURL, apiBaseURL, err := normalizeBaseURLs(rawURL)
	if err != nil {
		return nil, err
	}

	return &Client{
		eventBaseURL: eventBaseURL,
		apiBaseURL:   apiBaseURL,
		accessKey:    accessKey,
		secretKey:    secretKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		dialer: &websocket.Dialer{
			HandshakeTimeout: 30 * time.Second,
		},
		pingInterval: 5 * time.Second,
	}, nil
}

func normalizeBaseURLs(rawURL string) (*url.URL, *url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, nil, fmt.Errorf("parse platform URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil, fmt.Errorf("platform URL must use http or https")
	}
	if u.Host == "" {
		return nil, nil, fmt.Errorf("platform URL must include a host")
	}

	u.RawQuery = ""
	u.Fragment = ""
	eventBaseURL := *u
	switch {
	case eventBaseURL.Path == "" || eventBaseURL.Path == "/":
		eventBaseURL.Path = "/v1"
	case eventBaseURL.Path == "/v2-beta":
		eventBaseURL.Path = "/v1"
	default:
		eventBaseURL.Path = strings.TrimRight(eventBaseURL.Path, "/")
	}

	apiBaseURL := eventBaseURL
	if apiBaseURL.Path == "/v1" || strings.HasPrefix(apiBaseURL.Path, "/v1/") {
		apiBaseURL.Path = "/v2-beta" + strings.TrimPrefix(apiBaseURL.Path, "/v1")
	}
	return &eventBaseURL, &apiBaseURL, nil
}

func (c *Client) ConfigurationBaseURL() string {
	return c.eventBaseURL.String()
}

func (c *Client) endpoint(path string) string {
	u := *c.apiBaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (c *Client) subscriptionURL(eventNames []string) string {
	u := *c.eventBaseURL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/subscribe"
	query := url.Values{}
	sortedNames := append([]string(nil), eventNames...)
	sort.Strings(sortedNames)
	for _, eventName := range sortedNames {
		query.Add("eventNames", eventName)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func (c *Client) Run(handlers map[string]Handler) error {
	eventNames := make([]string, 0, len(handlers))
	for eventName := range handlers {
		eventNames = append(eventNames, eventName)
	}

	headers := http.Header{}
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.accessKey+":"+c.secretKey)))
	conn, response, err := c.dialer.Dial(c.subscriptionURL(eventNames), headers)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect to platform event stream: %w", err)
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	go c.sendPings(conn, done)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return fmt.Errorf("read platform event: %w", err)
		}
		message = bytes.TrimSpace(message)
		if len(message) == 0 {
			continue
		}

		event := &Event{}
		if err := json.Unmarshal(message, event); err != nil {
			continue
		}
		handler, ok := handlers[event.Name]
		if !ok {
			continue
		}
		if err := handler(event, c); err != nil && event.ReplyTo != "" {
			publishErr := c.Publish(&Publish{
				Name:                 event.ReplyTo,
				PreviousIDs:          []string{event.ID},
				Transitioning:        "error",
				TransitioningMessage: err.Error(),
			})
			if publishErr != nil {
				return errors.Join(err, publishErr)
			}
		}
	}
}

func (c *Client) sendPings(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *Client) Publish(payload *Publish) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode platform reply: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint("publish"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create platform reply request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.accessKey, c.secretKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("publish platform reply: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("publish platform reply: unexpected status %d", resp.StatusCode)
	}
	return nil
}
