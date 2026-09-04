package main

import (
	"fmt"
	"math"
	"strings"
	"time"
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
	// Kronos is an optional AI overlay (nil when the service is unavailable).
	// It does not vote like S0–S5 — it adjusts conviction/confidence after
	// the master signal is determined. See the "Kronos AI Overlay" section.
	Kronos *KronosPrediction
	// Reflection tracks past win rates for this symbol, attached after
	// the master signal is computed. Used to adjust confidence.
	Reflection *ReflectionSummary
	// AICouncilResult is filled in by the AI council (in executor.go) after
	// the indicator pre-filter passes. Empty until then.
	AICouncilResult *CouncilResult
}

// globalKronosClient is set once at startup by main(). nil means the Kronos
// service is unavailable — EvaluateMarketSnapshot then runs exactly as before.
var globalKronosClient *KronosClient

// globalKronosPredictions is a batch-fetched map set by main() before the
// signal evaluation loop. When populated, EvaluateMarketSnapshot reads from
// this cache instead of making individual /predict calls per asset.
var globalKronosPredictions map[string]KronosPrediction

// globalSentimentReport is set by main() before signal evaluation.
// When populated, the AI Council prompt includes news/social context.
var globalSentimentReport MarketSentimentReport

// EvaluateMarketSnapshot is the core decision engine.
// Signal priority (highest to lowest):
//
//	KRONOS AI → PRIMARY signal when service is up and direction != hold
//	S4 Funding Contrarian → LEADING signal, fires on extreme funding
//	S5 BB Squeeze Breakout → energy release after compression
//	Trend / Volume signals → lagging but reliable in strong regimes
//	S1/S2/S3 → supporting, never generate alone
//
// Only returns non-HOLD when a signal has been independently verified.
// With Kronos as primary, the AI prediction seeds the direction, then the
// indicator stack acts as confirmation (raises conviction) or vetoes (blocks).

// getKronosPrediction returns the Kronos prediction for a symbol by checking
// the batch cache ONLY. No per-symbol HTTP fallback — the batch pre-fetch in
// main() populates globalKronosPredictions before signal evaluation. A cache
// miss returns nil so the signal path never blocks on a slow Kronos service.
func getKronosPrediction(symbol string) *KronosPrediction {
	if globalKronosClient == nil {
		return nil
	}
	if p, ok := globalKronosPredictions[symbol]; ok {
		return &p
	}
	return nil
}

// kronosToAction maps a Kronos direction string to a SignalAction.
func kronosToAction(direction string) SignalAction {
	switch strings.ToLower(direction) {
	case "buy", "long":
		return ACTION_BUY
	case "sell", "short":
		return ACTION_SELL
	default:
		return ACTION_HOLD
	}
}

