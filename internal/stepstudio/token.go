package stepstudio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultBaseURL   = "https://studio.stepfun.com"
	AppID            = "10300"
	Platform         = "web"
	Language         = "zh-CN"
	PassportRefresh  = "/passport/proto.api.passport.v1.PassportService/RefreshToken"
	DefaultMarginSec = 180
)

func SplitToken(oasisToken string) (accessJWT, refreshJWT string, err error) {
	token := strings.TrimSpace(oasisToken)
	if token == "" {
		return "", "", errors.New("empty oasis-token")
	}
	parts := strings.Split(token, "...")
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}
	if len(parts) == 1 {
		return parts[0], parts[0], nil
	}
	return "", "", fmt.Errorf("invalid oasis-token format, expected 2 parts, got %d", len(parts))
}

func CombineToken(accessJWT, refreshJWT string) string {
	a := strings.TrimSpace(accessJWT)
	r := strings.TrimSpace(refreshJWT)
	if r == "" || a == r {
		return a
	}
	return a + "..." + r
}

func ParseJWTClaims(jwtStr string) (map[string]any, error) {
	parts := strings.Split(strings.TrimSpace(jwtStr), ".")
	if len(parts) < 2 {
		return nil, errors.New("invalid jwt format")
	}
	payloadB64 := parts[1]
	switch len(payloadB64) % 4 {
	case 2:
		payloadB64 += "=="
	case 3:
		payloadB64 += "="
	}

	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(payloadB64)
		if err != nil {
			data, err = base64.StdEncoding.DecodeString(payloadB64)
			if err != nil {
				return nil, fmt.Errorf("decode jwt payload: %w", err)
			}
		}
	}

	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal jwt claims: %w", err)
	}
	return claims, nil
}

func GetTokenExpiration(jwtStr string) time.Time {
	claims, err := ParseJWTClaims(jwtStr)
	if err != nil {
		return time.Time{}
	}
	expVal, ok := claims["exp"]
	if !ok {
		return time.Time{}
	}
	switch v := expVal.(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return time.Unix(i, 0)
		}
	}
	return time.Time{}
}

func ExtractDeviceID(jwtStr string) string {
	claims, err := ParseJWTClaims(jwtStr)
	if err != nil {
		return ""
	}
	if dev, ok := claims["device_id"].(string); ok && dev != "" {
		return dev
	}
	return ""
}

func NeedsRefresh(oasisToken string, margin time.Duration) bool {
	access, _, err := SplitToken(oasisToken)
	if err != nil {
		return false
	}
	exp := GetTokenExpiration(access)
	if exp.IsZero() {
		return false
	}
	if margin <= 0 {
		margin = DefaultMarginSec * time.Second
	}
	return time.Until(exp) < margin
}

func RefreshToken(ctx context.Context, client *http.Client, baseURL, oasisToken, webID string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	access, refresh, err := SplitToken(oasisToken)
	if err != nil {
		return "", err
	}

	targetURL := baseURL + PassportRefresh
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader("{}"))
	if err != nil {
		return "", fmt.Errorf("create refresh request: %w", err)
	}

	if webID == "" {
		webID = ExtractDeviceID(refresh)
		if webID == "" {
			webID = ExtractDeviceID(access)
		}
	}

	cookieVal := "Oasis-Token=" + CombineToken(access, refresh)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookieVal)
	req.Header.Set("oasis-appid", AppID)
	req.Header.Set("oasis-platform", Platform)
	req.Header.Set("oasis-language", Language)
	if webID != "" {
		req.Header.Set("oasis-webid", webID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("passport refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("passport refresh returned http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "Oasis-Token" && strings.TrimSpace(cookie.Value) != "" {
			return cookie.Value, nil
		}
	}

	var respJSON map[string]any
	if err := json.Unmarshal(bodyBytes, &respJSON); err == nil {
		if tok, ok := respJSON["token"].(string); ok && tok != "" {
			return tok, nil
		}
		if tok, ok := respJSON["access_token"].(string); ok && tok != "" {
			newRefresh := refresh
			if r, ok := respJSON["refresh_token"].(string); ok && r != "" {
				newRefresh = r
			}
			return CombineToken(tok, newRefresh), nil
		}
	}

	return oasisToken, nil
}

// ExtractCookieTokens extracts (Oasis-Token, Oasis-Webid) from a raw token or full Cookie header.
func ExtractCookieTokens(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "cookie:") {
		raw = strings.TrimSpace(raw[7:])
	}
	token := ""
	webid := ""
	parts := strings.Split(strings.ReplaceAll(raw, "\n", ";"), ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "=") {
			kv := strings.SplitN(part, "=", 2)
			name := strings.ToLower(strings.TrimSpace(kv[0]))
			val := strings.TrimSpace(kv[1])
			if name == "oasis-token" || name == "oasistoken" || name == "token" {
				token = val
			} else if name == "oasis-webid" || name == "oasiswebid" || name == "webid" {
				webid = val
			}
		} else if strings.Contains(part, "...") && token == "" {
			token = part
		} else if strings.HasPrefix(part, "eyJ") && token == "" {
			token = part
		}
	}
	if token == "" {
		token = strings.SplitN(raw, ";", 2)[0]
	}
	token = strings.Trim(strings.TrimSpace(token), "\"\x27")
	if idx := strings.IndexByte(token, ';'); idx >= 0 {
		token = strings.TrimSpace(token[:idx])
	}
	if webid == "" {
		webid = ExtractDeviceID(token)
	}
	return token, webid
}
