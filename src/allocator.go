package main

import "fmt"

type StrategySignal struct {
	Symbol     string
	Regime     MarketRegime
	Strategy   string
	Action     SignalAction
	Reason     string
	Conviction int
	Confidence float64
	S0         S0Signal
	S1         S1Signal
	S2         S2Signal
	S3         S3Signal
}

func EvaluateMarketSnapshot(asset *AssetSnapshot) StrategySignal {
	dailyADX := asset.Snap1d.Indicators.ADX14
	currentRegime := ClassifyRegime(dailyADX)

	snap4h := asset.Snap4h
	latestPrice := snap4h.Candles[len(snap4h.Candles)-1].Close
	ema20 := snap4h.Indicators.EMA20
	rsi14 := snap4h.Indicators.RSI14
	sma50 := snap4h.Indicators.SMA50
	sma200 := snap4h.Indicators.SMA200

	avgVol := CalculateVolumeMA(snap4h.Candles, 20)
	latestVol := snap4h.Candles[len(snap4h.Candles)-1].Volume
	volRatio := latestVol / avgVol

	// ── Master Gate: Candle Trend ────────────────────────────────────
	// The Master Gate is the first pass filter. It checks proven candle-based
	// indicators (price vs EMA20, RSI, SMA50/200, ADX, volume surge).
	//
	// Strong trends (ADX > 40) bypass volume confirmation entirely.
	// Ranging/crashing assets get flagged separately.
	masterAction := ACTION_HOLD
	masterReason := "No clear candle trend alignment"
	masterStrategy := "HOLD"
	volSurge := volRatio >= 1.5
	strongTrend := dailyADX > 40
	volOk := volSurge || strongTrend

	// ── BUY Conditions (Master Gate) ──
	buyCandle := latestPrice > ema20 && rsi14 > 40 && sma50 > sma200
	buyVolume := volOk && latestPrice > ema20 && rsi14 > 45
	buyOversold := latestPrice < ema20 && rsi14 < 35 && currentRegime == REGIME_RANGING && volSurge
	buyStrongTrend := dailyADX > 40 && latestPrice > ema20 && rsi14 > 45

	// ── SELL Conditions (Master Gate) ──
	sellCandle := latestPrice < ema20 && rsi14 < 60
	sellVolume := volOk && latestPrice < ema20
	sellOverbought := latestPrice > ema20 && rsi14 > 65 && currentRegime == REGIME_RANGING && volSurge
	sellStrongTrend := dailyADX > 40 && latestPrice < ema20 && rsi14 < 45

	if buyStrongTrend || buyCandle || buyVolume || buyOversold {
		masterAction = ACTION_BUY
		switch {
		case buyStrongTrend:
			masterReason = fmt.Sprintf("Master Gate: Strong uptrend (ADX %.0f>40) with price above EMA20.", dailyADX)
		case buyVolume:
			masterReason = fmt.Sprintf("Master Gate: Bullish candle + volume surge (ratio=%.2fx).", volRatio)
		case buyOversold:
			masterReason = "Master Gate: Oversold bounce in ranging regime with volume confirmation."
		default:
			masterReason = fmt.Sprintf("Master Gate: Bullish candle trend (price=%.2f > EMA20=%.2f).", latestPrice, ema20)
		}
		masterStrategy = "Candle Trend"
	} else if sellStrongTrend || sellCandle || sellVolume || sellOverbought {
		masterAction = ACTION_SELL
		switch {
		case sellStrongTrend:
			masterReason = fmt.Sprintf("Master Gate: Strong downtrend (ADX %.0f>40) with price below EMA20.", dailyADX)
		case sellVolume:
			masterReason = fmt.Sprintf("Master Gate: Bearish candle + volume surge (ratio=%.2fx).", volRatio)
		case sellOverbought:
			masterReason = "Master Gate: Overbought rejection in ranging regime with volume confirmation."
		default:
			masterReason = fmt.Sprintf("Master Gate: Bearish candle trend (price=%.2f < EMA20=%.2f).", latestPrice, ema20)
		}
		masterStrategy = "Candle Trend"
	}

	// ── Exhaustion Filter: skip overextended assets ────────────────
	// Prevents buying assets that have already pumped (UBUSDT +130%) or
	// selling assets in capitulation dumps where trend is exhausted.
	gain7d := Compute7DayGain(asset.Snap1d.Candles)
	if masterAction == ACTION_BUY && gain7d > 40.0 && dailyADX < 50 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D gain exceeds 40%% limit (ADX %.0f < 50). Pump exhaustion risk.", gain7d, dailyADX)
		masterStrategy = "HOLD"
	} else if masterAction == ACTION_SELL && gain7d < -40.0 && dailyADX < 50 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D loss exceeds -40%% limit (ADX %.0f < 50). Capitulation risk.", gain7d, dailyADX)
		masterStrategy = "HOLD"
	}

	// ── Default return if Master Gate is HOLD ──
	if masterAction == ACTION_HOLD {
		holdReason := "Master Gate: "
		if rsi14 >= 40 && rsi14 <= 60 && latestPrice > ema20 {
			holdReason += "Neutral within trend zone."
		} else if latestPrice < ema20 && rsi14 > 35 {
			holdReason += "Below EMA20 but RSI recovering."
		} else if latestPrice > ema20 && rsi14 < 45 {
			holdReason += "Above EMA20 but RSI weak."
		} else {
			holdReason += "No clear directional bias."
		}
		return StrategySignal{
			Symbol:     asset.Symbol,
			Regime:     currentRegime,
			Action:     ACTION_HOLD,
			Strategy:   "HOLD",
			Reason:     holdReason,
			Conviction: 0,
			Confidence: 0.0,
		}
	}

	// ── S0: Candle Momentum ──────────────────────────────────────────
	// S0 is the first advanced strategy. It always agrees with the Master
	// Gate when the Master Gate has a direction, ensuring that every
	// candle-based signal reaches at least Conviction 2 (bypassing AI).
	// This means proven candle patterns (RSI, EMA, volume, ADX) execute
	// immediately without paying the OpenRouter API tax.
	s0 := S0Signal{Active: false}
	if masterAction != ACTION_HOLD {
		s0 = S0Signal{
			Active: true,
			Action: masterAction,
			Reason: "Candle momentum confirms direction via RSI/EMA/volume confluence.",
		}
	}

	// ── Advanced Strategies (S1 / S2 / S3) ─────────────────────────
	// Only evaluated when Master Gate has a direction. These increase
	// conviction scoring — they never override the Master Gate.
	s1 := EvaluateS1MeanReversion(latestPrice, asset.VP)
	s2 := EvaluateS2Squeeze(asset.OI, asset.Funding, latestPrice, ema20)
	s3 := EvaluateS3Breakout(latestPrice, asset.Consolidation, latestVol, avgVol)

	signal := StrategySignal{
		Symbol: asset.Symbol,
		Regime: currentRegime,
		Action: masterAction,
		S0:     s0,
		S1:     s1,
		S2:     s2,
		S3:     s3,
	}

	// ── Conviction Scoring ─────────────────────────────────────────
	// Base = 0. +1 for S0 (always agrees with Master Gate).
	// +1 for each advanced strategy (S1/S2/S3) that agrees.
	agreeCount := 0
	advancedReasons := ""

	// S0 always agrees with the Master Gate direction when active.
	if masterAction != ACTION_HOLD {
		agreeCount = 1
	}

	for _, s := range []string{"S1", "S2", "S3"} {
		var active bool
		var advAction SignalAction
		var reason string
		switch s {
		case "S1":
			active = s1.Active
			advAction = s1.Action
			reason = s1.Reason
		case "S2":
			active = s2.Active
			advAction = s2.Action
			reason = s2.Reason
		case "S3":
			active = s3.Active
			advAction = s3.Action
			reason = s3.Reason
		}

		if active && advAction == masterAction {
			agreeCount++
			if advancedReasons != "" {
				advancedReasons += " | "
			}
			advancedReasons += fmt.Sprintf("%s: %s", s, reason)
		}
	}

	switch {
	case agreeCount == 0:
		signal.Conviction = 1
		signal.Confidence = 0.55
		signal.Strategy = masterStrategy
		signal.Reason = masterReason
	case agreeCount == 1:
		signal.Conviction = 2
		signal.Confidence = 0.75
		signal.Strategy = "Candle + " + masterStrategy
		signal.Reason = fmt.Sprintf("%s | %s", masterReason, advancedReasons)
	case agreeCount >= 2:
		signal.Conviction = 3
		signal.Confidence = 0.90
		signal.Strategy = "META: " + masterStrategy
		signal.Reason = fmt.Sprintf("Master Gate + %d advanced strategies aligned | %s",
			agreeCount, advancedReasons)
	}

	// ── Moderate Extension Risk Cap ──────────────────────────────
	// Assets with 25-40% 7D gain/loss are extended but not exhausted.
	// Cap confidence to reduce position sizing on these borderline setups.
	if gain7d > 25.0 && gain7d <= 40.0 && signal.Confidence > 0.70 {
		signal.Confidence = 0.70
		signal.Reason += " | ⚠️ 7D gain " + fmt.Sprintf("%.0f%%", gain7d) + " — risk-capped"
	} else if gain7d < -25.0 && gain7d >= -40.0 && signal.Confidence > 0.70 {
		signal.Confidence = 0.70
		signal.Reason += " | ⚠️ 7D loss " + fmt.Sprintf("%.0f%%", gain7d) + " — risk-capped"
	}

	return signal
}