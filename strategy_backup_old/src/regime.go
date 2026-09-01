// File location: ~/hermes-trading-bot/src/regime.go
// Hermes Trading Bot — Phase 2: Market Regime Classifier
// Uses 14-period daily ADX to classify the market as TRENDING, RANGING, or MIXED.

package main

// MarketRegime is an enum type describing the character of the market.
type MarketRegime int

const (
	REGIME_TRENDING MarketRegime = iota
	REGIME_RANGING
	REGIME_MIXED
)

// String returns a human-readable label for the regime.
func (r MarketRegime) String() string {
	switch r {
	case REGIME_TRENDING:
		return "TRENDING"
	case REGIME_RANGING:
		return "RANGING"
	case REGIME_MIXED:
		return "MIXED"
	default:
		return "UNKNOWN"
	}
}

// ClassifyRegime maps an ADX(14) value to a MarketRegime.
//
//	ADX > 25  → TRENDING (strong directional move)
//	ADX < 20  → RANGING  (low conviction chop)
//	else      → MIXED    (transition zone)
func ClassifyRegime(adx14 float64) MarketRegime {
	switch {
	case adx14 > 25:
		return REGIME_TRENDING
	case adx14 < 20:
		return REGIME_RANGING
	default:
		return REGIME_MIXED
	}
}