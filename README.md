# 🤖 Hermes Trading Bot

**Multi‑asset crypto swing trading engine with AI‑verified execution, live risk guards, and regime‑aware strategy allocation.**

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go) ![Bybit](https://img.shields.io/badge/Bybit_V5_API-Live-F7A600) ![OpenRouter](https://img.shields.io/badge/AI_Gateway-OpenRouter-8A2BE2) ![License](https://img.shields.io/badge/license-MIT-green)

---

## 🚀 Overview

Hermes is a **production‑grade swing trading bot** that ingests live OHLCV data from Bybit, computes technical indicators locally in pure Go, classifies market regime via ADX, and executes bracket orders with AI‑verified signals.

The entire pipeline runs autonomously via crontab, with **zero external dependencies** (Go stdlib only) and **no paid API subscriptions** for the math engine — all indicators are computed locally.

---

## ✨ Features

### 📊 Data Ingestion
- **12‑asset watchlist**: BTC, ETH, SOL, BNB, XRP, ADA, SUI, AVAX, NEAR, APT, LINK, RENDER
- **Bybit V5 REST API**: Fetches 4‑hour & daily OHLCV candles (200 bars each)
- **Rate‑limit friendly**: 200ms delay between symbols, connection pooling

### 🧠 Technical Indicators (Pure Go)
| Indicator | Period | Use |
|---|---|---|
| **EMA** | 20 | Short‑term trend direction |
| **SMA** | 50 / 200 | Medium & long‑term trend (Golden / Death Cross) |
| **RSI** | 14 | Overbought / oversold divergence |
| **ATR** | 14 | Stop‑loss buffer & position sizing |
| **ADX** | 14 (1D) | Trend strength → regime classification |
| **Bollinger Bands** | 20, 2σ | Mean reversion triggers |

### 🧭 Regime‑Aware Strategy

```
ADX(1D) > 25  → TRENDING      → Trend‑Following Momentum
ADX(1D) < 20  → RANGING       → Statistical Mean Reversion
20 ≤ ADX ≤ 25 → MIXED         → Neutral Filter (stand aside)
```

### 🤖 AI Verification Gateway
- **OpenRouter** validates every BUY signal before execution
- **Confidence scoring**: 0.60–1.00 scale
- **Zero API cost** on HOLD signals — only calls the LLM when a real entry is detected

### 🛡️ Multi‑Layer Risk Guards

| Guard | Threshold | Action |
|---|---|---|
| **Circuit Breaker** | Balance < $5.00 | 🚨 Full halt, `os.Exit(1)` |
| **Max Positions** | ≥ 5 open | ❄️ Freeze new entries |
| **Volume Profile** | Volume < 1.5× 20‑MA | ⛔ Block trend entries |
| **Confidence Sizing** | 0.60–0.69 → 0.5% risk | 📉 Reduced position |
| **Confidence Sizing** | 0.70–0.79 → 1.0% risk | 📊 Standard position |
| **Confidence Sizing** | ≥ 0.80 → 1.5% risk | 📈 Full position |
| **Co‑Ranking** | Top 3 by 7D gain | 🏆 Prevent correlated over‑exposure |

### 🔥 Order Execution
- **Isolated margin mode** (3x leverage cap)
- **Bracket orders**: Market entry + SL (2× ATR) + TP (2.5× ATR → 1:2.5 R:R)
- **Trailing stop**: EMA20 distance, activated at TP level
- **Dynamic precision**: Price‑aware decimal formatting for all 12 assets

### 🔍 Diagnostic Mode
```bash
./hermes-bot --mode=scan
```
Runs full ingestion + signal evaluation without AI verification or order execution — ideal for testing strategy changes.

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          CRONTAB (every 15min)                      │
│    */15 * * * * /home/hermes/hermes-trading-bot/hermes-bot         │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────────┐
│  1. 🚀 Engine Initialisation                                       │
│     ├── Load Bybit API credentials from environment                │
│     ├── Fetch live wallet balance (private endpoint)               │
│     └── Circuit breaker check (exit if < $5.00)                    │
├─────────────────────────────────────────────────────────────────────┤
│  2. 📊 Data Ingestion (12 assets × 2 timeframes)                  │
│     ├── 4h candles (limit=200) → ComputeAllIndicators()           │
│     └── 1d candles (limit=200) → ComputeAllIndicators() + ADX()   │
├─────────────────────────────────────────────────────────────────────┤
│  3. 🧠 Strategy Evaluation                                         │
│     ├── ClassifyRegime(ADX14) → TRENDING / RANGING / MIXED         │
│     ├── EvaluateMarketSnapshot(asset) → StrategySignal             │
│     └── Collect BUY signals into RankedSignal[] with 7D gain       │
├─────────────────────────────────────────────────────────────────────┤
│  4. 🏆 Relative Strength Co‑Ranking                                │
│     └── RankSignalsByGain(candidates, 3) → top 3 entries           │
├─────────────────────────────────────────────────────────────────────┤
│  5. 🤖 AI Verification Gateway                                     │
│     ├── OpenRouter validates signal (confidence + explanation)     │
│     └── Only executes if verdict == "CONFIRMED"                    │
├─────────────────────────────────────────────────────────────────────┤
│  6. 🔥 Order Execution                                             │
│     ├── Set isolated leverage (3x)                                 │
│     ├── Compute position size (confidence‑based risk %)            │
│     ├── Place market bracket order (entry, SL, TP)                 │
│     └── Activate trailing stop (EMA20 distance, TP‑triggered)      │
├─────────────────────────────────────────────────────────────────────┤
│  7. 📋 Dashboard & Logging                                         │
│     ├── Print Live Execution Dashboard (stdout)                    │
│     ├── Append to production_activity.log                          │
│     └── Done → next cron tick in 15min                             │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 🛠️ Getting Started

### Prerequisites

- **Go 1.22+** (for compilation)
- **Bybit account** with API key (read + trade permissions)
- **OpenRouter API key** (for AI verification)
- **Git** (for version control)

### Installation

```bash
# Clone the repository
git clone https://github.com/John4E656F/hermes-trading-bot.git
cd hermes-trading-bot

# Compile
go build -o hermes-bot src/*.go
```

### Configuration

Set these environment variables:

```bash
export BYBIT_API_KEY="your_bybit_api_key"
export BYBIT_API_SECRET="your_bybit_api_secret"
export OPENROUTER_API_KEY="your_openrouter_api_key"
```

Or add them to `~/.bashrc` / `~/.profile` for persistence:

```bash
echo 'export BYBIT_API_KEY="your_key"' >> ~/.bashrc
echo 'export BYBIT_API_SECRET="your_secret"' >> ~/.bashrc
echo 'export OPENROUTER_API_KEY="your_openrouter_key"' >> ~/.bashrc
source ~/.bashrc
```

### Usage

```bash
# Normal mode — full pipeline with AI verification & execution
./hermes-bot

# Scan mode — diagnostic only, no AI verification, no trades
./hermes-bot --mode=scan

# Force signal mode — override all indicators for testing
./hermes-bot --force-signal

# Position dashboard — live account & open positions
./hermes-bot --positions
```

### Crontab Setup (Autonomous Mode)

```bash
# Every 15 minutes
*/15 * * * * cd /home/hermes/hermes-trading-bot && ./hermes-bot >> production_activity.log 2>&1
```

---

## 📁 Project Structure

```
hermes-trading-bot/
├── .gitignore               # Go, IDE, secret & log exclusions
├── README.md                # This file
├── go.mod                   # Go module definition
└── src/
    ├── main.go              # Entry point: flags, ingestion loop, dashboard
    ├── ai_client.go         # OpenRouter AI verification gateway
    ├── allocator.go         # Strategy allocator: Trend / MeanRev / Neutral
    ├── check_positions.go   # --positions CLI: live account dashboard
    ├── client.go            # Bybit V5 REST client (public + HMAC auth)
    ├── executor.go          # Order execution: sizing, brackets, trailing stop
    ├── indicators.go        # Pure‑Go SMA, EMA, RSI, ATR, ADX, Bollinger
    ├── regime.go            # ADX‑based market regime classifier
    ├── risk_guards.go       # Position guard, volume MA, 7D gain, ranking
    ├── tracker.go           # Trade journal & P&L tracking
    └── types.go             # Core domain types (Candle, Indicators, Signals)
```

---

## 🧪 Strategy Logic

### Trend‑Following (TRENDING regime, ADX > 25)

```
BUY  ←  Close > EMA20  AND  SMA50 > SMA200  AND  RSI > 50  AND  Vol > 1.5× 20MA
HOLD ←  Criteria met BUT volume too weak (awaiting confirmation)
SELL ←  Close < EMA20  OR  RSI < 40
```

### Mean Reversion (RANGING regime, ADX < 20)

```
BUY  ←  Close ≤ Lower Bollinger Band  OR  RSI < 30
SELL ←  Close ≥ Upper Bollinger Band  OR  RSI > 70
HOLD ←  Price within neutral distribution
```

### Mixed Regime (20 ≤ ADX ≤ 25)
Always HOLD — low‑conviction environments are skipped entirely.

---

## 🛡️ Risk Management Summary

| Layer | Detail |
|---|---|
| **Capital Guard** | Live balance fetch every cycle; freeze if < $5.00 |
| **Position Limit** | Max 5 concurrent positions across watchlist |
| **Volume Filter** | No trend entries without 1.5× volume surge |
| **AI Gate** | All signals verified by OpenRouter before execution |
| **Confidence Sizing** | Trade risk scales with AI confidence (0.5% → 1.5%) |
| **Co‑Ranking** | Only top 3 assets by 7D gain execute per cycle |
| **Stop Loss** | 2× ATR trailing, fixed bracket |
| **Take Profit** | 2.5× ATR target (1:2.5 R:R) |
| **Trailing Stop** | EMA20 distance, activates at TP level |

---

## 🔮 Roadmap

- [x] **Phase 1** — Data ingestion + local indicators + regime classification
- [x] **Phase 2** — Strategy allocator + Bollinger Bands + ADX
- [x] **Phase 3** — AI verification + live balance + authenticated execution
- [x] **Phase 4** — Risk guards: volume filter, position cap, trailing stop, co‑ranking
- [ ] **Phase 5** — Telegram alerts for trade fills / stops / drawdown
- [ ] **Phase 6** — Multi‑exchange support (Binance, OKX)
- [ ] **Phase 7** — ML‑enhanced entry timing via local model inference

---

## 📜 License

MIT — free to use, modify, and distribute. No warranty, trade at your own risk.

---

*Built with ❤️ by [4E 65 6F](https://github.com/John4E656F)*
