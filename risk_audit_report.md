# Hermes Trading Bot — Risk Management Audit

**Audit Date:** 2026-09-03
**Scope:** ~/hermes-trading-bot/src/ (risk_guards.go, executor.go, btc_filter.go, reflection.go, allocator.go, main.go, types.go)
**Capital:** $95 USDT (example), 3x leverage, MaxLeverage=3 in ExecutionEngine

---

## 1. Position Sizing: Kelly Criterion

**FINDING: Kelly criterion is NOT used.** No edge probability, win/loss ratio, or Kelly fraction is computed anywhere.

**Actual approach:** Fixed-risk-per-trade with adjustments:
- Base tiers: 0.35% (baseline), 0.50% (confirmed), 0.75% (meta/high-conviction)
- BUY-side asymmetry: 0.65x multiplier (empirically justified — 0.77x win/loss size ratio from 2,971 calls)
- Drawdown ladder: 0.25–1.0x multiplier
- After-cap: ~0.12–0.75% equity at risk per trade

**Assessment:** The fixed-risk tiers have empirical backing (historical data from the bot's own trading). The asymmetry fix for BUY is well-justified. However, this doesn't adapt to actual edge. A pure Kelly approach would suggest:
- For a system with 50.4% WR and 0.77x win/loss ratio: f* = p - q/R = 0.504 - 0.496/(0.77) = 0.504 - 0.644 = **negative** — Kelly says don't trade BUY at all.
- For SELL (47.6% WR, 0.98x ratio): f* = 0.476 - 0.524/0.98 = 0.476 - 0.535 = **negative** — Kelly also says don't trade.

**This doesn't mean the bot shouldn't trade** — it means the raw master-signal WR doesn't reflect the full system with AI Council gate + SL/TP + confidence filtering. Post-filter performance will differ. But the absence of any Kelly calculation means sizing has no anchor to mathematical edge.

**SEVERITY: Medium.** Consider adding a fractional Kelly input (0.10–0.25 of computed f*) that dynamically scales base riskPct per signal type.

---

## 2. Drawdown Ladder

**FINDING: Ladder exists and works as described, but has a reset flaw.**

### Ladder Tiers (from main.go lines 387-398):
| Drawdown | Multiplier | Risk Reduction |
|----------|-----------|----------------|
| 0–4%     | 1.0x      | None           |
| 4–7%     | 0.75x     | 25%            |
| 7–10%    | 0.50x     | 50%            |
| 10%+     | 0.25x     | 75%            |
| 15%+     | HALT      | os.Exit(1)     |

**Verification:** `drawdownRiskMultiplier()` at main.go:387 correctly returns 1.0, 0.75, 0.50, 0.25. `globalDrawdownMultiplier` is wired into `ExecuteBracketTrade` at executor.go:211. HALT at 15% exits the binary.

**CRITICAL FLAW — `peak_equity.txt` persistence:**
- `peak_equity.txt` is written to the **current working directory** (no absolute path). If the cron job's working directory changes, the file is silently created in a new location and peak equity resets to 0. The drawdown ladder resets to 1.0x forever.
- If the file is accidentally deleted between runs, `readPeakEquity()` returns 0, `peakEquity = max(0, liveBalance)` on first run, then `writePeakEquity(liveBalance)` — peak resets.

**SEVERITY: High** — fix with absolute path resolution and crash if file is missing (rather than silently resetting).

---

## 3. Portfolio Cap (globalPortfolioRiskPct)

**FINDING: Only works intra-cycle. Reset to 0 each run. Cross-cycle cap is absent.**

**How it works:**
- `globalPortfolioRiskPct` is reset to 0 at main.go:268 (each cron run)
- Incremented per executed trade at executor.go:501
- Checked before each new trade at executor.go:222-233
- MAX_PORTFOLIO_RISK = 4% of total capital

**Intra-cycle math:**
- 5 trades × 0.75% each = 3.75% (within 4% cap) — fits
- With BUY asymmetry (0.65x): up to ~12 trades could fit in one cycle
- But Bybit's 5-position open limit (main.go:157) is the real constraint

**Cross-cycle failure:**
- Run 1: opens 3 positions at 0.50% each → globalPortfolioRiskPct = 1.50%
- Binary exits. `peak_equity.txt` updated.
- Run 2 (60m later): `globalPortfolioRiskPct = 0.0` (reset). `fetchOpenPositions()` counts 3 existing positions.
- Runs 2-5 open 2 more positions across runs → 5 total at up to 3.75% cumulative risk.
- **Portfolio cap did NOT prevent this because it resets each run.**

**The position count freeze (main.go:157) DOES work cross-cycle:**
```go
freezeEntries = openPosCount >= 5
```
This queries Bybit live. It's the real guard.

**But the risk % cap has no cross-cycle analog.** Five positions at 0.75% each = 3.75% aggregate SL risk. In a correlated crash (all crypto drops), this could all be lost simultaneously.

**SEVERITY: Medium.** Add a persistent cumulative risk tracker (file or Bybit position query → compute actual SL distances across all open positions → cap).

---

## 4. Stop Loss Placement

**FINDING: BUY-side SL is dangerously tight. SELL-side is appropriate.**

### Current SL multipliers (executor.go:241-267):
| Side | ADX < 25 | ADX 25–40 | ADX > 40 |
|------|----------|-----------|----------|
| BUY  | 1.5x ATR | 1.5x ATR  | 1.5x ATR |
| SELL | 3.0x ATR | 2.5x ATR  | 2.0x ATR |

**BUY SL = always 1.5x ATR regardless of market regime.** This is the single biggest risk issue.

**ATR context for typical crypto:**
- BTC 4h ATR ≈ 0.8–1.5% of price → BUY SL at 1.2–2.25% → reasonable but tight
- ALTs 4h ATR ≈ 2–5% of price → BUY SL at 3–7.5% → acceptable
- Low-ADX (<25) ranging markets: noise whipsaws on 1.5x ATR are frequent

**Fee gate (executor.go:271):** SL distance must exceed 3× round-trip friction (≈0.63% of price). BUY SL on low-volatility assets passes this easily. But the gate doesn't adjust for market regime.

**BUY vs SELL asymmetry analysis:**
```python
# Empirical data from 2,971 calls:
BUY: avg win +6.58%, avg loss -8.59% (0.77x ratio)
SELL: avg win ≈ avg loss (0.98x ratio)
```
The 1.5x ATR SL for BUY is a deliberate fix for the loser-size problem. But:
- At 1.5x ATR, a 4h candle wick could trigger the SL before reversing
- The fix reduces loser size but increases loser frequency
- **Net effect unknown** — needs A/B testing

**SEVERITY: High.** BUY SL should be 1.5x ATR only in trending markets (ADX > 25). In low-ADX environments, BUY SL should widen to 2.0x ATR to avoid noise whipsaws. Alternatively, re-test 1.5x ATR empirically after 200+ live trades and compare to 2.0x ATR baseline.

---

## 5. Leverage: 3x on $95 = $285 Max Position

**FINDING: Appropriate but the mental model is wrong.**

**Math:**
- Max notional = $95 × 3 = $285
- Typical notional per trade: riskAmount / atrDist × price
- For BTC ($60k, ATR=$800, riskPct=0.75%): $0.7125 risk / ($800 × 1.5) × $60,000 = **$35.63 notional** → margin = $11.88
- For ETH ($2.5k, ATR=$80): $0.7125 / ($80 × 2.0) × $2,500 = **$11.13 notional** → margin = $3.71

**Assessment:** 3x leverage is conservative. The actual risk driver is riskPct (0.35–0.75%), not leverage. Leverage only affects margin efficiency.

**Liquidation risk at 3x:** 33.3% adverse move wipes position. With SLs at 1.5–3.0x ATR:
- BTC: SL at ~1.5–4.5% from entry → liquidation at 33.3% → SL fires first ✓
- Small alt: SL at 3–7.5% → liquidation at 33.3% → SL fires first ✓
- **SL always fires before liquidation** — proper.

**SEVERITY: Low.** 3x is fine for $95 capital. Could even go to 5x without increasing risk (riskPct is the real limiter). Consider 5x for better capital efficiency.

---

## 6. Flash Crash, Black Swan, Exchange Outage

**FINDING: Multiple failure modes, no protection for gap-through scenarios.**

### Flash Crash (e.g., BTC -20% in minutes):
1. **Limit SP order won't fill** — It's a limit order at the entry price. In a flash crash, the entry won't trigger. If the bot is trying to BUY during a crash, the order sits unfilled. ✓ (protected by limit order design)
2. **SL is a limit TP/SL order** — Bybit's bracket SL is a conditional limit/stop order. If price gaps THROUGH the SL price, the stop-loss activates as a LIMIT order at the SL price, which won't fill in a fast market.
3. **Result: position bleeds to liquidation.**
4. No market-order SL alternative exists.

### Black Swan (e.g., exchange hack, regulatory ban, -50% move):
1. **Bybit API connectivity** — main.go:73-83 halts if wallet fetch fails in live mode ✓
2. **Open positions cannot be closed** — the bot has no emergency "close all positions" function. If Bybit stays up but prices plummet, positions ride down.
3. **3x leverage means 33.3% move = liquidation** — for most crypto assets, a 33.3% move is rare. For leveraged tokens or small caps, possible.
4. **Isolated margin protects wallet** — each position's loss is capped to its margin ✓
5. **No circuit breaker for volatility** — no VIX-style volatility guard, no "pause if 24h volatility > X%"
6. **No max daily loss limit** — only 15% total drawdown halt (not per-day)

### Exchange Outage:
1. **Bot halts if API is down at cron start** (main.go:77) — os.Exit(1) ✓
2. **If exchange goes down AFTER positions open** — SL/TP are server-side (Bybit holds them). They execute regardless of bot availability ✓
3. **If exchange goes down and server-side orders are also affected** — catastrophic loss. Uncontrollable. ✓ (platform risk, not bot risk)

### Risk Table:
| Scenario | Bot Response | Risk to Capital | Mitigation Needed |
|----------|-------------|-----------------|-------------------|
| Flash crash | Limit SL won't fill | Position loss + liquidation | Market-order SL |
| Black swan | Positions ride down | Up to 29% (see §7) | Volatility circuit breaker |
| Exchange outage (start) | Halts safely | 0% | None needed |
| Exchange outage (mid-trade) | Bybit server-side SL fires | Platform risk | None (unsolvable) |
| Gap through SL | SL becomes limit order, doesn't fill | Full margin at risk | Market-order SL |

**SEVERITY: Critical** — add market-order stop-loss option and volatility-based position freeze.

---

## 7. Path to Losing All Capital — Max Drawdown Scenario

**FINDING: Losing ALL capital requires a multi-stage compound failure. ~29% max realistic loss in a single cycle.**

### Worst Realistic Single-Cycle Loss:
```
Capital: $95.00
5 positions max (frozen at 5)
Each at 0.75% risk = $0.7125 risk each
BUY asymmetry = 0.65x → $0.463 risk each for BUY
Total risk at portfolio level: 5 × $0.75 (worst-case all SELL) = $3.75
With gap-through (SL doesn't fill): margin at risk per position
  BTC: ~$11.88 margin → $11.88 loss potential
  5 × $11.88 = $59.40 loss (62.5% of capital)
BUT portfolio cap would limit 5 SELL trades to 4% risk = $3.80 total SL risk
  Gap-through could exceed SL risk by 3-4x → ~$15 exposure
```

**Corrected worst case:**
- Portfolio cap allows 4% SL risk = $3.80
- Gap-through multiplier: 2-5x SL distance → $7.60–$19.00 loss (8–20% of capital)
- With 3x leverage and isolated margin, max loss per position = position margin
- Combined isolated margin across 5 positions ≈ $30–$50 → **31–52% of capital**

### Multi-Cycle Death Spiral:
```
1. Five trades → $15 loss (gap-through) → capital = $80
2. Drawdown: $15/$95 = 15.8% → **EMERGENCY HALT** → bot freezes
3. But existing positions still ride → if another gap-through → $5 more → $75
4. Next run (if restarted): drawdown 21% → HALT again
5. Bot keeps halting until positions close naturally
```

### Can all capital be lost?
**No, with current safeguards:**
- Isolated margin caps per-position loss
- 3x leverage means 33.3% move = full loss of that position's margin only
- 15% drawdown halt freezes new entries
- Portfolio cap limits aggregate SL risk to 4%

**Yes, if these conditions ALL combine:**
1. `peak_equity.txt` is corrupted/lost → drawdown ladder disabled
2. Exchange is very volatile but API stays up → gap-throughs on all positions
3. Bot keeps running (no halt)
4. Multiple cycles compound losses
5. Eventually capital erodes to near-zero

### Recommended Safeguards (Missing):
| Safeguard | Why Missing | Priority |
|-----------|-------------|----------|
| Volatility circuit breaker | No VIX/ATR-based pause | HIGH |
| Market-order SL | Bybit's bracket SL is limit-based not market | CRITICAL |
| Max daily loss (%) | Only total drawdown, not daily | MEDIUM |
| Cross-cycle portfolio risk | Counter resets per run | HIGH |
| Correlation-aware sizing | 5 correlated alts = same risk as 1 BTC | MEDIUM |
| Emergency close-all button | No "liquidate all" API call | LOW (manual) |
| `peak_equity.txt` failsafe | Silent reset on missing file | HIGH |

---

## Action Items Before Risking Real Money

### CRITICAL (Fix before any real capital):
1. **Market-order stop-loss**: Change the bracket SL from limit to market order so gaps-through get filled. On Bybit: `triggerBy="markPrice"` with market order execution. Without this, a 5% gap-through turns into a 15-20% loss or full liquidation.
2. **`peak_equity.txt` failsafe**: Use an absolute path (`~/.hermes/peak_equity.txt`). Crash loudly if file is deleted mid-run rather than silently resetting to 0.
3. **BUY SL regime-adaptation**: SL for BUY should use 2.0x ATR when daily ADX < 25 (ranging/noise), keep 1.5x ATR when ADX ≥ 25 (trending). The 1.5x blanket is too tight for low-volatility environments.

### HIGH (Fix before meaningful capital >$500):
4. **Volatility circuit breaker**: Pause trading if any asset has >15% 24h price swing, or if VIX-style cross-market volatility index is elevated. This prevents entering into a flash crash.
5. **Cross-cycle portfolio risk tracking**: Write globalPortfolioRiskPct to a file (alongside peak_equity.txt) so it persists across cron runs. Sum actual SL distances of all open positions from Bybit API rather than relying on in-memory counters.
6. **Max daily loss circuit breaker**: Track PnL per day. If losses exceed 5% of capital in a single day, freeze entries for the remainder of the day. This is separate from the total drawdown ladder.

### MEDIUM (Fix for scaling / production):
7. **Correlation-aware sizing**: If 3+ open positions are on the same side and same sector (all L1 alts longs), apply an additional 0.5x risk multiplier. A BTC flash crash would wipe correlated longs simultaneously.
8. **Emergency close-all function**: Expose an API endpoint or script that sends "close all" to Bybit for all open linear positions. Manual panic button.
9. **Kelly input (optional)**: Compute actual Sharpe/info ratio per signal type and use 0.1–0.25× fractional Kelly to dynamically adjust base riskPct. This improves sizing efficiency over time.
10. **Drawdown ladder granularity**: Add 2% and 5% intermediate tiers so the ladder steps down more gradually (avoids dropping from 1.0x to 0.75x at the 4% boundary).

### LOW (Nice-to-haves):
11. **Slippage model in position sizing**: Deduct expected slippage (0.5 × ATR bid-ask spread) from position size calculation. Currently assumes frictionless fills.
12. **`peak_equity.txt` corruption-safe format**: Use JSON with a checksum so silent corruption doesn't reset peak equity.

---

## Summary

The bot's risk architecture is **functional but has critical gaps** that could turn a normal loss into a catastrophic one:

- **The biggest danger is gap-through SLs** — limit-order SLs don't fill in fast markets, turning a 4% calculated risk into a full-margin liquidation.
- **BUY SL is too tight** (always 1.5x ATR) — empiricially justified but likely increases whipsaw frequency in low-ADX markets.
- **Portfolio risk tracking resets every cycle** — the 5-position freeze from Bybit is the only cross-cycle guard, but it counts positions, not risk $. Five 0.75% positions all in correlated alts is exponentially riskier than five 0.35% positions across uncorrelated assets.
- **Peak equity tracking silently fails** on cwd changes or file deletion.
- **No volatility guard** means the bot could enter trades during a crash.

The bot will probably survive normal market conditions. But in a flash crash (like 3/12/20 crypto -50% day), the gap-through risk on limit-order SLs could lose 20-50% of capital. Fix the market-order SL and `peak_equity.txt` path before risking real money. Add the volatility circuit breaker before scaling past $500.