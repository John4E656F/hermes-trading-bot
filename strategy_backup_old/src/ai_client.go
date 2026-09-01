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
	Verdict     string  `json:"verdict"` // "CONFIRMED" or "REJECTED"
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
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

// ValidateSignal sends the full signal context to an AI model for a second opinion.
// Returns CONFIRMED to proceed or REJECTED to abort — plus a plain-English explanation
// that is written to the trade log regardless of outcome.
func (ai *AIClient) ValidateSignal(sig StrategySignal, asset *AssetSnapshot) (AICorrectnessResponse, error) {
	var result AICorrectnessResponse

	if ai.APIKey == "" {
		return result, fmt.Errorf("OPENROUTER_API_KEY not set")
	}

	ind4h := asset.Snap4h.Indicators
	ind1d := asset.Snap1d.Indicators

	context := fmt.Sprintf(
		"Symbol:%s|Action:%s|Price:%.4f|Strategy:%s|Conviction:%d|Confidence:%.0f%%|"+
			"RSI:%.1f|ATR:%.4f|ADX:%.1f|WilliamsR:%.0f|BBWidth:%.2f%%|"+
			"FundingRate:%.5f|S4:%v(%s)|S5:%v(%s)|OI_Spike:%v|"+
			"Reason:%s",
		sig.Symbol, sig.Action, asset.CurrentPrice, sig.Strategy, sig.Conviction, sig.Confidence*100,
		ind4h.RSI14, ind4h.ATR14, ind1d.ADX14, ind4h.WilliamsR, ind4h.BBWidth,
		asset.Funding.CurrentRate,
		sig.S4.Active, sig.S4.Action,
		sig.S5.Active, sig.S5.Action,
		asset.OI.IsSpike,
		sig.Reason,
	)

	systemPrompt := `You are Hermes Risk Filter — a concise crypto futures trade validator.
Given a signal context, return ONLY valid JSON, no markdown, no extra text:
{"verdict":"CONFIRMED"|"REJECTED","confidence":0.0-1.0,"explanation":"1-2 sentence reason"}

Rules:
- REJECT if ATR=0 or Conviction<2
- REJECT if buying with WilliamsR>-20 (overbought) or selling with WilliamsR<-80 (oversold)

S4 FUNDING CONTRARIAN rules (overrides generic funding rules below):
- S4 SELL fires when funding is POSITIVE (longs overleveraged, paying to stay). CONFIRM.
- S4 BUY fires when funding is NEGATIVE (shorts overleveraged, paying to stay). CONFIRM.

Generic funding rules (NOT when S4 is the strategy):
- REJECT if FundingRate>0.0003 on a BUY (longs already over-leveraged)
- REJECT if FundingRate<-0.0001 on a SELL (shorts already crowded)
- CONFIRM S4 Funding Contrarian when funding extreme AND OI_Spike=true (crowd trapped)
- CONFIRM S5 BB Squeeze when BBWidth<4% (genuine compression before breakout)
- For ADX>35 trend signals: CONFIRM if price momentum agrees, REJECT if counter-trend
- Apply extra skepticism to signals where Confidence<75%`

	payload := OpenRouterRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: context},
		},
		MaxTokens:   80,
		Temperature: 0.1,
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
		return result, fmt.Errorf("openrouter API returned status %d", resp.StatusCode)
	}

	var orResp OpenRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&orResp); err != nil {
		return result, err
	}

	if len(orResp.Choices) == 0 {
		return result, fmt.Errorf("empty choices array from AI model")
	}

	raw := orResp.Choices[0].Message.Content
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		// Model ignored formatting instructions — treat as soft rejection
		return AICorrectnessResponse{
			Verdict:     "REJECTED",
			Confidence:  0.0,
			Explanation: "AI response parse failed: " + err.Error(),
		}, nil
	}

	return result, nil
}
