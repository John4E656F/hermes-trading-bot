#!/usr/bin/env python3
"""
Reconstruct outcome_log.jsonl from the legacy logs.

Rule: a row is migrated ONLY if it unambiguously represents a position that
was opened and is now closed. Everything else is written to a separate
quarantine file with the reason it was excluded.

The point of this script is NOT to maximise the number of migrated trades.
trade_history.json is a mixed bag -- real fills, orders that never filled,
signals that were skipped, and hand-written analysis notes all share one
array and one "trades" key. Counting that array as a trade list is how a
16-row file becomes a claimed "16 trades". Guessing at ambiguous rows would
carry that error forward into every statistic computed downstream, so
ambiguous rows are excluded and named instead.
"""

import json
import sys
from datetime import datetime, timezone

SCHEMA_VERSION = 2
OUT_PATH = "outcome_log.migrated.jsonl"
QUARANTINE_PATH = "outcome_log.quarantine.jsonl"

# Outcomes that mean "this position closed".
CLOSED_OUTCOMES = {"WIN", "LOSS"}
# Outcomes that mean "this never became a completed trade".
NOT_A_TRADE = {
    "PENDING": "order never resolved -- no exit, cannot compute PnL",
    "SKIPPED": "signal was never entered -- no position existed",
    "REVIEW": "hand-written analysis note, not a trade",
    "ANALYSIS": "hand-written analysis note, not a trade",
}


def iso(s):
    if not s:
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00")).astimezone(timezone.utc).isoformat()
    except (ValueError, AttributeError):
        return None


def blank_record():
    """Every schema field, defaulting to null. A field that cannot be
    reconstructed stays null -- it is never filled with 0."""
    return {
        "schema_version": SCHEMA_VERSION,
        "trade_id": None, "symbol": None, "side": None,
        "strategy_primary": None, "strategies_confirming": [],
        "entry_timestamp": None, "entry_price": None,
        "exit_timestamp": None, "exit_price": None,
        "quantity": None, "notional": None,
        "gross_pnl": None, "fees": None, "funding_cost": None,
        "slippage": None, "net_pnl": None,
        "initial_risk_usd": None, "result_r": None,
        "max_adverse_excursion": None, "max_favorable_excursion": None,
        "regime": None,
        "kronos_direction": None, "kronos_confidence": None,
        "council_verdict": None, "council_votes": [],
        "would_trade_without_ai": None,
        "exit_reason": None, "equity_before": None, "equity_after": None,
        "source": "migration", "incomplete": [],
    }


def migrate_history(path, migrated, quarantined):
    try:
        rows = json.load(open(path))["trades"]
    except (FileNotFoundError, KeyError, json.JSONDecodeError) as e:
        print(f"  {path}: unreadable ({e}) -- skipped")
        return

    for row in rows:
        outcome = (row.get("outcome") or "").upper()
        side = (row.get("side") or "").upper()
        tid = row.get("trade_id") or "?"

        if side == "ANALYSIS" or row.get("symbol") == "SYSTEM":
            quarantined.append({"trade_id": tid, "source_file": path,
                                "reason": "hand-written analysis note, not a trade",
                                "raw_outcome": outcome, "raw_side": side})
            continue

        if outcome in NOT_A_TRADE:
            quarantined.append({"trade_id": tid, "source_file": path,
                                "reason": NOT_A_TRADE[outcome],
                                "raw_outcome": outcome, "raw_side": side})
            continue

        if outcome not in CLOSED_OUTCOMES:
            quarantined.append({"trade_id": tid, "source_file": path,
                                "reason": f"unrecognised outcome {outcome!r} -- cannot classify",
                                "raw_outcome": outcome, "raw_side": side})
            continue

        pnl = row.get("pnl")
        if pnl is None:
            quarantined.append({"trade_id": tid, "source_file": path,
                                "reason": "closed but no pnl recorded -- nothing to measure",
                                "raw_outcome": outcome, "raw_side": side})
            continue

        r = blank_record()
        r["trade_id"] = f"migrated_{tid}"
        r["symbol"] = row.get("symbol")
        r["side"] = {"LONG": "BUY", "SHORT": "SELL"}.get(side, side)
        r["entry_timestamp"] = iso(row.get("date"))
        r["exit_timestamp"] = iso(row.get("closed_date"))
        r["entry_price"] = row.get("entry_price") or None
        r["net_pnl"] = pnl
        r["regime"] = row.get("regime_at_entry") or None
        r["equity_before"] = row.get("wallet_at_time") or None
        if r["equity_before"] is not None:
            r["equity_after"] = round(r["equity_before"] + pnl, 8)
        r["notional"] = row.get("position_size_usd") or None
        r["exit_reason"] = "MIGRATED_" + outcome

        # Everything the legacy format simply does not contain.
        missing = []
        if not r["exit_timestamp"]:
            missing.append("no_exit_timestamp")
        if row.get("stop_loss") and r["entry_price"]:
            # Risk per unit is recoverable, but position size in TOKENS is not,
            # so initial_risk_usd cannot be derived without guessing.
            missing.append("no_quantity_cannot_derive_initial_risk")
        else:
            missing.append("no_risk_basis")
        missing += [
            "no_exit_price",         # never recorded
            "gross_pnl_unknown",     # pnl is net of unknown costs
            "fees_unknown",
            "funding_unknown",
            "slippage_unknown",
            "excursions_unknown",
            "no_strategy_attribution",  # legacy rows carry prose, not a strategy id
            "no_ai_verdicts",
        ]
        r["incomplete"] = missing
        r["strategy_primary"] = "UNATTRIBUTED"
        migrated.append(r)


