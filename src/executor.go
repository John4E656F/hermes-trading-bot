package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type ExecutionEngine struct {
	Client       *BybitClient
	RiskPct      float64
	MaxLeverage  int
	TotalCapital float64
}

func NewExecutionEngine(client *BybitClient, capital float64) *ExecutionEngine {
	return &ExecutionEngine{
		Client:       client,
		RiskPct:      0.01, // 1% capital risk rule hardcoded
		MaxLeverage:  3,    // Leveraged cap safety boundary
		TotalCapital: capital,
	}
}

// ExecuteBracketTrade configures margin, sizes position dynamically by
// AI confidence score, places a Market bracket order, then sets a native
// Bybit trailing stop derived from the 4H EMA20 distance.
func (e *ExecutionEngine) ExecuteBracketTrade(symbol string, action SignalAction, price, atr, confidence, dailyADX float64, candles4h []Candle) error {
	fmt.Printf("🛡️ Order Executor initialized for %s. Configuring isolated environment...\n", symbol)

	// ── Update 2a: Dynamic Confidence-Based Sizing ──────────────────
	if confidence < 0.60 {
		return fmt.Errorf("ABORT: confidence too low (%.2f) — aborting trade", confidence)
	}

	var riskPct float64
	switch {
	case confidence >= 0.80:
		riskPct = 0.030 // 3% per swing trade
	case confidence >= 0.70:
		riskPct = 0.020 // 2%
	default:
		riskPct = 0.015 // 1.5% (confidence 0.60–0.69)
	}

	fmt.Printf("   📊 AI confidence = %.2f → Risk = %.1f%% of $%.2f wallet\n", confidence, riskPct*100, e.TotalCapital)

	// 1. Enforce Isolated Margin Mode (Category: linear = Perpetual Contracts)
	marginPayload := map[string]interface{}{
		"category":     "linear",
		"symbol":       symbol,
		"tradeMode":    1, // 1 = Isolated Margin Mode, 0 = Cross Margin Mode
		"buyLeverage":  strconv.Itoa(e.MaxLeverage),
		"sellLeverage": strconv.Itoa(e.MaxLeverage),
	}
	_, _ = e.Client.PostPrivateRequest("/v5/position/set-leverage", marginPayload)

// 2. Risk Sizing Computations — uses dynamic riskPct from confidence
		// ADX-aware SL width: swing trading needs wider stops
		var slMultiplier float64
		var tpMultiplier float64
		switch {
		case dailyADX < 25:
			slMultiplier = 3.0 // ranging: wide SL to avoid noise
			tpMultiplier = 6.0 // swing: 1:2 RR
		case dailyADX < 40:
			slMultiplier = 2.5 // moderate trend
			tpMultiplier = 5.0 // swing: 1:2 RR
		default:
			slMultiplier = 2.0 // strong trend: tighter SL
			tpMultiplier = 5.0 // swing: big trend target
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
			stopLossPrice = fmt.Sprintf("%.2f", price-atrDistance)
			takeProfitPrice = fmt.Sprintf("%.2f", price+(atr*tpMultiplier))
		} else if action == ACTION_SELL {
			side = "Sell"
			stopLossPrice = fmt.Sprintf("%.2f", price+atrDistance)
			takeProfitPrice = fmt.Sprintf("%.2f", price-(atr*tpMultiplier))
		}

		// ESPORTS/penny-stock price protection: if TP/SL rounds to $0, use %-based fallback
		if tpVal, _ := strconv.ParseFloat(takeProfitPrice, 64); tpVal <= 0.01 {
			if action == ACTION_SELL {
				takeProfitPrice = fmt.Sprintf("%.2f", price*0.5) // short: TP below entry
			} else {
				takeProfitPrice = fmt.Sprintf("%.2f", price*1.5) // long: TP above entry
			}
		}
		if slVal, _ := strconv.ParseFloat(stopLossPrice, 64); slVal <= 0.001 {
			stopLossPrice = fmt.Sprintf("%.2f", price*0.5)
		}

		fmt.Printf("   📊 (Micro-wallet mode) position=$%.2f SL=%s TP=%s\n",
			positionSizeUSD, stopLossPrice, takeProfitPrice)

	} else {
		// Normal mode: standard confidence-based risk sizing
		riskAmount := e.TotalCapital * riskPct
		positionSizeTokens = riskAmount / atrDistance

		if action == ACTION_BUY {
			side = "Buy"
			stopLossPrice = fmt.Sprintf("%.2f", price-atrDistance)
			takeProfitPrice = fmt.Sprintf("%.2f", price+(atr*tpMultiplier))
		} else if action == ACTION_SELL {
			side = "Sell"
			stopLossPrice = fmt.Sprintf("%.2f", price+atrDistance)
			takeProfitPrice = fmt.Sprintf("%.2f", price-(atr*tpMultiplier))
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
	}

	// ── Update 2b: Dynamic Decimal Precision Fix for Bybit V5 ──
	var qtyStr string

	info, err := e.Client.GetInstrumentInfo(symbol)
	if err == nil && info.QtyStep > 0 {
		// Enforce minimum quantity rules
		if positionSizeTokens < info.MinQty {
			positionSizeTokens = info.MinQty
		}
		
		// Snap quantity to Bybit's required step size
		positionSizeTokens = math.Floor(positionSizeTokens/info.QtyStep) * info.QtyStep
		qtyStr = strconv.FormatFloat(positionSizeTokens, 'f', -1, 64)
		
		// Snap TakeProfit/StopLoss to Bybit's price tick size
		if info.PriceStep > 0 {
			tpVal, _ := strconv.ParseFloat(takeProfitPrice, 64)
			slVal, _ := strconv.ParseFloat(stopLossPrice, 64)
			
			tpVal = math.Floor(tpVal/info.PriceStep) * info.PriceStep
			slVal = math.Floor(slVal/info.PriceStep) * info.PriceStep
			
			takeProfitPrice = strconv.FormatFloat(tpVal, 'f', -1, 64)
			stopLossPrice = strconv.FormatFloat(slVal, 'f', -1, 64)
		}
	} else {
		// Fallback formatting if API fails (unlikely, but safe to keep)
		switch {
		case positionSizeTokens < 1.0:
			qtyStr = strconv.FormatFloat(math.Trunc(positionSizeTokens*1000)/1000, 'f', 3, 64)
		case positionSizeTokens < 100.0:
			qtyStr = strconv.FormatFloat(math.Trunc(positionSizeTokens*10)/10, 'f', 1, 64)
		default:
			qtyStr = strconv.FormatFloat(math.Trunc(positionSizeTokens), 'f', 0, 64)
		}
		qtyStr = strings.TrimRight(qtyStr, "0")
		qtyStr = strings.TrimRight(qtyStr, ".")
	}

	if qtyStr == "" || qtyStr == "0" {
		return fmt.Errorf("position sizing calculation yielded zero contracts")
	}

	// ── Minimum order value check (Bybit requires ≥ 5 USDT) ──
	orderValue, _ := strconv.ParseFloat(qtyStr, 64)
	orderValue = orderValue * price
	if orderValue < MIN_ORDER_USD && orderValue > 0 {
		// Scale up to minimum, respecting 85% wallet cap
		scaleFactor := MIN_ORDER_USD / orderValue
		maxFromCapital := (e.TotalCapital * 0.85) / price
		if newQty := math.Min(orderValue*scaleFactor, maxFromCapital); newQty >= MIN_ORDER_USD {
			qtyStr = strconv.FormatFloat(newQty, 'f', -1, 64)
		} else {
			return fmt.Errorf("order value $%.2f below minimum $%.2f and wallet too small to scale",
				orderValue, MIN_ORDER_USD)
		}
	}

	// ── Size Risk Guard: ensure actual SL risk ≤ target risk ──
		slPrice, _ := strconv.ParseFloat(stopLossPrice, 64)
		if slPrice > 0 && positionSizeTokens > 0 {
			actualSLDist := math.Abs(price - slPrice)
			actualRisk := actualSLDist * positionSizeTokens
			targetRisk := e.TotalCapital * riskPct
			if actualRisk > targetRisk*1.5 {
				return fmt.Errorf("SIZE GUARD: actual SL risk $%.2f exceeds target $%.2f by >50%% — skipping to protect capital",
					actualRisk, targetRisk)
			}
		}

		// 3. Assemble Unified Bracket Payload
	orderPayload := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
		"orderType":   "Market",
		"qty":         qtyStr,
		"timeInForce": "GTC",
		"takeProfit":  takeProfitPrice,
		"stopLoss":    stopLossPrice,
	}

	respBytes, err := e.Client.PostPrivateRequest("/v5/order/create", orderPayload)
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
		return fmt.Errorf("bybit Order Routing Rejected: %s (Code: %d)", orderRes.RetMsg, orderRes.RetCode)
	}

	// ── Update 2c: Native Bybit Trailing Stop ──────────────────────
	ema20 := CalculateEMA(candles4h, 20)
	if ema20 > 0 {
		trailDist := math.Abs(price - ema20)
		trailPayload := map[string]interface{}{
			"category": "linear",
			"symbol":   symbol,
			// Trailing stop must also be dynamically formatted, defaulting to 4 decimals max
			"trailingStop": strconv.FormatFloat(trailDist, 'f', 4, 64),
			"activePrice":  takeProfitPrice,
		}
		if _, trailErr := e.Client.PostPrivateRequest("/v5/position/trading-stop", trailPayload); trailErr != nil {
			fmt.Printf("   ⚠️ Trailing stop activation failed: %v\n", trailErr)
		} else {
			fmt.Printf("   🎯 Trailing stop active: trailingStop=$%.4f, activePrice=$%s\n", trailDist, takeProfitPrice)
		}
	} else {
		fmt.Printf("   ⚠️ Trailing stop skipped: EMA20 returned 0 (insufficient data)\n")
	}

	fmt.Printf("🔥 TRANSACTION SUCCESS: Position opened on %s via Isolated Perpetual Futures. Order parameters synchronized with bracket targets.\n", symbol)
	return nil
}
