package main

import (
	"math"
)

// CalculateSMA calculates the Simple Moving Average of the last N closes
func CalculateSMA(candles []Candle, period int) float64 {
	if len(candles) < period {
		return 0.0
	}
	var sum float64
	startIdx := len(candles) - period
	for i := startIdx; i < len(candles); i++ {
		sum += candles[i].Close
	}
	return sum / float64(period)
}

// CalculateEMASeries returns EMA values for every candle in the slice.
// Indices before period-1 are zero (insufficient data to seed).
func CalculateEMASeries(candles []Candle, period int) []float64 {
	result := make([]float64, len(candles))
	if len(candles) < period {
		return result
	}
	multiplier := 2.0 / (float64(period) + 1.0)
	var sum float64
	for i := 0; i < period; i++ {
		sum += candles[i].Close
	}
	ema := sum / float64(period)
	result[period-1] = ema
	for i := period; i < len(candles); i++ {
		ema = ((candles[i].Close - ema) * multiplier) + ema
		result[i] = ema
	}
	return result
}

// CalculateEMA calculates the Exponential Moving Average of the last N closes
func CalculateEMA(candles []Candle, period int) float64 {
	if len(candles) < period {
		return 0.0
	}
	multiplier := 2.0 / (float64(period) + 1.0)
	
	// Seed with SMA of the first `period` candles
	currentEMA := CalculateSMA(candles[:period], period)
	
	for i := period; i < len(candles); i++ {
		currentEMA = ((candles[i].Close - currentEMA) * multiplier) + currentEMA
	}
	return currentEMA
}

// CalculateRSI calculates the Relative Strength Index using Wilder's smoothing
func CalculateRSI(candles []Candle, period int) float64 {
	if len(candles) < period+1 {
		return 50.0
	}

	var gains, losses float64
	for i := 1; i <= period; i++ {
		change := candles[i].Close - candles[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses -= change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	for i := period + 1; i < len(candles); i++ {
		change := candles[i].Close - candles[i-1].Close
		var currentGain, currentLoss float64
		if change > 0 {
			currentGain = change
		} else {
			currentLoss = -change
		}
		avgGain = ((avgGain * float64(period-1)) + currentGain) / float64(period)
		avgLoss = ((avgLoss * float64(period-1)) + currentLoss) / float64(period)
	}

	if avgLoss == 0 {
		return 100.0
	}
	rs := avgGain / avgLoss
	return 100.0 - (100.0 / (1.0 + rs))
}

// CalculateATR calculates the Average True Range
func CalculateATR(candles []Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0.0
	}

	var trSum float64
	for i := 1; i <= period; i++ {
		highLow := candles[i].High - candles[i].Low
		highClose := math.Abs(candles[i].High - candles[i-1].Close)
		lowClose := math.Abs(candles[i].Low - candles[i-1].Close)
		trSum += math.Max(highLow, math.Max(highClose, lowClose))
	}

	avgATR := trSum / float64(period)
	for i := period + 1; i < len(candles); i++ {
		highLow := candles[i].High - candles[i].Low
		highClose := math.Abs(candles[i].High - candles[i-1].Close)
		lowClose := math.Abs(candles[i].Low - candles[i-1].Close)
		tr := math.Max(highLow, math.Max(highClose, lowClose))
		avgATR = ((avgATR * float64(period-1)) + tr) / float64(period)
	}

	return avgATR
}

// ComputeAllIndicators populates standard baseline metrics
func ComputeAllIndicators(candles []Candle) Indicators {
	var ind Indicators
	if len(candles) == 0 {
		return ind
	}
	ind.EMA20 = CalculateEMA(candles, 20)
	ind.SMA50 = CalculateSMA(candles, 50)
	ind.SMA200 = CalculateSMA(candles, 200)
	ind.RSI14 = CalculateRSI(candles, 14)
	ind.ATR14 = CalculateATR(candles, 14)
	return ind
}

// CalculateBollingerBands computes the standard deviation bands around a 20 SMA
func CalculateBollingerBands(candles []Candle, period int, k float64) BollingerBands {
	if len(candles) < period {
		return BollingerBands{}
	}
	
	closes := make([]float64, period)
	var sum float64
	startIdx := len(candles) - period
	for i := 0; i < period; i++ {
		closes[i] = candles[startIdx+i].Close
		sum += closes[i]
	}
	basis := sum / float64(period)

	var varianceSum float64
	for _, cl := range closes {
		varianceSum += math.Pow(cl-basis, 2)
	}
	stdDev := math.Sqrt(varianceSum / float64(period))

	return BollingerBands{
		Upper: basis + (k * stdDev),
		Basis: basis,
		Lower: basis - (k * stdDev),
	}
}

// CalculateADX computes Average Directional Index using Wilder's smoothing technique
func CalculateADX(candles []Candle, period int) float64 {
	if len(candles) < (period * 2) {
		return 0.0 
	}

	n := len(candles)
	tr := make([]float64, n)
	plusDM := make([]float64, n)
	minusDM := make([]float64, n)

	for i := 1; i < n; i++ {
		upMove := candles[i].High - candles[i-1].High
		downMove := candles[i-1].Low - candles[i].Low

		if upMove > downMove && upMove > 0 {
			plusDM[i] = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM[i] = downMove
		}

		hL := candles[i].High - candles[i].Low
		hC := math.Abs(candles[i].High - candles[i-1].Close)
		lC := math.Abs(candles[i].Low - candles[i-1].Close)
		tr[i] = math.Max(hL, math.Max(hC, lC))
	}

	var sumTR, sumPlusDM, sumMinusDM float64
	for i := 1; i <= period; i++ {
		sumTR += tr[i]
		sumPlusDM += plusDM[i]
		sumMinusDM += minusDM[i]
	}

	smoothTR := make([]float64, n)
	smoothPlusDM := make([]float64, n)
	smoothMinusDM := make([]float64, n)

	smoothTR[period] = sumTR
	smoothPlusDM[period] = sumPlusDM
	smoothMinusDM[period] = sumMinusDM

	for i := period + 1; i < n; i++ {
		smoothTR[i] = smoothTR[i-1] - (smoothTR[i-1] / float64(period)) + tr[i]
		smoothPlusDM[i] = smoothPlusDM[i-1] - (smoothPlusDM[i-1] / float64(period)) + plusDM[i]
		smoothMinusDM[i] = smoothMinusDM[i-1] - (smoothMinusDM[i-1] / float64(period)) + minusDM[i]
	}

	dx := make([]float64, n)
	for i := period; i < n; i++ {
		if smoothTR[i] == 0 {
			continue
		}
		diPlus := (smoothPlusDM[i] / smoothTR[i]) * 100
		diMinus := (smoothMinusDM[i] / smoothTR[i]) * 100

		diff := math.Abs(diPlus - diMinus)
		sum := diPlus + diMinus
		if sum != 0 {
			dx[i] = (diff / sum) * 100
		}
	}

	var dxSum float64
	for i := period; i < period*2; i++ {
		dxSum += dx[i]
	}
	
	currentADX := dxSum / float64(period)
	for i := period * 2; i < n; i++ {
		currentADX = (currentADX*float64(period-1) + dx[i]) / float64(period)
	}

	return currentADX
}
