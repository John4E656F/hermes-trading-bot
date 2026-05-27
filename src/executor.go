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
func (e *ExecutionEngine) ExecuteBracketTrade(symbol string, action SignalAction, price, atr, confidence float64, candles4h []Candle) error {
	fmt.Printf("🛡️ Order Executor initialized for %s. Configuring isolated environment...\n", symbol)

	// ── Update 2a: Dynamic Confidence-Based Sizing ──────────────────
	if confidence < 0.60 {
		return fmt.Errorf("ABORT: confidence too low (%.2f) — aborting trade", confidence)
	}

	var riskPct float64
	switch {
	case confidence >= 0.80:
		riskPct = 0.015 // 1.5 % of live wallet
	case confidence >= 0.70:
		riskPct = 0.010 // 1.0 % of live wallet
	default:
		riskPct = 0.005 // 0.5 %  (confidence 0.60–0.69)
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
	var stopLossPrice, takeProfitPrice, side string
	atrDistance := atr * 2.0

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
			takeProfitPrice = fmt.Sprintf("%.2f", price+(atrDistance*2.5))
		} else {
			side = "Sell"
			stopLossPrice = fmt.Sprintf("%.2f", price+atrDistance)
			takeProfitPrice = fmt.Sprintf("%.2f", price-(atrDistance*2.5))
		}

		// ESPORTS/penny-stock price protection: if TP/SL rounds to $0, use %-based fallback
		if tpVal, _ := strconv.ParseFloat(takeProfitPrice, 64); tpVal <= 0.01 {
			takeProfitPrice = fmt.Sprintf("%.2f", price*1.5)
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
			takeProfitPrice = fmt.Sprintf("%.2f", price+(atrDistance*2.5))
		} else if action == ACTION_SELL {
			side = "Sell"
			stopLossPrice = fmt.Sprintf("%.2f", price+atrDistance)
			takeProfitPrice = fmt.Sprintf("%.2f", price-(atrDistance*2.5))
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
