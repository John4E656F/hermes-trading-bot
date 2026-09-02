# Hermes Trading Bot — Risk & Measurement Validation Report

**Date:** 2026-09-02
**Branch:** `claude/hermes-risk-patch-verify-wvh2fb`
**Scope:** Risk infrastructure and measurement only. No new strategies, indicators or
AI models were added, and no S1/S2/S3 entry or exit logic was tuned.

---

## 0. Starting state — what was actually there

A prior session was reported to have produced `HERMES_VALIDATION_REPORT.md`,
`executor_risk_fix.patch` and `episode_dedup.py`, and to have patched `executor.go`.

**None of those files existed.** Not in the working tree, not in `git` on any branch:

```
$ find / -name "HERMES_VALIDATION_REPORT.md" -o -name "executor_risk_fix.patch" \
       -o -name "episode_dedup.py"
(no output)

$ git log --all --oneline --name-only | grep -iE "validation|risk_fix|dedup"
(no matching files)
```

`src/executor.go` still carried the original risk table:

```go
case confidence >= 0.85:
    riskPct = 0.025 // 2.5% — META / high-conviction
case confidence >= 0.75:
    riskPct = 0.020 // 2.0%
default:
    riskPct = 0.015 // 1.5% — baseline Conv2/70%
```

Nothing from that session was committed. Step 0 was therefore a full re-derivation
rather than a verification.

### Environment constraints that shape everything below

| Constraint | Effect |
|---|---|
| `api.bybit.com`, `api.bytick.com`, `api.bybit.nl`, testnet — **all CloudFront geo-blocked** | No Bybit market data. Not a proxy policy denial; the CDN blocks this container's country. |
| `fapi.binance.com` — **geo-blocked** | Not available as a substitute either. |
| No `.env`, no Bybit API credentials | Nothing requiring account authentication could be executed against the live account. |
| OKX, Bitget, Gate.io, KuCoin, Coinbase — reachable | **OKX USDT-SWAP perpetuals are used as the market-data source** for Steps 3–5. |

> **Venue substitution — read this before acting on any backtest number below.**
> Hermes trades **Bybit** linear perpetuals. Every backtested and counterfactual
> figure in this report is computed on **OKX** USDT-SWAP prices, funding and open
> interest. For liquid pairs the OHLCV tracks closely, but funding rates are set
> per venue and differ materially, and thin alt-perp pairs can diverge more than
> majors. These are estimates of the strategy's behaviour on OKX data, not a
> replay of Bybit fills.

---

## Step 0 — Risk sizing and the portfolio open-risk cap

### Changes

`src/executor.go`:

```go
 	var riskPct float64
 	switch {
 	case confidence >= 0.85:
-		riskPct = 0.025 // 2.5% — META / high-conviction
+		riskPct = 0.0075 // 0.75% — highest-confidence tier
 	case confidence >= 0.75:
-		riskPct = 0.020 // 2.0%
+		riskPct = 0.0050 // 0.50%
 	default:
-		riskPct = 0.015 // 1.5% — baseline Conv2/70%
+		riskPct = 0.0035 // 0.35% — baseline Conv2/70%
 	}
```

New `MAX_PORTFOLIO_RISK_PCT = 0.03` and `currentOpenRiskPct()`, which sums
`|entry − stop| × size` across every live linear position. Positions with **no**
stop loss, or a stop on the wrong side of entry, are charged at **full notional** —
they can lose everything and should block further entries until a stop is attached.

The cap is enforced independently of the 5-position count limit. That limit bounds
how *many* bets are open and says nothing about how large each one is: five
positions at the old 2.5% was 12.5% of the account on one correlated move. When
open risk cannot be verified, the entry **fails closed**.

### Build verification (with real network access)

```
$ go build -o /dev/null ./src/ && echo "SRC BUILD OK"
SRC BUILD OK
$ go vet ./src/ && echo "SRC VET OK"
SRC VET OK
$ go list -deps ./src/ | grep godotenv
github.com/joho/godotenv          # dependency resolved normally over the network
```

Risk percentages are held at 0.75/0.50/0.35% pending Step 4.

---

## Step 1 — Peak-relative drawdown breaker

The previous tiers keyed off **absolute dollar** thresholds (`$20 / $50 / $75`),
which do not measure risk. An account that grows to $400 and falls to $80 has lost
80% of its capital while sitting in the "CAUTION MODE" band; the same $80 on an
account that never exceeded $85 is a 6% drawdown. Both were treated identically.

`src/equity_guard.go` (new) replaces them with tiers relative to persisted peak equity:

| Drawdown from peak | Tier | Risk multiplier | Min conviction | New entries |
|---|---|---|---|---|
| < 5% | `NORMAL` | ×1.00 | 2 | yes |
| ≥ 5% | `RISK_75` | ×0.75 | 2 | yes |
| ≥ 8% | `RISK_50` | ×0.50 | 2 | yes |
| ≥ 10% | `CONVICTION_3_ONLY` | ×0.50 | 3 | yes |
| ≥ 12% | `NO_NEW_ENTRIES` | ×0.50 | 3 | **no** |
| ≥ 15% | `HARD_HALT` | ×0 | — | **`os.Exit(1)`** |

**Persistence.** Peak equity is written to `equity_state.json` (temp file + rename)
alongside `signal_log.jsonl`. The bot runs on a 15-minute cron, so a peak held only
in memory resets to the current balance every invocation, making the measured
drawdown permanently 0% and the guard permanently inert. Basis is Bybit
`totalEquity` (wallet + unrealised PnL), which is what `fetchLiveBalance` already returns.

