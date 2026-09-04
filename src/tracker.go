package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type PositionReport struct {
	Symbol          string  `json:"symbol"`
	Side            string  `json:"side"`
	Size            string  `json:"size"`
	AvgPrice        string  `json:"avgPrice"`
	UnrealizedPnL   string  `json:"unrealisedPnl"`
	TakeProfit      string  `json:"takeProfit"`
	StopLoss        string  `json:"stopLoss"`
}

// PrintActivePositionsQueries reads live open derivatives contract exposure from Bybit V5
func PrintActivePositionsQueries(client *BybitClient) {
	respBytes, err := client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		fmt.Printf("⚠️ Failed pulling open positions matrix: %v\n", err)
		return
	}

	var res struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []PositionReport `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(respBytes, &res); err != nil || res.RetCode != 0 {
		fmt.Println("⚠️ Bybit Position query rejected or empty.")
		return
	}

	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("   📊 OPEN POSITIONS — HERMES BOT")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	hasPositions := false
	
	for _, pos := range res.Result.List {
		// Size check determines if position is actually active
		if pos.Size != "" && pos.Size != "0" {
			hasPositions = true
			fmt.Printf("\n  ● %s  [%s]\n", pos.Symbol, pos.Side)
			fmt.Printf("    Size: %s | Entry: %s\n", pos.Size, pos.AvgPrice)
			fmt.Printf("    uPnL: %s USDT | TP: %s | SL: %s\n", pos.UnrealizedPnL, pos.TakeProfit, pos.StopLoss)
		}
	}

	if !hasPositions {
		fmt.Println("\n  ℹ️  No active leveraged positions on account balance.")
		fmt.Println("     All clear — nothing running right now.")
	}
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// PrintRecentClosedPnLSummary prints historical finalized trades (Wins/Losses)
func PrintRecentClosedPnLSummary(client *BybitClient) {
	respBytes, err := client.GetPrivateRequest("/v5/position/closed-pnl?category=linear&limit=3")
	if err != nil {
		return
	}

	var res struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Symbol    string `json:"symbol"`
				Side      string `json:"side"`
				ClosedPnL string `json:"closedPnl"`
				Leverage  string `json:"leverage"`
				UpdatedAt string `json:"updatedTime"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(respBytes, &res) == nil && res.RetCode == 0 && len(res.Result.List) > 0 {
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("   📈 CLOSED TRADES — REFLECTION")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		for _, trade := range res.Result.List {
			verdict := "🟢 WIN"
			if strings.HasPrefix(trade.ClosedPnL, "-") {
				verdict = "🔴 LOSS"
			}
			fmt.Printf("\n  %s  %s  [%s x%s]\n", verdict, trade.Symbol, trade.Side, trade.Leverage)
			fmt.Printf("    Realized P&L: %s USDT\n", trade.ClosedPnL)
		}
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}
}
