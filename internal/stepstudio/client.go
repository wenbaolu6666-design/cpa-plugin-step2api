package stepstudio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	CreateConversationPath = "/api/step.openapi.devcenter.Dashboard/CreateConversation"
	PlaygroundStreamPath   = "/api/stdhttp/v1/studio/stream"
)

type ChatModelConfig struct {
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	Thinking    bool    `json:"thinking,omitempty"`
	Effort      string  `json:"effort,omitempty"`
}

type ChatModelPayload struct {
	ModelID string          `json:"model_id"`
	Config  ChatModelConfig `json:"config"`
}

type PlaygroundChatRequest struct {
	Model           ChatModelPayload `json:"model"`
	Content         string           `json:"content"`
	SystemPrompt    string           `json:"system_prompt,omitempty"`
	AssetIDs        []string         `json:"asset_ids"`
	EnableWebSearch bool             `json:"enable_web_search"`
}

type TurnStreamBody struct {
	ConversationID string                `json:"conversation_id"`
	Kind           string                `json:"kind"`
	Chat           PlaygroundChatRequest `json:"chat"`
}

type Client struct {
	baseURL    string
	token      string
	webID      string
	httpClient *http.Client
	mu         sync.RWMutex
}

func NewClient(baseURL, oasisToken, webID string, client *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if client == nil {
		client = &http.Client{
			Timeout: 300 * time.Second,
		}
	}
	if webID == "" {
		webID = ExtractDeviceID(oasisToken)
		if webID == "" {
			webID = uuid.NewString()
		}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      oasisToken,
		webID:      webID,
		httpClient: client,
	}
}

func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

func (c *Client) GetToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *Client) EnsureFreshToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if NeedsRefresh(c.token, DefaultMarginSec*time.Second) {
		newTok, err := RefreshToken(ctx, c.httpClient, c.baseURL, c.token, c.webID)
		if err != nil {
			return fmt.Errorf("ensure fresh token failed: %w", err)
		}
		c.token = newTok
	}
	return nil
}

func (c *Client) applyHeaders(req *http.Request) {
	c.mu.RLock()
	tok := c.token
	webID := c.webID
	c.mu.RUnlock()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "Oasis-Token="+tok)
	req.Header.Set("oasis-appid", AppID)
	req.Header.Set("oasis-platform", Platform)
	req.Header.Set("oasis-language", Language)
	if webID != "" {
		req.Header.Set("oasis-webid", webID)
	}
}

func (c *Client) CreateConversation(ctx context.Context) (string, error) {
	_ = c.EnsureFreshToken(ctx)

	bodyJSON, _ := json.Marshal(map[string]any{
		"type": "CONVERSATION_TYPE_CHAT",
	})

	targetURL := c.baseURL + CreateConversationPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", fmt.Errorf("create conversation request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create conversation http error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("create conversation returned http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		Conversation struct {
			ConversationID string `json:"conversation_id"`
		} `json:"conversation"`
	}
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return "", fmt.Errorf("unmarshal create conversation response: %w", err)
	}
	if res.Conversation.ConversationID == "" {
		return "", errors.New("empty conversation_id returned from upstream")
	}
	return res.Conversation.ConversationID, nil
}

func (c *Client) StreamPlayground(
	ctx context.Context,
	conversationID string,
	chatReq PlaygroundChatRequest,
) (<-chan StudioEvent, <-chan error, error) {
	_ = c.EnsureFreshToken(ctx)

	body := TurnStreamBody{
		ConversationID: conversationID,
		Kind:           "chat",
		Chat:           chatReq,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal stream request body: %w", err)
	}

	targetURL := c.baseURL + PlaygroundStreamPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("create stream request: %w", err)
	}
	c.applyHeaders(req)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("post stream error: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		errBytes, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("upstream stream returned http %d: %s", resp.StatusCode, string(errBytes))
	}

	eventsCh := make(chan StudioEvent, 16)
	errCh := make(chan error, 1)

	go func() {
		defer resp.Body.Close()
		defer close(eventsCh)
		defer close(errCh)

		reader := bufio.NewReader(resp.Body)
		var eventData strings.Builder

		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				errCh <- fmt.Errorf("read stream line error: %w", err)
				return
			}

			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if eventData.Len() > 0 {
					var ev StudioEvent
					if err := json.Unmarshal([]byte(eventData.String()), &ev); err == nil {
						eventsCh <- ev
						if ev.Type == EventMessageStop {
							return
						}
					}
					eventData.Reset()
				}
				continue
			}

			if strings.HasPrefix(line, "data: ") {
				eventData.WriteString(strings.TrimPrefix(line, "data: "))
			} else if strings.HasPrefix(line, "data:") {
				eventData.WriteString(strings.TrimPrefix(line, "data:"))
			}
		}

		if eventData.Len() > 0 {
			var ev StudioEvent
			if err := json.Unmarshal([]byte(eventData.String()), &ev); err == nil {
				eventsCh <- ev
			}
		}
	}()

	return eventsCh, errCh, nil
}
