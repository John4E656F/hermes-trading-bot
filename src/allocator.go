package main

import (
	"fmt"
	"math"
)

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
	S5         S5Signal
}

// EvaluateMarketSnapshot is the core decision engine.
// Signal priority (highest to lowest):
//
//	S4 Funding Contrarian → LEADING signal, fires on extreme funding
//	S5 BB Squeeze Breakout → energy release after compression
//	Trend / Volume signals → lagging but reliable in strong regimes
//
// Only returns non-HOLD when a signal has been independently verified.
func EvaluateMarketSnapshot(asset *AssetSnapshot) StrategySignal {
	dailyADX := asset.Snap1d.Indicators.ADX14
	currentRegime := ClassifyRegime(dailyADX)

	snap4h := asset.Snap4h
	latestPrice := snap4h.Candles[len(snap4h.Candles)-1].Close
	ema20 := snap4h.Indicators.EMA20
	rsi14 := snap4h.Indicators.RSI14
	wrPct := snap4h.Indicators.WilliamsR // -100 to 0
	vwap20 := snap4h.Indicators.VWAP20
	sma50 := snap4h.Indicators.SMA50
	bb := snap4h.Indicators.BBands
	bbWidth := snap4h.Indicators.BBWidth

	avgVol := CalculateVolumeMA(snap4h.Candles, 20)
	latestVol := snap4h.Candles[len(snap4h.Candles)-1].Volume
	volRatio := latestVol / avgVol
	gain7d := Compute7DayGain(asset.Snap1d.Candles)

	// ── Evaluate all sub-strategies ──────────────────────────────────
	s4 := EvaluateS4FundingContrarian(asset.Funding, asset.OI)
	s5 := EvaluateS5BBSqueeze(bb, latestPrice, latestVol, avgVol)
	s1 := EvaluateS1MeanReversion(latestPrice, asset.VP)
	s2 := EvaluateS2Squeeze(asset.OI, asset.Funding, latestPrice, ema20)
	s3 := EvaluateS3Breakout(latestPrice, asset.Consolidation, latestVol, avgVol)

	// ── Master Gate ───────────────────────────────────────────────────
	// Default: HOLD. Only trigger on high-quality setups.
	masterAction := ACTION_HOLD
	masterReason := "No high-confidence setup detected"
	masterStrategy := "HOLD"
	volSurge := volRatio >= 1.5

	// ── TIER 1: Funding Rate Contrarian (LEADING signal) ─────────────
	// S4 fires when market is structurally over-leveraged in one direction.
	// This is predictive, not lagging — it's the highest-priority signal.
	if s4.Active {
		masterAction = s4.Action
		masterReason = s4.Reason
		masterStrategy = "S4 Funding Contrarian"
	}

	// ── TIER 2: BB Squeeze Breakout (energy-release signal) ──────────
	// Only overrides HOLD; does not override S4.
	if masterAction == ACTION_HOLD && s5.Active {
		masterAction = s5.Action
		masterReason = s5.Reason
		masterStrategy = "S5 BB Squeeze"
	}

	// ── TIER 3: Strong Trend Signals ─────────────────────────────────
	if masterAction == ACTION_HOLD {
		// Strong downtrend: ADX>40, price below VWAP and EMA20, Williams%R bearish
		if dailyADX > 40 && latestPrice < ema20 && wrPct > -30 {
			masterAction = ACTION_SELL
			masterReason = fmt.Sprintf("Trend SELL: ADX %.0f>40, below EMA20, W%%R %.0f (not oversold).", dailyADX, wrPct)
			masterStrategy = "Trend Sell"
		} else if dailyADX > 40 && latestPrice > ema20 && wrPct < -70 {
			// Strong uptrend: ADX>40, price above VWAP and EMA20, Williams%R not yet overbought
			masterAction = ACTION_BUY
			masterReason = fmt.Sprintf("Trend BUY: ADX %.0f>40, above EMA20, W%%R %.0f (not overbought).", dailyADX, wrPct)
			masterStrategy = "Trend Buy"
		}
	}

	// ── TIER 4: Volume-Confirmed Strict Signals ───────────────────────
	if masterAction == ACTION_HOLD {
		// Strict SELL: price below EMA20+SMA50+VWAP, RSI bearish, ADX trending, volume
		vwapBearish := vwap20 == 0 || latestPrice < vwap20
		if latestPrice < ema20 && latestPrice < sma50 && vwapBearish &&
			rsi14 < 50 && dailyADX > 25 && volSurge {
			masterAction = ACTION_SELL
			masterReason = fmt.Sprintf("Strict SELL: below EMA20+SMA50+VWAP, RSI %.0f<50, ADX %.0f>25, vol %.2fx.", rsi14, dailyADX, volRatio)
			masterStrategy = "Strict Sell"
		}

		// Strict BUY: price above EMA20+SMA50+VWAP, RSI bullish, ADX trending, volume
		vwapBullish := vwap20 == 0 || latestPrice > vwap20
		if latestPrice > ema20 && latestPrice > sma50 && vwapBullish &&
			rsi14 > 50 && dailyADX > 25 && volSurge {
			masterAction = ACTION_BUY
			masterReason = fmt.Sprintf("Strict BUY: above EMA20+SMA50+VWAP, RSI %.0f>50, ADX %.0f>25, vol %.2fx.", rsi14, dailyADX, volRatio)
			masterStrategy = "Strict Buy"
		}
	}

	// ── TIER 5: Mean Reversion Extremes ──────────────────────────────
	if masterAction == ACTION_HOLD {
		// Oversold bounce: Williams%R very oversold + RSI<30 + volume
		if wrPct <= -85 && rsi14 < 30 && volRatio >= 2.0 && currentRegime == REGIME_RANGING {
			masterAction = ACTION_BUY
			masterReason = fmt.Sprintf("Oversold BUY: W%%R %.0f≤-85, RSI %.0f<30, vol %.2fx. Extreme mean reversion.", wrPct, rsi14, volRatio)
			masterStrategy = "Oversold Buy"
		}
		// Overbought rejection: Williams%R very overbought + RSI>70 + volume
		if wrPct >= -15 && rsi14 > 70 && volRatio >= 2.0 && currentRegime == REGIME_RANGING {
			masterAction = ACTION_SELL
			masterReason = fmt.Sprintf("Overbought SELL: W%%R %.0f≥-15, RSI %.0f>70, vol %.2fx. Extreme rejection.", wrPct, rsi14, volRatio)
			masterStrategy = "Overbought Sell"
		}
	}

	// ── Exhaustion Filter ─────────────────────────────────────────────
	// Block riding pumps/dumps that have already moved too far.
	if masterAction == ACTION_BUY && gain7d > 40.0 && dailyADX < 50 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D gain >40%% (ADX %.0f<50). Pump risk.", gain7d, dailyADX)
		masterStrategy = "HOLD"
	} else if masterAction == ACTION_SELL && gain7d < -40.0 && dailyADX < 50 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D loss <-40%% (ADX %.0f<50). Capitulation risk.", gain7d, dailyADX)
		masterStrategy = "HOLD"
	}

	// ── BB squeeze width safety: don't trade inside a tight range ────
	// When BB is compressed AND price hasn't broken out, the direction is unknown.
	// Only S5 is allowed to trade in a squeeze — other signals hold off.
	if masterAction != ACTION_HOLD && masterStrategy != "S5 BB Squeeze" && bbWidth > 0 && bbWidth < 2.0 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("BB SQUEEZE LOCK: BB width %.2f%%<2%% — direction undefined. Waiting for breakout.", bbWidth)
		masterStrategy = "HOLD"
	}

	// ── Funding execution filter ──────────────────────────────────────
	// When funding opposes our direction, reduce conviction. If S4 is active
	// AND contradicts masterAction, block the trade entirely.
	if s4.Active && s4.Action != masterAction && masterAction != ACTION_HOLD {
		return StrategySignal{
			Symbol:   asset.Symbol,
			Regime:   currentRegime,
			Action:   ACTION_HOLD,
			Strategy: "HOLD",
			Reason: fmt.Sprintf("FUNDING BLOCK: %s strategy blocked by S4 (%s). Cannot fight funding rate.",
				masterStrategy, s4.Reason),
			Conviction: 0,
			Confidence: 0.0,
		}
	}

	// ── HOLD return ───────────────────────────────────────────────────
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

	// ── S0: Independent candle momentum verification ──────────────────
	// S0 uses Williams%R + VWAP instead of just RSI for better accuracy.
	s0 := S0Signal{Active: false}
	if masterAction == ACTION_BUY {
		aboveVWAP := vwap20 == 0 || latestPrice > vwap20
		if rsi14 > 50 && latestPrice > ema20 && aboveVWAP && wrPct < -40 {
			s0 = S0Signal{Active: true, Action: ACTION_BUY,
				Reason: fmt.Sprintf("Momentum BUY: RSI %.0f>50, above EMA20+VWAP, W%%R %.0f (room to run).", rsi14, wrPct)}
		} else if dailyADX > 40 {
			s0 = S0Signal{Active: true, Action: ACTION_BUY,
				Reason: fmt.Sprintf("Strong trend bypass (ADX %.0f>40) — S0 confirmed.", dailyADX)}
		} else if rsi14 < 32 && volRatio >= 1.5 {
			s0 = S0Signal{Active: true, Action: ACTION_BUY,
				Reason: fmt.Sprintf("Oversold reversal: RSI %.0f<32 + vol surge %.1fx.", rsi14, volRatio)}
		}
	} else if masterAction == ACTION_SELL {
		belowVWAP := vwap20 == 0 || latestPrice < vwap20
		if rsi14 < 50 && latestPrice < ema20 && belowVWAP && wrPct > -60 {
			s0 = S0Signal{Active: true, Action: ACTION_SELL,
				Reason: fmt.Sprintf("Momentum SELL: RSI %.0f<50, below EMA20+VWAP, W%%R %.0f (room to fall).", rsi14, wrPct)}
		} else if dailyADX > 40 {
			s0 = S0Signal{Active: true, Action: ACTION_SELL,
				Reason: fmt.Sprintf("Strong trend bypass (ADX %.0f>40) — S0 confirmed.", dailyADX)}
		} else if rsi14 > 68 && volRatio >= 1.5 {
			s0 = S0Signal{Active: true, Action: ACTION_SELL,
				Reason: fmt.Sprintf("Overbought rejection: RSI %.0f>68 + vol surge %.1fx.", rsi14, volRatio)}
		}
	}


	signal := StrategySignal{
		Symbol: asset.Symbol,
		Regime: currentRegime,
		Action: masterAction,
		S0:     s0,
		S1:     s1,
		S2:     s2,
		S3:     s3,
		S4:     s4,
		S5:     s5,
	}

	// Conviction Scoring: S0 = 1 base point; each agreeing sub-strategy adds 1.

	agreeCount := 0
	advancedReasons := ""

	if s0.Active {
		agreeCount = 1
	}

	type subSignal struct {
		name   string
		active bool
		action SignalAction
		reason string
	}
	subs := []subSignal{
		{"S1", s1.Active, s1.Action, s1.Reason},
		{"S2", s2.Active, s2.Action, s2.Reason},
		{"S3", s3.Active, s3.Action, s3.Reason},
		{"S4", s4.Active, s4.Action, s4.Reason},
		{"S5", s5.Active, s5.Action, s5.Reason},
	}
	for _, sub := range subs {
		if sub.active && sub.action == masterAction {
			agreeCount++
			if advancedReasons != "" {
				advancedReasons += " | "
			}
			advancedReasons += fmt.Sprintf("%s: %s", sub.name, sub.reason)
		}
	}

	switch {
	case agreeCount <= 0:
		// Master Gate alone with no independent verification → skip.
		return StrategySignal{
			Symbol:     asset.Symbol,
			Regime:     currentRegime,
			Action:     ACTION_HOLD,
			Strategy:   "HOLD",
			Reason:     "Master Gate fired but no sub-strategy verified it. Insufficient confirmation.",
			Conviction: 0,
			Confidence: 0.0,
		}

	case agreeCount == 1:
		// S0 only. Eligible for boost if conditions are exceptional.
		boost := false
		boostReason := ""

		if dailyADX > 40 {
			if masterAction == ACTION_BUY && wrPct < -60 {
				boost = true
				boostReason = fmt.Sprintf("Strong trend (ADX %.0f>40) + W%%R %.0f<-60 (not overbought)", dailyADX, wrPct)
			} else if masterAction == ACTION_SELL && wrPct > -40 {
				boost = true
				boostReason = fmt.Sprintf("Strong trend (ADX %.0f>40) + W%%R %.0f>-40 (not oversold)", dailyADX, wrPct)
			}
		}
		if !boost && volRatio >= 2.0 {
			if masterAction == ACTION_BUY && wrPct < -65 {
				boost = true
				boostReason = fmt.Sprintf("Vol surge %.1fx + W%%R %.0f (strong dip)", volRatio, wrPct)
			} else if masterAction == ACTION_SELL && wrPct > -35 {
				boost = true
				boostReason = fmt.Sprintf("Vol surge %.1fx + W%%R %.0f (strong peak)", volRatio, wrPct)
			}
		}
		// Extreme Williams%R boost: at true extremes, even 1 confirmation is enough
		if !boost {
			if masterAction == ACTION_BUY && wrPct <= -90 && volRatio >= 1.5 {
				boost = true
				boostReason = fmt.Sprintf("Extreme W%%R %.0f ≤ -90 + vol %.1fx", wrPct, volRatio)
			} else if masterAction == ACTION_SELL && wrPct >= -10 && volRatio >= 1.5 {
				boost = true
				boostReason = fmt.Sprintf("Extreme W%%R %.0f ≥ -10 + vol %.1fx", wrPct, volRatio)
			}
		}

		if boost {
			signal.Conviction = 2
			signal.Confidence = 0.70
			signal.Strategy = "QUALITY: " + masterStrategy
			signal.Reason = fmt.Sprintf("%s | %s", masterReason, boostReason)
		} else {
			// Conviction 1 — logged only, never executed.
			signal.Conviction = 1
			signal.Confidence = 0.60
			signal.Strategy = masterStrategy
			signal.Reason = masterReason + " | S0 verified (sub-execution threshold)"
		}

	case agreeCount == 2:
		signal.Conviction = 2
		signal.Confidence = 0.75
		signal.Strategy = "CONFIRMED: " + masterStrategy
		signal.Reason = fmt.Sprintf("%s | %s", masterReason, advancedReasons)

	case agreeCount >= 3:
		signal.Conviction = 3
		signal.Confidence = 0.85
		signal.Strategy = "META: " + masterStrategy
		signal.Reason = fmt.Sprintf("META (%d aligned) | %s | %s", agreeCount, masterReason, advancedReasons)
	}

	// ── Funding Rate Headwind Penalty ─────────────────────────────────
	// When funding runs against our direction (but isn't extreme enough to block),
	// reduce confidence by 20% — the funding cost erodes our edge.
	if masterAction == ACTION_BUY && asset.Funding.CurrentRate > 0.0003 {
		signal.Confidence *= 0.80
		signal.Reason += fmt.Sprintf(" | ⚠️ funding headwind: longs paying +%.4f%%/8h", asset.Funding.CurrentRate*100)
	}
	if masterAction == ACTION_SELL && asset.Funding.CurrentRate < -0.0001 {
		signal.Confidence *= 0.80
		signal.Reason += fmt.Sprintf(" | ⚠️ funding headwind: shorts paying %.4f%%/8h", asset.Funding.CurrentRate*100)
	}

	// ── S4 Tailwind Bonus ─────────────────────────────────────────────
	// When S4 AGREES with masterAction, it's a strong structural alignment.
	// This can boost Conviction 2 → 3 if conditions are right.
	if s4.Active && s4.Action == masterAction && signal.Conviction == 2 {
		signal.Conviction = 3
		signal.Confidence = math.Min(signal.Confidence+0.05, 0.90)
		signal.Strategy = "META: " + masterStrategy + " + S4"
		signal.Reason += " | S4 TAILWIND: funding structure confirms direction."
	}

	// ── Risk Cap on extended moves ────────────────────────────────────
	if gain7d > 25.0 && gain7d <= 40.0 && signal.Confidence > 0.70 {
		signal.Confidence = 0.70
		signal.Reason += fmt.Sprintf(" | ⚠️ risk-capped (7D +%.0f%%)", gain7d)
	} else if gain7d < -25.0 && gain7d >= -40.0 && signal.Confidence > 0.70 {
		signal.Confidence = 0.70
		signal.Reason += fmt.Sprintf(" | ⚠️ risk-capped (7D %.0f%%)", gain7d)
	}

	return signal
}
