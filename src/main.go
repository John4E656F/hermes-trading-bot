package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Println("🚀 Hermes Production Engine Initializing...")

	// Check for testing argument flags
	forceSignalToggle := false
	scanMode := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--force-signal":
			forceSignalToggle = true
			fmt.Println("⚠️ DIAGNOSTIC MODE ACTIVE: Bypassing local math indicators to simulate trade logic...")
		case "--mode=scan":
			scanMode = true
			fmt.Println("🔍 SCAN MODE ACTIVE: Reading market data, computing indicators, and ranking setups. No orders will be routed.")
		}
	}

	client := NewBybitClient()

	// ── Phase 3: Live Capital Guard — fetch wallet balance from Bybit ──
	liveBalance, err := fetchLiveBalance(client)
	if err != nil {
		fmt.Printf("⚠️ Wallet balance fetch failed: %v — using last-known fallback of $51.73\n", err)
		liveBalance = 51.73 // fallback so the system can still function
	}

	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Printf("│ 💰 Bybit USDT Balance: $%.2f USDT\n", liveBalance)
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")

	// ── Emergency Circuit Breaker ──
	if !scanMode && liveBalance < 5.00 {
		fmt.Printf("🚨 [EMERGENCY HALT] Available account capital is dangerously low ($%.2f USDT). Halting all strategy calculations and freezing order routing!\n", liveBalance)
		os.Exit(1)
	}
	if scanMode && liveBalance < 5.00 {
		fmt.Printf("⚠️ [SCAN] Available balance is low ($%.2f USDT). Scan proceeds — no orders will be placed.\n", liveBalance)
	}

	watchlist := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "ADAUSDT", "SUIUSDT", "AVAXUSDT", "NEARUSDT", "APTUSDT", "LINKUSDT", "RENDERUSDT", "FETUSDT"}

	// ── Max Position Guard ──
	freezeEntries := false
	if !scanMode {
		openPosCount, err := fetchOpenPositionCount(client, watchlist)
		if err != nil {
			fmt.Printf("⚠️ Position guard query failed: %v\n", err)
			openPosCount = 0
		}
		fmt.Printf("📊 Active positions in watchlist: %d/5\n", openPosCount)
		freezeEntries = openPosCount >= 5
		if freezeEntries {
			fmt.Println("❄️ ENTRY FREEZE: Max concurrent exposure reached. All entry signals will be held.")
		}
	}

	// Wire Execution Engine with the LIVE balance
	executor := NewExecutionEngine(client, liveBalance)
	aiClient := NewAIClient()

	marketData := MarketData{
		Assets:      make(map[string]*AssetSnapshot),
		FetchedAt:   time.Now(),
		LiveBalance: liveBalance,
	}

	for _, symbol := range watchlist {
		fmt.Printf("Analyzing Ingestion Pipeline for %s...\n", symbol)

		candles4h, err := client.FetchKlines(symbol, "240", 200)
		if err != nil {
			fmt.Printf("⚠️ Error processing 4H data for %s: %v\n", symbol, err)
			continue
		}

		candles1d, err := client.FetchKlines(symbol, "D", 200)
		if err != nil {
			fmt.Printf("⚠️ Error processing 1D data for %s: %v\n", symbol, err)
			continue
		}

		if len(candles4h) == 0 || len(candles1d) == 0 {
			fmt.Printf("⚠️ [%s] Empty data packet received from exchange — skipping asset.\n", symbol)
			continue
		}

		ind4h := ComputeAllIndicators(candles4h)
		ind4h.BBands = CalculateBollingerBands(candles4h, 20, 2.0)

		ind1d := ComputeAllIndicators(candles1d)
		ind1d.ADX14 = CalculateADX(candles1d, 14)

		latestPrice := candles4h[len(candles4h)-1].Close

		marketData.Assets[symbol] = &AssetSnapshot{
			Symbol:       symbol,
			CurrentPrice: latestPrice,
			Snap4h:       TimeframeSnapshot{Interval: "240", Candles: candles4h, Indicators: ind4h},
			Snap1d:       TimeframeSnapshot{Interval: "D", Candles: candles1d, Indicators: ind1d},
		}

		time.Sleep(200 * time.Millisecond)
	}

	printAndExecuteSignals(marketData, aiClient, executor, forceSignalToggle, watchlist, freezeEntries, scanMode)

	// Compile dynamic telemetry reports directly from live endpoints
	PrintActivePositionsQueries(client)
	PrintRecentClosedPnLSummary(client)
}