**The hard halt latches.** Once `-15%` fires, `halted: true` persists and a recovery
above the line does not resume trading. Clearing requires deleting `equity_state.json`
or setting `"halted": false` — that is the manual review requirement.

**Scan mode never writes state.** Scan falls back to a dummy `$100` balance when the
wallet fetch fails; letting that touch the high-water mark would destroy it.

**The absolute floor is retained underneath, not instead of.** `ApplyAbsoluteFloor`
blocks entries when equity cannot fund a minimum-notional order plus fees. A large
account deep in drawdown clears that floor easily and is still restricted by the
percentage tiers above.

### Test output

```
$ go test ./src/ -run 'TestDrawdown|TestPeak|TestHardHalt|TestScanMode|TestAbsoluteFloor' -v
=== RUN   TestDrawdownTiers
    --- PASS: TestDrawdownTiers/at_peak                    (1000 -> 1000, NORMAL)
    --- PASS: TestDrawdownTiers/-4.9%_just_above_tier1      (1000 ->  951, NORMAL)
    --- PASS: TestDrawdownTiers/-5%_risk_75                 (1000 ->  950, RISK_75)
    --- PASS: TestDrawdownTiers/-7.9%                       (1000 ->  921, RISK_75)
    --- PASS: TestDrawdownTiers/-8%_risk_50                 (1000 ->  920, RISK_50)
    --- PASS: TestDrawdownTiers/-10%_conv3_only             (1000 ->  900, CONVICTION_3_ONLY)
    --- PASS: TestDrawdownTiers/-12%_no_new_entries         (1000 ->  880, NO_NEW_ENTRIES)
    --- PASS: TestDrawdownTiers/-15%_hard_halt              (1000 ->  850, HARD_HALT)
    --- PASS: TestDrawdownTiers/-40%_hard_halt              (1000 ->  600, HARD_HALT)
--- PASS: TestPeakSurvivesRestart      (peak survives a simulated process restart)
--- PASS: TestHardHaltLatches          (recovery to -2% stays HALTED)
--- PASS: TestScanModeDoesNotPersist   (scan-mode dummy balance does not touch the peak)
--- PASS: TestAbsoluteFloorIsSecondary (floor does not fire on a large account at -10%)
PASS
ok  	hermes-bot/src	0.014s
```

---

## Step 2 — `outcome_log.jsonl` normalized schema

### Why the outcome tracker produced nothing — two independent causes

**Cause 1: the log paths pointed outside the repository.**

```
$ grep -rn '"\.\./' src/*.go
src/outcome_tracker.go:163:	const outcomePath = "../outcome_log.jsonl"
src/outcome_tracker.go:180:	data, err := os.ReadFile("../outcome_log.jsonl")
src/trade_log.go:41:const tradeLogPath = "../trade_log.jsonl"

$ head -3 run-bot.sh
#!/bin/bash
# Hermes bot hourly scan — fast mode
cd /home/hermes/hermes-trading-bot || exit 1
```

The working directory *is* the repository root, so every outcome record was written
to `/home/hermes/outcome_log.jsonl` — one directory above the repo, outside version
control. Commit `69b61d3` fixed this for `kronos_log.jsonl` but not for these two.

**Cause 2: the tracker never ran in production.**

```go
if !scanMode {
    PrintActivePositionsQueries(client)
    PrintRecentClosedPnLSummary(client)
    UpdateTradeOutcomes(client)      // <- gated on !scanMode
}
```

`run-bot.sh` invokes the bot with `--mode=scan`. `UpdateTradeOutcomes` had therefore
**never executed**. Outcome recording is read-only bookkeeping and now runs in every mode.

### The schema

`src/outcome_log.go` defines `OutcomeRecord` — one line per **completed** trade, with
every field the task specified. Two provenance rules matter more than the field list:

- **A value that could not be determined is `null`, never `0`.** "No funding data"
  must not be silently averaged in as "zero funding cost".
- **Every failed enrichment step is named** in an `incomplete` array, so a consumer
  can filter on data quality instead of trusting every row equally. Enrichment
  failures degrade individual fields; they never drop a trade.

`buildOutcomeRecord` joins Bybit closed-PnL rows against `trade_log.jsonl` for
signal-time context using a **tight** time window (≤6h before the fill, ≤5min after).
A loose match would attribute a trade to the wrong strategy, which corrupts
per-strategy expectancy more quietly than no attribution at all. Unmatched trades
are recorded as `UNATTRIBUTED` and flagged.

`TradeLogEntry` gained `regime`, `confirming_strategies`, `council_votes`,
`kronos_direction` and `kronos_confidence`, recorded at entry. **Kronos is logged on
every trade whether or not it gated that trade** — a layer can only be measured
against the trades it rejected if its call on those trades was written down.

### Verification

Live Bybit is unreachable, so the pipeline was verified against a fixture in the
exact shape of `/v5/position/closed-pnl` (string-typed numbers, real field names):

```
$ go test ./src/ -run 'TestBuildOutcome|TestOutcomeDedup|TestUnattributed' -v
=== RUN   TestBuildOutcomeRecordFromBybitShape
--- PASS: TestBuildOutcomeRecordFromBybitShape (0.00s)
=== RUN   TestOutcomeDedupByTradeID
--- PASS: TestOutcomeDedupByTradeID (0.00s)      # 3 passes over the same rows -> 2 records
=== RUN   TestUnattributedTradeIsFlagged
--- PASS: TestUnattributedTradeIsFlagged (0.00s) # null result_r, not 0
PASS
ok  	hermes-bot/src	0.005s
```

The tests assert the derived fields, not just that a record appeared: position side
inverted from the closing side, `gross_pnl = closedPnl + openFee + closeFee`,
`result_r = net / (wallet × risk_pct)`, equity before/after, and that offline
enrichment leaves `funding_cost` and MAE/MFE `null` rather than `0`.

