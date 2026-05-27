package main

import (
	"math"
)

func ComputeVolumeProfile(candles []Candle, lookback int) VolumeProfile {
	if len(candles) == 0 {
		return VolumeProfile{}
	}

	startIdx := len(candles) - lookback
	if startIdx < 0 {
		startIdx = 0
	}
	activeCandles := candles[startIdx:]
	if len(activeCandles) == 0 {
		return VolumeProfile{}
	}

	minPrice := activeCandles[0].Low
	maxPrice := activeCandles[0].High
	totalVol := 0.0

	for _, c := range activeCandles {
		if c.Low < minPrice {
			minPrice = c.Low
		}
		if c.High > maxPrice {
			maxPrice = c.High
		}
		totalVol += c.Volume
	}

	if maxPrice == minPrice {
		return VolumeProfile{
			POC:         minPrice,
			VAH:         minPrice,
			VAL:         minPrice,
			TotalVolume: totalVol,
		}
	}

	numBins := 50
	binSize := (maxPrice - minPrice) / float64(numBins)
	bins := make([]float64, numBins)

	for _, c := range activeCandles {
		lowBin := int((c.Low - minPrice) / binSize)
		highBin := int((c.High - minPrice) / binSize)
		if lowBin >= numBins { lowBin = numBins - 1 }
		if highBin >= numBins { highBin = numBins - 1 }
		if lowBin < 0 { lowBin = 0 }

		binsSpanned := highBin - lowBin + 1
		volPerBin := c.Volume / float64(binsSpanned)
		for i := lowBin; i <= highBin; i++ {
			bins[i] += volPerBin
		}
	}

	pocBin := 0
	maxBinVol := 0.0
	for i, v := range bins {
		if v > maxBinVol {
			maxBinVol = v
			pocBin = i
		}
	}

	pocPrice := minPrice + (float64(pocBin)+0.5)*binSize

	// 70% value area
	targetVol := totalVol * 0.70
	currentVol := bins[pocBin]
	lowIdx := pocBin
	highIdx := pocBin

	for currentVol < targetVol && (lowIdx > 0 || highIdx < numBins-1) {
		volBelow := 0.0
		if lowIdx > 0 {
			volBelow = bins[lowIdx-1]
		}
		volAbove := 0.0
		if highIdx < numBins-1 {
			volAbove = bins[highIdx+1]
		}

		if volBelow > volAbove {
			lowIdx--
			currentVol += volBelow
		} else if volAbove > volBelow {
			highIdx++
			currentVol += volAbove
		} else {
			if lowIdx > 0 { lowIdx-- ; currentVol += bins[lowIdx] }
			if highIdx < numBins-1 { highIdx++ ; currentVol += bins[highIdx] }
		}
	}

	val := minPrice + float64(lowIdx)*binSize
	vah := minPrice + float64(highIdx+1)*binSize

	return VolumeProfile{
		POC:         pocPrice,
		VAH:         vah,
		VAL:         val,
		TotalVolume: totalVol,
	}
}

func EvaluateS1MeanReversion(price float64, vp VolumeProfile) S1Signal {
	sig := S1Signal{Active: false, Action: ACTION_HOLD, Reason: "Price within Value Area"}
	
	if vp.TotalVolume == 0 {
		sig.Reason = "No volume data for S1"
		return sig
	}

	if price <= vp.VAL {
		sig.Active = true
		sig.Action = ACTION_BUY
		sig.Reason = "Price at or below Value Area Low (VAL). Mean reversion to POC expected."
		// Prox: how far below VAL relative to VAL-POC dist (simplified)
		sig.Proximity = math.Abs(vp.VAL - price) / price
	} else if price >= vp.VAH {
		sig.Active = true
		sig.Action = ACTION_SELL
		sig.Reason = "Price at or above Value Area High (VAH). Mean reversion to POC expected."
		sig.Proximity = math.Abs(price - vp.VAH) / price
	}

	return sig
}
