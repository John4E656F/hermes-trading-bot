package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type CouncilVote struct {
	Model       string  `json:"model"`
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
	LatencyMs   int     `json:"latency_ms"`
	Error       string  `json:"error,omitempty"`
}

type CouncilResult struct {
	FinalVerdict     string        `json:"final_verdict"`
	Confidence       float64       `json:"confidence"`
	Votes            []CouncilVote `json:"votes"`
	ConsensusSummary string        `json:"consensus_summary"`
	ConfirmCount     int           `json:"confirm_count"`
	RejectCount      int           `json:"reject_count"`
	ErroredCount     int           `json:"errored_count"`
}

type councilMemberConfig struct {
	Model   string
	Name    string
	Timeout time.Duration
}

var councilMembers = []councilMemberConfig{
	{Model: "deepseek/deepseek-chat", Name: "DeepSeek", Timeout: 20 * time.Second},
	{Model: "anthropic/claude-sonnet-4", Name: "Claude Sonnet", Timeout: 25 * time.Second},
	{Model: "google/gemini-2.5-flash", Name: "Gemini Flash", Timeout: 15 * time.Second},
	{Model: "openai/gpt-4o-2024-11-20", Name: "GPT-4o", Timeout: 25 * time.Second},
}

type AICouncilClient struct {
	APIKey     string
	HTTPClient *http.Client
}