### Migration

`migrate_outcomes.py` reconstructs only what is unambiguous and quarantines the rest
with a stated reason.

```
$ python3 migrate_outcomes.py

  migrated   :   6 completed trades -> outcome_log.migrated.jsonl
  deduped    :   5 rows appearing in both history and journal
  quarantined:  20 rows -> outcome_log.quarantine.jsonl

  Exclusion reasons:
     12  order never resolved -- no exit, cannot compute PnL
      5  hand-written analysis note, not a trade
      3  order placed but no exit recorded -- position open or outcome never logged

  Migrated trades (net PnL is all that survives; no R, no fees, no exits):
    2026-05-28  XRPUSDT    BUY   net=$  -2.70  9 unusable fields
    2026-05-28  BEATUSDT   BUY   net=$  -2.69  9 unusable fields
    2026-05-28  UBUSDT     BUY   net=$  -2.83  9 unusable fields
    2026-05-29  NEARUSDT   BUY   net=$   0.00  9 unusable fields
    2026-05-30  NEARUSDT   BUY   net=$  -0.62  9 unusable fields
    2026-06-01  INJUSDT    BUY   net=$   2.46  9 unusable fields

    n=6  net=$-6.38  wins=1  losses=5
```

**`trade_history.json`'s 16-row `trades` array contains 6 actual completed trades.**
The other 10 are 6 unresolved orders and 4 hand-written `SYSTEM`/`ANALYSIS` notes.
The live decision log `trade_log.jsonl` contributed **zero** completed trades — all
3 rows are orders with no recorded exit.

Six trades is not a sample. No statistic computed on it means anything, and none is
computed on it below.

---

## Step 3 — Episode deduplication, applied to every strategy lens

### The mechanism

Hermes runs on a short cron and re-evaluates the same open market situation on
every pass, writing a fresh row each time. When those rows are later resolved
against the same forward price, **one market event becomes N rows in the
denominator**. That does not merely inflate the sample size — it inflates
*confidence*, because N correlated observations are counted as N independent ones.

### A wrong episode definition, and why it looked right

The first definition keyed on `(symbol, direction, exact resolution timestamp)`.
On `kronos_outcomes.jsonl` it works, because `resolved_at` is a shared batch
timestamp (571 distinct values over 3,106 rows). On `signal_log.jsonl` it collapses
**nothing**, because every row carries a nanosecond timestamp and therefore a unique
resolution instant. It reported a tidy `1.00x` inflation on every lens — while this
sat in the sample:

```
largest group ('ADAUSDT', 'SELL')  154 rows
    2026-06-10T18:56:34 Trend Sell          0.1636
    2026-06-10T19:58:24 Strict Sell         0.1609
    2026-06-11T07:16:35 Trend Sell          0.1646
    2026-06-11T13:29:01 Trend Sell          0.1646
    2026-06-11T14:23:54 Trend Sell          0.1646
    2026-06-11T16:26:36 QUALITY: Trend Sell 0.1652
    ...
```

154 rows of one standing bearish call on one asset.

**The definition used below:** an episode is a **maximal run of `(symbol, direction)`
rows whose consecutive gaps are shorter than the outcome horizon** (24h). A
24h-horizon call re-issued an hour later is not an independent observation — it
resolves against overlapping price action and comes from the same market state. A
new episode begins when the direction flips or the signal goes quiet for a full
horizon. The representative kept is the **first** observation, the only one
available in real time.

### Results — raw vs episode-deduplicated, per lens

```
$ python3 analysis/episode_dedup.py

A. kronos_outcomes.jsonl
========================================================================================================
Lens                                 raw n   raw acc   epi n   epi acc  inflation     delta
--------------------------------------------------------------------------------------------------------
Master blended action                 2971     43.4%     332     47.9%      8.95x    +4.5pp
Kronos (AI overlay)                    526     49.0%     186     43.5%      2.83x    -5.5pp
--------------------------------------------------------------------------------------------------------

B. signal_log.jsonl — 840 raw rows, resolved against OKX 1H OHLCV
    resolved: 762   dropped: 0   not listed on OKX: 13
    excluded: 1000TOSHIUSDT, BTRUSDT, BTWUSDT, DEXEUSDT, GWEIUSDT, HNTUSDT, IPUSDT,
              NIULAIUSDT, ONGUSDT, TMXUSDT, TONUSDT, VELVETUSDT, XMRUSDT
========================================================================================================
Lens                                 raw n   raw acc   epi n   epi acc  inflation     delta
--------------------------------------------------------------------------------------------------------
ALL SIGNALS (blended)                  762     39.6%     171     45.6%      4.46x    +6.0pp
S5 BB Squeeze                          107     56.1%      63     54.0%      1.70x    -2.1pp
Trend Sell                             435     31.5%      39     35.9%     11.15x    +4.4pp
Strict Sell                             76     48.7%      34     52.9%      2.24x    +4.3pp
S4 Funding Contrarian                   59     54.2%      24     50.0%      2.46x    -4.2pp
Strict Buy                              41     46.3%      17     41.2%      2.41x    -5.2pp
Trend Buy                               34     47.1%       8     37.5%      4.25x    -9.6pp
Overbought Sell                          9     11.1%       7     14.3%      1.29x    +3.2pp
Oversold Buy                             1      0.0%       1      0.0%      1.00x    +0.0pp
--------------------------------------------------------------------------------------------------------
```

### What these numbers say

