package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
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

// ─── Feature 4: Cross-cycle portfolio risk ───────────────────────────────

// LivePortfolioRiskPct returns the SUM of open risk across every live linear
// position, as a fraction of totalCapital.
//
// WHY THIS EXISTS
// Hermes is not a daemon. run-bot.sh invokes `timeout 120 ./hermes-bot` on a
// cron, so every cycle is a FRESH PROCESS and every package-level variable
// starts at its zero value. globalPortfolioRiskPct was reset to 0.0 at the top
// of each run and incremented only by trades that run placed, which made the
// portfolio cap blind to every position still open from earlier cycles: five
// positions carried in from previous runs plus a full cap's worth approved
// this run is roughly double the stated ceiling. Seeding from this function
// instead of from zero is what makes the cap mean what it says.
//
// Risk per position is the distance from average entry to the stop actually
// set ON THE EXCHANGE, times size — the dollars lost if that stop fills. A
// position with NO stop set is charged its FULL position value: it can in
// principle lose everything, and an unprotected position must never get a
// free pass in the risk sum.
//
// Bybit V5 GET /v5/position/list?category=linear&settleCoin=USDT returns
// result.list[] with all numbers as STRINGS:
//
//	{"retCode":0,"retMsg":"OK","result":{"list":[{
//	   "symbol":"NEARUSDT",
//	   "side":"Buy",              // position direction
//	   "size":"5.9",              // contracts held
//	   "avgPrice":"2.4353",       // average entry
//	   "positionValue":"14.368",  // size * avgPrice, quoted by the exchange
//	   "stopLoss":"2.0889",       // "" or "0" when no stop is set
//	   "takeProfit":"3.3063",
//	   "unrealisedPnl":"-0.12",
//	   "leverage":"3"
//	}]}}
func LivePortfolioRiskPct(client *BybitClient, totalCapital float64) (float64, error) {
	if totalCapital <= 0 {
		return 0, fmt.Errorf("total capital is %.2f — cannot compute a risk fraction", totalCapital)
	}

	respBytes, err := client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		return 0, fmt.Errorf("position list fetch: %w", err)
	}
	return portfolioRiskFromPositions(respBytes, totalCapital)
}

// portfolioRiskFromPositions is the parsing and arithmetic half of
// LivePortfolioRiskPct, split out so it can be tested against recorded Bybit
// payloads without a network round trip or account credentials.
func portfolioRiskFromPositions(respBytes []byte, totalCapital float64) (float64, error) {
	if totalCapital <= 0 {
		return 0, fmt.Errorf("total capital is %.2f — cannot compute a risk fraction", totalCapital)
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol        string `json:"symbol"`
				Side          string `json:"side"`
				Size          string `json:"size"`
				AvgPrice      string `json:"avgPrice"`
				PositionValue string `json:"positionValue"`
				StopLoss      string `json:"stopLoss"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, fmt.Errorf("parse position list: %w", err)
	}
	if resp.RetCode != 0 {
		return 0, fmt.Errorf("bybit error [%d]: %s", resp.RetCode, resp.RetMsg)
	}

	totalRiskUSD := 0.0
	counted := 0
	for _, pos := range resp.Result.List {
		size, _ := strconv.ParseFloat(pos.Size, 64)
		if size <= 0 {
			continue // flat rows are returned with size "0"
		}
		avgPrice, _ := strconv.ParseFloat(pos.AvgPrice, 64)
		if avgPrice <= 0 {
			continue
		}
		counted++

		// Prefer the exchange's own positionValue; fall back to size * entry.
		notional, _ := strconv.ParseFloat(pos.PositionValue, 64)
		if notional <= 0 {
			notional = avgPrice * size
		}

		// Bybit returns "" or "0" for an unset stop loss.
		slPrice, slErr := strconv.ParseFloat(pos.StopLoss, 64)
		if slErr != nil || slPrice <= 0 {
			totalRiskUSD += notional
			fmt.Printf("   ⚠️ %s has NO stop loss — counting full position value $%.2f as at-risk\n",
				pos.Symbol, notional)
			continue
		}

		// A stop on the wrong side of entry offers no protection.
		var adverse float64
		switch pos.Side {
		case "Buy":
			adverse = avgPrice - slPrice
		case "Sell":
			adverse = slPrice - avgPrice
		default:
			adverse = math.Abs(avgPrice - slPrice)
		}
		if adverse <= 0 {
			totalRiskUSD += notional
			fmt.Printf("   ⚠️ %s stop $%.6f is not protective vs entry $%.6f — counting full value $%.2f\n",
				pos.Symbol, slPrice, avgPrice, notional)
			continue
		}

		risk := adverse * size
		if risk > notional {
			risk = notional // cannot lose more than the position is worth
		}
		totalRiskUSD += risk
		fmt.Printf("   • %s %s: entry $%.6f stop $%.6f size %.6f → risk $%.4f\n",
			pos.Symbol, pos.Side, avgPrice, slPrice, size, risk)
	}

	pct := totalRiskUSD / totalCapital
	fmt.Printf("   📊 Live open risk across %d position(s): $%.4f = %.2f%% of $%.2f equity\n",
		counted, totalRiskUSD, pct*100, totalCapital)
	return pct, nil
}
