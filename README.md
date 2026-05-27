# 🤖 Hermes Trading Bot

**Multi‑asset crypto swing trading engine with AI‑verified execution, live risk guards, and a Three-Layer Meta-Strategy allocation.**

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go) ![Bybit](https://img.shields.io/badge/Bybit_V5_API-Live-F7A600) ![OpenRouter](https://img.shields.io/badge/AI_Gateway-OpenRouter-8A2BE2) ![License](https://img.shields.io/badge/license-MIT-green)

---

## 🚀 Overview

Hermes is a **production‑grade swing trading bot** that ingests live OHLCV data from Bybit, computes technical indicators locally in pure Go, evaluates setups across three independent strategy lenses (Mean Reversion, Open Interest/Funding Squeeze, and Multi-Week Breakout), and executes bracket orders with AI‑verified signals.

The entire pipeline runs autonomously via crontab, with **zero external dependencies** (Go stdlib only) and **no paid API subscriptions** for the math engine.

---

## ✨ Features

### 📊 Data Ingestion & Dynamic Watchlists
- **Dynamic Watchlist Scanning**: Use `--watchlist=top100` to automatically fetch and scan the top 100 USDT pairs by 24h turnover, or rely on the default fixed 13-asset list.
- **Bybit V5 REST API**: Fetches 4‑hour & daily OHLCV candles, real-time Open Interest, and Funding Rate history.
- **Rate‑limit friendly**: Concurrent data fetching with a 10-goroutine semaphore pool to respect Bybit rate limits.

### 🧠 Three-Layer Meta-Strategy (Pure Go)

The bot evaluates each asset across three independent lenses to calculate a `Conviction` score (1, 2, or 3):

1. **Strategy 1: Mean Reversion (Volume Profile)**
   - Computes a dynamically sizing 50-bin volume profile.
   - Triggers buys below the Value Area Low (VAL) and sells above the Value Area High (VAH), targeting reversion to the Point of Control (POC).

2. **Strategy 2: The Alpha Generator (OI & Funding Squeeze)**
   - Tracks mathematical deviations in derivatives liquidity.
   - Triggers "Short Squeeze" long setups when Open Interest spikes (>8% in 24h) and Funding Rates flip deeply negative, whilst price acts against the 20-EMA.

3. **Strategy 3: The Capital Scaler (Multi-Week Consolidation Breakout)**
   - Monitors for extended horizontal volatility compression (e.g., 3+ weeks in a < 5% range).
   - "Primes" the asset and triggers on the eventual breakout accompanied by a 1.5x volume surge.

### 🤖 AI Verification & Conviction-Based Routing

The strategy conviction score dictates position sizing and AI validation:
- **Conviction 1 (0.60 Confidence)**: 0.5% risk. Standard trade. Routed through OpenRouter AI for macro confirmation before execution.
- **Conviction 2 (0.75 Confidence)**: 1.0% risk. Double alignment. AI validation bypassed for speed and cost efficiency.
- **Conviction 3 (0.90 Confidence)**: 1.5% risk. The "Liquidation Breakout" Meta-Signal. Maximum risk applied. AI validation bypassed.

### 🛡️ Multi‑Layer Risk Guards

| Guard | Threshold | Action |
|---|---|---|
| **Circuit Breaker** | Balance < $5.00 | 🚨 Full halt, `os.Exit(1)` |
| **Max Positions** | ≥ 5 open | ❄️ Freeze new entries |
| **AI Gate** | All single-strategy signals verified | 🤖 Block illogical setups |
| **Co‑Ranking** | Top 3 by 7D gain | 🏆 Prevent correlated over‑exposure |

### 🔥 Order Execution
- **Isolated margin mode** (3x leverage cap)
- **Bracket orders**: Market entry + SL (2× ATR) + TP (2.5× ATR → 1:2.5 R:R)
- **Trailing stop**: EMA20 distance, activated at TP level
- **Dynamic precision**: Price‑aware decimal formatting for all assets

### 🔍 Diagnostic Mode
```bash
go run . --mode=scan --watchlist=top100
```
Runs full ingestion + multi-strategy evaluation across the top 100 volume leaders without AI verification or order execution — ideal for scanning the market daily.

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          CRONTAB (every 15min)                      │
│    */15 * * * * cd /home/hermes/hermes-trading-bot/src && go run .  │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────────┐
│  1. 🚀 Engine Initialisation                                       │
│     ├── Load Bybit API credentials from environment                │
│     ├── Fetch live wallet balance (private endpoint)               │
│     └── Circuit breaker check (exit if < $5.00)                    │
├─────────────────────────────────────────────────────────────────────┤
│  2. 📊 Concurrent Data Ingestion                                   │
│     ├── 4h/1d candles (limit=200) → ComputeAllIndicators()         │
│     └── Public Endpoints → Fetch OI + Fetch Funding                │
├─────────────────────────────────────────────────────────────────────┤
│  3. 🧠 Meta-Strategy Evaluation                                    │
│     ├── EvaluateS1MeanReversion(VP)                                │
│     ├── EvaluateS2Squeeze(OI, Funding, Price, EMA20)               │
│     ├── EvaluateS3Breakout(Consolidation, Volume)                  │
│     └── Calculate Conviction (1, 2, or 3) & Assign Confidence      │
├─────────────────────────────────────────────────────────────────────┤
│  4. 🏆 Relative Strength Co‑Ranking                                │
│     └── RankSignalsByGain(candidates, 3) → top 3 entries           │
├─────────────────────────────────────────────────────────────────────┤
│  5. 🤖 Execution Routing                                           │
│     ├── If Conviction ≥ 2: Direct Execution (Bypass AI)            │
│     └── If Conviction == 1: OpenRouter AI validation required      │
├─────────────────────────────────────────────────────────────────────┤
│  6. 🔥 Order Execution                                             │
│     ├── Set isolated leverage (3x)                                 │
│     ├── Compute position size (confidence‑based risk %)            │
│     ├── Place market bracket order (entry, SL, TP)                 │
│     └── Activate trailing stop (EMA20 distance, TP‑triggered)      │
├─────────────────────────────────────────────────────────────────────┤
│  7. 📋 Dashboard & Logging                                         │
│     ├── Print Live Execution Dashboard (stdout)                    │
│     └── Done → next cron tick in 15min                             │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 🛠️ Getting Started

### Prerequisites

- **Go 1.22+** (for compilation)
- **Bybit account** with API key (read + trade permissions)
- **OpenRouter API key** (for AI verification of Conviction 1 trades)
- **Git** (for version control)

### Installation

```bash
# Clone the repository
git clone https://github.com/John4E656F/hermes-trading-bot.git
cd hermes-trading-bot/src

# Compile
go build -o hermes-bot .
```

### Configuration

Set these environment variables:

```bash
export BYBIT_API_KEY="your_bybit_api_key"
export BYBIT_API_SECRET="your_bybit_api_secret"
export OPENROUTER_API_KEY="your_openrouter_api_key"
```

Or add them to `~/.bashrc` / `~/.profile` for persistence.

### Usage

```bash
# Normal mode — full pipeline with AI verification & execution
go run .

# Scan mode — diagnostic only, no AI verification, no trades, dynamic top 100 list
go run . --mode=scan --watchlist=top100

# Force signal mode — override all indicators for testing
go run . --force-signal

# Position dashboard — live account & open positions
go run . --positions
```

---

## 📁 Project Structure

```
hermes-trading-bot/
├── README.md                # This file
├── go.mod                   # Go module definition
└── src/
    ├── main.go              # Entry point: flags, ingestion loop, dashboard
    ├── ai_client.go         # OpenRouter AI verification gateway
    ├── allocator.go         # Meta-strategy allocator & conviction scoring
    ├── volume_profile.go    # S1: Volume Profile & Mean Reversion
    ├── oi_funding.go        # S2: Open Interest, Funding Rates & Squeeze Setup
    ├── consolidation.go     # S3: Multi-Week Consolidation Breakout
    ├── client.go            # Bybit V5 REST client (public + HMAC auth)
    ├── executor.go          # Order execution: sizing, brackets, trailing stop
    ├── indicators.go        # Pure‑Go SMA, EMA, RSI, ATR, ADX, Bollinger
    ├── risk_guards.go       # Position guard, volume MA, 7D gain, ranking
    ├── tracker.go           # Trade journal & P&L tracking
    └── types.go             # Core domain types (VP, OI, Consolidation, Signals)
```

---

## 🔮 Roadmap

- [x] **Phase 1** — Data ingestion + local indicators + regime classification
- [x] **Phase 2** — Strategy allocator + Bollinger Bands + ADX
- [x] **Phase 3** — AI verification + live balance + authenticated execution
- [x] **Phase 4** — Risk guards: volume filter, position cap, trailing stop, co‑ranking
- [x] **Phase 5** — Three-Layer Meta-Strategy (Volume Profile, Open Interest, Consolidation)
- [ ] **Phase 6** — Telegram alerts for trade fills / stops / drawdown
- [ ] **Phase 7** — Multi‑exchange support (Binance, OKX)

---

## 📜 License

MIT — free to use, modify, and distribute. No warranty, trade at your own risk.

---

*Built with ❤️ by [4E 65 6F](https://github.com/John4E656F)*
