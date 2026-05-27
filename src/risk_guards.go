package main

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ─── Feature 1: Max Position Guard ───────────────────────────────────────

// fetchOpenPositionCount queries Bybit for active linear positions whose
// symbols appear in our watchlist.  Returns the count so the caller can
// enforce the max-concurrent-exposure freeze (5/5).
func fetchOpenPositionCount(client *BybitClient, watchlist []string) (int, error) {
	respBytes, err := client.GetPrivateRequest("/v5/position/list?category=linear")
	if err != nil {
		return 0, fmt.Errorf("position list http: %w", err)
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol string `json:"symbol"`
				Size   string `json:"size"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, fmt.Errorf("parse position list: %w", err)
	}
	if resp.RetCode != 0 {
		return 0, fmt.Errorf("bybit error [%d]: %s", resp.RetCode, resp.RetMsg)
	}

	// Build a set for O(1) lookup
	watchSet := make(map[string]bool, len(watchlist))
	for _, s := range watchlist {
		watchSet[s] = true
	}

	count := 0
	for _, pos := range resp.Result.List {
		// FIXED: Check for both "0" and empty string "" to accurately count active positions
		if watchSet[pos.Symbol] && pos.Size != "0" && pos.Size != "" {
			count++
		}
	}
	return count, nil
}

// ─── Feature 2: Volume Profile Validation (helper) ───────────────────────

// CalculateVolumeMA computes the simple moving average of volume over the
// last `period` candles, EXCLUDING the very latest (still-forming) candle.
// This gives us a baseline to compare the most recent closed candle against.
func CalculateVolumeMA(candles []Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}
	// Use candles[len(candles)-period-1 … len(candles)-2] — the last
	// `period` fully-closed candles before the current one.
	sum := 0.0
	start := len(candles) - period - 1
	end := len(candles) - 1
	for i := start; i < end; i++ {
		sum += candles[i].Volume
	}
	return sum / float64(period)
}

// ─── Feature 3: Relative Strength Co-ranking ────────────────────────────

// Compute7DayGain returns the percentage price change over the trailing 7
// daily candles.  Positive means the asset has been rising.
func Compute7DayGain(candles1d []Candle) float64 {
	if len(candles1d) < 8 {
		return 0
	}
	old := candles1d[len(candles1d)-8].Close
	now := candles1d[len(candles1d)-1].Close
	if old == 0 {
		return 0
	}
	return (now - old) / old * 100.0
}

// RankedSignal pairs a validated signal with its trailing 7-day strength
// so the co-ranking sort can put the strongest performers first.
type RankedSignal struct {
	Asset  *AssetSnapshot
	Signal StrategySignal
	Gain7D float64
}

// RankSignalsByGain sorts signals from strongest to weakest 7-day gain and
// returns the top N.  Use for BUY signals — highest gain first.
func RankSignalsByGain(signals []RankedSignal, max int) []RankedSignal {
	if len(signals) <= max {
		sort.Slice(signals, func(i, j int) bool {
			return signals[i].Gain7D > signals[j].Gain7D
		})
		return signals
	}

	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Gain7D > signals[j].Gain7D
	})
	return signals[:max]
}

// RankSignalsByLowestGain sorts signals from weakest to strongest 7-day gain
// and returns the top N.  Use for SELL signals — worst performers first.
func RankSignalsByLowestGain(signals []RankedSignal, max int) []RankedSignal {
	if len(signals) <= max {
		sort.Slice(signals, func(i, j int) bool {
			return signals[i].Gain7D < signals[j].Gain7D
		})
		return signals
	}

	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Gain7D < signals[j].Gain7D
	})
	return signals[:max]
}
