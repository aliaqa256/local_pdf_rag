package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"rag-service/internal/infrastructure/config"
)

// LLMClient defines a provider-agnostic interface for text generation
type LLMClient interface {
	GenerateText(ctx context.Context, prompt string) (string, error)
}

type GoogleGeminiAdapter struct {
	Client *http.Client
	Config *config.Config
}

type geminiContentPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiContentPart `json:"parts"`
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Error      *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

func NewGoogleGeminiAdapter(cfg *config.Config) (*GoogleGeminiAdapter, error) {
	transport := &http.Transport{}
	if cfg.GoogleDNS != "" {
		dialer := &net.Dialer{Timeout: 30 * time.Second}
		resolverIP := cfg.GoogleDNS
		// Custom resolver via DialContext using a fixed DNS server
		dialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
			// Force DNS lookups through the specified resolver by overriding the default resolver
			net.DefaultResolver = &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, net.JoinHostPort(resolverIP, "53"))
				},
			}
			return dialer.DialContext(ctx, network, address)
		}
		transport = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			DialContext:     dialContext,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}

	client := &http.Client{Timeout: 120 * time.Second, Transport: transport}

	if cfg.GoogleAPIKey == "" {
		return nil, fmt.Errorf("missing GOOGLE_API_KEY in configuration")
	}

	return &GoogleGeminiAdapter{Client: client, Config: cfg}, nil
}

func (g *GoogleGeminiAdapter) GenerateText(ctx context.Context, prompt string) (string, error) {
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", g.Config.GoogleModel)

	// Optional Persian system guidance if app language is Persian
	if g.Config.AppLanguage == "fa" {
		prompt = "لطفاً فقط به زبان فارسی، روان و خلاصه پاسخ بده. اگر پاسخ در متن موجود نبود، صریح بگو که اطلاعات کافی در متن موجود نیست.\n\n" + prompt
	}

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiContentPart{{Text: prompt}}},
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(data))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.Config.GoogleAPIKey)
	req.Header.Set("User-Agent", "rag-service/1.0")
	if g.Config.AppLanguage == "fa" {
		req.Header.Set("Accept-Language", "fa-IR,fa;q=0.9")
	}

	resp, err := g.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gemini returned status %d: %s", resp.StatusCode, string(body))
	}

	var gr geminiResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if gr.Error != nil {
		return "", fmt.Errorf("gemini error: %s", gr.Error.Message)
	}

	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned empty response")
	}

	var output string
	for _, part := range gr.Candidates[0].Content.Parts {
		if output == "" {
			output = part.Text
		} else {
			output += "\n" + part.Text
		}
	}

	return output, nil
}
