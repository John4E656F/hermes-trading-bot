package main

import (
	"fmt"
	"math"
	"time"
)

// MarketAnalysis is a cross-asset snapshot computed once per cycle before any
// signal evaluation. It answers: "is this a good environment to trade right now?"
type MarketAnalysis struct {
	// Funding landscape — how many assets are crowded in each direction
	ExtremeNegFunding int    // shorts paying hard (funding < -0.04%)
	ExtremePossFunding int    // longs paying hard (funding > +0.05%)
	NeutralFunding    int
	FundingBias       string // "SHORTS_CROWDED" | "LONGS_CROWDED" | "BALANCED"

	// Crowded-trade warning: when >40% of watchlist has same extreme funding,
	// the signal is market-wide rather than symbol-specific — worse timing for S4.
	FundingCrowded     bool
	CrowdedDirection   string // which direction is crowded

	// Volatility regime — how many assets are in BB compression
	CompressedCount int    // BB Width < 3%
	ElevatedCount   int    // BB Width > 8%
	VolRegime       string // "COMPRESSED" | "NORMAL" | "ELEVATED"

	// Trend strength across watchlist
	TrendingCount int // ADX > 25
	RangingCount  int
	MarketMode    string // "TRENDING" | "RANGING" | "MIXED"

	// OI breadth
	OISpikeCount int

	// Funding settlement proximity (Bybit settles every 8H: 00:00, 08:00, 16:00 UTC)
	MinutesToSettlement int
	NearSettlement      bool   // < 45 min → peak squeeze pressure
	SettlementBoost     string // "STRONG" | "MODERATE" | "WEAK"

	// Tradability summary
	S4Favorable     bool
	ConditionReason string
}

// AnalyzeMarket computes a cross-asset market analysis from ingested data.
// Called once after data ingestion, before signal evaluation.
func AnalyzeMarket(data MarketData, watchlist []string) MarketAnalysis {
	ma := MarketAnalysis{}
	total := 0

	for _, sym := range watchlist {
		asset, ok := data.Assets[sym]
		if !ok {
			continue
		}
		total++

		// Funding landscape
		rate := asset.Funding.CurrentRate
		if rate < -0.0004 { // -0.04%/8h — strong negative (shorts paying a lot)
			ma.ExtremeNegFunding++
		} else if rate > 0.0005 { // +0.05%/8h — strong positive
			ma.ExtremePossFunding++
		} else {
			ma.NeutralFunding++
		}

		// Volatility
		bw := asset.Snap4h.Indicators.BBWidth
		if bw > 0 && bw < 3.0 {
			ma.CompressedCount++
		} else if bw > 8.0 {
			ma.ElevatedCount++
		}

		// Trend
		adx := asset.Snap1d.Indicators.ADX14
		if adx > 25 {
			ma.TrendingCount++
		} else {
			ma.RangingCount++
		}

		// OI
		if asset.OI.IsSpike {
			ma.OISpikeCount++
		}
	}

	// Funding bias
	switch {
	case ma.ExtremeNegFunding > ma.ExtremePossFunding:
		ma.FundingBias = "SHORTS_CROWDED"
	case ma.ExtremePossFunding > ma.ExtremeNegFunding:
		ma.FundingBias = "LONGS_CROWDED"
	default:
		ma.FundingBias = "BALANCED"
	}

	// Crowded-trade warning: if >40% of watchlist has same extreme funding,
	// the opportunity is market-wide (macro driven) not asset-specific.
	// S4 contrarian signals have worse individual timing in this regime.
	if total > 0 {
		negRatio := float64(ma.ExtremeNegFunding) / float64(total)
		posRatio := float64(ma.ExtremePossFunding) / float64(total)
		if negRatio > 0.40 {
			ma.FundingCrowded = true
			ma.CrowdedDirection = "SELL"
		} else if posRatio > 0.40 {
			ma.FundingCrowded = true
			ma.CrowdedDirection = "BUY"
		}
	}

	// Volatility regime
	switch {
	case total > 0 && float64(ma.CompressedCount)/float64(total) > 0.5:
		ma.VolRegime = "COMPRESSED"
	case total > 0 && float64(ma.ElevatedCount)/float64(total) > 0.3:
		ma.VolRegime = "ELEVATED"
	default:
		ma.VolRegime = "NORMAL"
	}

	// Market mode
	switch {
	case total > 0 && float64(ma.TrendingCount)/float64(total) > 0.6:
		ma.MarketMode = "TRENDING"
	case total > 0 && float64(ma.RangingCount)/float64(total) > 0.6:
		ma.MarketMode = "RANGING"
	default:
		ma.MarketMode = "MIXED"
	}

	// Funding settlement proximity
	ma.MinutesToSettlement, ma.NearSettlement = minutesToNextSettlement()
	switch {
	case ma.MinutesToSettlement <= 45:
		ma.SettlementBoost = "STRONG" // peak pressure window
	case ma.MinutesToSettlement <= 120:
		ma.SettlementBoost = "MODERATE"
	default:
		ma.SettlementBoost = "WEAK"
	}

	// S4 favorability: good when there's genuine crowding in one direction
	// but NOT market-wide crowding (which means macro, not squeeze)
	hasExtreme := ma.ExtremeNegFunding > 0 || ma.ExtremePossFunding > 0
	ma.S4Favorable = hasExtreme && !ma.FundingCrowded

	switch {
	case !hasExtreme:
		ma.ConditionReason = "No extreme funding detected — S4 likely idle this cycle"
	case ma.FundingCrowded:
		ma.ConditionReason = fmt.Sprintf(
			"⚠️ CROWDED: %d/%d assets with extreme %s funding — macro-driven, not squeeze-specific",
			int(math.Max(float64(ma.ExtremeNegFunding), float64(ma.ExtremePossFunding))), total, ma.FundingBias)
	default:
		ma.ConditionReason = fmt.Sprintf(
			"✅ Selective extremes: %d neg / %d pos funding — individual squeeze setups valid",
			ma.ExtremeNegFunding, ma.ExtremePossFunding)
	}

	return ma
}

