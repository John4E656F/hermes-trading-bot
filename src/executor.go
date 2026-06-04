package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	TAKER_FEE_RATE = 0.00055 // 0.055% per side on Bybit perpetuals
	MIN_ORDER_USD  = 5.0     // Bybit minimum notional
)

type ExecutionEngine struct {
	Client       *BybitClient
	MaxLeverage  int
	TotalCapital float64
}

func NewExecutionEngine(client *BybitClient, capital float64) *ExecutionEngine {
	return &ExecutionEngine{
		Client:       client,
		MaxLeverage:  3,
		TotalCapital: capital,
	}
}

// ExecuteBracketTrade places a bracket order (entry + SL + TP) and immediately
// registers a break-even conditional order so profits are protected once 1R is gained.
func (e *ExecutionEngine) ExecuteBracketTrade(symbol string, action SignalAction, price, atr, confidence, dailyADX float64, candles4h []Candle) error {
	fmt.Printf("🛡️ Executor: %s %s @ $%.4f (conf=%.0f%%, ADX=%.1f)\n", action, symbol, price, confidence*100, dailyADX)

	if confidence < 0.70 {
		return fmt.Errorf("ABORT: confidence %.2f below execution floor 0.70", confidence)
	}

	// ── Dynamic risk sizing by confidence ────────────────────────────
	var riskPct float64
	switch {
	case confidence >= 0.85:
		riskPct = 0.025 // 2.5% — META / high-conviction
	case confidence >= 0.75:
		riskPct = 0.020 // 2.0%
	default:
		riskPct = 0.015 // 1.5% — baseline Conv2/70%
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
	// Round-trip fee + estimated slippage = ~0.20% of position value.
	// TP distance must be >= 3× the fee cost to have a positive expected value.
	roundTripFriction := price * (TAKER_FEE_RATE*2 + 0.001) // fee + slippage
	if atrDist < roundTripFriction*3 {
		return fmt.Errorf("FEE GATE: SL distance $%.4f < 3× friction cost $%.4f — insufficient R:R after fees",
			atrDist, roundTripFriction*3)
	}

	// ── Set Isolated Margin ───────────────────────────────────────────
	e.Client.PostPrivateRequest("/v5/position/set-leverage", map[string]interface{}{
		"category": "linear", "symbol": symbol,
		"tradeMode": 1,
		"buyLeverage":  strconv.Itoa(e.MaxLeverage),
		"sellLeverage": strconv.Itoa(e.MaxLeverage),
	})

<<<<<<< HEAD
// 2. Risk Sizing Computations — uses dynamic riskPct from confidence
		// ADX-aware SL/TP: tighter SL means better RR
		// Ranging (ADX<25): 2x ATR SL, 6x ATR TP → 1:3 RR (need 25% win rate to break even)
		// Mixed  (25-40):  2x ATR SL, 5x ATR TP → 1:2.5 RR (need 29% win rate)
		// Trend  (ADX>40): 1.5x ATR SL, 5x ATR TP → 1:3.3 RR (need 23% win rate)
		var slMultiplier float64
		var tpMultiplier float64
		switch {
		case dailyADX < 25:
			slMultiplier = 2.0
			tpMultiplier = 6.0
		case dailyADX < 40:
			slMultiplier = 2.0
			tpMultiplier = 5.0
		default:
			slMultiplier = 1.5
			tpMultiplier = 5.0
		}
		var stopLossPrice, takeProfitPrice, side string
		atrDistance := atr * slMultiplier

	// ── Micro-wallet mode (balance < $20): go all-in on the strongest signal ──
	const MIN_ORDER_USD = 5.0
	var positionSizeTokens float64

	if e.TotalCapital < 20.0 {
		// All-in on this single best signal at the minimum contract value
		positionSizeUSD := math.Max(MIN_ORDER_USD, math.Min(e.TotalCapital*0.85, 10.0))
		positionSizeTokens = positionSizeUSD / price

		if action == ACTION_BUY {
			side = "Buy"
			stopLossPrice = strconv.FormatFloat(price-atrDistance, 'f', 8, 64)
			takeProfitPrice = strconv.FormatFloat(price+(atr*tpMultiplier), 'f', 8, 64)
		} else if action == ACTION_SELL {
			side = "Sell"
			stopLossPrice = strconv.FormatFloat(price+atrDistance, 'f', 8, 64)
			takeProfitPrice = strconv.FormatFloat(price-(atr*tpMultiplier), 'f', 8, 64)
		}

		// Penny-stock price protection: if TP/SL is zero after precision loss, use %-based fallback
		if tpVal, _ := strconv.ParseFloat(takeProfitPrice, 64); tpVal <= 0 {
			if action == ACTION_SELL {
				takeProfitPrice = strconv.FormatFloat(price*0.5, 'f', 8, 64)
			} else {
				takeProfitPrice = strconv.FormatFloat(price*1.5, 'f', 8, 64)
			}
		}
		if slVal, _ := strconv.ParseFloat(stopLossPrice, 64); slVal <= 0 {
			stopLossPrice = strconv.FormatFloat(price*0.5, 'f', 8, 64)
		}

		fmt.Printf("   📊 (Micro-wallet mode) position=$%.2f SL=%s TP=%s\n",
			positionSizeUSD, stopLossPrice, takeProfitPrice)

=======
	// ── Position sizing ───────────────────────────────────────────────
	var stopLossPrice, takeProfitPrice, side string
	var positionSizeTokens float64

	if e.TotalCapital < 20.0 {
		// Micro-wallet: go in at minimum viable size
		posUSD := math.Max(MIN_ORDER_USD, math.Min(e.TotalCapital*0.85, 10.0))
		positionSizeTokens = posUSD / price
>>>>>>> zen-keller-l8bGb
	} else {
		riskAmount := e.TotalCapital * riskPct
<<<<<<< HEAD
		positionSizeTokens = riskAmount / atrDistance

		if action == ACTION_BUY {
			side = "Buy"
			stopLossPrice = strconv.FormatFloat(price-atrDistance, 'f', 8, 64)
			takeProfitPrice = strconv.FormatFloat(price+(atr*tpMultiplier), 'f', 8, 64)
		} else if action == ACTION_SELL {
			side = "Sell"
			stopLossPrice = strconv.FormatFloat(price+atrDistance, 'f', 8, 64)
			takeProfitPrice = strconv.FormatFloat(price-(atr*tpMultiplier), 'f', 8, 64)
		} else {
			return nil
		}

		// Absolute minimum fallback
		if symbol == "BNBUSDT" && positionSizeTokens < 0.01 {
			positionSizeTokens = 0.01
		} else if symbol == "BTCUSDT" && positionSizeTokens < 0.001 {
			positionSizeTokens = 0.001
		} else if positionSizeTokens < 0.1 {
			positionSizeTokens = 0.1
		}
=======
		positionSizeTokens = riskAmount / atrDist
>>>>>>> zen-keller-l8bGb
	}

	switch action {
	case ACTION_BUY:
		side = "Buy"
		stopLossPrice = fmt.Sprintf("%.4f", price-atrDist)
		takeProfitPrice = fmt.Sprintf("%.4f", price+(atr*tpMult))
	case ACTION_SELL:
		side = "Sell"
		stopLossPrice = fmt.Sprintf("%.4f", price+atrDist)
		takeProfitPrice = fmt.Sprintf("%.4f", price-(atr*tpMult))
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
			tpVal = math.Floor(tpVal/info.PriceStep) * info.PriceStep
			slVal = math.Floor(slVal/info.PriceStep) * info.PriceStep
			takeProfitPrice = strconv.FormatFloat(tpVal, 'f', -1, 64)
			stopLossPrice = strconv.FormatFloat(slVal, 'f', -1, 64)
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
		} else {
			return fmt.Errorf("order value $%.2f below minimum $%.2f and wallet too small to scale", orderValue, MIN_ORDER_USD)
		}
	}

	// ── Size risk guard ───────────────────────────────────────────────
	slPrice, _ := strconv.ParseFloat(stopLossPrice, 64)
	if slPrice > 0 && positionSizeTokens > 0 {
		actualRisk := math.Abs(price-slPrice) * positionSizeTokens
		targetRisk := e.TotalCapital * riskPct
		if actualRisk > targetRisk*1.5 {
			return fmt.Errorf("SIZE GUARD: actual SL risk $%.2f > 1.5× target $%.2f", actualRisk, targetRisk)
		}
	}

	// ── Place bracket order ───────────────────────────────────────────
	fmt.Printf("   📐 qty=%s | SL=%s | TP=%s | risk_pct=%.1f%%\n", qtyStr, stopLossPrice, takeProfitPrice, riskPct*100)

	respBytes, err := e.Client.PostPrivateRequest("/v5/order/create", map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
		"orderType":   "Market",
		"qty":         qtyStr,
		"timeInForce": "GTC",
		"takeProfit":  takeProfitPrice,
		"stopLoss":    stopLossPrice,
	})
	if err != nil {
		return err
	}

	var orderRes struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(respBytes, &orderRes); err != nil {
		return err
	}
	if orderRes.RetCode != 0 {
		return fmt.Errorf("bybit rejected order: %s (code %d)", orderRes.RetMsg, orderRes.RetCode)
	}

	// ── Activate EMA20-based trailing stop ────────────────────────────
	ema20 := CalculateEMA(candles4h, 20)
	if ema20 > 0 {
		trailDist := math.Abs(price - ema20)
		if trailDist > 0 {
			trailPayload := map[string]interface{}{
				"category":     "linear",
				"symbol":       symbol,
				"trailingStop": strconv.FormatFloat(trailDist, 'f', 4, 64),
				"activePrice":  takeProfitPrice,
			}
			if _, trailErr := e.Client.PostPrivateRequest("/v5/position/trading-stop", trailPayload); trailErr != nil {
				fmt.Printf("   ⚠️ Trailing stop failed: %v\n", trailErr)
			} else {
				fmt.Printf("   🎯 Trailing stop: dist=$%.4f activates at $%s\n", trailDist, takeProfitPrice)
			}
		}
	}

	fmt.Printf("✅ ORDER PLACED: %s %s qty=%s | SL=$%s TP=$%s\n", side, symbol, qtyStr, stopLossPrice, takeProfitPrice)
	return nil
}
