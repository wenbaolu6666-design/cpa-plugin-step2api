package stepstudio

import (
	"context"
	"strings"
	"testing"
)

func TestLoadImageDataFromDataURI(t *testing.T) {
	client := NewClient("", "token", "web-id", nil)
	data, mimeType, name, err := client.LoadImageData(context.Background(),
		"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
		"", "")
	if err != nil {
		t.Fatalf("LoadImageData failed: %v", err)
	}
	if len(data) == 0 || mimeType != "image/png" {
		t.Fatalf("unexpected image data: len=%d mime=%s", len(data), mimeType)
	}
	if !strings.HasSuffix(name, ".png") {
		t.Errorf("unexpected image name: %s", name)
	}
}
