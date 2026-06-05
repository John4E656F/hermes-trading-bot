package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	kronosServiceURL = "http://localhost:8765"
	kronosTimeout    = 90 * time.Second // inference for 13 symbols can take ~60s on CPU
)

// kronosCandlePayload is what the Python service expects per candle.
type kronosCandlePayload struct {
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
	TS     int64   `json:"ts"` // unix milliseconds
}

// FetchKronosPredictions sends a batch request to the Kronos microservice and
// updates each AssetSnapshot in-place with the predicted price change.
// Silently no-ops if the service is unreachable — S6 stays inactive.
func FetchKronosPredictions(assets map[string]*AssetSnapshot) {
	type symbolReq struct {
		Symbol  string                `json:"symbol"`
		Candles []kronosCandlePayload `json:"candles"`
	}
	type batchReq struct {
		Requests []symbolReq `json:"requests"`
	}

	var reqs []symbolReq
	for sym, asset := range assets {
		candles := asset.Snap4h.Candles
		// Use last 90 candles — Kronos lookback window.
		if len(candles) > 90 {
			candles = candles[len(candles)-90:]
		}
		var cd []kronosCandlePayload
		for _, c := range candles {
			cd = append(cd, kronosCandlePayload{
				Open:   c.Open,
				High:   c.High,
				Low:    c.Low,
				Close:  c.Close,
				Volume: c.Volume,
				TS:     c.Timestamp.UnixMilli(),
			})
		}
		reqs = append(reqs, symbolReq{Symbol: sym, Candles: cd})
	}

	payload, err := json.Marshal(batchReq{Requests: reqs})
	if err != nil {
		fmt.Printf("⚠️ Kronos: failed to marshal request: %v\n", err)
		return
	}

	client := &http.Client{Timeout: kronosTimeout}
	resp, err := client.Post(
		kronosServiceURL+"/predict_batch",
		"application/json",
		bytes.NewBuffer(payload),
	)
	if err != nil {
		// Service not running — skip S6 silently for this cycle.
		fmt.Printf("⚠️ Kronos service unreachable — S6 signals disabled this cycle.\n")
		return
	}
	defer resp.Body.Close()

	var result struct {
		Predictions map[string]struct {
			Direction  string  `json:"direction"`
			ChangePct  float64 `json:"change_pct"`
			Confidence float64 `json:"confidence"`
			Error      string  `json:"error,omitempty"`
		} `json:"predictions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("⚠️ Kronos: failed to decode response: %v\n", err)
		return
	}

	for sym, pred := range result.Predictions {
		asset, ok := assets[sym]
		if !ok {
			continue
		}
		if pred.Error != "" {
			continue // inference failed for this symbol — leave KronosPred = 0
		}
		asset.KronosPred = pred.ChangePct
		asset.KronosConf = pred.Confidence
	}

	fmt.Printf("🤖 Kronos predictions received for %d symbol(s).\n", len(result.Predictions))
}

// EvaluateS6Kronos converts the stored Kronos prediction into an S6Signal.
// A signal only fires when the predicted move exceeds the threshold AND
// confidence is meaningful — prevents noisy low-conviction forecasts.
func EvaluateS6Kronos(predictedChangePct, confidence float64) S6Signal {
	const (
		buyThreshold  = 1.5 // % predicted gain to generate BUY
		sellThreshold = 1.5 // % predicted loss to generate SELL
		minConfidence = 0.3 // minimum model confidence to act on
	)

	sig := S6Signal{
		Active:             false,
		Action:             ACTION_HOLD,
		PredictedChangePct: predictedChangePct,
		Confidence:         confidence,
	}

	if predictedChangePct == 0 && confidence == 0 {
		sig.Reason = "Kronos unavailable"
		return sig
	}

	if confidence < minConfidence {
		sig.Reason = fmt.Sprintf("Kronos low-confidence forecast (%.0f%% < 30%% threshold)", confidence*100)
		return sig
	}

	if predictedChangePct >= buyThreshold {
		sig.Active = true
		sig.Action = ACTION_BUY
		sig.Reason = fmt.Sprintf("Kronos AI: +%.2f%% forecast over next 12h (conf %.0f%%)",
			predictedChangePct, confidence*100)
	} else if predictedChangePct <= -sellThreshold {
		sig.Active = true
		sig.Action = ACTION_SELL
		sig.Reason = fmt.Sprintf("Kronos AI: %.2f%% forecast over next 12h (conf %.0f%%)",
			predictedChangePct, confidence*100)
	} else {
		sig.Reason = fmt.Sprintf("Kronos AI neutral: %.2f%% forecast (below ±%.1f%% threshold)",
			predictedChangePct, buyThreshold)
	}

	return sig
}
