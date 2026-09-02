package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	TAKER_FEE_RATE = 0.00055 // 0.055% per side on Bybit perpetuals
	MIN_ORDER_USD  = 5.0     // Bybit minimum notional

	// MAX_PORTFOLIO_RISK_PCT caps the SUM of open risk across every live
	// position, as a fraction of wallet equity. The 5-position count limit
	// does not bound risk: five positions at 2.5% each is 12.5% of the account
	// on a single correlated move. This cap is enforced independently of the
	// count limit — a new entry is refused when open risk + the new position's
	// risk would exceed it, regardless of how few positions are open.
	MAX_PORTFOLIO_RISK_PCT = 0.03 // 3% total open risk
)

type ExecutionEngine struct {
	Client       *BybitClient
	AICouncil    *AICouncilClient
	MaxLeverage  int
	TotalCapital float64

	// RiskMultiplier scales every position's risk budget. Set from the
	// peak-relative drawdown guard (see equity_guard.go): 1.0 at normal
	// equity, 0.75 past −5% from peak, 0.50 past −8%. Zero blocks entries.
	RiskMultiplier float64
}

func NewExecutionEngine(client *BybitClient, capital float64) *ExecutionEngine {
	return &ExecutionEngine{
		Client:         client,
		AICouncil:      NewAICouncilClient(),
		MaxLeverage:    3,
		TotalCapital:   capital,
		RiskMultiplier: 1.0,
	}
}

// EvaluateSignalCouncil runs the AI Council gate for display/scan purposes without
// placing any orders. Returns the council result and a human-readable line for the dashboard.
func (e *ExecutionEngine) EvaluateSignalCouncil(sig StrategySignal, asset *AssetSnapshot) (CouncilResult, string) {
	var councilResult CouncilResult
	var councilLine string

	if e.AICouncil == nil || e.AICouncil.APIKey == "" {
		councilResult = CouncilResult{
			FinalVerdict:     "UNAVAILABLE",
			Confidence:       0.0,
			ConsensusSummary: "No OpenRouter API key configured",
		}
		councilLine = "   🤖 AI Council: UNAVAILABLE (no API key)"
		return councilResult, councilLine
	}

	// Only evaluate non-HOLD signals to save API costs
	if sig.Action == ACTION_HOLD {
		councilLine = "   🤖 AI Council: SKIPPED (signal is HOLD)"
		return councilResult, councilLine
	}

	sentimentBlock := ""
	if globalSentimentReport.TopCryptoNews != "" || len(globalSentimentReport.SymbolSentiment) > 0 {
		sentimentBlock = globalSentimentReport.FormatForPrompt()
	}

	r := e.AICouncil.EvaluateSignal(sig, asset, sentimentBlock)
	councilResult = r

	if r.ErroredCount == len(councilMembers) {
		councilLine = fmt.Sprintf("   ❌ AI Council: ALL %d MODELS ERRORED", len(councilMembers))
	} else {
		councilLine = fmt.Sprintf("   🤖 AI Council: %s (%.0f%%) — %d confirm / %d reject (%d errors)",
			r.FinalVerdict, r.Confidence*100,
			r.ConfirmCount, r.RejectCount, r.ErroredCount)
	}

	return councilResult, councilLine
}

