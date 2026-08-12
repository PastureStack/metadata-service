package platformevents

import (
	"bytes"
	"context"
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
	origin       trustedOrigin
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

	client := &Client{
		eventBaseURL: eventBaseURL,
		apiBaseURL:   apiBaseURL,
		origin:       originFor(eventBaseURL),
		accessKey:    accessKey,
		secretKey:    secretKey,
		dialer: &websocket.Dialer{
			HandshakeTimeout: 30 * time.Second,
		},
		pingInterval: 5 * time.Second,
	}
	client.httpClient = &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: client.checkRedirect,
	}
	return client, nil
}

func normalizeBaseURLs(rawURL string) (*url.URL, *url.URL, error) {
	u, err := validatePlatformBaseURL(rawURL)
	if err != nil {
		return nil, nil, err
	}
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

func (c *Client) endpoint(endpointPath string) *url.URL {
	u := *c.apiBaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(endpointPath, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return &u
}

func (c *Client) subscriptionURL(eventNames []string) (*url.URL, error) {
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
		if !eventNamePattern.MatchString(eventName) {
			return nil, fmt.Errorf("event name has an unsupported format")
		}
		query.Add("eventNames", eventName)
	}
	u.RawQuery = query.Encode()
	if err := c.validateNetworkURL(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) Run(handlers map[string]Handler) error {
	return c.RunContext(context.Background(), handlers)
}

func (c *Client) RunContext(ctx context.Context, handlers map[string]Handler) error {
	eventNames := make([]string, 0, len(handlers))
	for eventName := range handlers {
		eventNames = append(eventNames, eventName)
	}

	subscriptionURL, err := c.subscriptionURL(eventNames)
	if err != nil {
		return err
	}
	webSocketURL := subscriptionURL.String()
	if !platformRequestURLPattern.MatchString(webSocketURL) || !isValidRedirect(subscriptionURL, c.origin) {
		return fmt.Errorf("event stream URL is outside the trusted platform origin")
	}

	headers := http.Header{}
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.accessKey+":"+c.secretKey)))
	conn, response, err := c.dialer.DialContext(ctx, webSocketURL, headers)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect to platform event stream: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(maxResponseBytes)

	done := make(chan struct{})
	defer close(done)
	go c.sendPings(conn, done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
				TransitioningMessage: "metadata event processing failed",
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
	req, err := c.newAuthenticatedRequest(http.MethodPost, c.endpoint("publish"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create platform reply request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
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

func (c *Client) ConfigurationContentURL(requestedVersion, appliedVersion string) (*url.URL, error) {
	if !versionPattern.MatchString(requestedVersion) || !versionPattern.MatchString(appliedVersion) {
		return nil, fmt.Errorf("configuration version has an unsupported format")
	}
	u := *c.eventBaseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/configcontent/metadata-answers"
	query := url.Values{}
	query.Set("client", "v2")
	if requestedVersion != "" {
		query.Set("requestedVersion", requestedVersion)
	}
	if appliedVersion != "" {
		query.Set("version", appliedVersion)
	}
	u.RawQuery = query.Encode()
	if err := c.validateNetworkURL(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) DownloadConfiguration(requestedVersion string) (*http.Response, error) {
	return c.DownloadConfigurationContext(context.Background(), requestedVersion)
}

func (c *Client) DownloadConfigurationContext(ctx context.Context, requestedVersion string) (*http.Response, error) {
	u, err := c.ConfigurationContentURL(requestedVersion, "")
	if err != nil {
		return nil, err
	}
	req, err := c.newAuthenticatedRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) ReportAppliedConfiguration(version string) (*http.Response, error) {
	return c.ReportAppliedConfigurationContext(context.Background(), version)
}

func (c *Client) ReportAppliedConfigurationContext(ctx context.Context, version string) (*http.Response, error) {
	u, err := c.ConfigurationContentURL("", version)
	if err != nil {
		return nil, err
	}
	req, err := c.newAuthenticatedRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) newAuthenticatedRequest(method string, target *url.URL, body io.Reader) (*http.Request, error) {
	return c.newAuthenticatedRequestWithContext(context.Background(), method, target, body)
}

func (c *Client) newAuthenticatedRequestWithContext(ctx context.Context, method string, target *url.URL, body io.Reader) (*http.Request, error) {
	if err := c.validateNetworkURL(target); err != nil {
		return nil, err
	}
	rawURL := target.String()
	if !platformRequestURLPattern.MatchString(rawURL) {
		return nil, fmt.Errorf("platform request URL has an unsupported format")
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.accessKey, c.secretKey)
	return req, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("platform request URL is missing")
	}
	if err := c.validateNetworkURL(req.URL); err != nil {
		return nil, err
	}
	rawURL := req.URL.String()
	if !platformRequestURLPattern.MatchString(rawURL) {
		return nil, fmt.Errorf("platform request URL has an unsupported format")
	}
	return c.httpClient.Do(req)
}

func (c *Client) validateNetworkURL(candidate *url.URL) error {
	if candidate == nil || !platformRequestURLPattern.MatchString(candidate.String()) {
		return fmt.Errorf("platform request URL has an unsupported format")
	}
	if !isValidRedirect(candidate, c.origin) {
		return fmt.Errorf("platform request URL is outside the trusted origin")
	}
	return nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("platform redirect limit exceeded")
	}
	if req == nil || req.URL == nil || !platformRequestURLPattern.MatchString(req.URL.String()) || !isValidRedirect(req.URL, c.origin) {
		return fmt.Errorf("platform redirect crosses the trusted origin")
	}
	req.SetBasicAuth(c.accessKey, c.secretKey)
	return nil
}