- **Inflation is not uniform, so an aggregate figure hides it.** `Trend Sell` is
  inflated **11.15×** (435 rows → 39 episodes); `S5 BB Squeeze` only **1.70×**. The
  strategy that fires most often on a persistent condition is the one whose sample
  size is most illusory — and `Trend Sell` is 435 of the 762 resolved rows, 57% of
  the raw log.
- **Deduplication moves accuracy in both directions.** It is not a uniform haircut.
  Kronos drops from 49.0% to **43.5%**; the master action rises from 43.4% to 47.9%.
  Whichever direction it moves, the honest denominator is the smaller one.
- **Every episode-deduplicated lens sits near or below a coin flip.** The best is
  `S5 BB Squeeze` at 54.0% over 63 episodes; the worst meaningful one is
  `Trend Sell` at 35.9% over 39.
- **`n` is small.** After deduplication the entire signal history is **171 independent
  episodes**, and no single lens exceeds 63. At n=63, a 54.0% hit rate has a 95%
  confidence interval of roughly 41%–66% — it does not distinguish itself from chance.

### S1/S2/S3 could not be measured from `signal_log.jsonl` at all

This is not a logging gap; it is the design. From `src/allocator.go:56`:

```go
//	S1/S2/S3 → supporting, never generate alone
```

S1/S2/S3 never produce a master strategy label, so they never appear in the log's
`strategy` field, and no amount of deduplication recovers an accuracy for them.
The lens table above is complete for what was recorded.

Two things were done about it:
1. `SignalSnapshotEntry` now records `s1_action`…`s5_action`, `active_strategies`,
   `kronos_direction`, `council_verdict` and `regime` on every evaluated signal, so
   each lens is measurable **going forward**.
2. Their historical behaviour is measured by replaying them independently against
   historical OHLCV — which is Step 4.

---

## Step 6 — Co-ranking mislabeling

### What it claimed

`README.md`:

```
| **Co‑Ranking** | Top 3 by 7D gain/loss, strategy dedup | 🏆 Prevent correlation |
```

`src/risk_guards.go`:

```go
// ─── Feature 3: Relative Strength Co-ranking ────────────────────────────
// RankedSignal pairs a validated signal with its trailing 7-day strength
// so the co-ranking sort can put the strongest performers first.
```

`src/main.go`:

```go
// Only take the strongest signal per strategy type per cycle.
// Prevents correlated entries (e.g. two S4 BUY signals simultaneously).
```

### What it does

It sorts candidates by trailing 7-day price change and keeps the strongest (for
BUYs) or weakest (for SELLs) three. That is a momentum / relative-strength filter.

It does not merely fail to prevent correlated exposure — **it selects for it**.
Assets that move together have *similar* 7-day gains, so ranking by 7-day gain and
taking the top three preferentially picks names that already move as a group. In a
market where the majors move together most of the time, "top 3 by 7D gain" is close
to "three names with the same beta to the same move".

The strategy-dedup step alongside it limits how many entries share a strategy
**label**, which is also not correlation control: one `S4 Funding Contrarian` BUY
and one `Trend Buy` on two assets that move together are two labels and one
directional bet.

### Changes

| Before | After |
|---|---|
| `RankSignalsByGain` | `RankByRelativeStrength` |
| `RankSignalsByLowestGain` | `RankByRelativeWeakness` |
| `Feature 3: Relative Strength Co-ranking` | `Feature 3: Relative-Strength Ranking` |
| README: `Co‑Ranking … 🏆 Prevent correlation` | `Relative‑Strength Ranking … 📈 Prefer momentum leaders/laggards (no correlation protection)` |

The doc comment now states plainly what the function does and does not do, and the
strategy-dedup comment no longer claims correlation protection.

Real correlation-cluster logic was **not** built — out of scope for this pass, as
specified. What bounds correlated exposure today is the 3% portfolio open-risk cap
from Step 0, which limits total damage from a single correlated move regardless of
how many names it is spread across. That is a blunt instrument, not a substitute
for cluster logic, and it is worth building properly later.

### Verification

```
$ grep -rn "RankSignalsByGain\|RankSignalsByLowestGain" src/*.go
(no matches)

$ go build -o /dev/null ./src/ && go vet ./src/ && go test ./src/
ok  	hermes-bot/src	0.010s
```

---

## Step 4 — Walk-forward backtest

### Method

- **Universe:** the 62 symbols the bot actually evaluated, taken from `signal_log.jsonl`.
  49 are listed on OKX; **13 are not** and are excluded rather than silently dropped:
  `1000TOSHIUSDT, BTRUSDT, BTWUSDT, DEXEUSDT, GWEIUSDT, HNTUSDT, IPUSDT, NIULAIUSDT,
  ONGUSDT, TMXUSDT, TONUSDT, VELVETUSDT, XMRUSDT`.
- **Span:** 2023-12-07 → 2026-09-02 (**2.7 years**), 4H bars with daily context.
- **Walk-forward:** every value at bar *i* is computed from bars `0..i` only. No
  parameter is fitted in the backtest — the strategy parameters are read from the
  shipped code — so the entire history is a single out-of-sample window.
- **Costs:** `TAKER_FEE_RATE` (0.055%) per side, funding at every 8h settlement inside
  the hold window signed by side, 5bps slippage per side (the bot's own fee gate
  assumes 10bps round-trip, so this is its working assumption).
- **Conservative fills:** a bar whose range contains **both** the stop and the target
  resolves as the **stop**. Intrabar sequence is unknowable from OHLCV, and the
  optimistic assumption is how backtests manufacture edges that do not survive a live
  book. Entries fill at the signal bar's close; the live bot places a *limit* order
  that may never fill, so this is **generous** to the strategy.
- **Episode-deduplicated** per Step 3: one trade per `(symbol, side, entry bar)`.

