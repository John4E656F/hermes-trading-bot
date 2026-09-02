package main

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ─── Feature 1: Max Position Guard ───────────────────────────────────────

// fetchOpenPositions queries Bybit for active linear positions whose symbols
// appear in our watchlist. Returns:
//   - count: total open positions (for the 5/5 freeze)
//   - openSymbols: map[symbol]side ("Buy"/"Sell") for per-symbol duplicate guard
func fetchOpenPositions(client *BybitClient, watchlist []string) (int, map[string]string, error) {
	respBytes, err := client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		return 0, nil, fmt.Errorf("position list http: %w", err)
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol string `json:"symbol"`
				Size   string `json:"size"`
				Side   string `json:"side"` // "Buy" or "Sell"
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, nil, fmt.Errorf("parse position list: %w", err)
	}
	if resp.RetCode != 0 {
		return 0, nil, fmt.Errorf("bybit error [%d]: %s", resp.RetCode, resp.RetMsg)
	}

	watchSet := make(map[string]bool, len(watchlist))
	for _, s := range watchlist {
		watchSet[s] = true
	}

	openSymbols := make(map[string]string)
	count := 0
	for _, pos := range resp.Result.List {
		if watchSet[pos.Symbol] && pos.Size != "0" && pos.Size != "" {
			count++
			openSymbols[pos.Symbol] = pos.Side
		}
	}
	return count, openSymbols, nil
}

// fetchOpenPositionCount is kept for call sites that only need the count.
func fetchOpenPositionCount(client *BybitClient, watchlist []string) (int, error) {
	count, _, err := fetchOpenPositions(client, watchlist)
	return count, err
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

// ─── Feature 3: Relative-Strength Ranking ───────────────────────────────
//
// NAMING NOTE: this was previously called "co-ranking" and documented as
// preventing correlated exposure. It does not do that and never did. It sorts
// candidates by trailing 7-day price change and keeps the strongest (or
// weakest) N. Two assets that move together — which is most of the crypto
// majors most of the time — will tend to have SIMILAR 7-day gains, so this
// sort actively selects for correlated names rather than against them.
//
// Real correlation control would need a return-correlation matrix across the
// candidate set and a cluster-exposure cap. That is deliberately not built
// here; this is only the honest label for the sort that exists.

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

// RankedSignal pairs a validated signal with its trailing 7-day price change
// so the relative-strength sort can put the strongest performers first.
type RankedSignal struct {
	Asset  *AssetSnapshot
	Signal StrategySignal
	Gain7D float64
}

// RankByRelativeStrength sorts signals from strongest to weakest 7-day price
// change and returns the top N. Use for BUY signals — highest gain first.
// This is a momentum/relative-strength filter. It provides no correlation
// protection.
func RankByRelativeStrength(signals []RankedSignal, max int) []RankedSignal {
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

// RankByRelativeWeakness sorts signals from weakest to strongest 7-day price
// change and returns the top N. Use for SELL signals — worst performers first.
// This is a momentum/relative-strength filter. It provides no correlation
// protection.
func RankByRelativeWeakness(signals []RankedSignal, max int) []RankedSignal {
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
