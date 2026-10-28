package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type llmClient struct {
	config config
	http   *http.Client
}

type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("LLM API が HTTP %d を返しました: %s", e.status, e.body)
}

func newLLMClient(cfg config) *llmClient {
	return &llmClient{
		config: cfg,
		http:   &http.Client{Timeout: 90 * time.Second},
	}
}

func (c *llmClient) analyze(ctx context.Context, mode, logText string) (string, error) {
	var result string
	err := retry(ctx, c.config.LLMProvider+" API", retryableHTTPError, func() error {
		var err error
		switch c.config.LLMProvider {
		case "gemini":
			result, err = c.analyzeGemini(ctx, mode, logText)
		case "openai":
			result, err = c.analyzeOpenAI(ctx, mode, logText)
		default:
			err = fmt.Errorf("未対応の LLM_PROVIDER です: %q", c.config.LLMProvider)
		}
		return err
	})
	return result, err
}

func (c *llmClient) analyzeGemini(ctx context.Context, mode, logText string) (string, error) {
	payload := struct {
		SystemInstruction struct {
			Parts []textPart `json:"parts"`
		} `json:"system_instruction"`
		Contents []struct {
			Parts []textPart `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			MaxOutputTokens int `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}{}
	payload.SystemInstruction.Parts = []textPart{{Text: systemPrompt}}
	payload.Contents = []struct {
		Parts []textPart `json:"parts"`
	}{{Parts: []textPart{{Text: buildUserMessage(mode, logText)}}}}
	payload.GenerationConfig.MaxOutputTokens = c.config.GeminiMaxTokens

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("Gemini リクエストの作成に失敗しました: %w", err)
	}
	endpoint := "https://generativelanguage.googleapis.com/v1beta/models/" + c.config.GeminiModel + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("Gemini リクエストの作成に失敗しました: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.config.GeminiAPIKey)

	var response struct {
		Candidates []struct {
			Content struct {
				Parts []textPart `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := c.doJSON(req, &response); err != nil {
		return "", err
	}
	if len(response.Candidates) == 0 {
		return "", fmt.Errorf("Gemini API が候補を返しませんでした")
	}
	var output strings.Builder
	for _, part := range response.Candidates[0].Content.Parts {
		output.WriteString(part.Text)
	}
	return strings.TrimSpace(output.String()), nil
}

func (c *llmClient) analyzeOpenAI(ctx context.Context, mode, logText string) (string, error) {
	payload := struct {
		Model           string `json:"model"`
		Instructions    string `json:"instructions"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}{
		Model:           c.config.OpenAIModel,
		Instructions:    systemPrompt,
		Input:           buildUserMessage(mode, logText),
		MaxOutputTokens: c.config.OpenAIMaxTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("OpenAI リクエストの作成に失敗しました: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("OpenAI リクエストの作成に失敗しました: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.OpenAIAPIKey)

	var response struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := c.doJSON(req, &response); err != nil {
		return "", err
	}
	var output strings.Builder
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				output.WriteString(content.Text)
			}
		}
	}
	return strings.TrimSpace(output.String()), nil
}

type textPart struct {
	Text string `json:"text"`
}

func (c *llmClient) doJSON(req *http.Request, target interface{}) error {
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("LLM API への接続に失敗しました: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("LLM API の応答読み込みに失敗しました: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &httpStatusError{status: response.StatusCode, body: strings.TrimSpace(string(responseBody))}
	}
	if err := json.Unmarshal(responseBody, target); err != nil {
		return fmt.Errorf("LLM API の応答解析に失敗しました: %w", err)
	}
	return nil
}