Everything is verified rather than assumed — see "Correctness of the toolchain" below.

### Results

```
$ python3 analysis/run_backtest.py

Loaded 49 symbols in 1252s; 13 not available on OKX
History span: 2023-12-07 -> 2026-09-02

============================================================================================================================================
WALK-FORWARD RESULTS — episode-deduplicated, net of fees, funding and slippage
============================================================================================================================================
Configuration                  n   net ret    maxDD     win  avgW R  avgL R     exp R     PF  Sharpe Sortino    longR   shortR
--------------------------------------------------------------------------------------------------------------------------------------------
S1 mean reversion           3953  -132.82%  144.49%   40.2%    1.29   -0.98  -0.0672   0.89   -2.06   -3.31   -0.033   -0.098
S2 OI/funding squeeze         71     5.31%    2.57%   43.7%    1.42   -0.84  +0.1495   1.32    1.99    3.81   +0.114   +0.212
S3 consolidation breakout      3    -0.23%    0.61%   66.7%    0.38   -1.21  -0.1542   0.62   -0.53   -0.57   -0.154      n/a
BLENDED (flat 0.50% risk)    544    -9.80%   30.58%   44.3%    1.19   -1.01  -0.0360   0.94   -0.43   -0.67   -0.117   -0.006
BLENDED (tiered sizing)      544    -7.82%   25.78%   44.3%    1.19   -1.01  -0.0360   0.94   -0.43   -0.67   -0.117   -0.006
--------------------------------------------------------------------------------------------------------------------------------------------

COST DECOMPOSITION (why gross and net differ)
Configuration               gross exp R   net exp R     fees $   funding $  slippage $  avg MAE R  avg MFE R
------------------------------------------------------------------------------------------------------------
S1 mean reversion               -0.0498     -0.0672    3252.55      186.58     2956.86       0.96       1.10
S2 OI/funding squeeze           +0.0670     +0.1495      53.69     -346.84       48.81       0.81       1.16
S3 consolidation breakout       -0.0715     -0.1542      10.53        1.89        9.57       0.86       1.58
BLENDED (flat 0.50% risk)       -0.0150     -0.0360     668.97      -97.74      608.15       0.89       1.07
BLENDED (tiered sizing)         -0.0150     -0.0360     590.08      -94.72      536.43       0.89       1.07
------------------------------------------------------------------------------------------------------------

EXIT REASON MIX
  S1 mean reversion          {'SL_HIT': 2199, 'TRAIL_STOP': 932, 'TP_HIT': 460, 'TIMEOUT': 362}
  S2 OI/funding squeeze      {'SL_HIT': 33, 'TRAIL_STOP': 20, 'TP_HIT': 10, 'TIMEOUT': 8}
  S3 consolidation breakout  {'TRAIL_STOP': 2, 'SL_HIT': 1}
  BLENDED (flat 0.50% risk)  {'SL_HIT': 290, 'TRAIL_STOP': 195, 'TP_HIT': 42, 'TIMEOUT': 17}
```

### The table above is not interpretable without error bars

```
$ python3 analysis/significance.py

EXPECTANCY WITH ERROR BARS (per-trade R, two-sided t)

Configuration                     n     exp R      sd               95% CI       t  significant?
--------------------------------------------------------------------------------------------
S1 mean reversion              3953   -0.0672    1.25   [-0.1061, -0.0283]   -3.39  yes
S2 OI/funding squeeze            71   +0.1495    1.28   [-0.1473, +0.4463]   +0.99  NO — indistinguishable from zero
S3 consolidation breakout         3   -0.1542    0.93   [-1.2072, +0.8988]   -0.29  NO — indistinguishable from zero
BLENDED                         544   -0.0360    1.19   [-0.1361, +0.0641]   -0.71  NO — indistinguishable from zero
BLENDED (tiered)                544   -0.0360    1.19   [-0.1361, +0.0641]   -0.71  NO — indistinguishable from zero
--------------------------------------------------------------------------------------------
```

- **The blended configuration has no measurable edge in either direction.** Point
  estimate −0.0360R, profit factor 0.94, but the confidence interval spans zero. At
  n=544 the correct statement is "no edge detected", not "loses money".
- **S1 is the one statistically established result, and it is negative.** −0.0672R over
  3,953 trades, t=−3.39. S1 is also the highest-firing lens in the system.
- **S2's positive number is not a finding.** n=71, t=0.99, interval spans zero widely.
  It is also the thinnest sample available: OKX open-interest history reaches back only
  240 days, so S2 could not be evaluated over most of the 2.7-year span.
- **S3 produced 3 trades in 2.7 years.** The consolidation criteria (21 days inside a 5%
  range, then a breakout on 1.5× volume) are effectively never satisfied on this universe.

### The blended configuration is negative *before* costs

Gross expectancy is **−0.0150R**; costs carry it to −0.0360R. This is not an edge being
eroded by fees — there is no edge to erode. Fees and slippage roughly double the loss,
but removing them entirely would not produce a profitable system.

Funding is a small **credit** for the blended set (−$97.74) because it runs net short
into predominantly positive funding.

### A near-miss worth recording

The funding-exact split initially looked like it rescued the system:

```
$ python3 analysis/funding_exact.py
BLENDED (flat 0.50% risk)     95    12.03%    3.86%   55.8%   +0.2533   1.64    4.22    7.86
```

**It is an artifact.** OKX publishes only ~3 months of funding history, so:

```
BLENDED  funding-exact     95 trades   2026-06-02 → 2026-09-02
BLENDED  funding-imputed  449 trades   2023-12-18 → 2026-06-02
Imputed trades entered on or after 2026-06-02: 0
  -> The two groups do not overlap in time at all.
     'Funding-exact' is a synonym for 'the last three months'.

  last 3 months        n=95   exp +0.2533R  95% CI [+0.0173, +0.4894]  t=+2.10
  everything before    n=449  exp -0.0972R  95% CI [-0.2070, +0.0126]  t=-1.74
```