// fetchLiveBalance calls Bybit's private wallet-balance endpoint and extracts
// the totalAvailableBalance for USDT in the Unified account.
func fetchLiveBalance(client *BybitClient) (float64, error) {
	respBytes, err := client.GetPrivateRequest("/v5/account/wallet-balance?accountType=UNIFIED&coin=USDT")
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}

	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
				Coin                  []struct {
					Coin          string `json:"coin"`
					WalletBalance string `json:"walletBalance"`
				} `json:"coin"`
			} `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, fmt.Errorf("parse response: %w", err)
	}

	if resp.RetCode != 0 {
		return 0, fmt.Errorf("bybit API error [%d]: %s", resp.RetCode, resp.RetMsg)
	}

	if len(resp.Result.List) == 0 {
		return 0, fmt.Errorf("no wallet data in response")
	}

	// Prefer totalAvailableBalance; fall back to coin walletBalance.
	balStr := resp.Result.List[0].TotalAvailableBalance
	if balStr == "" || balStr == "0" {
		if len(resp.Result.List[0].Coin) > 0 {
			balStr = resp.Result.List[0].Coin[0].WalletBalance
		}
	}

	balance, err := parseFloat(balStr)
	if err != nil {
		return 0, fmt.Errorf("parse balance %q: %w", balStr, err)
	}

	return balance, nil
}

// parseFloat is a tiny helper that wraps strconv.ParseFloat for wallet amounts.
func parseFloat(s string) (float64, error) {
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, err
	}
	return v, nil
}

func printAndExecuteSignals(data MarketData, ai *AIClient, exec *ExecutionEngine, forceActive bool, watchlist []string, freezeEntries bool, scanMode bool) {
	fmt.Println("\n=========================================================================================")
	fmt.Println("                          HERMES LIVE EXECUTION DASHBOARD                            ")
	fmt.Println("=========================================================================================")
	fmt.Printf("💰 Live Bybit Balance: $%.2f USDT\n", data.LiveBalance)
	fmt.Println("=========================================================================================")
	fmt.Printf("%-10s | %-12s | %-10s | %-22s | %-6s\n", "SYMBOL", "REGIME (1D)", "ADX (1D)", "ACTIVE STRATEGY", "SIGNAL")
fmt.Println("-----------------------------------------------------------------------------------------")

	// ── Pass 1: Collect all BUY signals with 7D gain ──
	var candidates []RankedSignal
	freezeBanner := ""
	if freezeEntries {
		freezeBanner = " ❄️ FROZEN"
	}

	for _, symbol := range watchlist {
		asset, exists := data.Assets[symbol]
		if !exists {
			continue
		}

		sig := EvaluateMarketSnapshot(asset)

		// If simulation command override is turned on, force a buy path setup
		if forceActive {
			sig.Action = ACTION_BUY
			sig.Reason = "FORCED DIAGNOSTIC SIMULATION OVERRIDE"
		}

		var regimeStr string
		switch sig.Regime {
		case REGIME_TRENDING:
			regimeStr = "TRENDING 📈"
		case REGIME_RANGING:
			regimeStr = "RANGING ↔️"
		default:
			regimeStr = "MIXED 🔄"
		}

		fmt.Printf("%-10s | %-12s | %-10.2f | %-22s | %-6s%s\n",
			asset.Symbol,
			regimeStr,
			asset.Snap1d.Indicators.ADX14,
			sig.Strategy,
			sig.Action,
			freezeBanner,
		)
		fmt.Printf("   ┗━ Local Reason: %s\n", sig.Reason)

		if sig.Action == ACTION_BUY {
			gain7d := Compute7DayGain(asset.Snap1d.Candles)
			candidates = append(candidates, RankedSignal{
				Asset:  asset,
				Signal: sig,
				Gain7D: gain7d,
			})
			fmt.Printf("   📊 7-Day Strength: %+.2f%%\n", gain7d)
		}

		if sig.Action == ACTION_HOLD {
			fmt.Println("   🛡️ AI Gateway Status: [IDLE] (No API costs incurred while Local Signal is HOLD)\n")
		}
	}

	// ── Pass 2: Rank by relative strength, cap at 3 ──
	if scanMode {
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  🔍 SCAN: VOLUME PROFILE + RELATIVE STRENGTH REPORT")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		// Extra volume diagnostics for trending-regime assets
		fmt.Printf("%-10s %-12s %-10s  %-22s  %-8s\n", "SYMBOL", "REGIME", "ADX(1D)", "VOLUME SURGE", "7D GAIN")
		for _, c := range candidates {
			asset := c.Asset
			avgVol := CalculateVolumeMA(asset.Snap4h.Candles, 20)
			latestVol := asset.Snap4h.Candles[len(asset.Snap4h.Candles)-1].Volume
			surge := latestVol > avgVol*1.5
			surgeLabel := "❌ No"
			if surge {
				surgeLabel = "✅ YES"
			}
			fmt.Printf("%-10s %-12s %-10.2f  %-22s  %+.2f%%\n",
				asset.Symbol, c.Signal.Regime.String(), asset.Snap1d.Indicators.ADX14, surgeLabel, c.Gain7D)
			fmt.Printf("   Latest Vol: %.0f  |  20-MA Vol: %.0f  |  Ratio: %.2fx\n", latestVol, avgVol, latestVol/avgVol)
		}

		// Ranked top 3
		top3 := RankSignalsByGain(candidates, 3)
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  🏆 TOP 3 RELATIVE STRENGTH LEADERS (7D Gain)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("%-3s %-10s %-14s %-12s %-10s\n", "RNK", "SYMBOL", "STRATEGY", "REGIME", "7D GAIN")
		for i, c := range top3 {
			fmt.Printf("#%-2d %-10s %-14s %-12s %+.2f%%\n",
				i+1, c.Asset.Symbol, c.Signal.Strategy, c.Signal.Regime.String(), c.Gain7D)
		}
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("\n🔍 SCAN COMPLETE — No orders routed. Use normal mode to execute.")

	} else if freezeEntries {
		fmt.Println("\n❄️ ENTRY FREEZE: Max positions reached. All BUY signals are dashboard-only — no orders placed.\n")
	} else if len(candidates) > 0 {
		originalCount := len(candidates)
		filtered := RankSignalsByGain(candidates, 3)
		if len(filtered) < originalCount {
			fmt.Printf("\n📊 Relative strength co-ranking: %d candidates → top %d by 7-day gain\n", originalCount, len(filtered))
		}
		fmt.Println()

		// ── Pass 3: AI verify + execute filtered set ──
		for _, c := range filtered {
			asset := c.Asset
			sig := c.Signal
			fmt.Printf("💡 [%s] Active trade setup found! Initiating OpenRouter AI Verification layer...\n", asset.Symbol)
			aiResp, err := ai.ValidateSignal(sig, asset.CurrentPrice, asset.Snap4h.Indicators.RSI14, asset.Snap4h.Indicators.ATR14)
			if err != nil {
				fmt.Printf("   ❌ AI Gateway Error: %v\n\n", err)
				continue
			}
			fmt.Printf("   🤖 AI VERDICT: [%s] (Confidence: %.2f)\n", aiResp.Verdict, aiResp.Confidence)
			fmt.Printf("   🤖 AI Reason: %s\n", aiResp.Explanation)

			if aiResp.Verdict == "CONFIRMED" {
				fmt.Println("   💸 Signal authorized by AI. Passing transaction payload to Bybit...")
				err := exec.ExecuteBracketTrade(asset.Symbol, sig.Action, asset.CurrentPrice, asset.Snap4h.Indicators.ATR14, aiResp.Confidence, asset.Snap4h.Candles)
				if err != nil {
					fmt.Printf("   ❌ Order Execution Failure: %v\n\n", err)
				}
			} else {
				fmt.Println("   🛡️ Signal REJECTED by AI risk layer. Order routing halted.\n")
			}
		}
	}
	fmt.Println("=========================================================================================")
}
