package main



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