// minutesToNextSettlement returns minutes until the next Bybit funding settlement
// and whether we're in the near-settlement window.
// Bybit perpetuals settle every 8H: 00:00, 08:00, 16:00 UTC.
func minutesToNextSettlement() (int, bool) {
	now := time.Now().UTC()
	hourOfDay := now.Hour()
	minuteOfHour := now.Minute()
	totalMinutes := hourOfDay*60 + minuteOfHour

	// Settlement hours: 0, 8, 16 → in minutes: 0, 480, 960
	settlements := []int{0, 480, 960, 1440} // 1440 = next day 00:00
	minDist := 9999
	for _, s := range settlements {
		d := s - totalMinutes
		if d < 0 {
			d += 1440
		}
		if d < minDist {
			minDist = d
		}
	}
	return minDist, minDist <= 45
}

// PrintMarketAnalysis prints a formatted market conditions table.
func PrintMarketAnalysis(ma MarketAnalysis, total int) {
	fmt.Println("\n╔═════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                    MARKET CONDITIONS ANALYSIS                   ║")
	fmt.Println("╠═════════════════════════════════════════════════════════════════╣")

	// Funding landscape
	fmt.Printf("║  FUNDING LANDSCAPE  %-46s║\n", "")
	fmt.Printf("║  Extreme neg (shorts crowded): %-3d  Extreme pos (longs crowded): %-3d  ║\n",
		ma.ExtremeNegFunding, ma.ExtremePossFunding)
	fmt.Printf("║  Bias: %-15s  Vol Regime: %-10s  Mode: %-8s  ║\n",
		ma.FundingBias, ma.VolRegime, ma.MarketMode)

	// Settlement timing
	settlementBar := fmt.Sprintf("%d min → %s pressure", ma.MinutesToSettlement, ma.SettlementBoost)
	if ma.NearSettlement {
		settlementBar = "⚡ " + settlementBar + " (PEAK WINDOW)"
	}
	fmt.Printf("║  Settlement: %-52s║\n", settlementBar)

	// OI
	fmt.Printf("║  OI Spikes: %-3d  Compressed Assets: %-3d  Trending: %-3d  Ranging: %-3d  ║\n",
		ma.OISpikeCount, ma.CompressedCount, ma.TrendingCount, ma.RangingCount)

	fmt.Println("╠═════════════════════════════════════════════════════════════════╣")

	// Crowded warning
	if ma.FundingCrowded {
		fmt.Printf("║  ⚠️  CROWDED MARKET: Too many assets with same extreme funding.       ║\n")
		fmt.Printf("║     S4 signals are macro-driven this cycle — reduced edge.           ║\n")
	} else if ma.S4Favorable {
		fmt.Printf("║  ✅ SELECTIVE CONDITIONS: Funding extremes are asset-specific.        ║\n")
		fmt.Printf("║     S4 contrarian signals have higher edge this cycle.               ║\n")
	} else {
		fmt.Printf("║  ➖ NEUTRAL: No extreme funding detected — S4 likely silent.         ║\n")
	}

	fmt.Printf("║  %s\n", ma.ConditionReason)
	fmt.Println("╚═════════════════════════════════════════════════════════════════╝")
}
