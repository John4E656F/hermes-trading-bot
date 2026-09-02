package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestDumpIndicatorsForPortCheck prints the Go indicator values for a fixed
// candle set so the Python ports in analysis/indicators.py can be diffed
// against them numerically. A backtest built on a drifted port measures a
// different system than the one running live.
func TestDumpIndicatorsForPortCheck(t *testing.T) {
	path := os.Getenv("CANDLES_JSON")
	if path == "" {
		t.Skip("set CANDLES_JSON to run the port check")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Ts                          int64   `json:"ts"`
		Open, High, Low, Close, Vol float64 `json:"-"`
		O                           float64 `json:"open"`
		H                           float64 `json:"high"`
		L                           float64 `json:"low"`
		C                           float64 `json:"close"`
		V                           float64 `json:"volume"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	candles := make([]Candle, len(rows))
	for i, r := range rows {
		candles[i] = Candle{
			Timestamp: time.UnixMilli(r.Ts).UTC(),
			Open:      r.O, High: r.H, Low: r.L, Close: r.C, Volume: r.V,
		}
	}

	bb := CalculateBollingerBands(candles, 20, 2.0)
	vp := ComputeVolumeProfile(candles, 30)
	out := map[string]float64{
		"ema20":     CalculateEMA(candles, 20),
		"sma50":     CalculateSMA(candles, 50),
		"rsi14":     CalculateRSI(candles, 14),
		"atr14":     CalculateATR(candles, 14),
		"adx14":     CalculateADX(candles, 14),
		"williams":  CalculateWilliamsR(candles, 14),
		"vwap20":    CalculateVWAP(candles, 20),
		"bb_upper":  bb.Upper,
		"bb_basis":  bb.Basis,
		"bb_lower":  bb.Lower,
		"vol_ma20":  CalculateVolumeMA(candles, 20),
		"vp_poc":    vp.POC,
		"vp_vah":    vp.VAH,
		"vp_val":    vp.VAL,
		"gain7d":    Compute7DayGain(candles),
	}
	b, _ := json.Marshal(out)
	fmt.Println("PORTCHECK " + string(b))
}
