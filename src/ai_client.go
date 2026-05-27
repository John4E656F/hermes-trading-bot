package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type OpenRouterRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenRouterResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type AICorrectnessResponse struct {
	Verdict     string `json:"verdict"` // "CONFIRMED" or "REJECTED"
	Confidence  float64 `json:"confidence"`
	Explanation string `json:"explanation"`
}

type AIClient struct {
	APIKey     string
	HTTPClient *http.Client
}

func NewAIClient() *AIClient {
	return &AIClient{
		APIKey: os.Getenv("OPENROUTER_API_KEY"),
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// ValidateSignal passes local parameters to DeepSeek Flash for structural macro confirmation
func (ai *AIClient) ValidateSignal(sig StrategySignal, price float64, rsi float64, atr float64) (AICorrectnessResponse, error) {
	var result AICorrectnessResponse

	if ai.APIKey == "" {
		return result, fmt.Errorf("missing OPENROUTER_API_KEY environment variable")
	}

	// 1. Compress indicators to minimize token spending completely
	compactContext := fmt.Sprintf(
		"Symbol:%s|Price:%.2f|Strat:%s|Action:%s|Conviction:%d|RSI:%.1f|ATR:%.2f",
		sig.Symbol, price, sig.Strategy, sig.Action, sig.Conviction, rsi, atr,
	)

// 2. Build system directives to enforce zero-conversational output
		systemPrompt := "You are Hermes Risk Filter. Validate the trade based on strategy: Mean Reversion (S1) expects RSI<35 for buy, RSI>65 for sell. Squeeze (S2) expects extreme OI/funding. Breakout (S3) expects volatility. Reject if ATR=0. Return JSON: {\\\"verdict\\\":\\\"CONFIRMED\\\"|\\\"REJECTED\\\",\\\"confidence\\\":0.0-1.0,\\\"explanation\\\":\\\"1-sentence reason\\\"}. No markdown, no wrappers."

	payload := OpenRouterRequest{
		Model: "google/gemini-2.5-flash", // Using high-speed ultra-cheap flash structure via OpenRouter
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: compactContext},
		},
		MaxTokens:   60,
		Temperature: 0.1, // Near-zero temperature ensures absolute logical deterministic reasoning
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}

	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return result, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ai.APIKey)
	req.Header.Set("HTTP-Referer", "https://github.com/hermes-bot") 

	resp, err := ai.HTTPClient.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("openrouter API returned error status: %d", resp.StatusCode)
	}

	var openRouterResp OpenRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&openRouterResp); err != nil {
		return result, err
	}

	if len(openRouterResp.Choices) == 0 {
		return result, fmt.Errorf("received empty choice array from AI model")
	}

	// 3. Extract and unmarshal raw text directly into structural Go model
	rawJSONContent := openRouterResp.Choices[0].Message.Content
	err = json.Unmarshal([]byte(rawJSONContent), &result)
	if err != nil {
		// Fallback parse step if model included text decorations despite instructions
		return AICorrectnessResponse{
			Verdict:     "REJECTED",
			Confidence:  0.0,
			Explanation: "Failed parsing JSON structure safely: " + err.Error(),
		}, nil
	}

	return result, nil
}
