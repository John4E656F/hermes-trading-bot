package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// Attempt to load .env file if present, and overload any shell variables
	_ = godotenv.Overload("../.env")

	fmt.Println("🚀 Hermes Production Engine Initializing...")

	// Check for testing argument flags
	forceSignalToggle := false
	scanMode := false
	useTop100 := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--force-signal":
			forceSignalToggle = true
			fmt.Println("⚠️ DIAGNOSTIC MODE ACTIVE: Bypassing local math indicators to simulate trade logic...")
		case "--mode=scan":
			scanMode = true
			fmt.Println("🔍 SCAN MODE ACTIVE: Reading market data, computing indicators, and ranking setups. No orders will be routed.")
		case "--watchlist=top100":
			useTop100 = true
			fmt.Println("🔁 TOP 100 MODE: Fetching top USDT pairs by 24h volume instead of fixed watchlist.")
		}
	}

	client := NewBybitClient()

	// ── Phase 3: Live Capital Guard — fetch wallet balance from Bybit ──
	liveBalance, err := fetchLiveBalance(client)
	if err != nil {
		fmt.Printf("🚨 Wallet balance fetch failed: %v\n", err)
		if !scanMode {
			fmt.Println("🚨 [EMERGENCY HALT] Cannot verify live capital. Halting to protect funds.")
			os.Exit(1)
		} else {
			fmt.Println("⚠️ [SCAN] Proceeding with dummy balance of $100.00 for scan mode calculations.")
			liveBalance = 100.00
		}
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

	// ── Build watchlist ──
	var watchlist []string
	if useTop100 {
		var err error
		watchlist, err = client.FetchTopSymbols(100)
		if err != nil {
			fmt.Printf("⚠️ Failed to fetch top 100: %v — falling back to default 13\n", err)
			watchlist = []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "ADAUSDT", "SUIUSDT", "AVAXUSDT", "NEARUSDT", "APTUSDT", "LINKUSDT", "RENDERUSDT", "FETUSDT"}
		} else {
			fmt.Printf("📈 Scanning top %d USDT pairs by 24h volume\n", len(watchlist))
		}
	} else {
		watchlist = []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "ADAUSDT", "SUIUSDT", "AVAXUSDT", "NEARUSDT", "APTUSDT", "LINKUSDT", "RENDERUSDT", "FETUSDT"}
	}

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

	// ── Concurrent Data Ingestion Pipeline ──
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 10) // Concurrency semaphore: max 10 parallel requests to respect Bybit rate limits

	fmt.Printf("⚡ Initiating concurrent ingestion pipeline for %d assets...\n", len(watchlist))

	for _, symbol := range watchlist {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire token
			defer func() { <-sem }() // Release token

			candles4h, err := client.FetchKlines(sym, "240", 200)
			if err != nil {
				fmt.Printf("⚠️ Error processing 4H data for %s: %v\n", sym, err)
				return
			}

			candles1d, err := client.FetchKlines(sym, "D", 200)
			if err != nil {
				fmt.Printf("⚠️ Error processing 1D data for %s: %v\n", sym, err)
				return
			}

			if len(candles4h) == 0 || len(candles1d) == 0 {
				return
			}

			ind4h := ComputeAllIndicators(candles4h)
			ind4h.BBands = CalculateBollingerBands(candles4h, 20, 2.0)

			ind1d := ComputeAllIndicators(candles1d)
			ind1d.ADX14 = CalculateADX(candles1d, 14)

			latestPrice := candles4h[len(candles4h)-1].Close

			// Fetch OI + Funding (public endpoints, no auth)
			oiRaw, _ := client.FetchOpenInterest(sym)
			fundingRaw, _ := client.FetchFundingHistory(sym)

			// Compute derivatives
			vp := ComputeVolumeProfile(candles1d, 30)
			oiSnap := ParseAndComputeOI(oiRaw)
			fundSnap := ParseAndComputeFunding(fundingRaw)
			consol := DetectConsolidation(candles1d, 21, 5.0)

			mu.Lock()
			marketData.Assets[sym] = &AssetSnapshot{
				Symbol:       sym,
				CurrentPrice: latestPrice,
				Snap4h:       TimeframeSnapshot{Interval: "240", Candles: candles4h, Indicators: ind4h},
				Snap1d:       TimeframeSnapshot{Interval: "D", Candles: candles1d, Indicators: ind1d},
				VP:           vp,
				OI:           oiSnap,
				Funding:      fundSnap,
				Consolidation: consol,
			}
			mu.Unlock()
		}(symbol)
	}

	wg.Wait() // Block until all concurrent workers finish
	fmt.Println("✅ Ingestion complete.")

	printAndExecuteSignals(marketData, aiClient, executor, forceSignalToggle, watchlist, freezeEntries, scanMode)

	// ── 4-Hour Macro Snapshot Broadcast ──
	if is4HourBoundary() {
		sendTelegramSnapshot(client, marketData, watchlist, freezeEntries)
	}

	// Compile dynamic telemetry reports directly from live endpoints
	if !scanMode {
		PrintActivePositionsQueries(client)
		PrintRecentClosedPnLSummary(client)
	}
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
	fmt.Printf("%-10s | %-12s | %-10s | %-22s | %-6s\n", "SYMBOL", "CONVICTION", "ADX (1D)", "ACTIVE STRATEGY", "SIGNAL")
	fmt.Println("-----------------------------------------------------------------------------------------")

	// ── Pass 1: Collect all actionable signals (BUY + SELL) with 7D gain ──
	var buyCandidates, sellCandidates []RankedSignal
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
			sig.Conviction = 1
			sig.Confidence = 0.60
		}

		fmt.Printf("%-10s | %-12d | %-10.2f | %-22s | %-6s%s\n",
			asset.Symbol,
			sig.Conviction,
			asset.Snap1d.Indicators.ADX14,
			sig.Strategy,
			sig.Action,
			freezeBanner,
		)
		fmt.Printf("   ┗━ Local Reason: %s\n", sig.Reason)

		if sig.Action == ACTION_BUY {
			gain7d := Compute7DayGain(asset.Snap1d.Candles)
			buyCandidates = append(buyCandidates, RankedSignal{
				Asset:  asset,
				Signal: sig,
				Gain7D: gain7d,
			})
			fmt.Printf("   📊 7-Day Strength: %+.2f%%\n", gain7d)
		}
		if sig.Action == ACTION_SELL {
			gain7d := Compute7DayGain(asset.Snap1d.Candles)
			sellCandidates = append(sellCandidates, RankedSignal{
				Asset:  asset,
				Signal: sig,
				Gain7D: gain7d,
			})
			fmt.Printf("   📉 7-Day Weakness: %+.2f%%\n", gain7d)
		}

		if sig.Action == ACTION_HOLD {
			fmt.Println("   🛡️ AI Gateway Status: [IDLE] (No API costs incurred while Local Signal is HOLD)")
		}
	}

	// ── Pass 2: Rank each pool by contextual criteria, cap at 3 per side ──
	if scanMode {
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  🔍 SCAN: VOLUME PROFILE + RELATIVE STRENGTH REPORT")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		allCandidates := append(buyCandidates, sellCandidates...)
		fmt.Printf("%-10s %-12s %-10s  %-22s  %-10s\n", "SYMBOL", "REGIME", "ADX(1D)", "VOLUME SURGE", "7D GAIN")
		for _, c := range allCandidates {
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

		topLongs := RankSignalsByGain(buyCandidates, 3)
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  🏆 TOP LONGS (Ranked by 7D Gain)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		if len(topLongs) > 0 {
			fmt.Printf("%-3s %-10s %-14s %-12s %-10s\n", "RNK", "SYMBOL", "STRATEGY", "REGIME", "7D GAIN")
			for i, c := range topLongs {
				fmt.Printf("#%-2d %-10s %-14s %-12s %+.2f%%\n",
					i+1, c.Asset.Symbol, c.Signal.Strategy, c.Signal.Regime.String(), c.Gain7D)
			}
		} else {
			fmt.Println("  (No BUY signals — market lacking strength leaders)")
		}

		topShorts := RankSignalsByLowestGain(sellCandidates, 3)
		fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  🐻 TOP SHORTS (Ranked by Weakest 7D Performance)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		if len(topShorts) > 0 {
			fmt.Printf("%-3s %-10s %-14s %-12s %-10s\n", "RNK", "SYMBOL", "STRATEGY", "REGIME", "7D GAIN")
			for i, c := range topShorts {
				fmt.Printf("#%-2d %-10s %-14s %-12s %+.2f%%\n",
					i+1, c.Asset.Symbol, c.Signal.Strategy, c.Signal.Regime.String(), c.Gain7D)
			}
		} else {
			fmt.Println("  (No SELL signals — no assets showing material weakness)")
		}
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("\n🔍 SCAN COMPLETE — No orders routed. Use normal mode to execute.")

	} else if freezeEntries {
		fmt.Println("\n❄️ ENTRY FREEZE: Max positions reached. All BUY signals are dashboard-only — no orders placed.")
	} else {
		// Merge ranked longs + ranked shorts, cap combined at 3
		topLongs := RankSignalsByGain(buyCandidates, 3)
		topShorts := RankSignalsByLowestGain(sellCandidates, 3)
		var filtered []RankedSignal
		filtered = append(filtered, topLongs...)
		filtered = append(filtered, topShorts...)

		// Sort merged pool so strongest signals execute first
		sort.Slice(filtered, func(i, j int) bool {
			return math.Abs(filtered[i].Gain7D) > math.Abs(filtered[j].Gain7D)
		})

		// Cap total execution at 3 to respect position sizing limits
		if len(filtered) > 3 {
			filtered = filtered[:3]
		}

		total := len(buyCandidates) + len(sellCandidates)
		if len(filtered) < total {
			fmt.Printf("\n📊 Contextual ranking: %d candidates (L:%d/S:%d) → top %d for execution\n",
				total, len(buyCandidates), len(sellCandidates), len(filtered))
		}
		fmt.Println()

		// ── Pass 3: Local confidence + AI verify + execute filtered set ──
		for _, c := range filtered {
			asset := c.Asset
			sig := c.Signal

			// ── Local confidence assessment ──
			localConfident := false
			
			if sig.Conviction >= 2 {
				localConfident = true
			}

			if localConfident {
				fmt.Printf("💡 [%s] Local confidence=%.0f%% (Conviction %d) — bypassing AI, executing directly.\n", asset.Symbol, sig.Confidence*100, sig.Conviction)
				err := exec.ExecuteBracketTrade(asset.Symbol, sig.Action, asset.CurrentPrice, asset.Snap4h.Indicators.ATR14, sig.Confidence, asset.Snap4h.Candles)
				if err != nil {
					fmt.Printf("   ❌ Order Execution Failure: %v\n\n", err)
				}
				continue
			}

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
				fmt.Println("   🛡️ Signal REJECTED by AI risk layer. Order routing halted.")
			}
		}
	}
	fmt.Println("=========================================================================================")
}

