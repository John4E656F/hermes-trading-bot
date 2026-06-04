package main

// BTCRegime describes the macro market environment inferred from BTC's daily structure.
type BTCRegime int

const (
	BTCNeutral BTCRegime = iota
	BTCBull
	BTCBear
)

func (r BTCRegime) String() string {
	switch r {
	case BTCBull:
		return "BULL"
	case BTCBear:
		return "BEAR"
	default:
		return "NEUTRAL"
	}
}

// BTCRegimeLabel returns an emoji-annotated label for dashboard display.
func BTCRegimeLabel(r BTCRegime) string {
	switch r {
	case BTCBull:
		return "🟢 BTC MACRO BULL"
	case BTCBear:
		return "🔴 BTC MACRO BEAR"
	default:
		return "🟡 BTC MACRO NEUTRAL"
	}
}

// ComputeBTCRegime classifies BTC's macro regime using its daily price structure.
// Scores 4 independent signals — 3+ positive → Bull, 1 or fewer → Bear.
func ComputeBTCRegime(btc *AssetSnapshot) BTCRegime {
	if btc == nil {
		return BTCNeutral
	}
	ind := btc.Snap1d.Indicators
	price := btc.CurrentPrice

	score := 0
	if price > ind.EMA20 {
		score++
	}
	if price > ind.SMA50 {
		score++
	}
	if ind.RSI14 > 52 {
		score++
	}
	if ind.ADX14 > 20 {
		score++
	}

	switch {
	case score >= 3:
		return BTCBull
	case score <= 1:
		return BTCBear
	default:
		return BTCNeutral
	}
}
