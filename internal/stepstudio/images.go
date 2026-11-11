package stepstudio

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const maxImageBytes = 20 << 20

// LoadImageData resolves a data URI or remote URL into raw image bytes.
func (c *Client) LoadImageData(ctx context.Context, rawURL, mimeHint, nameHint string) ([]byte, string, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, "", "", fmt.Errorf("empty image url")
	}
	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		header, payload, ok := strings.Cut(rawURL, ",")
		if !ok {
			return nil, "", "", fmt.Errorf("invalid data uri")
		}
		mediaType := strings.TrimPrefix(header, "data:")
		isBase64 := false
		if idx := strings.Index(mediaType, ";"); idx >= 0 {
			isBase64 = strings.Contains(mediaType[idx+1:], "base64")
			mediaType = mediaType[:idx]
		}
		mediaType = strings.TrimSpace(mediaType)
		if mimeHint != "" {
			mediaType = mimeHint
		}
		var data []byte
		var err error
		if isBase64 {
			data, err = decodeBase64(payload)
			if err != nil {
				return nil, "", "", fmt.Errorf("decode image data uri: %w", err)
			}
		} else {
			unescaped, errUnescape := url.QueryUnescape(payload)
			if errUnescape != nil {
				unescaped = payload
			}
			data = []byte(unescaped)
		}
		if len(data) == 0 {
			return nil, "", "", fmt.Errorf("empty image data")
		}
		if len(data) > maxImageBytes {
			return nil, "", "", fmt.Errorf("image too large: %d bytes", len(data))
		}
		return data, mediaType, imageName(nameHint, rawURL, mediaType), nil
	}

	lower := strings.ToLower(rawURL)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return nil, "", "", fmt.Errorf("unsupported image url scheme")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("create image request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", "", fmt.Errorf("download image returned http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read image body: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, "", "", fmt.Errorf("image too large: over %d bytes", maxImageBytes)
	}
	if len(data) == 0 {
		return nil, "", "", fmt.Errorf("empty image body")
	}
	mimeType := resp.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = mimeType[:idx]
	}
	mimeType = strings.TrimSpace(mimeType)
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = mimeHint
	}
	return data, mimeType, imageName(nameHint, rawURL, mimeType), nil
}

func decodeBase64(payload string) ([]byte, error) {
	payload = strings.TrimSpace(payload)
	if data, err := base64.StdEncoding.DecodeString(payload); err == nil {
		return data, nil
	}
	if data, err := base64.RawStdEncoding.DecodeString(payload); err == nil {
		return data, nil
	}
	return base64.URLEncoding.DecodeString(payload)
}

func imageName(nameHint, rawURL, mimeType string) string {
	name := strings.TrimSpace(nameHint)
	if name == "" {
		if parsed, err := url.Parse(rawURL); err == nil {
			name = path.Base(parsed.Path)
		}
	}
	if name == "" || name == "." || name == "/" {
		name = "image"
	}
	if path.Ext(name) == "" {
		switch strings.ToLower(mimeType) {
		case "image/jpeg":
			name += ".jpg"
		case "image/png":
			name += ".png"
		case "image/gif":
			name += ".gif"
		case "image/webp":
			name += ".webp"
		default:
			name += ".png"
		}
	}
	return name
}
