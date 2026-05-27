package main

import (
	"fmt"
)

type SignalAction string

const (
	ACTION_BUY  SignalAction = "BUY"
	ACTION_SELL SignalAction = "SELL"
	ACTION_HOLD SignalAction = "HOLD"
)

type StrategySignal struct {
	Symbol   string
	Regime   MarketRegime
	Strategy string
	Action   SignalAction
	Reason   string
}

func EvaluateMarketSnapshot(asset *AssetSnapshot) StrategySignal {
	dailyADX := asset.Snap1d.Indicators.ADX14
	currentRegime := ClassifyRegime(dailyADX)

	snap4h := asset.Snap4h
	latestPrice := snap4h.Candles[len(snap4h.Candles)-1].Close
	ind := snap4h.Indicators

	signal := StrategySignal{
		Symbol: asset.Symbol,
		Regime: currentRegime,
	}

	switch currentRegime {
	case REGIME_TRENDING:
		signal.Strategy = "Trend-Following Momentum"

		// Volume profile validation: latest closed candle must have
		// adequate volume relative to the 20-MA average.
		// Threshold is ADX-dependent — very strong trends (ADX > 40)
		// need no volume confirmation; the trend itself is the signal.
		avgVol := CalculateVolumeMA(snap4h.Candles, 20)
		latestVol := snap4h.Candles[len(snap4h.Candles)-1].Volume
		volRatio := latestVol / avgVol
		var volOk bool
		var volMsg string
		if dailyADX > 40 {
			volOk = true
			volMsg = fmt.Sprintf("Strong trend (ADX %.0f>40) bypasses volume gate (ratio=%.2fx).", dailyADX, volRatio)
		} else {
			volOk = volRatio >= 1.5
			volMsg = fmt.Sprintf("4H volume ratio=%.2fx (needs 1.5x)", volRatio)
		}

		if latestPrice > ind.EMA20 && ind.SMA50 > ind.SMA200 && ind.RSI14 > 50 && volOk {
			signal.Action = ACTION_BUY
			signal.Reason = "Price above EMA20, Golden Cross intact, RSI strong. " + volMsg
		} else if latestPrice > ind.EMA20 && ind.SMA50 > ind.SMA200 && ind.RSI14 > 50 && !volOk {
			signal.Action = ACTION_HOLD
			signal.Reason = "Trend criteria met but " + volMsg + " below threshold."
		} else if latestPrice < ind.EMA20 || ind.RSI14 < 40 {
			signal.Action = ACTION_SELL
			signal.Reason = "Price broken below major 20 EMA threshold or momentum fully collapsed."
		} else {
			signal.Action = ACTION_HOLD
			signal.Reason = "Trend is active but asset is in macro consolidation phase."
		}

	case REGIME_RANGING:
		signal.Strategy = "Statistical Mean Reversion"
		if latestPrice <= ind.BBands.Lower || ind.RSI14 < 30 {
			signal.Action = ACTION_BUY
			signal.Reason = "Asset swept outer structural Bollinger Band or reached extreme statistical oversold levels."
		} else if latestPrice >= ind.BBands.Upper || ind.RSI14 > 70 {
			signal.Action = ACTION_SELL
			signal.Reason = "Asset tag reached upper Bollinger boundary or achieved standard exhaustion limits."
		} else {
			signal.Action = ACTION_HOLD
			signal.Reason = "Price trading safely within neutral distribution boundaries."
		}

	default:
		signal.Strategy = "Regime Neutral Filter"
		signal.Action = ACTION_HOLD
		signal.Reason = "ADX signals a mixed, low-conviction environment. Standing aside to protect core capital."
	}

	return signal
}
