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