// ── 4-Hour Boundary Check ─────────────────────────────────────────────
func is4HourBoundary() bool {
	now := time.Now().UTC()
	hour := now.Hour()
	min := now.Minute()
	return min < 15 && (hour%4 == 0)
}

// ── 4-Hour Macro Snapshot Telegram Broadcast ──────────────────────────
func sendTelegramSnapshot(client *BybitClient, data MarketData, watchlist []string, freezeEntries bool) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		fmt.Println("⚠️ TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID not set — skipping 4H snapshot")
		return
	}

	liveBalance, err := fetchLiveBalance(client)
	if err != nil {
		liveBalance = data.LiveBalance
	}

	openPosCount, err := fetchOpenPositionCount(client, watchlist)
	if err != nil {
		openPosCount = -1
	}

	var trending, ranging []string
	var candidates []RankedSignal

	for _, symbol := range watchlist {
		asset, exists := data.Assets[symbol]
		if !exists {
			continue
		}
		adx := asset.Snap1d.Indicators.ADX14
		price := asset.CurrentPrice

		line := fmt.Sprintf("• %s — $%.4f (ADX: %.1f)", symbol, price, adx)
		if adx > 25 {
			trending = append(trending, line)
		} else {
			ranging = append(ranging, line)
		}

		gain7d := Compute7DayGain(asset.Snap1d.Candles)
		candidates = append(candidates, RankedSignal{
			Asset:  asset,
			Gain7D: gain7d,
		})
	}

	top3 := RankSignalsByGain(candidates, 3)
	var top3Str []string
	for i, c := range top3 {
		top3Str = append(top3Str, fmt.Sprintf("#%d %s (%+.2f%%)", i+1, c.Asset.Symbol, c.Gain7D))
	}
	top3Label := "None"
	if len(top3Str) > 0 {
		top3Label = strings.Join(top3Str, ", ")
	}

	cbStatus := "🟢 GREEN"
	if liveBalance < 5.00 {
		cbStatus = "🔴 RED"
	}

	posLabel := fmt.Sprintf("%d/5", openPosCount)
	if openPosCount < 0 {
		posLabel = "?"
	}

	trendingBlock := "• None"
	if len(trending) > 0 {
		trendingBlock = strings.Join(trending, "\n")
	}
	rangingBlock := "• None"
	if len(ranging) > 0 {
		rangingBlock = strings.Join(ranging, "\n")
	}

	msg := fmt.Sprintf(
		"<b>📊 HERMES 4-HOUR MACRO SNAPSHOT</b>\n"+
			"━━━━━━━━━━━━━━━━━━━━━━━━\n"+
			"💰 Account Equity: <b>$%.2f</b> USDT | Live Positions: <b>%s</b>\n\n"+
			"<b>🟢 TRENDING MATRICES (ADX > 25):</b>\n%s\n\n"+
			"<b>🟡 RANGING / MIXED CHOP (ADX ≤ 25):</b>\n%s\n\n"+
			"<b>🛡️ FILTER STATUS:</b>\n"+
			"• 7-Day Leaders: %s\n"+
			"• Circuit Breaker: %s\n",
		liveBalance, posLabel,
		trendingBlock,
		rangingBlock,
		top3Label,
		cbStatus,
	)

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", msg)
	form.Set("parse_mode", "HTML")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.PostForm(apiURL, form)
	if err != nil {
		fmt.Printf("⚠️ Telegram snapshot send error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 {
		fmt.Println("📡 4-Hour macro snapshot broadcast via Telegram")
	} else {
		fmt.Printf("⚠️ Telegram API error (HTTP %d): %s\n", resp.StatusCode, string(body))
	}
}