// ExecuteBracketTrade places a limit bracket order (entry + SL + TP) after passing
// an AI validation gate. Every signal — executed or rejected — is written to trade_log.jsonl
// so we can audit quality and improve over time.
func (e *ExecutionEngine) ExecuteBracketTrade(sig StrategySignal, asset *AssetSnapshot) error {
	symbol := sig.Symbol
	action := sig.Action
	price := asset.CurrentPrice
	atr := asset.Snap4h.Indicators.ATR14
	confidence := sig.Confidence
	dailyADX := asset.Snap1d.Indicators.ADX14
	candles4h := asset.Snap4h.Candles

	fmt.Printf("🛡️ Executor: %s %s @ $%.4f (conv=%d conf=%.0f%% ADX=%.1f)\n",
		action, symbol, price, sig.Conviction, confidence*100, dailyADX)

	if confidence < 0.70 {
		return fmt.Errorf("ABORT: confidence %.2f below execution floor 0.70", confidence)
	}

	// ── Build trade log skeleton (filled in as we progress) ──────────
	entry := TradeLogEntry{
		Timestamp:   time.Now().UTC(),
		Symbol:      symbol,
		Side:        string(action),
		OrderType:   "Limit",
		EntryPrice:  price,
		ATR:         atr,
		ADX:         dailyADX,
		RSI:         asset.Snap4h.Indicators.RSI14,
		WilliamsR:   asset.Snap4h.Indicators.WilliamsR,
		BBWidth:     asset.Snap4h.Indicators.BBWidth,
		FundingRate: asset.Funding.CurrentRate,
		S4Active:    sig.S4.Active,
		S5Active:    sig.S5.Active,
		Conviction:  sig.Conviction,
		Confidence:  confidence,
		Strategy:    sig.Strategy,
		Reason:      sig.Reason,
		WalletBal:   e.TotalCapital,
		Regime:      sig.Regime.String(),
	}
	for _, sub := range []struct {
		name   string
		active bool
		action SignalAction
	}{
		{"S1", sig.S1.Active, sig.S1.Action},
		{"S2", sig.S2.Active, sig.S2.Action},
		{"S3", sig.S3.Active, sig.S3.Action},
		{"S4", sig.S4.Active, sig.S4.Action},
		{"S5", sig.S5.Active, sig.S5.Action},
	} {
		if sub.active && sub.action == action {
			entry.ConfirmingStrategies = append(entry.ConfirmingStrategies, sub.name)
		}
	}
	// Kronos is logged on every trade regardless of whether it gated this one.
	// Counterfactual measurement of the overlay needs its call recorded even
	// when the call was ignored.
	if sig.Kronos != nil {
		entry.KronosDirection = sig.Kronos.Direction
		entry.KronosConfidence = sig.Kronos.Confidence
	}

	// ── AI Council Gate ────────────────────────────────────────────────
	// Uses the pre-computed council result from the signal loop.
	// If unavailable, evaluates on-demand (no sentiment context in that case).
	var councilResult *CouncilResult
	if sig.AICouncilResult != nil {
		councilResult = sig.AICouncilResult
	} else {
		sentimentBlock := ""
		if globalSentimentReport.TopCryptoNews != "" || len(globalSentimentReport.SymbolSentiment) > 0 {
			sentimentBlock = globalSentimentReport.FormatForPrompt()
		}
		r := e.AICouncil.EvaluateSignal(sig, asset, sentimentBlock)
		councilResult = &r
		sig.AICouncilResult = councilResult
	}
	entry.AIVerdict = councilResult.FinalVerdict
	entry.AIConfidence = councilResult.Confidence
	entry.AIReason = councilResult.ConsensusSummary
	for _, v := range councilResult.Votes {
		entry.CouncilVotes = append(entry.CouncilVotes, fmt.Sprintf("%s:%s", v.Model, v.Verdict))
	}
	fmt.Printf("   🤖 AI Council: %s (%.0f%%) — %d confirm / %d reject (%d errors)\n",
		councilResult.FinalVerdict, councilResult.Confidence*100,
		councilResult.ConfirmCount, councilResult.RejectCount, councilResult.ErroredCount)

	if councilResult.FinalVerdict == "REJECTED" {
		entry.Executed = false
		entry.SkipReason = truncate("AI Council rejected: "+councilResult.ConsensusSummary, 200)
		AppendTradeLog(entry)
		return fmt.Errorf("AI COUNCIL: signal rejected — %s", truncate(councilResult.ConsensusSummary, 100))
	}

	// ── Dynamic risk sizing by confidence ────────────────────────────
	// The previous table (2.5% / 2.0% / 1.5%) contradicted README.md and, at
	// the 5-position limit, permitted up to 12.5% of equity at risk at once.
	// These values are deliberately below the README figures: with no
	// measured positive expectancy on this system, position risk is set to
	// survive a long losing run rather than to maximise a hoped-for edge.
	var riskPct float64
	switch {
	case confidence >= 0.85:
		riskPct = 0.0075 // 0.75% — META / high-conviction (was 2.5%)
	case confidence >= 0.75:
		riskPct = 0.0050 // 0.50% — CONFIRMED (was 2.0%)
	default:
		riskPct = 0.0035 // 0.35% — baseline Conv2/70% (was 1.5%)
	}

	// Drawdown-scaled risk. A zero multiplier means the guard has blocked
	// entries entirely; treat a missing (zero-value) multiplier as unrestricted
	// so a caller that never set it is not silently frozen.
	if e.RiskMultiplier == 0 && drawdownGuard.Tier == "" {
		e.RiskMultiplier = 1.0
	}
	if e.RiskMultiplier <= 0 {
		entry.Executed = false
		entry.SkipReason = fmt.Sprintf("DRAWDOWN GUARD [%s]: entries blocked (%.2f%% below peak $%.2f)",
			drawdownGuard.Tier, drawdownGuard.DrawdownPct, drawdownGuard.PeakEquity)
		AppendTradeLog(entry)
		return fmt.Errorf("DRAWDOWN GUARD [%s]: new entries blocked at %.2f%% below peak",
			drawdownGuard.Tier, drawdownGuard.DrawdownPct)
	}
	if e.RiskMultiplier < 1.0 {
		fmt.Printf("   📉 Drawdown guard [%s]: risk %.2f%% × %.2f = %.2f%%\n",
			drawdownGuard.Tier, riskPct*100, e.RiskMultiplier, riskPct*e.RiskMultiplier*100)
	}
	riskPct *= e.RiskMultiplier
	entry.RiskPct = riskPct

	// ── Portfolio-wide open-risk cap ─────────────────────────────────
	// Independent of the 5-position count limit. Refuses the entry when the
	// sum of live open risk plus this position's risk would breach the cap.
	if openRiskPct, riskErr := e.currentOpenRiskPct(); riskErr != nil {
		// Fail closed: if open risk cannot be verified, do not add exposure.
		entry.Executed = false
		entry.SkipReason = truncate("RISK CAP: cannot verify open risk: "+riskErr.Error(), 200)
		AppendTradeLog(entry)
		return fmt.Errorf("RISK CAP: cannot verify current open risk — refusing entry: %w", riskErr)
	} else if openRiskPct+riskPct > MAX_PORTFOLIO_RISK_PCT {
		entry.Executed = false
		entry.SkipReason = fmt.Sprintf("RISK CAP: open risk %.2f%% + new %.2f%% > %.2f%% cap",
			openRiskPct*100, riskPct*100, MAX_PORTFOLIO_RISK_PCT*100)
		AppendTradeLog(entry)
		return fmt.Errorf("RISK CAP: open portfolio risk %.2f%% + new position %.2f%% exceeds %.2f%% cap",
			openRiskPct*100, riskPct*100, MAX_PORTFOLIO_RISK_PCT*100)
	} else {
		fmt.Printf("   🧮 Open portfolio risk: %.2f%% + %.2f%% new = %.2f%% (cap %.2f%%)\n",
			openRiskPct*100, riskPct*100, (openRiskPct+riskPct)*100, MAX_PORTFOLIO_RISK_PCT*100)
	}

	// ── ADX-aware SL/TP multipliers ───────────────────────────────────
	var slMult, tpMult float64
	switch {
	case dailyADX < 25:
		slMult = 3.0
		tpMult = 7.5 // 2.5:1 RR
	case dailyADX < 40:
		slMult = 2.5
		tpMult = 6.25 // 2.5:1 RR
	default:
		slMult = 2.0
		tpMult = 5.0 // 2.5:1 RR
	}
	atrDist := atr * slMult

	// ── Fee-adjusted minimum R:R gate ────────────────────────────────
	roundTripFriction := price * (TAKER_FEE_RATE*2 + 0.001)
	if atrDist < roundTripFriction*3 {
		entry.Executed = false
		entry.SkipReason = fmt.Sprintf("FEE GATE: SL dist $%.4f < 3× friction $%.4f", atrDist, roundTripFriction*3)
		AppendTradeLog(entry)
		return fmt.Errorf("FEE GATE: SL distance $%.4f < 3× friction cost $%.4f — insufficient R:R after fees",
			atrDist, roundTripFriction*3)
	}

	// ── Set Isolated Margin ───────────────────────────────────────────
	// NOTE: On Bybit Unified Trading Accounts (UTA), tradeMode=1 (isolated)
	// may be rejected if the account is in portfolio margin mode. We log the
	// result so you can see in the output whether isolated margin is actually set.
	// If positions show "Cross" on Bybit, the account type doesn't support
	// per-symbol isolation — consider switching to a Standard account.
	{
		leverageResp, leverageErr := e.Client.PostPrivateRequest("/v5/position/set-leverage", map[string]interface{}{
			"category":     "linear",
			"symbol":       symbol,
			"tradeMode":    1,
			"buyLeverage":  strconv.Itoa(e.MaxLeverage),
			"sellLeverage": strconv.Itoa(e.MaxLeverage),
		})
		if leverageErr != nil {
			fmt.Printf("   ⚠️ Margin mode set FAILED (%v) — position may use Cross margin\n", leverageErr)
		} else {
			var lvRes struct {
				RetCode int    `json:"retCode"`
				RetMsg  string `json:"retMsg"`
			}
			if json.Unmarshal(leverageResp, &lvRes) == nil {
				if lvRes.RetCode == 0 {
					fmt.Printf("   ✅ Isolated margin set: %dx leverage\n", e.MaxLeverage)
				} else {
					fmt.Printf("   ⚠️ Margin mode rejected (code %d: %s) — position may use Cross margin\n",
						lvRes.RetCode, lvRes.RetMsg)
				}
			}
		}
	}

	// ── Position sizing ───────────────────────────────────────────────
	var stopLossPrice, takeProfitPrice, limitPriceStr, side string
	var positionSizeTokens float64

	if e.TotalCapital < 20.0 {
		posUSD := math.Max(MIN_ORDER_USD, math.Min(e.TotalCapital*0.85, 10.0))
		positionSizeTokens = posUSD / price
	} else {
		riskAmount := e.TotalCapital * riskPct
		positionSizeTokens = riskAmount / atrDist
	}

	switch action {
	case ACTION_BUY:
		side = "Buy"
		stopLossPrice = fmt.Sprintf("%.4f", price-atrDist)
		takeProfitPrice = fmt.Sprintf("%.4f", price+(atr*tpMult))
		limitPriceStr = fmt.Sprintf("%.4f", price)
	case ACTION_SELL:
		side = "Sell"
		stopLossPrice = fmt.Sprintf("%.4f", price+atrDist)
		takeProfitPrice = fmt.Sprintf("%.4f", price-(atr*tpMult))
		limitPriceStr = fmt.Sprintf("%.4f", price)
	default:
		return nil
	}

	// ── Snap to instrument precision ─────────────────────────────────
	var qtyStr string
	info, err := e.Client.GetInstrumentInfo(symbol)
	if err == nil && info.QtyStep > 0 {
		if positionSizeTokens < info.MinQty {
			positionSizeTokens = info.MinQty
		}
		positionSizeTokens = math.Floor(positionSizeTokens/info.QtyStep) * info.QtyStep
		qtyStr = strconv.FormatFloat(positionSizeTokens, 'f', -1, 64)

		if info.PriceStep > 0 {
			tpVal, _ := strconv.ParseFloat(takeProfitPrice, 64)
			slVal, _ := strconv.ParseFloat(stopLossPrice, 64)
			limVal, _ := strconv.ParseFloat(limitPriceStr, 64)
			tpVal = math.Floor(tpVal/info.PriceStep) * info.PriceStep
			slVal = math.Floor(slVal/info.PriceStep) * info.PriceStep
			limVal = math.Floor(limVal/info.PriceStep) * info.PriceStep
			takeProfitPrice = strconv.FormatFloat(tpVal, 'f', -1, 64)
			stopLossPrice = strconv.FormatFloat(slVal, 'f', -1, 64)
			limitPriceStr = strconv.FormatFloat(limVal, 'f', -1, 64)
		}
	} else {
		switch {
		case positionSizeTokens < 1.0:
			qtyStr = strconv.FormatFloat(math.Trunc(positionSizeTokens*1000)/1000, 'f', 3, 64)
		case positionSizeTokens < 100.0:
			qtyStr = strconv.FormatFloat(math.Trunc(positionSizeTokens*10)/10, 'f', 1, 64)
		default:
			qtyStr = strconv.FormatFloat(math.Trunc(positionSizeTokens), 'f', 0, 64)
		}
		qtyStr = strings.TrimRight(strings.TrimRight(qtyStr, "0"), ".")
	}

	if qtyStr == "" || qtyStr == "0" {
		entry.Executed = false
		entry.SkipReason = "position sizing yielded zero contracts"
		AppendTradeLog(entry)
		return fmt.Errorf("position sizing yielded zero contracts for %s", symbol)
	}

	// ── Minimum notional check ────────────────────────────────────────
	orderValue, _ := strconv.ParseFloat(qtyStr, 64)
	orderValue *= price
	if orderValue < MIN_ORDER_USD && orderValue > 0 {
		scaleFactor := MIN_ORDER_USD / orderValue
		maxFromCapital := (e.TotalCapital * 0.85) / price
		if newQty := math.Min(orderValue*scaleFactor/price, maxFromCapital); newQty*price >= MIN_ORDER_USD {
			qtyStr = strconv.FormatFloat(newQty, 'f', -1, 64)
			// Keep positionSizeTokens in step with qtyStr. The SIZE GUARD below
			// validates positionSizeTokens while the order submits qtyStr, so
			// leaving this stale meant the guard checked the PRE-scale-up size
			// and the exchange received the larger one — exactly the case the
			// guard exists to catch. This matters most at deep drawdown on a
			// small account, where the risk multiplier shrinks the order below
			// the minimum notional and this branch scales it back up.
			positionSizeTokens = newQty
		} else {
			entry.Executed = false
			entry.SkipReason = fmt.Sprintf("order value $%.2f below minimum and wallet too small", orderValue)
			AppendTradeLog(entry)
			return fmt.Errorf("order value $%.2f below minimum $%.2f and wallet too small to scale", orderValue, MIN_ORDER_USD)
		}
	}

	// ── Size risk guard ───────────────────────────────────────────────
	slPrice, _ := strconv.ParseFloat(stopLossPrice, 64)
	if slPrice > 0 && positionSizeTokens > 0 {
		actualRisk := math.Abs(price-slPrice) * positionSizeTokens
		targetRisk := e.TotalCapital * riskPct
		if actualRisk > targetRisk*2.0 {
			entry.Executed = false
			entry.SkipReason = fmt.Sprintf("SIZE GUARD: actual SL risk $%.2f > 2.0× target $%.2f", actualRisk, targetRisk)
			AppendTradeLog(entry)
			return fmt.Errorf("SIZE GUARD: actual SL risk $%.2f > 2.0× target $%.2f", actualRisk, targetRisk)
		}
	}

	fmt.Printf("   📐 qty=%s limit=%s | SL=%s | TP=%s | risk=%.1f%%\n",
		qtyStr, limitPriceStr, stopLossPrice, takeProfitPrice, riskPct*100)

	// ── Place limit bracket order ─────────────────────────────────────
	respBytes, err := e.Client.PostPrivateRequest("/v5/order/create", map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
		"orderType":   "Limit",
		"price":       limitPriceStr,
		"qty":         qtyStr,
		"timeInForce": "GTC",
		"takeProfit":  takeProfitPrice,
		"stopLoss":    stopLossPrice,
	})
	if err != nil {
		entry.Executed = false
		entry.SkipReason = "API error: " + err.Error()
		entry.Qty = qtyStr
		entry.StopLoss = stopLossPrice
		entry.TakeProfit = takeProfitPrice
		AppendTradeLog(entry)
		return err
	}

	var orderRes struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(respBytes, &orderRes); err != nil {
		entry.Executed = false
		entry.SkipReason = "response parse error: " + err.Error()
		AppendTradeLog(entry)
		return err
	}
	if orderRes.RetCode != 0 {
		entry.Executed = false
		entry.SkipReason = fmt.Sprintf("Bybit rejected: %s (code %d)", orderRes.RetMsg, orderRes.RetCode)
		entry.Qty = qtyStr
		entry.StopLoss = stopLossPrice
		entry.TakeProfit = takeProfitPrice
		AppendTradeLog(entry)
		return fmt.Errorf("bybit rejected order: %s (code %d)", orderRes.RetMsg, orderRes.RetCode)
	}

	// ── Activate EMA20-based trailing stop ────────────────────────────
	// Activation price is set at the MIDPOINT between entry and TP so the
	// trailing stop locks in profit well before the TP level.
	// This means the two orders compete cleanly:
	//   - Price hits midpoint  → trailing stop activates, starts following
	//   - Price hits full TP   → TP fires first, position closed, trail cancelled
	//   - Price reverses early → trailing stop closes at retracement (still in profit)
	// Setting activePrice = takeProfitPrice (the old bug) caused both to fire
	// at the same level, often closing below TP due to the retracement offset.
	ema20 := CalculateEMA(candles4h, 20)
	if ema20 > 0 {
		trailDist := math.Abs(price - ema20)
		if trailDist > 0 {
			tpVal, _ := strconv.ParseFloat(takeProfitPrice, 64)
			var activationPrice float64
			if action == ACTION_BUY {
				activationPrice = price + (tpVal-price)*0.5 // midpoint entry → TP
			} else {
				activationPrice = price - (price-tpVal)*0.5 // midpoint entry → TP
			}
			activationStr := strconv.FormatFloat(activationPrice, 'f', 4, 64)
			if info.PriceStep > 0 {
				activationPrice = math.Floor(activationPrice/info.PriceStep) * info.PriceStep
				activationStr = strconv.FormatFloat(activationPrice, 'f', -1, 64)
			}
			trailPayload := map[string]interface{}{
				"category":     "linear",
				"symbol":       symbol,
				"trailingStop": strconv.FormatFloat(trailDist, 'f', 4, 64),
				"activePrice":  activationStr,
			}
			if _, trailErr := e.Client.PostPrivateRequest("/v5/position/trading-stop", trailPayload); trailErr != nil {
				fmt.Printf("   ⚠️ Trailing stop failed: %v\n", trailErr)
			} else {
				fmt.Printf("   🎯 Trailing stop: dist=$%.4f activates at $%s (midpoint to TP)\n", trailDist, activationStr)
			}
		}
	}

	// ── Log successful trade ──────────────────────────────────────────
	entry.Qty = qtyStr
	entry.StopLoss = stopLossPrice
	entry.TakeProfit = takeProfitPrice
	entry.RiskPct = riskPct
	entry.Executed = true
	AppendTradeLog(entry)

	fmt.Printf("✅ LIMIT ORDER PLACED: %s %s qty=%s limit=$%s | SL=$%s TP=$%s\n",
		side, symbol, qtyStr, limitPriceStr, stopLossPrice, takeProfitPrice)
	return nil
}

