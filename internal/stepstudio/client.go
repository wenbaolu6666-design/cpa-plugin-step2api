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
	CreateConversationPath     = "/api/step.openapi.devcenter.Dashboard/CreateConversation"
	PlaygroundStreamPath       = "/api/stdhttp/v1/studio/stream"
	QueryStudioRateLimitPath   = "/api/step.openapi.devcenter.Dashboard/QueryStudioRateLimit"
	GetStepPlanStatusPath      = "/api/step.openapi.devcenter.Dashboard/GetStepPlanStatus"
	QueryAccountBalancePath    = "/api/step.openapi.devcenter.Dashboard/QueryAccountBalance"
	QueryStepPlanRateLimitPath = "/api/step.openapi.devcenter.Dashboard/QueryStepPlanRateLimit"
)

type PlatformAccountBalanceResponse struct {
	Balance           string `json:"balance"`
	Payment           string `json:"payment"`
	CostTotal         string `json:"cost_total"`
	Voucher           string `json:"voucher"`
	Credit            string `json:"credit"`
	VoucherExpireTime int64  `json:"voucher_expire_time"`
}

type StepPlanCreditBucket struct {
	Type           int    `json:"type"`
	CreditTotal    string `json:"credit_total"`
	CreditResidual string `json:"credit_residual"`
	ExpireAt       string `json:"expire_at"`
	NextResetAt    string `json:"next_reset_at"`
}

type StepPlanCreditRateLimit struct {
	SubscriptionCreditLeftRate  float64                `json:"subscription_credit_left_rate"`
	SubscriptionCreditResetTime string                 `json:"subscription_credit_reset_time"`
	TopupCreditLeftRate         float64                `json:"topup_credit_left_rate"`
	CreditBuckets               []StepPlanCreditBucket `json:"credit_buckets"`
}

type StepPlanRateLimitResponse struct {
	Status                 int                      `json:"status"`
	Desc                   string                   `json:"desc"`
	PlanFamily             int                      `json:"plan_family"`
	FiveHourUsageLeftRate  float64                  `json:"five_hour_usage_left_rate"`
	FiveHourUsageResetTime string                   `json:"five_hour_usage_reset_time"`
	WeeklyUsageLeftRate    float64                  `json:"weekly_usage_left_rate"`
	WeeklyUsageResetTime   string                   `json:"weekly_usage_reset_time"`
	PlanCreditRateLimit    *StepPlanCreditRateLimit `json:"plan_credit_rate_limit"`
}

type StudioCreditRateLimit struct {
	SubscriptionCreditLeftRate  float64 `json:"subscription_credit_left_rate"`
	SubscriptionCreditResetTime string  `json:"subscription_Credit_reset_time"`
}

type StudioRateLimitResponse struct {
	FreeRate               float64                `json:"free_rate"`
	FiveHourUsageLeftRate  float64                `json:"five_hour_usage_left_rate"`
	FiveHourUsageResetTime string                 `json:"five_hour_usage_reset_time"`
	WeeklyUsageLeftRate    float64                `json:"weekly_usage_left_rate"`
	WeeklyUsageResetTime   string                 `json:"weekly_usage_reset_time"`
	CreditRateLimit        *StudioCreditRateLimit `json:"credit_rate_limit"`
}

type StepPlanSubscription struct {
	PlanType    int    `json:"plan_type"`
	Name        string `json:"name"`
	Status      int    `json:"status"`
	PayChannel  int    `json:"pay_channel"`
	ActivatedAt string `json:"activated_at"`
	ExpiredAt   string `json:"expired_at"`
	AutoRenew   bool   `json:"auto_renew"`
	PlanID      string `json:"plan_id"`
	PlanFamily  int    `json:"plan_family"`
}

type StepPlanStatusResponse struct {
	Status       int                   `json:"status"`
	Desc         string                `json:"desc"`
	Subscription *StepPlanSubscription `json:"subscription"`
}

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

func (c *Client) QueryRateLimit(ctx context.Context) (*StudioRateLimitResponse, error) {
	_ = c.EnsureFreshToken(ctx)

	targetURL := c.baseURL + QueryStudioRateLimitPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create rate limit request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rate limit http error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		if newTok, errRef := RefreshToken(ctx, c.httpClient, c.baseURL, c.GetToken(), c.webID); errRef == nil && newTok != "" {
			c.SetToken(newTok)
			reqRetry, _ := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
			c.applyHeaders(reqRetry)
			if respRetry, errRetry := c.httpClient.Do(reqRetry); errRetry == nil {
				defer respRetry.Body.Close()
				if respRetry.StatusCode < 400 {
					bodyBytes, _ = io.ReadAll(respRetry.Body)
					resp.StatusCode = respRetry.StatusCode
				}
			}
		}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("rate limit returned http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var res StudioRateLimitResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("unmarshal rate limit response: %w", err)
	}
	return &res, nil
}

func (c *Client) GetStepPlanStatus(ctx context.Context) (*StepPlanStatusResponse, error) {
	_ = c.EnsureFreshToken(ctx)

	targetURL := c.baseURL + GetStepPlanStatusPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create step plan request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("step plan http error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		if newTok, errRef := RefreshToken(ctx, c.httpClient, c.baseURL, c.GetToken(), c.webID); errRef == nil && newTok != "" {
			c.SetToken(newTok)
			reqRetry, _ := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
			c.applyHeaders(reqRetry)
			if respRetry, errRetry := c.httpClient.Do(reqRetry); errRetry == nil {
				defer respRetry.Body.Close()
				if respRetry.StatusCode < 400 {
					bodyBytes, _ = io.ReadAll(respRetry.Body)
					resp.StatusCode = respRetry.StatusCode
				}
			}
		}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("step plan returned http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var res StepPlanStatusResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("unmarshal step plan response: %w", err)
	}
	return &res, nil
}

func (c *Client) QueryAccountBalance(ctx context.Context) (*PlatformAccountBalanceResponse, error) {
	_ = c.EnsureFreshToken(ctx)

	targetURL := c.baseURL + QueryAccountBalancePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create account balance request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("account balance http error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("account balance returned http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var res PlatformAccountBalanceResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("unmarshal account balance response: %w", err)
	}
	return &res, nil
}

func (c *Client) QueryStepPlanRateLimit(ctx context.Context) (*StepPlanRateLimitResponse, error) {
	_ = c.EnsureFreshToken(ctx)

	targetURL := c.baseURL + QueryStepPlanRateLimitPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create step plan rate limit request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("step plan rate limit http error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("step plan rate limit returned http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var res StepPlanRateLimitResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("unmarshal step plan rate limit response: %w", err)
	}
	return &res, nil
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
