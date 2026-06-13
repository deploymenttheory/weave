// Port of lume's Unattended/AnthropicClient.swift on raw net/http: a
// minimal Claude API client for the computer-use loop. Request shape kept
// identical: POST /v1/messages with the computer_20251124 tool, max_tokens
// 4096, anthropic-version 2023-06-01 and the computer-use-2025-11-24 beta
// flag; 429/5xx retried with backoff.
//go:build darwin

package unattended

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deploymenttheory/weave/internal/objcutil"
)

const (
	anthropicBaseURL   = "https://api.anthropic.com/v1/messages"
	anthropicVersion   = "2023-06-01"
	anthropicBetaFlag  = "computer-use-2025-11-24"
	anthropicMaxTokens = 4096
)

// AnthropicClient sends computer-use conversations to the Claude API.
type AnthropicClient struct {
	APIKey        string
	Model         string
	DisplayWidth  int // dimensions reported in the computer tool definition
	DisplayHeight int
	httpClient    *http.Client
}

func NewAnthropicClient(apiKey string, model string, displayWidth int, displayHeight int) *AnthropicClient {
	return &AnthropicClient{
		APIKey:        apiKey,
		Model:         model,
		DisplayWidth:  displayWidth,
		DisplayHeight: displayHeight,
		httpClient:    &http.Client{Timeout: 120 * time.Second},
	}
}

// anthropicContentBlock is a tagged union over text / image / tool_use /
// tool_result blocks (zero fields are omitted, so one struct serves all).
type anthropicContentBlock struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	Source *anthropicImageSource `json:"source,omitempty"`

	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	ToolUseID string                  `json:"tool_use_id,omitempty"`
	Content   []anthropicContentBlock `json:"content,omitempty"`
	IsError   bool                    `json:"is_error,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg"
	Data      string `json:"data"`
}

func anthropicTextBlock(text string) anthropicContentBlock {
	return anthropicContentBlock{Type: "text", Text: text}
}

func anthropicImageBlock(jpegBase64 string) anthropicContentBlock {
	return anthropicContentBlock{Type: "image", Source: &anthropicImageSource{
		Type: "base64", MediaType: "image/jpeg", Data: jpegBase64,
	}}
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicResponse struct {
	StopReason string                  `json:"stop_reason"`
	Content    []anthropicContentBlock `json:"content"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// SendMessage posts the conversation and returns the assistant response.
func (c *AnthropicClient) SendMessage(ctx context.Context, systemPrompt string, messages []anthropicMessage) (*anthropicResponse, error) {
	body, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": anthropicMaxTokens,
		"system":     systemPrompt,
		"tools": []map[string]any{{
			"type":              "computer_20251124",
			"name":              "computer",
			"display_width_px":  c.DisplayWidth,
			"display_height_px": c.DisplayHeight,
		}},
		"messages": messages,
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		request, err := http.NewRequestWithContext(ctx, "POST", anthropicBaseURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("content-type", "application/json")
		request.Header.Set("x-api-key", c.APIKey)
		request.Header.Set("anthropic-version", anthropicVersion)
		request.Header.Set("anthropic-beta", anthropicBetaFlag)

		response, err := c.httpClient.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		// Retry rate limits and server errors.
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			lastErr = fmt.Errorf("Claude API returned status %d: %s", response.StatusCode, objcutil.TextPreview(data))
			continue
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Claude API returned status %d: %s", response.StatusCode, objcutil.TextPreview(data))
		}

		parsed := &anthropicResponse{}
		if err := json.Unmarshal(data, parsed); err != nil {
			return nil, err
		}
		if parsed.Error != nil {
			return nil, fmt.Errorf("Claude API error (%s): %s", parsed.Error.Type, parsed.Error.Message)
		}
		return parsed, nil
	}
	return nil, lastErr
}
