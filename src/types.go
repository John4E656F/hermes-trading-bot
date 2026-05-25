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
	EMA20    float64
	SMA50    float64
	SMA200   float64
	RSI14    float64
	ATR14    float64
	ADX14    float64
	BBands   BollingerBands
}

type TimeframeSnapshot struct {
	Interval   string
	Candles    []Candle
	Indicators Indicators
}

type AssetSnapshot struct {
	Symbol       string
	CurrentPrice float64
	Snap4h       TimeframeSnapshot
	Snap1d       TimeframeSnapshot
}
// MarketData is the top-level result containing every tracked asset.
type MarketData struct {
	Assets      map[string]*AssetSnapshot
	FetchedAt   time.Time
	LiveBalance float64 // Phase 3: live wallet balance pulled from Bybit
}