func EvaluateMarketSnapshot(asset *AssetSnapshot) StrategySignal {
	// ── Symbol Ban Check (MoA recommendation) ──────────────────────────
	// Banned symbols return HOLD immediately — no signal evaluation, no API costs.
	if IsBannedSymbol(asset.Symbol) {
		return StrategySignal{
			Symbol:     asset.Symbol,
			Action:     ACTION_HOLD,
			Strategy:   "HOLD",
			Reason:     "Symbol banned by reflection filter (WR < 30% after 10+ calls)",
			Conviction: 0,
			Confidence: 0.0,
		}
	}

	dailyADX := asset.Snap1d.Indicators.ADX14
	currentRegime := ClassifyRegime(dailyADX)

	snap4h := asset.Snap4h
	if len(snap4h.Candles) == 0 {
		return StrategySignal{Symbol: asset.Symbol, Regime: currentRegime, Action: ACTION_HOLD, Strategy: "HOLD", Reason: "No candle data available"}
	}
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
	var volRatio float64
	if avgVol > 0 {
		volRatio = latestVol / avgVol
	}
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

	// ── Kronos Vote: weighted contributor, NOT prime override ──────────
	// MoA FIX (2026-09-02): Converted from prime directive to weighted vote.
	// Kronos BUY: 53.3% WR (+536% PnL) — strong, but the indicator stack
	// retains full veto power. Kronos SELL: 47.6% WR (-762% PnL) — actually
	// hurts performance. Kronos is captured as a bonus vote in conviction scoring.
	// The kronosVote and kronosConf are used later to boost agreeCount.
	kronosPred := getKronosPrediction(asset.Symbol)
	var kronosVote SignalAction = ACTION_HOLD
	var kronosConf float64 = 0.0
	if kronosPred != nil {
		kronosVote = kronosToAction(kronosPred.Direction)
		kronosConf = kronosPred.Confidence
		// Log every Kronos prediction so we can later join against
		// realized price moves and measure AI vs indicator accuracy.
		AppendKronosLog(KronosLogEntry{
			Timestamp:        time.Now().UTC(),
			Symbol:           asset.Symbol,
			Price:            asset.CurrentPrice,
			MasterAction:     ACTION_HOLD, // will be determined after indicator stack
			MasterStrategy:   "PENDING",
			PreConviction:    0,
			PreConfidence:    kronosConf,
			KronosDirection:  kronosPred.Direction,
			KronosZone:       kronosPred.Zone,
			KronosComposite:  kronosPred.Composite,
			KronosConfidence: kronosConf,
			KronosPrice:      kronosPred.Price,
			Agreement:        "pending",
		})
	}

	// ── TIER 1: Funding Rate Contrarian (LEADING signal) ─────────────
	// Only fires when masterAction is still HOLD (Kronos didn't override).
	// S4 fires when market is structurally over-leveraged in one direction.
	// This is predictive, not lagging — it's the highest-priority signal.
	//
	// Price Confirmation Guard: funding extremes only mean "crowd is wrong"
	// when price has NOT already confirmed the crowd's direction.
	// If price is >5% below SMA50 on a BUY signal, shorts are likely CORRECT
	// and just paying funding to hold a winning position — not a squeeze setup.
	if s4.Active {
		blocked := false

		// CRITICAL FIX (2026-09-03): Allow S4 BUY through when shorts are
		// EXTREMELY crowded (funding ≤ 2× extreme threshold).
		// At -0.001/s (0.3%/day), shorts pay $109.50/yr to hold $100 position.
		// This IS economically unsustainable regardless of price position.
		// The "shorts paying on a winning position" logic is correct at
		// -0.0005 but wrong at -0.001 — even if price is below SMA50,
		// the funding bleed will force de-risking.
		extremeShort := asset.Funding.CurrentRate <= -0.001
		extremeLong := asset.Funding.CurrentRate >= 0.001

		if s4.Action == ACTION_BUY && extremeShort {
			// Override guard: -0.001+ funding is always structurally unsustainable.
			// Price position doesn't matter — shorts WILL be forced out by funding cost.
			masterAction = s4.Action
			masterReason = fmt.Sprintf(
				"S4 EXTREME SHORT SQUEEZE: rate %.4f%% (shorts paying %.3f%%/day). Funding override activated.",
				asset.Funding.CurrentRate*100, math.Abs(asset.Funding.CurrentRate)*3*100)
			masterStrategy = "S4 Funding Contrarian"
		} else if s4.Action == ACTION_BUY && sma50 > 0 && latestPrice < sma50*0.98 {
			blocked = true
			masterReason = fmt.Sprintf(
				"S4 BUY blocked: price $%.4f is %.1f%% below SMA50 $%.4f — shorts paying funding on a winning position, not a squeeze.",
				latestPrice, (1-latestPrice/sma50)*100, sma50)
		} else if s4.Action == ACTION_SELL && extremeLong {
			// Symmetric override for extreme long funding
			masterAction = s4.Action
			masterReason = fmt.Sprintf(
				"S4 EXTREME LONG SQUEEZE: rate +%.4f%% (longs paying %.3f%%/day). Funding override activated.",
				asset.Funding.CurrentRate*100, math.Abs(asset.Funding.CurrentRate)*3*100)
			masterStrategy = "S4 Funding Contrarian"
		} else if s4.Action == ACTION_SELL && sma50 > 0 && latestPrice > sma50*1.02 {
			blocked = true
			masterReason = fmt.Sprintf(
				"S4 SELL blocked: price $%.4f is %.1f%% above SMA50 $%.4f — longs paying funding on a winning position, not a squeeze.",
				latestPrice, (latestPrice/sma50-1)*100, sma50)
		}
		// Additional: OI declining confirms shorts are capitulating
		if !blocked && s4.Action == ACTION_BUY && asset.OI.Change24h < -5 && extremeShort {
			masterReason += fmt.Sprintf(" | OI -%.0f%% — shorts capitulating, squeeze firing.", math.Abs(asset.OI.Change24h))
		}
		if !blocked {
			masterAction = s4.Action
			masterReason = s4.Reason
			masterStrategy = "S4 Funding Contrarian"
		}
	}

	// ── TIER 2: BB Squeeze Breakout (energy-release signal) ──────────
	// Only overrides HOLD; does not override S4.
	if masterAction == ACTION_HOLD && s5.Active {
		masterAction = s5.Action
		masterReason = s5.Reason
		masterStrategy = "S5 BB Squeeze"
	}

	// ── TIER 3: Strong Trend Signals ─────────────────────────────────
	// CRITICAL FIX (2026-09-02): Trend BUY removed — it systematically bought
	// into pullbacks in overextended uptrends (ADX>40 means trend has been
	// running for days). The combination of price>EMA20 + W%R<-70 captured
	// shallow dips in mature trends where the stop would get hit on first
	// real retracement. This is the #1 contributor to the 0.77x win/loss
	// ratio on BUY side.
	// Trend SELL kept: performs fine (0.98x ratio on SELL side).
	// FIX (2026-09-03): Removed wrPct > -70 guard — in trending markets
	// (ADX>40), W%R often goes below -70 on temporary pullbacks within
	// a bear trend. The key condition is price < EMA20 in a strong trend.
	if masterAction == ACTION_HOLD {
		if dailyADX > 40 && latestPrice < ema20 {
			masterAction = ACTION_SELL
			masterReason = fmt.Sprintf("Trend SELL: ADX %.0f>40, below EMA20 (trend has room to fall).", dailyADX)
			masterStrategy = "Trend Sell"
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

		// Strict BUY: price above EMA20+SMA50+VWAP, RSI bullish, ADX trending, volume.
		// Symmetric with Strict SELL — pure indicator alignment required on both sides.
		// CRITICAL FIX (2026-09-02): Added gain7d < 15% guard to prevent buying
		// into extended rallies. The 7D Performance Gate handles caps retroactively,
		// but blocking at the entry tier prevents the whole signal chain.
		vwapBullish := vwap20 == 0 || latestPrice > vwap20
		if latestPrice > ema20 && latestPrice > sma50 && vwapBullish &&
			rsi14 > 50 && dailyADX > 25 && volSurge && gain7d > -10.0 && gain7d < 15.0 {
			masterAction = ACTION_BUY
			masterReason = fmt.Sprintf("Strict BUY: above EMA20+SMA50+VWAP, RSI %.0f>50, ADX %.0f>25, vol %.2fx, 7D %.1f%%.", rsi14, dailyADX, volRatio, gain7d)
			masterStrategy = "Strict Buy"
		}
	}

// ── TIER 5: Mean Reversion Extremes ──────────────────────────────
	// Mean reversion only works when price is near its average (within the range).
	// If price has crashed far below EMA20, "oversold" is continuation not reversal.
	// Guard: price must be within 5% of EMA20 for the bounce to be structurally valid.
	// Symmetric: same 5% distance check on the overbought SELL side.
	if masterAction == ACTION_HOLD && ema20 > 0 {
		priceDistFromEMA := math.Abs(latestPrice-ema20) / ema20
		if wrPct <= -85 && rsi14 < 30 && volRatio >= 2.0 &&
			currentRegime == REGIME_RANGING && priceDistFromEMA <= 0.05 {
			masterAction = ACTION_BUY
			masterReason = fmt.Sprintf("Oversold BUY: W%%R %.0f≤-85, RSI %.0f<30, vol %.2fx, price within %.1f%%%% of EMA20.", wrPct, rsi14, volRatio, priceDistFromEMA*100)
			masterStrategy = "Oversold Buy"
		}
		if wrPct >= -15 && rsi14 > 70 && volRatio >= 2.0 &&
			currentRegime == REGIME_RANGING && priceDistFromEMA <= 0.05 {
			masterAction = ACTION_SELL
			masterReason = fmt.Sprintf("Overbought SELL: W%%R %.0f≥-15, RSI %.0f>70, vol %.2fx, price within %.1f%%%% of EMA20.", wrPct, rsi14, volRatio, priceDistFromEMA*100)
			masterStrategy = "Overbought Sell"
		}
	}

	// ── Exhaustion Filter ─────────────────────────────────────────────
	// Block riding pumps/dumps that have already moved too far.
	// CRITICAL FIX (MoA 2026-09-02): Thresholds tightened based on historical
	// data showing 0.77x win/loss ratio on BUY — most tops form at 15-25% gains.
	if masterAction == ACTION_BUY {
		if gain7d > 35.0 {
			// Hard block: 35%+ in 7 days is always exhaustion regardless of ADX
			masterAction = ACTION_HOLD
			masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D gain >35%% — structural pump risk.", gain7d)
			masterStrategy = "HOLD"
		} else if gain7d > 20.0 && dailyADX < 35 {
			masterAction = ACTION_HOLD
			masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D gain >20%% (ADX %.0f<35). Pump risk.", gain7d, dailyADX)
			masterStrategy = "HOLD"
		}
	} else if masterAction == ACTION_SELL {
		if gain7d < -20.0 {
			masterAction = ACTION_HOLD
			masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D loss <-20%% — structural capitulation.", gain7d)
			masterStrategy = "HOLD"
		} else if gain7d < -12.0 && dailyADX < 35 {
			masterAction = ACTION_HOLD
			masterReason = fmt.Sprintf("Exhaustion: %.0f%% 7D loss <-12%% (ADX %.0f<35). Capitulation risk.", gain7d, dailyADX)
			masterStrategy = "HOLD"
		}
	}

	// ── Extended Rally Guard (BUY-specific) ───────────────────────────
	// CRITICAL FIX (2026-09-02): The general exhaustion filter only blocks
	// BUY when ADX < 50. In strong trends (ADX > 50), buys sail through even
	// with 40%+ 7D gains — the #1 pattern causing the 0.77x win/loss ratio.
	// This guard blocks BUY when the asset is already extended in ANY trend.
	// Even -10% 7D buys are dangerous (falling knife) — handled separately.
	if masterAction == ACTION_BUY && gain7d > 25.0 {
		masterAction = ACTION_HOLD
		masterReason = fmt.Sprintf("Extended rally GUARD: %.0f%% 7D gain >25%% — buying tops is the #1 loss pattern.", gain7d)
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
		// CRITICAL FIX (2026-09-03): S4 funding contrarian BUY is structurally
		// predictive — it doesn't need price-action momentum to confirm.
		// S4 BUY at -0.001+ funding is valid regardless of price position.
		// S0 only verifies that the direction isn't actively contradicted.
		if masterStrategy == "S4 Funding Contrarian" {
			// S4 already signals extreme shorts. S0 checks: as long as price
			// isn't in full capitulation (W%R > -95 means not at absolute bottom),
			// the momentum isn't contradicting the squeeze thesis.
			if wrPct > -95 && rsi14 > 20 {
				s0 = S0Signal{Active: true, Action: ACTION_BUY,
					Reason: fmt.Sprintf("S4 BUY S0 bypass: funding rate %.4f%% extreme. W%%R %.0f has room to run.",
						asset.Funding.CurrentRate*100, wrPct)}
			}
		} else {
			// Confirmed bullish: price above VWAP, Williams%R leaving oversold
			aboveVWAP := vwap20 == 0 || latestPrice > vwap20
			if rsi14 > 50 && latestPrice > ema20 && aboveVWAP && wrPct < -40 {
				s0 = S0Signal{Active: true, Action: ACTION_BUY,
					Reason: fmt.Sprintf("Momentum BUY: RSI %.0f>50, above EMA20+VWAP, W%%R %.0f (room to run).", rsi14, wrPct)}
			} else if dailyADX > 40 {
				s0 = S0Signal{Active: true, Action: ACTION_BUY,
					Reason: fmt.Sprintf("Strong trend bypass (ADX %.0f>40) — S0 confirmed.", dailyADX)}
			}
		}
	} else if masterAction == ACTION_SELL {
		belowVWAP := vwap20 == 0 || latestPrice < vwap20
		// W%R > -80: price is not yet at extreme oversold — still has room to fall.
		// Was > -60 which never fired in genuine downtrends (W%R often -70 to -90).
		if rsi14 < 50 && latestPrice < ema20 && belowVWAP && wrPct > -80 {
			s0 = S0Signal{Active: true, Action: ACTION_SELL,
				Reason: fmt.Sprintf("Momentum SELL: RSI %.0f<50, below EMA20+VWAP, W%%R %.0f (room to fall).", rsi14, wrPct)}
		} else if dailyADX > 40 {
			s0 = S0Signal{Active: true, Action: ACTION_SELL,
				Reason: fmt.Sprintf("Strong trend bypass (ADX %.0f>40) — S0 confirmed.", dailyADX)}
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

	// ── Conviction Scoring ────────────────────────────────────────────
	// Base: 1 if S0 verified independently. +1 per agreeing sub-strategy.
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

	// ── Kronos Weighted Vote ──────────────────────────────────────────
	// MoA FIX: Kronos is no longer a prime directive. Instead it contributes
	// to agreeCount when its direction matches masterAction AND confidence > 0.6.
	// Historical: Kronos BUY 53.3% WR (+536% PnL) — high confidence agrees with
	// indicator stack → strong combined signal.
	if kronosVote == masterAction && kronosConf > 0.6 && masterAction != ACTION_HOLD {
		agreeCount++
		advancedReasons += fmt.Sprintf(" | Kronos AI: %s (conf=%.0f%%)", kronosVote, kronosConf*100)
	} else if kronosVote != ACTION_HOLD && kronosVote != masterAction && masterAction != ACTION_HOLD {
		// Kronos disagrees — interesting but not penalized; the indicator stack
		// already decided. Just note it in the reason for later analysis.
		advancedReasons += fmt.Sprintf(" | Kronos disagrees (%s vs master %s)", kronosVote, masterAction)
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

			// CRITICAL FIX (2026-09-03): Trend SELL auto-boost.
			// Trend SELL is our strongest signal type (ADX>40 + below EMA20).
			// S0 already verified it via the ADX>40 bypass. No sub-strategy
			// can agree because shorts-crowded trending markets have:
			//   S1 → VAH out of reach, S2 → funding fires BUY, S3 → no consolidation
			//        S4 → funding fires BUY, S5 → no BB squeeze active
			// This is a structural impossibility, not a weak signal.
			// Grant Conv2 automatically for ADX>40 + S0-confirmed Trend SELL.
			if masterStrategy == "Trend Sell" && dailyADX > 40 {
				boost = true
				boostReason = fmt.Sprintf("Trend SELL auto-boost: ADX %.0f>40, S0 confirmed — strongest signal type", dailyADX)
			}
			// Strict SELL with multi-MA alignment is also structurally sound
			if !boost && masterStrategy == "Strict Sell" && dailyADX > 25 {
				boost = true
				boostReason = fmt.Sprintf("Strict SELL auto-boost: ADX %.0f>25, multi-MA alignment", dailyADX)
			}
			// S4 Funding Contrarian is self-validating by funding economics
			if !boost && masterStrategy == "S4 Funding Contrarian" {
				boost = true
				boostReason = "S4 Funding Contrarian: funding extreme confirms direction"
			}
			// S5 BB Squeeze breakout is self-validating
			if !boost && strings.HasPrefix(masterStrategy, "S5") {
				boost = true
				boostReason = "S5 BB Squeeze: self-validating breakout energy release"
			}

			if !boost && dailyADX > 40 {
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

	// ── Kronos AI Overlay (signal assignment only) ────────────────────
	// The Kronos prediction was already fetched and logged in the Kronos
	// Prime block above. Here we just attach it to the signal struct for
	// downstream consumers (dashboard, logging, replay analysis).
	// When Kronos seeded the primary direction, the indicator stack's
	// agreement is already reflected in the conviction score above.
	if kronosPred != nil {
		signal.Kronos = kronosPred
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
	if gain7d > 15.0 && gain7d <= 40.0 && signal.Confidence > 0.60 {
		signal.Confidence = math.Min(signal.Confidence, 0.65)
		signal.Reason += fmt.Sprintf(" | ⚠️ risk-capped (7D +%.0f%%, capped at 65%%", gain7d)
	} else if gain7d < -10.0 && gain7d >= -40.0 && signal.Confidence > 0.60 {
		signal.Confidence = math.Min(signal.Confidence, 0.65)
		signal.Reason += fmt.Sprintf(" | ⚠️ risk-capped (7D %.0f%%, capped at 65%%", gain7d)
	}

	// ── Reflection Overlay ────────────────────────────────────────────
	// Read past performance for this symbol and apply confidence multiplier.
	// Computed from kronos_outcomes.jsonl — tracks per-symbol master signal win rate.
	if ref := GetReflection(asset.Symbol, signal.Action); ref != nil {
		signal.Reflection = ref
		oldConf := signal.Confidence
		// CRITICAL FIX (2026-09-03): Only apply reflection multiplier when
		// Conviction >= 2. Conv1 signals are logged but never executed — they
		// should not reduce confidence for future legitimate signals.
		if signal.Conviction >= 2 {
			signal.Confidence = math.Min(signal.Confidence*ref.ConfidenceMultiplier, 0.95)
		}
		if signal.Confidence != oldConf {
			signal.Reason += fmt.Sprintf(" | Reflection: %.0f%% WR → %.1fx mult (conf %.0f%%→%.0f%%)",
				ref.WinRate*100, ref.ConfidenceMultiplier, oldConf*100, signal.Confidence*100)
		}
		signal.Reason += fmt.Sprintf(" | Lesson: %s", ref.Lesson)
	}

	// ── 7D Performance Gate ───────────────────────────────────────────
	// CRITICAL FIX (MoA 2026-09-02): Symmetric falling knife guard for BUY.
	// Historical data: BUY losers average -8.59% vs winners +6.58% (0.77x ratio).
	// The original guard only capped confidence — it never blocked.
	// Now: hard block at -15%, soft block (Conv1 forced) at -8%.
	// For SELL: tightened from gain7d < -15% to gain7d < -10%.
	if signal.Action == ACTION_BUY {
		if gain7d < -15.0 {
			// Hard block: -15% in 7 days is a structural breakdown — catching
			// a falling knife that will keep falling. No exceptions.
			signal.Action = ACTION_HOLD
			signal.Conviction = 0
			signal.Confidence = 0.0
			signal.Strategy = "HOLD"
			signal.Reason = fmt.Sprintf("FALLING KNIFE BLOCK: 7D %.1f%% < -15%% — structural breakdown, BUY blocked.", gain7d)
			return signal
		}
		if gain7d < -8.0 && signal.Confidence > 0.50 {
			signal.Confidence = math.Min(signal.Confidence, 0.45)
			signal.Conviction = 1
			signal.Reason += fmt.Sprintf(" | 🔪 falling knife: 7D %.1f%% < -8%% — forced Conv1 (%.0f%% conf)", gain7d, signal.Confidence*100)
		}
		// Buying into extended rallies — the #1 cause of large BUY losers
		if gain7d > 15.0 && signal.Confidence > 0.50 {
			signal.Confidence = math.Min(signal.Confidence, 0.55)
			if signal.Conviction > 1 {
				signal.Conviction--
			}
			signal.Reason += fmt.Sprintf(" | 🚀 extended rally: 7D %.1f%% > 15%% — buying top risk, reduced to %.0f%%", gain7d, signal.Confidence*100)
		}
	}
	if signal.Action == ACTION_SELL && gain7d < -10.0 && signal.Confidence > 0.50 {
		signal.Confidence = math.Min(signal.Confidence, 0.55)
		if signal.Conviction > 1 {
			signal.Conviction--
		}
		signal.Reason += fmt.Sprintf(" | 🐻 late sell: 7D %.1f%% < -10%% — move played out, reduced to %.0f%%", gain7d, signal.Confidence*100)
	}

	// ── BUY Conviction Floor ──────────────────────────────────────────
	// CRITICAL FIX (2026-09-02): BUY signals at Conviction 1 are never
	// executed (executor requires Conv2+ anyway), but they can waste AI
	// council API calls and pollute signal ranking. Force Conv1 BUY to HOLD.
	// SELL Conv1 is allowed through for council evaluation — sells have
	// healthy 0.98x ratio and the extra screening is valuable.
	// FIX (2026-09-03): Reverted — Conv1 BUY with S0 verified and vol surge
	// or ADX>40 bypass now goes through. The execute path checks Conv2+ anyway,
	// but the boost logic in agreeCount==1 already handles Conv2 promotion.
	// Only pure Conv0 (no S0, no boost) BUY is blocked.
	if signal.Action == ACTION_BUY && signal.Conviction <= 0 {
		signal.Action = ACTION_HOLD
		signal.Strategy = "HOLD"
		signal.Reason = "BUY Conviction Floor: no verification — insufficient evidence for long entries."
		signal.Conviction = 0
		signal.Confidence = 0.0
	}

	return signal
}