"Funding-exact" and "the last three months" are the same 95 trades. The split is a
period comparison, not a data-quality one, and says nothing about whether imputation
distorted the headline. The last three months did look better — on a recent window
selected after the fact, at n=95, which is exactly the shape of a result that does not
replicate.

### Reading notes

- **`maxDD 144.49%` for S1 is not a typo.** Position size is computed from a fixed
  starting equity rather than compounding, so the curve is additive. It means S1 lost
  1.44× the starting capital at its worst point.
- **Tiered sizing changes only magnitude.** Identical trades, identical expectancy in R;
  the smaller risk budget reduces the loss (−9.80% → −7.82%) and the drawdown
  (30.58% → 25.78%). Sizing controls damage, not edge.
- **MAE ≈ MFE across every configuration** (0.89 vs 1.07 R for blended). Trades go about
  as far against as for — the signature of entries with no directional information.

### Correctness of the toolchain

A backtest is only worth what its engine is worth, so each layer is checked rather than
trusted:

```
$ CANDLES_JSON=/tmp/candles.json go test ./src/ -run TestDumpIndicatorsForPortCheck
  15/15 Python indicator ports match the Go originals to 0.000e+00 relative difference

$ python3 analysis/verify_series.py
  ema20/rsi14/atr14/adx14 series equal the pointwise forms at every index (max 0.000e+00)

$ python3 analysis/verify_bar_state.py
  fast bar state == full-prefix recomputation across 16 fields
  sub-strategy + master-signal disagreements: 0

$ python3 analysis/verify_simulator.py
  15/15 checks on hand-built price paths: TP/SL fills both sides, cost application,
  no look-ahead from the signal bar, the fee gate, funding sign convention, MAE/MFE
```

### What is NOT modelled

Stated plainly, because each of these would move the numbers:

- **Venue.** OKX prices, funding and OI — not Bybit. Funding differs per venue, which
  bears directly on S2 and S4.
- **Limit-order fill risk.** Entries are assumed to fill at the signal bar's close. The
  live bot posts a limit order that may never fill. This flatters the strategy.
- **The AI layers.** Kronos, the AI Council, reflection memory and sentiment are
  non-deterministic external calls that cannot be replayed from OHLCV. The blended
  configuration above is the deterministic indicator stack as it behaves when Kronos
  returns hold/unavailable. Those layers are measured separately in Step 5.
- **S2 coverage.** OI history reaches back 240 days, so S2 is evaluated over roughly
  a quarter of the span the other lenses are.

---

## Step 5 — Counterfactual measurement of every AI layer

### Method

The question a filter must answer is not "is it accurate?" but **"does acting on it
beat ignoring it?"** A filter that is right 60% of the time still destroys expectancy
if the 40% it blocks were the large winners.

Every recorded signal is replayed through the **same** bracket simulator used in Step 4
— ATR stop and target, trailing stop, taker fees, funding, slippage — **regardless of
whether the layer let it through**. The trade a layer *blocked* gets a result in R just
like the trade it allowed. A layer helps only if the set it **allowed** has higher
expectancy than the set it **blocked**.

### Layer 1 — Kronos AI overlay

```
$ python3 analysis/counterfactual.py
  kronos_outcomes.jsonl: 3106 rows -> 1140 episodes

Set                                             n     exp R    win%      PF    total R
--------------------------------------------------------------------------------------------
WITHOUT Kronos (trade every signal)           991   -0.3630   29.6%    0.48    -359.71
WITH Kronos filter (only when it agrees)       59   -0.5254   22.0%    0.26     -31.00
  -> what the filter BLOCKS (disagrees)        70   -0.3138   25.7%    0.51     -21.97
  -> Kronos had no call (hold)                862   -0.3558   30.4%    0.49    -306.74
  -> Kronos made any directional call         129   -0.4106   24.0%    0.39     -52.97
--------------------------------------------------------------------------------------------
```

The point estimates run the wrong way for the layer: what Kronos **allowed** (−0.5254R)
is worse than what it **blocked** (−0.3138R). But that ordering is not established:

```
$ python3 analysis/layer_significance.py

  allowed − blocked  = -0.2117R   Welch t = -1.46   NOT significant
  allowed − baseline = -0.1625R   Welch t = -1.62   NOT significant
```

**Kronos does not measurably change expectancy in either direction.** It makes a
directional call on only 129 of 991 episodes (87% "hold"), so it is inert on most
signals and indistinguishable from noise on the rest.

### Layer 2 — 5-model AI Council

```
  signal_log.jsonl: 28 Council rejections, 12 Council passes recorded

Set                                             n     exp R    win%      PF    total R
--------------------------------------------------------------------------------------------
WITHOUT Council (trade both sets)              28   -0.6274   14.3%    0.23     -17.57
WITH Council filter (it allowed)               11   -0.8030    9.1%    0.11      -8.83
  -> what the Council BLOCKED                  17   -0.5138   17.6%    0.31      -8.73
--------------------------------------------------------------------------------------------

  allowed − blocked  = -0.2892R   Welch t = -1.04   NOT significant
  allowed − baseline = -0.1756R   Welch t = -0.74   NOT significant
```

Same shape, same non-result, on a far smaller sample. **n=11 allowed and n=17 blocked
is not a measurement of anything** — it is reported with its sample size so it cannot be
mistaken for one.