def migrate_trade_log(path, migrated, quarantined):
    """trade_log.jsonl is a DECISION log, not a trade list: it contains
    rejections, skips and API errors alongside fills. Only executed rows are
    even candidates, and none of them carry an exit -- so all of them are
    quarantined as open-or-unknown rather than migrated."""
    try:
        lines = [l for l in open(path).read().splitlines() if l.strip()]
    except FileNotFoundError:
        print(f"  {path}: not found -- skipped")
        return

    for i, line in enumerate(lines):
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            quarantined.append({"trade_id": f"{path}:{i}", "source_file": path,
                                "reason": "malformed JSON line"})
            continue

        tid = f"{row.get('symbol')}_{row.get('timestamp')}"
        if not row.get("executed"):
            quarantined.append({"trade_id": tid, "source_file": path,
                                "reason": f"not executed: {row.get('skip_reason') or 'no reason recorded'}"})
            continue
        quarantined.append({"trade_id": tid, "source_file": path,
                            "reason": "order placed but no exit recorded -- position open or outcome never logged"})


def main():
    migrated, quarantined = [], []
    print("Reconstructing completed trades from legacy logs\n")

    print("trade_history.json:")
    migrate_history("trade_history.json", migrated, quarantined)
    print("trade_journal.json:")
    migrate_history("trade_journal.json", migrated, quarantined)
    print("trade_log.jsonl:")
    migrate_trade_log("trade_log.jsonl", migrated, quarantined)

    # trade_history.json and trade_journal.json overlap heavily.
    seen, deduped = set(), []
    for r in migrated:
        key = (r["symbol"], r["entry_timestamp"], r["net_pnl"])
        if key in seen:
            continue
        seen.add(key)
        deduped.append(r)
    dupes = len(migrated) - len(deduped)

    with open(OUT_PATH, "w") as f:
        for r in deduped:
            f.write(json.dumps(r) + "\n")
    with open(QUARANTINE_PATH, "w") as f:
        for q in quarantined:
            f.write(json.dumps(q) + "\n")

    print(f"\n  migrated   : {len(deduped):>3} completed trades -> {OUT_PATH}")
    print(f"  deduped    : {dupes:>3} rows appearing in both history and journal")
    print(f"  quarantined: {len(quarantined):>3} rows -> {QUARANTINE_PATH}")

    reasons = {}
    for q in quarantined:
        reasons[q["reason"]] = reasons.get(q["reason"], 0) + 1
    print("\n  Exclusion reasons:")
    for reason, n in sorted(reasons.items(), key=lambda kv: -kv[1]):
        print(f"    {n:>3}  {reason}")

    if deduped:
        print("\n  Migrated trades (net PnL is all that survives; no R, no fees, no exits):")
        for r in deduped:
            ts = (r["entry_timestamp"] or "?")[:10]
            print(f"    {ts}  {r['symbol']:<10} {r['side']:<5} net=${r['net_pnl']:>7.2f}  "
                  f"{len(r['incomplete'])} unusable fields")
        total = sum(r["net_pnl"] for r in deduped)
        wins = sum(1 for r in deduped if r["net_pnl"] > 0)
        print(f"\n    n={len(deduped)}  net=${total:.2f}  wins={wins}  losses={len(deduped)-wins}")
        print("    This sample is too small for any statistic to be meaningful.")


if __name__ == "__main__":
    sys.exit(main())