func NewAICouncilClient() *AICouncilClient {
	return &AICouncilClient{
		APIKey: os.Getenv("OPENROUTER_API_KEY"),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (council *AICouncilClient) EvaluateSignal(sig StrategySignal, asset *AssetSnapshot, sentiment string) CouncilResult {
	var result CouncilResult

	if council.APIKey == "" {
		result.FinalVerdict = "UNAVAILABLE"
		result.ConsensusSummary = "OPENROUTER_API_KEY not set"
		return result
	}

	systemPrompt := buildCouncilSystemPrompt()
	userPrompt := buildCouncilUserPrompt(sig, asset, sentiment)

	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, member := range councilMembers {
		wg.Add(1)
		m := member
		go func() {
			defer wg.Done()
			start := time.Now()
			vote, err := council.callModel(m, systemPrompt, userPrompt)
			latency := time.Since(start)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				vote = CouncilVote{
					Model: m.Name,
					Verdict: "ERROR",
					Error:   err.Error(),
				}
			}
			vote.Model = m.Name
			vote.LatencyMs = int(latency.Milliseconds())
			result.Votes = append(result.Votes, vote)
		}()
	}
	wg.Wait()

	var confirmCount, rejectCount, errorCount int
	var totalConfidence float64
	var validVotes int

	for _, v := range result.Votes {
		switch v.Verdict {
		case "CONFIRMED":
			confirmCount++
			totalConfidence += v.Confidence
			validVotes++
		case "REJECTED":
			rejectCount++
			totalConfidence += v.Confidence
			validVotes++
		default:
			errorCount++
		}
	}

	result.ConfirmCount = confirmCount
	result.RejectCount = rejectCount
	result.ErroredCount = errorCount

	if validVotes > 0 {
		result.Confidence = totalConfidence / float64(validVotes)
	}

	if confirmCount > rejectCount {
		result.FinalVerdict = "CONFIRMED"
	} else if rejectCount > confirmCount {
		result.FinalVerdict = "REJECTED"
	} else {
		strongestVote := ""
		strongestConf := 0.0
		for _, v := range result.Votes {
			if v.Confidence > strongestConf && v.Verdict != "ERROR" {
				strongestConf = v.Confidence
				strongestVote = v.Verdict
			}
		}
		result.FinalVerdict = strongestVote
		if result.FinalVerdict == "" {
			result.FinalVerdict = "REJECTED"
		}
	}

	var parts []string
	for _, v := range result.Votes {
		if v.Error != "" {
			parts = append(parts, fmt.Sprintf("%s: ERROR (%s)", v.Model, v.Error))
		} else {
			short := v.Explanation
			if len(short) > 80 {
				short = short[:80] + "..."
			}
			parts = append(parts, fmt.Sprintf("%s: %s (%.0f%% %s)",
				v.Model, v.Verdict, v.Confidence*100, short))
		}
	}
	result.ConsensusSummary = strings.Join(parts, "\n")

	return result
}

func (council *AICouncilClient) callModel(member councilMemberConfig, systemPrompt, userPrompt string) (CouncilVote, error) {
	var vote CouncilVote

	payload := map[string]interface{}{
		"model": member.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens":  150,
		"temperature": 0.1,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return vote, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return vote, fmt.Errorf("request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+council.APIKey)
	req.Header.Set("HTTP-Referer", "https://github.com/hermes-bot")

	client := &http.Client{Timeout: member.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return vote, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return vote, fmt.Errorf("API returned %d", resp.StatusCode)
	}

	var orResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&orResp); err != nil {
		return vote, fmt.Errorf("decode: %w", err)
	}
	if len(orResp.Choices) == 0 {
		return vote, fmt.Errorf("empty choices")
	}

	raw := orResp.Choices[0].Message.Content
	var parsed CouncilVote
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return CouncilVote{
			Verdict:     "REJECTED",
			Confidence:  0.0,
			Explanation: "Parse error: " + err.Error() + " — raw: " + raw,
		}, nil
	}

	switch parsed.Verdict {
	case "CONFIRMED", "REJECTED":
	default:
		parsed.Verdict = "REJECTED"
		parsed.Explanation = "Invalid verdict: " + raw
	}
	if parsed.Confidence <= 0 || parsed.Confidence > 1 {
		parsed.Confidence = 0.5
	}
	if parsed.Explanation == "" {
		parsed.Explanation = "No explanation"
	}

	return parsed, nil
}

func buildCouncilSystemPrompt() string {
	return `You are a crypto perpetual futures trade validator. Given signal + market context, return ONLY valid JSON — no markdown:
{"verdict":"CONFIRMED"|"REJECTED","confidence":0.0-1.0,"explanation":"1-2 sentence reason"}

CONFIRM if: strong trend alignment + volume + supportive news/sentiment + reasonable funding
REJECT if: mixed indicators, crowded funding, overbought/oversold extremes, news contradicts direction
Be conservative. Better to miss a trade than take a bad one.`
}

func buildCouncilUserPrompt(sig StrategySignal, asset *AssetSnapshot, sentiment string) string {
	ind4h := asset.Snap4h.Indicators
	ind1d := asset.Snap1d.Indicators

	context := fmt.Sprintf(
		"Symbol:%s|Action:%s|Price:%.4f|Strategy:%s|Conviction:%d|Confidence:%.0f%%|"+
			"RSI:%.1f|ATR:%.4f|ADX:%.1f|WilliamsR:%.0f|BBWidth:%.2f%%|"+
			"FundingRate:%.5f|OI_Spike:%v|7D_Gain:%.1f%%|Reason:%s",
		sig.Symbol, sig.Action, asset.CurrentPrice, sig.Strategy, sig.Conviction, sig.Confidence*100,
		ind4h.RSI14, ind4h.ATR14, ind1d.ADX14, ind4h.WilliamsR, ind4h.BBWidth,
		asset.Funding.CurrentRate, asset.OI.IsSpike,
		Compute7DayGain(asset.Snap1d.Candles), sig.Reason,
	)

	if sentiment != "" {
		context += "\n\nMarket Sentiment:\n" + sentiment
	}

	return context
}

func (council *AICouncilClient) FallbackValidateSignal(sig StrategySignal, asset *AssetSnapshot) CouncilVote {
	systemPrompt := buildCouncilSystemPrompt()
	userPrompt := buildCouncilUserPrompt(sig, asset, "")

	fallback := councilMemberConfig{
		Model:   "deepseek/deepseek-chat",
		Name:    "DeepSeek (fallback)",
		Timeout: 20 * time.Second,
	}

	vote, err := council.callModel(fallback, systemPrompt, userPrompt)
	if err != nil {
		return CouncilVote{
			Model:       "DeepSeek (fallback)",
			Verdict:     "CONFIRMED",
			Confidence:  0.5,
			Explanation: "Fallback error, proceeding: " + err.Error(),
		}
	}
	return vote
}