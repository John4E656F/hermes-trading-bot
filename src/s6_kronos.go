package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const kronosBaseURL = "http://localhost:8765"

// KronosClient talks to the local Kronos AI prediction microservice.
// Use NewKronosClient() to construct — it returns nil if the service is down,
// so callers can treat Kronos as a purely optional overlay.
type KronosClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// KronosPrediction mirrors the /predict/{symbol} response shape:
//
//	{"symbol":"BTC","price":60800,"composite":0.229,"zone":"fear","direction":"sell","confidence":0.71}
type KronosPrediction struct {
	Symbol     string  `json:"symbol"`
	Price      float64 `json:"price"`
	Composite  float64 `json:"composite"`
	Zone       string  `json:"zone"`
	Direction  string  `json:"direction"` // "buy" / "sell" / "hold"
	Confidence float64 `json:"confidence"`
}

// NewKronosClient probes the health-check endpoint with a short timeout.
// Returns nil if the service is unreachable — Kronos becomes a no-op overlay.
func NewKronosClient() *KronosClient {
	probe := &http.Client{Timeout: 5 * time.Second}
	resp, err := probe.Get(kronosBaseURL + "/")
	if err != nil {
		fmt.Println("⚠️ Kronos AI service not detected at :8765 — running without S6 overlay.")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("⚠️ Kronos AI health check returned %d — running without S6 overlay.\n", resp.StatusCode)
		return nil
	}

	fmt.Println("🤖 Kronos AI service detected at :8765 — S6 overlay active.")
	return &KronosClient{
		BaseURL:    kronosBaseURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// kronosSymbol strips the "USDT" suffix — the service expects bare tickers
// like "BTC", "ETH", "SOL" rather than Bybit's "BTCUSDT" pairs.
func kronosSymbol(symbol string) string {
	return strings.TrimSuffix(symbol, "USDT")
}

// FetchPrediction calls GET /predict/{symbol} for a single asset.
func (k *KronosClient) FetchPrediction(symbol string) (*KronosPrediction, error) {
	if k == nil {
		return nil, fmt.Errorf("kronos client unavailable")
	}

	url := fmt.Sprintf("%s/predict/%s", k.BaseURL, kronosSymbol(symbol))
	resp, err := k.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("kronos predict request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kronos predict returned status %d", resp.StatusCode)
	}

	var pred KronosPrediction
	if err := json.NewDecoder(resp.Body).Decode(&pred); err != nil {
		return nil, fmt.Errorf("kronos predict decode: %w", err)
	}
	return &pred, nil
}

// FetchBatchPredictions calls POST /predict_batch with bare ticker symbols
// and returns a map keyed by the ORIGINAL (Bybit-format) symbol so callers
// don't need to do their own suffix bookkeeping.
func (k *KronosClient) FetchBatchPredictions(symbols []string) (map[string]KronosPrediction, error) {
	if k == nil {
		return nil, fmt.Errorf("kronos client unavailable")
	}

	bare := make([]string, len(symbols))
	bareToFull := make(map[string]string, len(symbols))
	for i, s := range symbols {
		b := kronosSymbol(s)
		bare[i] = b
		bareToFull[b] = s
	}

	payload, err := json.Marshal(map[string][]string{"symbols": bare})
	if err != nil {
		return nil, fmt.Errorf("kronos batch marshal: %w", err)
	}

	resp, err := k.HTTPClient.Post(k.BaseURL+"/predict_batch", "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("kronos batch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kronos batch returned status %d", resp.StatusCode)
	}

	var preds []KronosPrediction
	if err := json.NewDecoder(resp.Body).Decode(&preds); err != nil {
		return nil, fmt.Errorf("kronos batch decode: %w", err)
	}

	out := make(map[string]KronosPrediction, len(preds))
	for _, p := range preds {
		full, ok := bareToFull[p.Symbol]
		if !ok {
			full = p.Symbol + "USDT"
		}
		out[full] = p
	}
	return out, nil
}
