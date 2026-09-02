# 🤖 Hermes Trading Bot

**Multi‑asset crypto swing trading engine with 5‑model AI Council validation, reflection memory, market sentiment pre‑fetch, and multi‑layer risk guards.**

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go) ![Bybit](https://img.shields.io/badge/Bybit_V5_API-Live-F7A600) ![OpenRouter](https://img.shields.io/badge/AI_Gateway-OpenRouter-8A2BE2) ![License](https://img.shields.io/badge/license-MIT-green)

---

## 🚀 Overview

Hermes is a **production‑grade swing trading bot** that ingests live OHLCV data from Bybit, evaluates signals across 6 strategy lenses (S0–S5), validates trades through a **5‑model AI Council** via OpenRouter, and executes bracket orders with dynamic position sizing, trailing stops, and multi‑layer risk protection.

It runs autonomously via cron every hour, scanning the **top 50 USDT pairs by volume** and delivering a full dashboard to Telegram.

---

## ✨ Features

### 📊 Data Ingestion & Watchlists
- **Top 50 by default** — fetches the 50 highest-volume USDT pairs by 24h turnover
- **Concurrent pipeline** — 10‑goroutine semaphore pool for rate‑limit safety
- **All endpoints** — 4h/1d OHLCV, Open Interest, Funding Rate history, wallet balance
- **Configurable scan size** — `--watchlist=top13`, `--watchlist=top50`, `--watchlist=top100`

### 🧠 5-Model AI Council
All non‑HOLD signals are validated by **5 independent LLMs** voting on trade quality:

| Model | Provider | Timeout |
|-------|----------|--------|
| **DeepSeek V4 Flash** | OpenRouter | 20s |
| **Claude Sonnet** | OpenRouter | 25s |
| **Gemini 2.5 Flash** | OpenRouter | 15s |
| **GPT-4o** | OpenRouter | 25s |
| **GLM 5.3 Flash** | Z.ai via OpenRouter | 15s |

**Majority vote** decides CONFIRM or REJECT. Every signal gets:
- `🤖 AI Council: CONFIRMED/REJECTED (X%) — N confirm / M reject (E errors)`
- Per‑model breakdown in dry‑run mode with verdict, confidence, and latency

### 📖 Reflection Memory
Tracks **2,971 historical outcomes** per symbol from `kronos_outcomes.jsonl`:
- Win rate, recent trend (improving/declining/stable)
- Winner vs loser size ratio
- **Confidence multiplier**: 0.5x–1.5x based on reliability
- Injects lessons: *"BTCUSDT: 36% WR, losers larger than winners — tighten stops"*

### 📰 Market Sentiment Pre‑Fetch
Before the signal loop, Hermes pre‑fetches:
- **CoinGecko** top crypto headlines
- **StockTwits** per‑symbol sentiment
- **Reddit r/CryptoCurrency** hot posts

All injected into the AI Council prompt as market context.

### 🧩 Strategy Stack (S0–S5)

| Strategy | Trigger | Role |
|----------|---------|------|
| **S0: Momentum** | RSI + EMA20 + VWAP + Williams%R | Base conviction verification |
| **S1: Volume Profile** | VAL/VAH mean reversion | Supporting |
| **S2: OI Squeeze** | OI spike + funding divergence | Supporting |
| **S3: Breakout** | Consolidation + vol surge | Supporting |
| **S4: Funding Contrarian** | Extreme funding rates (+0.1% or -0.05%) | **Leading signal** |
| **S5: BB Squeeze** | Bollinger Band compression breakout | Energy release |

Conviction scoring: S0 base (1) +1 per agreeing sub‑strategy → Conviction 1–3.

### 🛡️ Multi‑Layer Risk Guards

| Guard | Threshold | Action |
|-------|-----------|--------|
| **Drawdown Ladder** | 4%→7%→10%→15% | 🟡 Reduce risk 25% → 🟠 50% → 🔴 Conv3 only → 🚨 Full halt |
| **Max Positions** | ≥ 5 open | ❄️ Entry freeze |
| **AI Council** | Rejected by majority | 🧠 Block signal |
| **Reflection** | < 35% win rate → 0.75x multiplier | 📉 Reduce confidence |
| **7D Performance** | BUY with 7D loss > 10% → cap at 55% conf | 🔪 Block falling knives |
| **7D Performance** | SELL with 7D loss > 15% → cap at 55% conf | 🐻 Block late sells |
| **Exhaustion** | 7D gain > 40% / loss > 15% + ADX < 50 | 🛑 Block extended moves |
| **Friction Gate** | ATR distance < 3× round‑trip fee | 💸 Block insufficient R:R |
| **Risk Cap** | 7D > +15% or < -10% → cap at 65% confidence | ⚠️ Reduce variance |
| **Co‑Ranking** | Top 3 by 7D gain/loss, strategy dedup | 🏆 Prevent correlation |
| **Double‑Entry** | Existing position or open order on symbol | ⏸️ Skip |
| **BTC Regime** | Bear→block Conv1-2 longs, Bull→block Conv1-2 shorts | 🟢🟡🔴 Macro filter |

### 🔥 Order Execution
- Isolated margin (3x leverage cap)
- Bracket orders: limit entry + SL (2× ATR) + TP (2.5× ATR)
- Trailing stop: EMA20 distance, activated at midpoint to TP
- **Dynamic risk sizing**: 0.35% / 0.50% / 0.75% per trade (confidence-based)
- **Max portfolio exposure**: 5 positions × 0.75% = 3.75% max stop-out risk
- Dynamic precision & minimum notional validation
- Fee‑adjusted minimum R:R gate

### 📋 Dashboard Outputs
- **Live Execution Dashboard** — per‑symbol signals with conviction, ADX, strategy, AI Council verdict
- **Market Conditions Analysis** — funding landscape, OI spikes, trending/ranging breakdown
- **Volume Profile Report** — surge ratios, 7D gain ranking
- **Top Longs / Shorts** — ranked by conviction × gain
- **4‑Hour Macro Snapshot** — Telegram broadcast every 4h boundary

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                   CRON (every 60min)                         │
│   ~/.hermes/scripts/run-bot.sh --watchlist=top50 --scan     │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│  1. 🚀 Engine Initialisation                                │
│      ├── Load .env → Bybit API + OpenRouter key            │
│      ├── Fetch wallet balance ($95.04 USDT)                │
│      └── Circuit breaker (< $5 → halt)                     │
├─────────────────────────────────────────────────────────────┤
│  2. 📊 Concurrent Data Ingestion (10 goroutines)           │
│      ├── 4h/1d candles (200 limit) → all indicators        │
│      ├── OI + Funding per symbol                           │
│      └── Kronos AI predictions (batch cache)               │
├─────────────────────────────────────────────────────────────┤
│  3. 📰 Pre‑Fetch Market Sentiment                          │
│      ├── CoinGecko headlines                               │
│      ├── StockTwits per‑symbol sentiment                   │
│      └── Reddit r/CryptoCurrency                           │
├─────────────────────────────────────────────────────────────┤
│  4. 📖 Compute Reflection Memory                           │
│      └── 91 symbol profiles from 2,923 outcomes            │
├─────────────────────────────────────────────────────────────┤
│  5. 🧩 Evaluate Signals (S0–S5 + Conviction)              │
│      ├── 6 strategies evaluated per asset                  │
│      ├── Conviction 1-3 scoring                            │
│      └── Reflection overlay → confidence multiplier        │
├─────────────────────────────────────────────────────────────┤
│  6. 🧠 AI Council (on non‑HOLD signals only)              │
│      ├── 5 models vote via OpenRouter                      │
│      ├── Majority decides CONFIRMED / REJECTED             │
│      └── Sentiment context injected into prompt            │
├─────────────────────────────────────────────────────────────┤
│  7. 📋 Dashboard Display + Telegram Delivery               │
│      ├── Signal table with AI verdicts                     │
│      ├── Market conditions analysis                        │
│      └── Volume / relative strength report                 │
├─────────────────────────────────────────────────────────────┤
│  8. 🔥 [Live mode only] Order Execution                   │
│      ├── Position filter → rank → dedup → execute          │
│      └── Bracket order + trailing stop                     │
└─────────────────────────────────────────────────────────────┘
```

---

## 🛠️ Getting Started

### Prerequisites
- **Go 1.22+**
- **Bybit account** with API key (read + trade)
- **OpenRouter API key** (for AI Council)

### Installation

```bash
git clone https://github.com/John4E656F/hermes-trading-bot.git
cd hermes-trading-bot
go build -o hermes-bot ./src/
```

### Configuration (`.env`)

```bash
BYBIT_API_KEY=your_api_key
BYBIT_API_SECRET=your_api_secret
OPENROUTER_API_KEY=sk-or-v1-...
COINGECKO_API_KEY=CG-...   # optional, improves sentiment
```

### Usage

```bash
# Scan mode — full dashboard, AI Council verdicts, no orders
./hermes-bot --mode=scan

# Dry-run mode — per-model vote breakdown transparency
./hermes-bot --mode=dry

# Override watchlist size
./hermes-bot --watchlist=top13 --mode=scan
./hermes-bot --watchlist=top100 --mode=dry

# Force signal override (diagnostic)
./hermes-bot --force-signal
```

### Cron Setup

```bash
# Script lives at ~/.hermes/scripts/run-bot.sh
# Runs every 60min, delivers to Telegram
hermes cron create \
  --schedule="every 60m" \
  --script=run-bot.sh \
  --no-agent \
  --deliver=telegram \
  --name=hermes-bot-1h
```

---

## 📁 Project Structure

```
hermes-trading-bot/
├── README.md                # This file
├── go.mod                   # Go module definition
├── .env                     # API credentials (gitignored)
├── run-bot.sh               # Cron entrypoint script
├── hermes-bot               # Compiled binary
├── kronos_outcomes.jsonl    # Historical outcome data
├── kronos_log.jsonl         # Kronos prediction log
├── signal_log.jsonl         # Signal snapshot log
├── trade_log.jsonl          # Executed trades log
└── src/
    ├── main.go              # Entry point: flags, ingestion, dashboard
    ├── allocator.go         # Meta-strategy allocator + conviction + guards
    ├── ai_council.go        # 5-model AI Council voting via OpenRouter
    ├── ai_client.go         # Legacy single-model AI client
    ├── reflection.go        # Per-symbol win rate tracking + lesson builder
    ├── news_fetcher.go      # Market sentiment pre-fetch (CoinGecko, StockTwits, Reddit)
    ├── executor.go          # Bracket order execution + trailing stop + council gate
    ├── client.go            # Bybit V5 REST client (HMAC auth)
    ├── indicators.go        # Pure‑Go SMA, EMA, RSI, ATR, ADX, BBands, Williams%R
    ├── market_analyzer.go   # Funding/OI landscape analysis
    ├── volume_profile.go    # S1: Volume Profile & Mean Reversion
    ├── oi_funding.go        # S2: Open Interest, Funding Rates
    ├── consolidation.go     # S3: Multi-Week Consolidation Breakout
    ├── s6_kronos.go         # Kronos AI overlay client (optional)
    ├── types.go             # Core domain types
    ├── risk_guards.go       # Position guard, volume MA, 7D gain
    ├── tracker.go           # Trade journal & P&L tracking
    ├── trade_log.go         # Trade log JSONL writer
    ├── signal_log.go        # Signal snapshot JSONL writer
    ├── kronos_log.go        # Kronos prediction log writer
    ├── outcome_tracker.go   # 24h outcome resolution
    ├── btc_filter.go        # BTC macro regime filter
    ├── order_manager.go     # Stale limit order cancellation
    ├── position_manager.go  # Breakeven stop management
    ├── regime.go            # Market regime classifier
    └── macd.go              # MACD indicator (reference)
```

---

## 🔮 Roadmap

- [x] **Phase 1–4** — Ingestion, indicators, AI gate, risk guards
- [x] **Phase 5** — Three-Layer Meta-Strategy (VP, OI, Consolidation)
- [x] **Phase 6** — 5-Model AI Council + reflection memory + sentiment pre-fetch
- [x] **Phase 7** — Strategy guards: 7D performance gate, exhaustion filter, risk cap
- [x] **Phase 8** — Telegram delivery + 4H macro snapshot + cron automation
- [ ] **Phase 9** — Backtest engine for strategy parameter optimization
- [ ] **Phase 10** — Multi‑exchange support (Binance, OKX)

---

## 📊 Historical Performance

| Metric | BUY | SELL | Total |
|--------|-----|------|-------|
| **Calls** | 682 | 2,289 | 2,971 |
| **Win Rate** | 50.4% | 41.3% | 43.4% |
| **Avg Win** | +6.58% | +3.89% | — |
| **Avg Loss** | -8.59% | -3.95% | — |
| **Total PnL** | -640.7% | +1,641.5% | **+1,000.8%** |

SELL signals are strongly profitable despite lower WR. BUY signals lose money despite 50% WR — **the 5‑model AI Council + 7D performance guards directly target this weakness.**

---

## 📜 License

MIT — free to use, modify, and distribute. No warranty, trade at your own risk.

---

*Built with ❤️ by [4E 65 6F](https://github.com/John4E656F)*