package main

import "fmt"

// MACDPoint holds one bar's worth of MACD data.
type MACDPoint struct {
	MACD      float64
	Signal    float64
	Histogram float64
}

// CalculateEMAFromValues computes an EMA on a raw float64 slice.
// Used to derive the MACD signal line (EMA of the MACD line).
func CalculateEMAFromValues(values []float64, period int) []float64 {
	result := make([]float64, len(values))
	if len(values) < period {
		return result
	}
	mult := 2.0 / (float64(period) + 1.0)
	var sum float64
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	ema := sum / float64(period)
	result[period-1] = ema
	for i := period; i < len(values); i++ {
		ema = ((values[i] - ema) * mult) + ema
		result[i] = ema
	}
	return result
}

// CalculateMACDSeries builds a full MACD(12,26,9) series for the given candles.
// Returns nil when there is insufficient data.
func CalculateMACDSeries(candles []Candle) []MACDPoint {
	const fast, slow, sigPeriod = 12, 26, 9
	if len(candles) < slow+sigPeriod {
		return nil
	}

	fastEMA := CalculateEMASeries(candles, fast)
	slowEMA := CalculateEMASeries(candles, slow)

	// MACD line is valid from index (slow-1) onward.
	macdLine := make([]float64, len(candles))
	for i := slow - 1; i < len(candles); i++ {
		macdLine[i] = fastEMA[i] - slowEMA[i]
	}

	// Compute signal line as EMA(9) of the valid MACD segment.
	validSegment := macdLine[slow-1:]
	sigLine := CalculateEMAFromValues(validSegment, sigPeriod)

	// Build final result where the signal line is valid.
	var out []MACDPoint
	for i := sigPeriod - 1; i < len(validSegment); i++ {
		out = append(out, MACDPoint{
			MACD:      validSegment[i],
			Signal:    sigLine[i],
			Histogram: validSegment[i] - sigLine[i],
		})
	}
	return out
}

// EvaluateS4MACD detects MACD(12,26,9) crossovers on the 4H candles.
// Only fires on actual crossovers or zero-line crosses to keep signal quality high.
func EvaluateS4MACD(candles4h []Candle) S4Signal {
	sig := S4Signal{Active: false, Action: ACTION_HOLD, Reason: "No MACD crossover"}

	series := CalculateMACDSeries(candles4h)
	if len(series) < 3 {
		sig.Reason = "Insufficient candles for MACD(12,26,9)"
		return sig
	}

	n := len(series)
	cur := series[n-1]
	prev := series[n-2]

	goldenCross := prev.MACD <= prev.Signal && cur.MACD > cur.Signal
	deathCross := prev.MACD >= prev.Signal && cur.MACD < cur.Signal
	bullZero := prev.MACD <= 0 && cur.MACD > 0
	bearZero := prev.MACD >= 0 && cur.MACD < 0

	if goldenCross {
		sig.Active = true
		sig.Action = ACTION_BUY
		if bullZero {
			sig.CrossType = "GoldenCross+ZeroLine"
			sig.Reason = fmt.Sprintf("MACD golden cross AND zero-line cross — strong bullish shift. MACD=%.5f Sig=%.5f", cur.MACD, cur.Signal)
		} else {
			sig.CrossType = "GoldenCross"
			sig.Reason = fmt.Sprintf("MACD golden cross: MACD crossed above signal line. MACD=%.5f Sig=%.5f", cur.MACD, cur.Signal)
		}
	} else if deathCross {
		sig.Active = true
		sig.Action = ACTION_SELL
		if bearZero {
			sig.CrossType = "DeathCross+ZeroLine"
			sig.Reason = fmt.Sprintf("MACD death cross AND zero-line cross — strong bearish shift. MACD=%.5f Sig=%.5f", cur.MACD, cur.Signal)
		} else {
			sig.CrossType = "DeathCross"
			sig.Reason = fmt.Sprintf("MACD death cross: MACD crossed below signal line. MACD=%.5f Sig=%.5f", cur.MACD, cur.Signal)
		}
	} else if bullZero && !goldenCross {
		// MACD crossed zero but not signal — still bullish momentum signal
		sig.Active = true
		sig.Action = ACTION_BUY
		sig.CrossType = "ZeroLineBull"
		sig.Reason = fmt.Sprintf("MACD crossed above zero-line — momentum turning positive. MACD=%.5f", cur.MACD)
	} else if bearZero && !deathCross {
		sig.Active = true
		sig.Action = ACTION_SELL
		sig.CrossType = "ZeroLineBear"
		sig.Reason = fmt.Sprintf("MACD crossed below zero-line — momentum turning negative. MACD=%.5f", cur.MACD)
	}

	return sig
}
