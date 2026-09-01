package main

import "time"

type Candle struct {
	Timestamp time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

type BollingerBands struct {
	Upper float64
	Basis float64
	Lower float64
}

type Indicators struct {
	EMA20     float64
	SMA50     float64
	SMA200    float64
	RSI14     float64
	ATR14     float64
	ADX14     float64
	WilliamsR float64 // -100 (oversold) to 0 (overbought); faster than RSI
	VWAP20    float64 // 20-period volume-weighted average price
	BBands    BollingerBands
	BBWidth   float64 // (Upper-Lower)/Basis × 100 — squeeze when < 4%
}

type TimeframeSnapshot struct {
	Interval   string
	Candles    []Candle
	Indicators Indicators
}

// ── Volume Profile ──
type VolumeProfile struct {
	POC         float64 // Point of Control — highest volume price
	VAH         float64 // Value Area High
	VAL         float64 // Value Area Low
	TotalVolume float64
}

// ── Open Interest ──
type OIDataPoint struct {
	OpenInterest float64
	Timestamp    int64
}
type OISnapshot struct {
	Current   float64
	Change24h float64 // percentage
	IsSpike   bool    // |Change24h| > 8%
}

// ── Funding Rate ──
type FundingDataPoint struct {
	FundingRate float64
	Timestamp   int64
}
type FundingSnapshot struct {
	CurrentRate float64
	IsNegative  bool // rate < -0.02% (extreme — shorts paying a lot)
	IsPositive  bool // rate > 0.05% (extreme — longs paying a lot)
	IsExtreme   bool
}

// ── Consolidation ──
type ConsolidationState struct {
	IsConsolidating bool
	RangeHigh       float64
	RangeLow        float64
	RangePct        float64
	DurationDays    int
}

type SignalAction string

const (
	ACTION_BUY  SignalAction = "BUY"
	ACTION_SELL SignalAction = "SELL"
	ACTION_HOLD SignalAction = "HOLD"
)

// ── Per-Strategy Sub-Signals ──
type S0Signal struct { Active bool; Action SignalAction; Reason string }
type S1Signal struct { Active bool; Action SignalAction; Reason string; Proximity float64 }
type S2Signal struct { Active bool; Action SignalAction; Reason string; SqueezeType string }
type S3Signal struct { Active bool; Action SignalAction; Reason string; Primed bool }
// S4: Funding Rate Contrarian — LEADING signal. Extreme funding predicts reversals.
type S4Signal struct { Active bool; Action SignalAction; Reason string; FundingRate float64 }
// S5: Bollinger Band Squeeze Breakout — energy-release signal after compression.
type S5Signal struct { Active bool; Action SignalAction; Reason string; BBWidthPct float64 }

type AssetSnapshot struct {
	Symbol        string
	CurrentPrice  float64
	Snap4h        TimeframeSnapshot
	Snap1d        TimeframeSnapshot
	VP            VolumeProfile
	OI            OISnapshot
	Funding       FundingSnapshot
	Consolidation ConsolidationState
}

// MarketData is the top-level result containing every tracked asset.
type MarketData struct {
	Assets      map[string]*AssetSnapshot
	FetchedAt   time.Time
	LiveBalance float64 // Phase 3: live wallet balance pulled from Bybit
}
