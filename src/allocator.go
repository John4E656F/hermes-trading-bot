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
	S4         S4Signal
}

// EvaluateMarketSnapshot makes the FINAL decision on every trade.
// This is the core strategy. It must be PROFITABLE or return HOLD.
// The market is currently bearish (~60%+ SELL signals).
// This strategy is STRICT: it defaults to HOLD and only triggers on clear setups.
func EvaluateMarketSnapshot(asset *AssetSnapshot) StrategySignal {
	dailyADX := asset.Snap1d.Indicators.ADX14
	currentRegime := ClassifyRegime(dailyADX)

	snap4h := asset.Snap4h
	latestPrice := snap4h.Candles[len(snap4h.Candles)-1].Close
	ema20 := snap4h.Indicators.EMA20
	rsi14 := snap4h.Indicators.RSI14
	sma50 := snap4h.Indicators.SMA50

	avgVol := CalculateVolumeMA(snap4h.Candles, 20)
	latestVol := snap4h.Candles[len(snap4h.Candles)-1].Volume
	volRatio := latestVol / avgVol
	gain7d := Compute7DayGain(asset.Snap1d.Candles)

	// ── Master Gate ────────────────────────────────────────────────
	// Default: HOLD. Only trigger on genuinely strong setups.
	masterAction := ACTION_HOLD
	masterReason := "No high-confidence setup detected"
	masterStrategy := "HOLD"
	volSurge := volRatio >= 1.5
	strongTrend := dailyADX > 40

	// ── HIGH-CONFIDENCE SELL (bearish market bias) ────────────────
	// Requires ALL of: price below EMA20+SMA50, RSI bearish, ADX trending, volume
	sellStrict := latestPrice < ema20 &&
		latestPrice < sma50 &&
		rsi14 < 50 &&
		dailyADX > 25 &&
		volSurge

	// Strong trend sell (ADX > 40, no volume needed)
	sellTrend := dailyADX > 40 &&
		latestPrice < ema20 &&
		rsi14 < 45

	// Oversold mean reversion SHORT (rare - shorting into weakness)
	sellOverbought := latestPrice > ema20 &&
		rsi14 > 70 &&
		dailyADX > 25 &&
		volRatio >= 2.0 &&
		currentRegime == REGIME_RANGING

	// ── HIGH-CONFIDENCE BUY (only when very strong) ────────────────
	// In this bearish market, BUY requires exceptional confirmation
	buyStrict := latestPrice > ema20 &&
		latestPrice > sma50 &&
		rsi14 > 50 &&
		dailyADX > 25 &&
		volSurge

	// Only allow weak-trend buy on massive volume
	buyVolume := dailyADX < 25 &&
		latestPrice > ema20 &&
		latestPrice > sma50 &&
		rsi14 > 50 &&
		volRatio >= 3.0

	// Oversold bounce (only when truly oversold with volume)
	buyOversold := latestPrice < ema20 &&
		rsi14 < 30 &&
		currentRegime == REGIME_RANGING &&
		volRatio >= 2.0

	// Strong trend buy (ADX > 40 confirmed uptrend)
	buyStrongTrend := dailyADX > 40 &&
		latestPrice > ema20 &&
		rsi14 > 50

	// ── Gate application ──
	if sellStrict {
		masterAction = ACTION_SELL
		masterReason = fmt.Sprintf("Strict SELL: price below EMA20+SMA50, RSI %.0f<50, ADX %.0f>25, vol %.2fx.", rsi14, dailyADX, volRatio)
		masterStrategy = "Strict Sell"
	} else if sellTrend {
		masterAction = ACTION_SELL
		masterReason = fmt.Sprintf("Trend SELL: ADX %.0f>40 strong downtrend, price below EMA20, RSI %.0f<45.", dailyADX, rsi14)
		masterStrategy = "Trend Sell"
	} else if sellOverbought {
		masterAction = ACTION_SELL
		masterReason = fmt.Sprintf("Overbought SELL: RSI %.0f>70, ADX %.0f>25, volume %.2fx. Rejection setup.", rsi14, dailyADX, volRatio)
		masterStrategy = "Overbought Sell"
	} else if buyStrongTrend {
		masterAction = ACTION_BUY
		masterReason = fmt.Sprintf("Strong trend BUY: ADX %.0f>40 uptrend, RSI %.0f>50, above EMA20.", dailyADX, rsi14)
		masterStrategy = "Trend Buy"
	} else if buyVolume {
		masterAction = ACTION_BUY
		masterReason = fmt.Sprintf("Volume BUY: price above EMA20+SMA50, vol %.2fx>3x, RSI %.0f>50.", volRatio, rsi14)
		masterStrategy = "Volume Buy"
	} else if buyOversold {
		masterAction = ACTION_BUY
		masterReason = fmt.Sprintf("Oversold BUY: RSI %.0f<30, volume %.2fx>2x. Reversal zone.", rsi14, volRatio)
		masterStrategy = "Oversold Buy"
	} else if buyStrict {
		masterAction = ACTION_BUY
		masterReason = fmt.Sprintf("Strict BUY: above EMA20+SMA50, RSI %.0f>50, ADX %.0f>25, vol %.2fx.", rsi14, dailyADX, volRatio)
		masterStrategy = "Strict Buy"
	}

	// ── Exhaustion Filter ─────────────────────────────────────────
	if masterAction == ACTION_BUY && gain7d > 40.0 && dailyADX < 50 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D gain >40%% (ADX %.0f<50). Pump risk.", gain7d, dailyADX)
		masterStrategy = "HOLD"
	} else if masterAction == ACTION_SELL && gain7d < -40.0 && dailyADX < 50 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D loss <-40%% (ADX %.0f<50). Capitulation risk.", gain7d, dailyADX)
		masterStrategy = "HOLD"
	}

	// ── HOLD return ──────────────────────────────────────────────
	if masterAction == ACTION_HOLD {
		return StrategySignal{
			Symbol:     asset.Symbol,
			Regime:     currentRegime,
			Action:     ACTION_HOLD,
			Strategy:   "HOLD",
			Reason:     "Master Gate: " + masterReason,
			Conviction: 0,
			Confidence: 0.0,
		}
	}

	// ── S0: Independent candle-pattern verification ────────────────
	// S0 confirms the master gate using different lenses.
	// Oversold/overbought reversal setups have their own S0 path so those
	// master gate conditions are not silently dead.
	s0 := S0Signal{Active: false}
	switch {
	case masterAction == ACTION_BUY && rsi14 > 50 && latestPrice > ema20 && latestPrice > sma50:
		s0 = S0Signal{Active: true, Action: ACTION_BUY,
			Reason: "Candle momentum: RSI>50, price above EMA20+SMA50."}
	case masterAction == ACTION_SELL && rsi14 < 50 && latestPrice < ema20 && latestPrice < sma50:
		s0 = S0Signal{Active: true, Action: ACTION_SELL,
			Reason: "Candle momentum: RSI<50, price below EMA20+SMA50."}
	case strongTrend:
		s0 = S0Signal{Active: true, Action: masterAction,
			Reason: fmt.Sprintf("Strong trend (ADX %.0f>40) confirms direction.", dailyADX)}
	case masterAction == ACTION_BUY && rsi14 < 32 && volRatio >= 1.5:
		// Oversold reversal: price below EMA20 but deeply oversold with volume
		s0 = S0Signal{Active: true, Action: ACTION_BUY,
			Reason: fmt.Sprintf("Oversold reversal: RSI %.0f<32 + vol surge %.1fx.", rsi14, volRatio)}
	case masterAction == ACTION_SELL && rsi14 > 68 && volRatio >= 1.5:
		// Overbought rejection: price above EMA20 but deeply overbought with volume
		s0 = S0Signal{Active: true, Action: ACTION_SELL,
			Reason: fmt.Sprintf("Overbought rejection: RSI %.0f>68 + vol surge %.1fx.", rsi14, volRatio)}
	}

	// ── Advanced Strategies ───────────────────────────────────────
	s1 := EvaluateS1MeanReversion(latestPrice, asset.VP)
	s2 := EvaluateS2Squeeze(asset.OI, asset.Funding, latestPrice, ema20)
	s3 := EvaluateS3Breakout(latestPrice, asset.Consolidation, latestVol, avgVol)
	s4 := EvaluateS4MACD(snap4h.Candles)

	signal := StrategySignal{
		Symbol: asset.Symbol,
		Regime: currentRegime,
		Action: masterAction,
		S0:     s0,
		S1:     s1,
		S2:     s2,
		S3:     s3,
		S4:     s4,
	}

	// ── Honest Conviction Scoring ─────────────────────────────────
	// S0 = 1 point (if independently verified). S1/S2/S3/S4 each add 1 if aligned.
	agreeCount := 0
	advancedReasons := ""

	if s0.Active {
		agreeCount = 1
	}

	type stratCheck struct {
		name   string
		active bool
		action SignalAction
		reason string
	}
	checks := []stratCheck{
		{"S1", s1.Active, s1.Action, s1.Reason},
		{"S2", s2.Active, s2.Action, s2.Reason},
		{"S3", s3.Active, s3.Action, s3.Reason},
		{"S4", s4.Active, s4.Action, s4.Reason},
	}
	for _, c := range checks {
		if c.active && c.action == masterAction {
			agreeCount++
			if advancedReasons != "" {
				advancedReasons += " | "
			}
			advancedReasons += fmt.Sprintf("%s: %s", c.name, c.reason)
		}
	}

	switch {
	case agreeCount <= 0:
		// Master Gate alone with no S0/S1/S2/S3 verification = skip
		return StrategySignal{
			Symbol:     asset.Symbol,
			Regime:     currentRegime,
			Action:     ACTION_HOLD,
			Strategy:   "HOLD",
			Reason:     "Master Gate triggered but no strategy verified it. Skipping.",
			Conviction: 0,
			Confidence: 0.0,
		}
	case agreeCount == 1:
		// Only S0 verified. Boost to Conv 2+ if conditions are exceptional.
		boost := false
		boostReason := ""

		// Strong trend boost: ADX > 40 + RSI aligned with direction
		if dailyADX > 40 {
			if masterAction == ACTION_BUY && rsi14 > 55 {
				boost = true
				boostReason = fmt.Sprintf("Strong trend (ADX %.0f>40) + RSI %.0f>55", dailyADX, rsi14)
			} else if masterAction == ACTION_SELL && rsi14 < 45 {
				boost = true
				boostReason = fmt.Sprintf("Strong trend (ADX %.0f>40) + RSI %.0f<45", dailyADX, rsi14)
			}
		}

		// Volume surge boost: 2x+ volume + RSI strongly aligned
		if !boost && volRatio >= 2.0 {
			if masterAction == ACTION_BUY && rsi14 > 55 {
				boost = true
				boostReason = fmt.Sprintf("Volume surge %.1fx + RSI %.0f>55", volRatio, rsi14)
			} else if masterAction == ACTION_SELL && rsi14 < 45 {
				boost = true
				boostReason = fmt.Sprintf("Volume surge %.1fx + RSI %.0f<45", volRatio, rsi14)
			}
		}

		// Oversold bounce / Overbought rejection boost
		if !boost {
			if masterAction == ACTION_BUY && rsi14 < 35 && volRatio >= 1.5 {
				boost = true
				boostReason = fmt.Sprintf("Oversold bounce RSI %.0f<35 + vol %.1fx", rsi14, volRatio)
			} else if masterAction == ACTION_SELL && rsi14 > 65 && volRatio >= 1.5 {
				boost = true
				boostReason = fmt.Sprintf("Overbought rejection RSI %.0f>65 + vol %.1fx", rsi14, volRatio)
			}
		}

		if boost {
			signal.Conviction = 2
			signal.Confidence = 0.70
			signal.Strategy = "QUALITY: " + masterStrategy
			signal.Reason = fmt.Sprintf("%s | %s", masterReason, boostReason)
		} else {
			// Standard Conviction 1 — stays below execution threshold
			signal.Conviction = 1
			signal.Confidence = 0.65
			signal.Strategy = masterStrategy
			signal.Reason = masterReason + " | Verified by S0 (low conviction)"
		}
	case agreeCount == 2:
		signal.Conviction = 2
		signal.Confidence = 0.75
		signal.Strategy = "S0 + " + masterStrategy
		signal.Reason = fmt.Sprintf("%s | %s", masterReason, advancedReasons)
	case agreeCount >= 3:
		signal.Conviction = 3
		signal.Confidence = 0.85
		signal.Strategy = "META: " + masterStrategy
		signal.Reason = fmt.Sprintf("META (%d strategies aligned) | %s | %s",
			agreeCount, masterReason, advancedReasons)
	}

	// ── Risk Cap ─────────────────────────────────────────────────
	if gain7d > 25.0 && gain7d <= 40.0 && signal.Confidence > 0.70 {
		signal.Confidence = 0.70
		signal.Reason += " | ⚠️ risk-capped (7D +" + fmt.Sprintf("%.0f%%", gain7d) + ")"
	} else if gain7d < -25.0 && gain7d >= -40.0 && signal.Confidence > 0.70 {
		signal.Confidence = 0.70
		signal.Reason += " | ⚠️ risk-capped (7D " + fmt.Sprintf("%.0f%%", gain7d) + ")"
	}

	return signal
}