The Council is the most expensive component in the system: five model calls per
non-HOLD signal, per cycle. Its measured contribution to expectancy is zero, on a
sample too small to have detected a contribution if one existed. **That is a statement
about the evidence, not a verdict on the Council.** The instrumentation added in Step 2
(`council_verdict`, `council_votes` on every logged signal) is what makes a real
measurement possible from here.

### Layer 3 — TradingAgents-derived agents

```
  No TradingAgents-derived code exists in this repository. Nothing to measure.
```

Verified by search across all Go, Python and Markdown sources: no `TradingAgents`,
`Tauric`, bull/bear researcher, risk debator or fundamentals-analyst code has been
added to this repository. There are no such verdicts to counterfactual.

### Control — market regime, for scale

```
Set                                             n     exp R      95% CI
--------------------------------------------------------------------------------------------
regime = MIXED                                 41   +0.0540   [-0.2719, +0.3800]
regime = RANGING                              120   +0.1345   [-0.0805, +0.3494]
regime = TRENDING                             830   -0.4555   [-0.5160, -0.3950]
--------------------------------------------------------------------------------------------

  RANGING − TRENDING = +0.5900R   Welch t = +5.18   significant
```

**The regime split moves expectancy by 0.59R and is highly significant. Neither AI
layer moves it measurably at all.** Whatever is driving results in this system, it is
not the AI stack — it is that the strategies bleed badly in ADX>25 trending markets
(−0.4555R over 830 episodes) and roughly break even elsewhere.

This is a control, not a recommendation: "only trade in RANGING" is a filter chosen
after seeing the data, on a set the strategies were not designed for, and it would need
prospective validation before it meant anything.

### One caveat on the baseline

The −0.3630R baseline here is the **ungated** signal population — every master action
in `kronos_outcomes.jsonl`, with no conviction floor, no confidence floor, no top-3
ranking, no BTC macro filter, no strategy dedup. Step 4's blended figure (−0.0360R) is
the **gated** set. They are not the same population and should not be compared as if
the difference were a treatment effect.

Over roughly the same window (2026-06 → 2026-09) the ungated set ran −0.3630R (n=991)
while the gated set ran +0.2533R (n=95). That is suggestive that the deterministic gate
stack does real work — but the gates *select* that subset, which is precisely what makes
the comparison unreliable, and the same gated configuration over the full 2.7 years is
−0.0360R. Treat it as a hypothesis to test prospectively, not a finding.

---

## Verdict

**Hermes does not have demonstrable positive expectancy after fees, funding and
slippage.**

Over **544 episode-deduplicated trades across 2.7 years** in the blended,
conviction-stacked configuration the system actually ships:

| Metric | Value |
|---|---|
| Expectancy | **−0.0360 R** per trade (95% CI −0.1361 to +0.0641) |
| Profit factor | **0.94** |
| Sharpe | **−0.43** |
| Sortino | **−0.67** |
| Max drawdown | **30.58%** (25.78% at the new tiered sizing) |
| Net return | **−9.80%** (−7.82% at the new tiered sizing) |
| Win rate | 44.3% |
| Avg winner / loser | +1.19 R / −1.01 R |
| Long / short expectancy | −0.117 R / −0.006 R |

The precise statement is **"no edge detected"**, not "proven to lose money". At n=544
the confidence interval spans zero. The point estimate is negative, the profit factor is
below 1.0, and the Sharpe is negative — but none of that is statistically established.

What **is** established, at the 95% level:

- **S1 mean reversion is a losing strategy**: −0.0672 R over 3,953 trades, t = −3.39.
  It is also the highest-firing lens in the system.
- **Trending markets are where the money goes**: −0.4555 R over 830 episodes,
  t = +5.18 against ranging markets.

What is **not** established, and should not be claimed in either direction:

- S2 (+0.1495 R, n=71, t=0.99) — the only positive per-strategy point estimate, on the
  thinnest sample, over a window limited to 240 days by OI history depth.
- S3 — 3 trades in 2.7 years.
- Kronos — inert on 87% of signals, indistinguishable from noise on the rest.
- The AI Council — n=11 allowed, n=17 blocked. Not a measurement.
- The blended configuration itself.

### Three observations that constrain what a fix could look like

1. **The blended configuration is negative before costs** (gross −0.0150 R). Fees and
   slippage roughly double the loss, but eliminating them entirely would not produce a
   profitable system. This is not an edge being eroded by friction.

2. **MAE ≈ MFE on every configuration** (0.89 vs 1.07 R blended). Trades travel about as
   far against as for — the signature of entries carrying no directional information.
   Every episode-deduplicated accuracy lens in Step 3 independently sits near a coin
   flip, which is the same finding from a different direction.

3. **Position sizing changes damage, not edge.** The Step 0/1 risk work takes the loss
   from −9.80% to −7.82% and the drawdown from 30.58% to 25.78% on *identical trades*
   with *identical* −0.0360 R expectancy. That is exactly what risk controls are for,
   and it is not evidence that anything improved.

### What this report does not establish

- **Nothing here is Bybit data.** Prices, funding and open interest are OKX. Funding is
  venue-specific, which bears directly on S2 and S4.
- **Fills are assumed.** Entries fill at the signal bar's close; the live bot posts a
  limit order that may never fill. This **flatters** the strategy.
- **The live account was never touched.** No API credentials were available, so the
  Step 2 outcome pipeline is verified against a fixture in Bybit's exact response
  shape, not against live closed-PnL. Its first real test is the next production run.
- **6 real trades exist in the live history.** The entire live record reconstructible
  from the legacy logs is 6 completed trades, net −$6.38. No statistic in this report
  rests on them.

### Recommended next step

