package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	apiBase           = "https://api.bot.qq.com"
	accessTokenURL    = apiBase + "/app/getAppAccessToken"
	groupAndC2CIntent = 1 << 25
)

type Message struct {
	ID           string       `json:"id"`
	Content      string       `json:"content"`
	Author       User         `json:"author"`
	MessageScene MessageScene `json:"message_scene"`
}

type User struct {
	ID         string `json:"id"`
	UserOpenID string `json:"user_openid"`
}

func (u User) OpenID() string {
	if u.UserOpenID != "" {
		return u.UserOpenID
	}
	return u.ID
}

type MessageScene struct {
	Ext []string `json:"ext"`
}

type Handler func(context.Context, Message)

type Client struct {
	appID      string
	secret     string
	httpClient *http.Client
	tokens     tokenManager
	seq        atomic.Int64
	mu         sync.Mutex
	sessionID  string
}

func New(appID, secret string) *Client {
	c := &Client{
		appID:  appID,
		secret: secret,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
	c.seq.Store(-1)
	c.tokens.client = c
	return c
}

func (c *Client) Run(ctx context.Context, handler Handler) error {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.runSession(ctx, handler)
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("QQ Gateway 连接中断", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff + time.Duration(rand.IntN(500))*time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (c *Client) runSession(ctx context.Context, handler Handler) error {
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return err
	}
	gateway, err := c.gateway(ctx, token)
	if err != nil {
		return err
	}
	conn, response, err := websocket.Dial(ctx, gateway, nil)
	if err != nil {
		if response != nil {
			return fmt.Errorf("dial gateway: HTTP %s: %w", response.Status, err)
		}
		return fmt.Errorf("dial gateway: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	var hello envelope
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if hello.Op != 10 {
		return fmt.Errorf("unexpected hello opcode %d", hello.Op)
	}
	var helloData struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(hello.Data, &helloData); err != nil || helloData.HeartbeatInterval <= 0 {
		return fmt.Errorf("invalid hello payload: %w", err)
	}

	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID != "" && c.seq.Load() >= 0 {
		err = wsjson.Write(ctx, conn, map[string]any{
			"op": 6,
			"d": map[string]any{
				"token":      "QQBot " + token,
				"session_id": sessionID,
				"seq":        c.seq.Load(),
			},
		})
	} else {
		err = wsjson.Write(ctx, conn, map[string]any{
			"op": 2,
			"d": map[string]any{
				"token":   "QQBot " + token,
				"intents": groupAndC2CIntent,
				"shard":   []int{0, 1},
				"properties": map[string]string{
					"$os":      "linux",
					"$browser": "qq-traffic-bot",
					"$device":  "qq-traffic-bot",
				},
			},
		})
	}
	if err != nil {
		return fmt.Errorf("authenticate gateway: %w", err)
	}

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	heartbeatErr := make(chan error, 1)
	go c.heartbeat(heartbeatCtx, conn, time.Duration(helloData.HeartbeatInterval)*time.Millisecond, heartbeatErr)

	for {
		var event envelope
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			status := websocket.CloseStatus(err)
			if status == 4006 || status == 4007 {
				c.clearSession()
			}
			return err
		}
		if event.Sequence != nil {
			c.seq.Store(*event.Sequence)
		}
		switch event.Op {
		case 0:
			switch event.Type {
			case "READY":
				var ready struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(event.Data, &ready); err != nil {
					return err
				}
				c.mu.Lock()
				c.sessionID = ready.SessionID
				c.mu.Unlock()
				slog.Info("QQ Gateway 已连接")
			case "RESUMED":
				slog.Info("QQ Gateway 会话已恢复")
			case "C2C_MESSAGE_CREATE":
				var message Message
				if err := json.Unmarshal(event.Data, &message); err != nil {
					slog.Error("解析 C2C 消息失败", "error", err)
					continue
				}
				handler(ctx, message)
			}
		case 7:
			return errors.New("gateway requested reconnect")
		case 9:
			c.clearSession()
			return errors.New("invalid gateway session")
		case 11:
			// Heartbeat ACK.
		}
		select {
		case err := <-heartbeatErr:
			return err
		default:
		}
	}
}

func (c *Client) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration, errors chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var seq any
			if current := c.seq.Load(); current >= 0 {
				seq = current
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := wsjson.Write(writeCtx, conn, map[string]any{"op": 1, "d": seq})
			cancel()
			if err != nil {
				select {
				case errors <- fmt.Errorf("send heartbeat: %w", err):
				default:
				}
				return
			}
		}
	}
}

func (c *Client) clearSession() {
	c.mu.Lock()
	c.sessionID = ""
	c.mu.Unlock()
	c.seq.Store(-1)
}

type envelope struct {
	Op       int             `json:"op"`
	Data     json.RawMessage `json:"d"`
	Sequence *int64          `json:"s"`
	Type     string          `json:"t"`
}

func (c *Client) gateway(ctx context.Context, token string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/gateway", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "QQBot "+token)
	var response struct {
		URL string `json:"url"`
	}
	if err := c.doJSON(request, &response); err != nil {
		return "", err
	}
	if response.URL == "" {
		return "", errors.New("gateway response did not contain url")
	}
	return response.URL, nil
}

func (c *Client) SendReply(ctx context.Context, openID, messageID, content string) error {
	payload := map[string]any{
		"content":  content,
		"msg_type": 0,
		"msg_id":   messageID,
		"msg_seq":  1,
	}
	return c.send(ctx, openID, payload)
}

func (c *Client) SendActive(ctx context.Context, openID, content string) error {
	payload := map[string]any{
		"content":  content,
		"msg_type": 0,
	}
	return c.send(ctx, openID, payload)
}

func (c *Client) send(ctx context.Context, openID string, payload map[string]any) error {
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := apiBase + "/v2/users/" + url.PathEscape(openID) + "/messages"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "QQBot "+token)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return c.doJSON(request, nil)
}

func (c *Client) doJSON(request *http.Request, output any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("QQ API %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if output == nil {
		io.Copy(io.Discard, response.Body)
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

type tokenManager struct {
	client    *Client
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func (m *tokenManager) Get(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && time.Until(m.expiresAt) > 5*time.Minute {
		return m.token, nil
	}
	body, err := json.Marshal(map[string]string{
		"appId":        m.client.appID,
		"clientSecret": m.client.secret,
	})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, accessTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	var response struct {
		AccessToken string        `json:"access_token"`
		ExpiresIn   flexibleInt64 `json:"expires_in"`
	}
	if err := m.client.doJSON(request, &response); err != nil {
		return "", err
	}
	if response.AccessToken == "" || response.ExpiresIn <= 0 {
		return "", errors.New("invalid access token response")
	}
	m.token = response.AccessToken
	m.expiresAt = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	return m.token, nil
}

type flexibleInt64 int64

func (v *flexibleInt64) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	value = strings.Trim(value, `"`)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("parse integer %q: %w", value, err)
	}
	*v = flexibleInt64(parsed)
	return nil
}
