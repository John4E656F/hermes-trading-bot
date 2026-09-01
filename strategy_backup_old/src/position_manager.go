package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// ManageBreakevenStops scans all open positions and moves the stop-loss to
// break-even (entry + fee buffer) once unrealized P&L covers the entry fee cost.
//
// Why: A profitable position that reverses back to loss is the most demoralizing
// outcome in trading. Moving SL to break-even converts "potential win" into
// "guaranteed no-loss", improving long-term expectancy without reducing win rate.
func ManageBreakevenStops(client *BybitClient) {
	posResp, err := client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		fmt.Printf("⚠️ [BE-Stop] Failed to fetch positions: %v\n", err)
		return
	}

	var posData struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol        string `json:"symbol"`
				Side          string `json:"side"`
				Size          string `json:"size"`
				AvgPrice      string `json:"avgPrice"`
				MarkPrice     string `json:"markPrice"`
				StopLoss      string `json:"stopLoss"`
				UnrealisedPnl string `json:"unrealisedPnl"`
				PositionIM    string `json:"positionIM"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(posResp, &posData); err != nil || posData.RetCode != 0 {
		return
	}

	for _, pos := range posData.Result.List {
		if pos.Size == "" || pos.Size == "0" {
			continue
		}

		entry, _ := strconv.ParseFloat(pos.AvgPrice, 64)
		currentSL, _ := strconv.ParseFloat(pos.StopLoss, 64)
		upl, _ := strconv.ParseFloat(pos.UnrealisedPnl, 64)
		im, _ := strconv.ParseFloat(pos.PositionIM, 64)
		size, _ := strconv.ParseFloat(pos.Size, 64)

		if entry == 0 || im == 0 || size == 0 {
			continue
		}

		// Break-even price: entry + round-trip fee buffer so we at minimum cover costs.
		// Using 2× taker fee to cover both sides of the trade.
		feeBuf := entry * TAKER_FEE_RATE * 2.5 // 2.5× for safety margin

		var bePrice float64
		var alreadyProtected bool

		if pos.Side == "Buy" {
			bePrice = entry + feeBuf
			alreadyProtected = currentSL >= bePrice
		} else {
			bePrice = entry - feeBuf
			alreadyProtected = currentSL > 0 && currentSL <= bePrice
		}

		if alreadyProtected {
			continue
		}

		// Move to break-even only when we're sufficiently in profit.
		// Threshold: unrealized P&L >= 50% of initial margin (covers the fee + buffer).
		threshold := im * 0.50
		if upl < threshold {
			continue
		}

		info, err := client.GetInstrumentInfo(pos.Symbol)
		if err == nil && info.PriceStep > 0 {
			bePrice = math.Floor(bePrice/info.PriceStep) * info.PriceStep
		}

		payload := map[string]interface{}{
			"category":    "linear",
			"symbol":      pos.Symbol,
			"stopLoss":    strconv.FormatFloat(bePrice, 'f', 4, 64),
			"slTriggerBy": "MarkPrice",
		}
		respBytes, err := client.PostPrivateRequest("/v5/position/trading-stop", payload)
		if err != nil {
			fmt.Printf("   ⚠️ [BE-Stop] %s update failed: %v\n", pos.Symbol, err)
			continue
		}

		var res struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
		}
		json.Unmarshal(respBytes, &res)
		if res.RetCode == 0 {
			fmt.Printf("   🔒 [BE-Stop] %s %s: SL moved to break-even $%.4f (was $%.4f, UPL=$%.2f)\n",
				pos.Symbol, pos.Side, bePrice, currentSL, upl)
		} else {
			fmt.Printf("   ⚠️ [BE-Stop] %s rejected: %s\n", pos.Symbol, res.RetMsg)
		}
	}
}

// EnforceMaxHoldPeriod closes positions in ranging markets that have been open
// too long and are accumulating funding rate drag without directional movement.
// Only acts when the live signal has turned to HOLD or reversed.
func EnforceMaxHoldPeriod(client *BybitClient, data MarketData) {
	posResp, err := client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		return
	}

	var posData struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Symbol        string `json:"symbol"`
				Side          string `json:"side"`
				Size          string `json:"size"`
				UnrealisedPnl string `json:"unrealisedPnl"`
				CreatedTime   string `json:"createdTime"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(posResp, &posData); err != nil || posData.RetCode != 0 {
		return
	}

	for _, pos := range posData.Result.List {
		if pos.Size == "" || pos.Size == "0" {
			continue
		}
		asset, exists := data.Assets[pos.Symbol]
		if !exists {
			continue
		}

		adx := asset.Snap1d.Indicators.ADX14
		sig := EvaluateMarketSnapshot(asset)

		// Close if: position is in ranging market (ADX<25) AND signal has turned HOLD or reversed
		isRangingAndStale := adx < 25 && sig.Action == ACTION_HOLD
		isConflicting := (pos.Side == "Buy" && sig.Action == ACTION_SELL) ||
			(pos.Side == "Sell" && sig.Action == ACTION_BUY)

		if !isRangingAndStale && !isConflicting {
			continue
		}

		upl, _ := strconv.ParseFloat(pos.UnrealisedPnl, 64)
		// Only close if not deeply losing (let SL handle those) or if signal reversed
		if upl < -5.0 && !isConflicting {
			continue
		}

		closeSide := "Sell"
		if pos.Side == "Sell" {
			closeSide = "Buy"
		}

		reason := "ranging stale (ADX<25, signal=HOLD)"
		if isConflicting {
			reason = fmt.Sprintf("trend flip: position %s but signal now %s", pos.Side, sig.Action)
		}

		respBytes, err := client.PostPrivateRequest("/v5/order/create", map[string]interface{}{
			"category":    "linear",
			"symbol":      pos.Symbol,
			"side":        closeSide,
			"orderType":   "Market",
			"qty":         pos.Size,
			"reduceOnly":  true,
		})
		if err != nil {
			continue
		}
		var res struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
		}
		json.Unmarshal(respBytes, &res)
		if res.RetCode == 0 {
			fmt.Printf("   🔄 [MaxHold] Closed %s %s — %s (UPL=$%.2f)\n",
				pos.Symbol, pos.Side, reason, upl)
		}
	}
}