Run the fixed pipeline and collect real outcome records. Every measurement above is
either backtest-derived (OKX proxy data) or built on 171 independent live signal
episodes. The Step 2 instrumentation now records what is needed — per-strategy
attribution, AI verdicts on trades whether gated or not, R-normalised results, MAE/MFE
— so that in some months there is a live sample worth testing instead of a 2.67×-to-
11×-inflated one worth nothing.

The risk controls from Steps 0 and 1 are appropriate to that posture: they are sized to
survive a long losing run, which is what a system with no measured edge should be
sized for.

---

## Addendum — Is it the entries or the exits?

The blended configuration targets **2.5:1** reward:risk (SL 3×ATR, TP 7.5×ATR)
and returns **−0.0360 R**. Two explanations fit that, and they call for opposite
responses:

- **The exits are giving back a real edge** — fix the exits, keep the signals.
- **The entries are directionless** — no exit rule will help.

`analysis/exit_efficiency.py` separates them from the trade dump the walk-forward
backtest already wrote. It is the difference between "tune this" and "rebuild this",
so it was worth answering before touching anything.

### 1. Where each exit lands against that trade's own best excursion

```
$ python3 analysis/exit_efficiency.py

exit reason        n  avg result R  avg MFE R  avg MAE R   MFE-result gap
----------------------------------------------------------------------------
SL_HIT           290        -1.045      0.421      1.312           +1.466
TRAIL_STOP       195        +0.899      1.643      0.395           +0.745
TP_HIT            42        +2.447      2.913      0.327           +0.465
TIMEOUT           17        +0.316      0.950      0.627           +0.634
----------------------------------------------------------------------------

winners: n=241 (44.3%)  avg realised +1.189R  avg MFE 1.841R
  winners that EVER reached the 2.5R take-profit level: 46/241
  winners that reached even 2.0R at their best point:   75/241

Ceiling — expectancy if EVERY trade exited at its own best point: +1.068R
Actual expectancy: -0.036R
```

**The 2.5:1 design is fiction.** Only **46 of 241 winners** ever touched 2.5R, and
only 42 trades in 544 exited at the target. The trailing stop harvests at +0.899 R
against a 1.643 R best point — a real 0.745 R gap, but far too small to explain the
result. Stops fire on trades whose best excursion averaged just **0.421 R**: those
positions barely moved in favour before reversing.

### 2. No take-profit level rescues it

Sweeping every fixed target against the recorded excursions, stop held at 1 R
(adverse-first ordering, the same conservative rule the backtest uses when a bar
contains both levels):

```
 TP level  expectancy R  win rate  profit factor
--------------------------------------------------
    1.00R       -0.0876     45.6%           0.84
    1.25R       +0.0044     45.0%           1.01
    1.50R       -0.0184     43.9%           0.97
    1.75R       -0.0170     43.9%           0.97
    2.00R       -0.0102     43.8%           0.98
    2.25R       -0.0140     43.8%           0.97
    2.50R       -0.0115     43.8%           0.98   <- current design
    3.00R       -0.0102     43.8%           0.98
--------------------------------------------------
best in sweep: TP 1.25R -> +0.0044R at PF 1.01
```

The **best** outcome across the entire sweep is **+0.0044 R at profit factor 1.01** —
indistinguishable from zero, and it is the peak of a curve fitted *after* seeing the
data. Treat it as an upper bound on what exit tuning could ever achieve here, not as
a setting to adopt.

### 3. Entry quality, stripped of every exit rule

```
median MFE 0.964R    median MAE 1.021R
trades where MFE > MAE: 255/544 (46.9%)   <- 50% is a coin flip
```

**This is the finding that settles it.** Across 544 trades, price is *slightly more
likely to move against the position first* than for it. The entries carry no
directional information, so there is no edge for any exit rule to preserve.

### What this rules out

Each of these is a common explanation for a losing system, and the data clears all
of them:

| Suspect | Verdict |
|---|---|
| Fees and slippage | **Not the cause.** Gross expectancy is −0.0150 R — negative before any cost is applied. |
| The exit rules | **Not the cause.** No take-profit level in the sweep reaches meaningful positive expectancy. |
| Position sizing | **Not the cause.** Identical trades at tiered vs flat risk give identical R. Sizing moved the loss from −9.80% to −7.82%; it controls damage, never edge. |
| The risk-infrastructure bugs fixed in this branch | **Real, and worth fixing** — they would have caused serious harm *if* the system made money. They do not create it. |

### The one large effect that is not noise

```
RANGING   +0.1345 R  (n=120)
TRENDING  −0.4555 R  (n=830)      RANGING − TRENDING = +0.59 R, Welch t = +5.18
```

The regime split moves expectancy by **0.59 R** and is the only large, statistically
significant effect anywhere in this study — an order of magnitude beyond anything the
AI stack contributes. The strategies bleed in ADX>25 trending markets and roughly
break even elsewhere.

Two caveats before acting on it: it is a filter identified *after* seeing the data,
and ADX>25 is a crude regime proxy. It is a hypothesis to test prospectively, not a
result.

### Cheapest changes that follow from the measurements

1. **S1 mean reversion** — the only statistically established result in the study
   (−0.0672 R, n=3953, t=−3.39), it is negative, and it fires more than every other
   lens combined.
2. **The 5-model AI Council** — five model calls per non-HOLD signal, per cycle, for
   a measured contribution to expectancy of zero on a sample too small to have
   detected one. That is a real recurring cost against no measured benefit.
3. **S3 consolidation breakout** — 3 trades in 2.7 years. Effectively dead code.

None of these were changed in this branch: the sessions that produced it were scoped
to measurement and risk infrastructure, and strategy changes were explicitly out of
scope. They are recorded here as what the numbers imply, for a separate decision.
