package main

import "fmt"

func DetectConsolidation(candles1d []Candle, minDays int, maxRangePct float64) ConsolidationState {
	if len(candles1d) < minDays {
		return ConsolidationState{}
	}

	startIdx := len(candles1d) - minDays
	activeCandles := candles1d[startIdx:]

	highestHigh := activeCandles[0].High
	lowestLow := activeCandles[0].Low

	for _, c := range activeCandles {
		if c.High > highestHigh {
			highestHigh = c.High
		}
		if c.Low < lowestLow {
			lowestLow = c.Low
		}
	}

	midpoint := (highestHigh + lowestLow) / 2.0
	rangePct := 0.0
	if midpoint > 0 {
		rangePct = ((highestHigh - lowestLow) / midpoint) * 100.0
	}

	isConsolidating := rangePct <= maxRangePct

	return ConsolidationState{
		IsConsolidating: isConsolidating,
		RangeHigh:       highestHigh,
		RangeLow:        lowestLow,
		RangePct:        rangePct,
		DurationDays:    minDays,
	}
}

// EvaluateS5BBSqueeze detects Bollinger Band compression and fires when price
// breaks out of the squeeze with volume confirmation.
//
// The edge: low volatility (squeeze) always precedes high volatility (breakout).
// When BBands compress below 4% width AND price breaks a band with volume,
// the move tends to follow through because it represents stored energy releasing.
func EvaluateS5BBSqueeze(bb BollingerBands, latestPrice, latestVol, avgVol float64) S5Signal {
	sig := S5Signal{}

	if bb.Basis == 0 || avgVol == 0 {
		sig.Reason = "No BB/volume data for S5"
		return sig
	}

	bbWidthPct := ((bb.Upper - bb.Lower) / bb.Basis) * 100.0
	sig.BBWidthPct = bbWidthPct
	volConfirmed := latestVol >= avgVol*1.5

	if bbWidthPct >= 4.0 {
		sig.Reason = fmt.Sprintf("BB width %.2f%% — no squeeze active (need <4%%)", bbWidthPct)
		return sig
	}

	// Squeeze is active. Check for breakout.
	if latestPrice > bb.Upper && volConfirmed {
		sig.Active = true
		sig.Action = ACTION_BUY
		sig.Reason = fmt.Sprintf(
			"BB SQUEEZE BREAKOUT UP: width %.2f%%, price above upper band, vol %.1fx surge.",
			bbWidthPct, latestVol/avgVol)
	} else if latestPrice < bb.Lower && volConfirmed {
		sig.Active = true
		sig.Action = ACTION_SELL
		sig.Reason = fmt.Sprintf(
			"BB SQUEEZE BREAKDOWN: width %.2f%%, price below lower band, vol %.1fx surge.",
			bbWidthPct, latestVol/avgVol)
	} else {
		sig.Reason = fmt.Sprintf("BB SQUEEZE PRIMED: width %.2f%% — waiting for directional break.", bbWidthPct)
	}

	return sig
}

func EvaluateS3Breakout(price float64, consol ConsolidationState, latestVolume float64, avgVolume float64) S3Signal {
	sig := S3Signal{Active: false, Action: ACTION_HOLD, Reason: "No breakout setup detected", Primed: false}

	if !consol.IsConsolidating && consol.DurationDays > 0 {
		sig.Reason = "Asset is not in multi-week consolidation"
		return sig
	}

	if consol.IsConsolidating {
		sig.Primed = true
		sig.Reason = "Asset in tight consolidation. Waiting for breakout."

		volOk := latestVolume >= avgVolume*1.5

		if price > consol.RangeHigh {
			if volOk {
				sig.Active = true
				sig.Action = ACTION_BUY
				sig.Reason = "Bullish breakout from consolidation with volume confirmation."
			} else {
				sig.Reason = "Bullish breakout attempt without volume confirmation (potential fake-out)."
			}
		} else if price < consol.RangeLow {
			if volOk {
				sig.Active = true
				sig.Action = ACTION_SELL
				sig.Reason = "Bearish breakdown from consolidation with volume confirmation."
			} else {
				sig.Reason = "Bearish breakdown attempt without volume confirmation (potential fake-out)."
			}
		}
	}

	return sig
}
