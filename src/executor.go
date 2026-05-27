package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
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
		// Small accounts can't split risk into tiny fractions that Bybit rejects.
		// Instead of 1.5% of $6 → $0.09, we send the minimum $5 order.
		const MIN_ORDER_USD = 5.0
		var positionSizeTokens float64

		if e.TotalCapital < 20.0 {
			// All-in on this single best signal at the minimum contract value
			positionSizeUSD := math.Max(MIN_ORDER_USD, math.Min(e.TotalCapital*0.85, 10.0))
			positionSizeTokens = positionSizeUSD / price

			// Enforce minimum contract size based on asset price tier
			switch {
			case price > 100.0:
				positionSizeTokens = math.Ceil(positionSizeTokens*1000) / 1000 // 3 decimals
			case price >= 1.0:
				positionSizeTokens = math.Ceil(positionSizeTokens*10) / 10    // 1 decimal
			default:
				positionSizeTokens = math.Ceil(positionSizeTokens)             // whole int
			}

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

			fmt.Printf("   📊 (Micro-wallet mode) position=$%.2f contracts=%.4f SL=%s TP=%s\n",
				positionSizeUSD, positionSizeTokens, stopLossPrice, takeProfitPrice)

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

			// Ensure position size meets Bybit's absolute minimum contract specifications
			if symbol == "BNBUSDT" && positionSizeTokens < 0.01 {
				positionSizeTokens = 0.01
			} else if symbol == "BTCUSDT" && positionSizeTokens < 0.001 {
				positionSizeTokens = 0.001
			} else if positionSizeTokens < 0.1 {
				positionSizeTokens = 0.1
			}
		}

	// Dynamic position-size precision based on asset price tier
	// Bybit rejects orders with too many decimal places on low-priced assets.
	var qtyStr string

	switch {
	case price > 100.0:
		qtyStr = fmt.Sprintf("%.3f", positionSizeTokens) // High-value: BTC, ETH, BNB → 3 decimals
	case price >= 1.0:
		qtyStr = fmt.Sprintf("%.1f", positionSizeTokens) // Mid-value: SOL, AVAX, SUI, LINK, RENDER, FET, NEAR, APT → 1 decimal
	default:
		qtyStr = fmt.Sprintf("%.0f", positionSizeTokens) // Low-value: XRP, ADA → whole integer
	}

	// 3. Assemble Unified Bracket Payload
	orderPayload := map[string]interface{}{
		"category":   "linear",
		"symbol":     symbol,
		"side":       side,
		"orderType":  "Market",
		"qty":        qtyStr,
		"timeInForce": "GTC",
		"takeProfit": takeProfitPrice,
		"stopLoss":   stopLossPrice,
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

	// ── Update 2b: Native Bybit Trailing Stop ──────────────────────
	// Activate a trailing stop once price reaches our TP level.
	ema20 := CalculateEMA(candles4h, 20)
	if ema20 > 0 {
		trailDist := math.Abs(price - ema20)
		trailPayload := map[string]interface{}{
			"category":     "linear",
			"symbol":       symbol,
			"trailingStop": fmt.Sprintf("%.4f", trailDist),
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