// currentOpenRiskPct returns the SUM of open risk across every live linear
// position, expressed as a fraction of TotalCapital.
//
// Risk for one position is the distance from average entry to its stop loss,
// multiplied by position size: the dollar amount that is lost if the stop is
// hit. A position with NO stop loss set is charged at full notional — it can
// in principle lose everything, and it should block further entries until a
// stop is attached.
//
// This is the quantity the 5-position count limit fails to bound. Count limits
// constrain how MANY bets are open; they say nothing about how large each one
// is, so five maximum-size entries can put a multiple of the intended risk
// budget on a single correlated move.
func (e *ExecutionEngine) currentOpenRiskPct() (float64, error) {
	if e.TotalCapital <= 0 {
		return 0, fmt.Errorf("total capital is %.2f — cannot compute risk fraction", e.TotalCapital)
	}

	respBytes, err := e.Client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		return 0, fmt.Errorf("position list fetch: %w", err)
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol   string `json:"symbol"`
				Side     string `json:"side"`
				Size     string `json:"size"`
				AvgPrice string `json:"avgPrice"`
				StopLoss string `json:"stopLoss"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, fmt.Errorf("parse position list: %w", err)
	}
	if resp.RetCode != 0 {
		return 0, fmt.Errorf("bybit error [%d]: %s", resp.RetCode, resp.RetMsg)
	}

	totalRiskUSD := 0.0
	for _, pos := range resp.Result.List {
		size, _ := strconv.ParseFloat(pos.Size, 64)
		if size <= 0 {
			continue
		}
		avgPrice, _ := strconv.ParseFloat(pos.AvgPrice, 64)
		if avgPrice <= 0 {
			continue
		}
		notional := avgPrice * size

		// Bybit returns "" or "0" for an unset stop loss.
		slPrice, slErr := strconv.ParseFloat(pos.StopLoss, 64)
		if slErr != nil || slPrice <= 0 {
			totalRiskUSD += notional
			fmt.Printf("   ⚠️ %s has NO stop loss — counting full notional $%.2f as open risk\n",
				pos.Symbol, notional)
			continue
		}

		// A stop on the wrong side of entry (already breached, or misconfigured)
		// offers no protection — charge full notional rather than a negative risk.
		adverse := 0.0
		switch pos.Side {
		case "Buy":
			adverse = avgPrice - slPrice
		case "Sell":
			adverse = slPrice - avgPrice
		default:
			adverse = math.Abs(avgPrice - slPrice)
		}
		if adverse <= 0 {
			totalRiskUSD += notional
			fmt.Printf("   ⚠️ %s stop $%.6f is not protective vs entry $%.6f — counting full notional $%.2f\n",
				pos.Symbol, slPrice, avgPrice, notional)
			continue
		}

		risk := adverse * size
		if risk > notional {
			risk = notional // cannot lose more than the position is worth
		}
		totalRiskUSD += risk
	}

	return totalRiskUSD / e.TotalCapital, nil
}
