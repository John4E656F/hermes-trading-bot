package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "--positions" {
		showPositions()
		os.Exit(0)
	}
}

func showPositions() {
	client := NewBybitClient()

	balResp, err := client.GetPrivateRequest("/v5/account/wallet-balance?accountType=UNIFIED&coin=USDT")
	if err != nil {
		fmt.Printf("❌ Balance fetch failed: %v\n", err)
		return
	}
	var bal struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
				TotalPerpetualUPL     string `json:"totalPerpetualUPL"`
				TotalEquity           string `json:"totalEquity"`
				TotalWalletBalance    string `json:"totalWalletBalance"`
				Coin                  []struct {
					Coin               string `json:"coin"`
					WalletBalance      string `json:"walletBalance"`
					Equity             string `json:"equity"`
					AvailableToWithdraw string `json:"availableToWithdraw"`
				} `json:"coin"`
			} `json:"list"`
		} `json:"result"`
	}
	json.Unmarshal(balResp, &bal)
	if bal.RetCode != 0 {
		fmt.Printf("❌ Bybit error [%d]: %s\n", bal.RetCode, bal.RetMsg)
		return
	}
	if len(bal.Result.List) == 0 {
		fmt.Println("📭 No wallet data.")
		return
	}
	acct := bal.Result.List[0]

	var wb, eq, avail string
	for _, c := range acct.Coin {
		if c.Coin == "USDT" {
			wb = c.WalletBalance
			eq = c.Equity
			avail = c.AvailableToWithdraw
			break
		}
	}

	fmt.Println("┌─────────────────────────────────────────────────┐")
	fmt.Println("│ 💰 BYBIT ACCOUNT SUMMARY                       │")
	fmt.Println("├─────────────────────────────────────────────────┤")
	fmt.Printf("│ Wallet Balance:    $%s USDT\n", wb)
	fmt.Printf("│ Equity:            $%s USDT\n", eq)
	fmt.Printf("│ Unrealised P&L:    $%s USDT\n", acct.TotalPerpetualUPL)
	fmt.Printf("│ Available:         $%s USDT\n", avail)
	fmt.Println("└─────────────────────────────────────────────────┘")

	posResp, err := client.GetPrivateRequest("/v5/position/list?category=linear&settleCoin=USDT")
	if err != nil {
		fmt.Printf("❌ Position fetch failed: %v\n", err)
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
				EntryPrice    string `json:"avgPrice"`
				MarkPrice     string `json:"markPrice"`
				LiqPrice      string `json:"liqPrice"`
				UnrealisedPnl string `json:"unrealisedPnl"`
				RealisedPnl   string `json:"cumRealisedPnl"`
				Leverage      string `json:"leverage"`
				PositionValue string `json:"positionValue"`
				PositionIM    string `json:"positionIM"`
				StopLoss      string `json:"stopLoss"`
				TakeProfit    string `json:"takeProfit"`
			} `json:"list"`
		} `json:"result"`
	}
	json.Unmarshal(posResp, &posData)
	if posData.RetCode != 0 {
		fmt.Printf("❌ Bybit error [%d]: %s\n", posData.RetCode, posData.RetMsg)
		return
	}

	if len(posData.Result.List) == 0 {
		fmt.Println("\n📭 No open positions.")
		return
	}

	fmt.Println("\n┌─────────────────────────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ 📊 OPEN POSITIONS                                                                  │")
	fmt.Println("├─────────────────────────────────────────────────────────────────────────────────────┤")
	fmt.Printf("%-8s %-5s %-6s %-10s %-10s  %-13s  %-4s\n", "SYMBOL", "SIDE", "SIZE", "ENTRY", "MARK", "UNREALISED", "LEV")
	totalUPL := 0.0
	for _, p := range posData.Result.List {
		var upl float64
		fmt.Sscanf(p.UnrealisedPnl, "%f", &upl)
		totalUPL += upl
		icon := "⚪"
		if upl > 0 {
			icon = "🟢"
		}
		if upl < 0 {
			icon = "🔴"
		}
		fmt.Printf("%-8s %-5s %-6s $%-9s $%-9s %s $%-10.4f %-4sx\n",
			p.Symbol, p.Side, p.Size, p.EntryPrice, p.MarkPrice, icon, upl, p.Leverage)
		if p.StopLoss != "0" {
			fmt.Printf("   🛡️ SL: $%s\n", p.StopLoss)
		}
		if p.TakeProfit != "0" {
			fmt.Printf("   🎯 TP: $%s\n", p.TakeProfit)
		}
	}
	fmt.Println("└─────────────────────────────────────────────────────────────────────────────────────┘")
	fmt.Printf("\n📈 Total Floating P&L: $%.2f USDT\n", totalUPL)
}