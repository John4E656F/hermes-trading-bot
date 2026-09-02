#!/usr/bin/env python3
"""
Episode deduplication, applied to every strategy lens.

THE PROBLEM
-----------
Hermes runs on a short cron. Each run re-evaluates the same open market
situation and writes a fresh row. When those rows are later resolved against
the same forward price, one market event -- one thing that either happened or
did not -- becomes N rows in the accuracy denominator.

That does not merely inflate the sample size. It inflates CONFIDENCE in
whatever the sample says, because N correlated observations are counted as N
independent ones. A 49% accuracy over "3,106 predictions" sounds like a
measured near-coin-flip; over the true number of independent episodes it is a
smaller and different number.

THE FIX
-------
An EPISODE is one continuous directional call on one asset: a maximal run of
(symbol, direction) rows in which consecutive rows are separated by LESS than
the outcome horizon. A 24h-horizon call re-issued an hour later is not an
independent observation of anything -- it resolves against overlapping price
action and is driven by the same market state. A new episode begins only when
the direction flips or the signal goes quiet for a full horizon.

Deduplication keeps one representative per episode: the FIRST observation,
which is the only one available in real time, before the outcome could have
influenced anything.

Note on a weaker definition: keying on (symbol, direction, exact resolution
timestamp) collapses nothing in signal_log.jsonl, because every row carries a
nanosecond timestamp and therefore a unique resolution instant. It appears to
show 1.00x inflation while 154 rows of the same standing ADAUSDT SELL call sit
in the sample. That is why the run-based definition is used below.

This is applied to Kronos and to every strategy lens, side by side, so the
inflation is visible per lens rather than only in aggregate.
"""

import collections
import datetime as dt
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import hermes_data as hd

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HORIZON_MS = 24 * 3600 * 1000  # matches the bot's fixed 24h Kronos resolution
MOVE_THRESHOLD_PCT = 0.0


def load_jsonl(name):
    path = os.path.join(REPO, name)
    if not os.path.exists(path):
        return []
    out = []
    for line in open(path):
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return out


def parse_ts(s):
    """RFC3339 -> epoch ms. Handles the bot's nanosecond precision."""
    if not s:
        return None
    s = s.replace("Z", "")
    if "." in s:
        head, frac = s.split(".", 1)
        frac = "".join(ch for ch in frac if ch.isdigit())[:6].ljust(6, "0")
        s = f"{head}.{frac}"
    else:
        s = s + ".000000"
    try:
        return int(dt.datetime.strptime(s, "%Y-%m-%dT%H:%M:%S.%f")
                   .replace(tzinfo=dt.timezone.utc).timestamp() * 1000)
    except ValueError:
        return None


def dedup_episodes(rows, key_fn, order_fn):
    """Keep the earliest row per episode key (exact-key form)."""
    groups = collections.defaultdict(list)
    for r in rows:
        k = key_fn(r)
        if k is None:
            continue
        groups[k].append(r)
    return [min(g, key=order_fn) for g in groups.values()]


def dedup_runs(rows, group_fn, time_fn, gap_ms=HORIZON_MS):
    """
    Collapse each maximal run of same-group rows into its FIRST row.

    A new episode starts only when the group changes or when more than one
    outcome horizon has passed since the previous observation.
    """
    groups = collections.defaultdict(list)
    for r in rows:
        g = group_fn(r)
        t = time_fn(r)
        if g is None or t is None:
            continue
        groups[g].append((t, r))

    kept = []
    for g, items in groups.items():
        items.sort(key=lambda x: x[0])
        last = None
        for t, r in items:
            if last is None or t - last >= gap_ms:
                kept.append(r)
            last = t
    return kept


def accuracy(rows, result_fn):
    correct = wrong = nocall = 0
    for r in rows:
        v = result_fn(r)
        if v is None:
            nocall += 1
        elif v:
            correct += 1
        else:
            wrong += 1
    graded = correct + wrong
    return {"n_rows": len(rows), "graded": graded, "correct": correct,
            "wrong": wrong, "no_call": nocall,
            "accuracy": (correct / graded * 100) if graded else None}


def kronos_lenses():
    rows = load_jsonl("kronos_outcomes.jsonl")
    if not rows:
        return {}

    def k_result(r):
        v = r.get("kronos_result")
        return None if v in (None, "no_call") else (v == "correct")

    def m_result(r):
        v = r.get("master_result")
        return None if v in (None, "no_call") else (v == "correct")

    for r in rows:
        r["_ts"] = parse_ts(r.get("timestamp"))

    out = {}
    for label, result_fn, dirn in (
        ("Kronos (AI overlay)", k_result, lambda r: r.get("kronos_direction")),
        ("Master blended action", m_result, lambda r: r.get("master_action")),
    ):
        raw = accuracy(rows, result_fn)
        kept = dedup_runs(rows,
                          group_fn=lambda r, d=dirn: (r.get("symbol"), d(r)),
                          time_fn=lambda r: r.get("_ts"))
        out[label] = {"raw": raw, "dedup": accuracy(kept, result_fn)}
    return out


def resolve_signals(rows, verbose=True):
    by_symbol = collections.defaultdict(list)
    for r in rows:
        ts = parse_ts(r.get("timestamp"))
        if ts is None:
            continue
        r["_ts"] = ts
        by_symbol[r["symbol"]].append(r)

    resolved, unlisted, unresolved = [], set(), 0
    for i, (symbol, sigs) in enumerate(sorted(by_symbol.items())):
        candles = hd.fetch_candles(symbol, "1H", max_bars=3000)
        if not candles:
            unlisted.add(symbol)
            continue
        if verbose:
            print(f"    [{i+1}/{len(by_symbol)}] {symbol:<12} {len(candles):>5} 1H bars, "
                  f"{len(sigs):>4} signals", flush=True)
        first_ts, last_ts = candles[0]["ts"], candles[-1]["ts"]
        for r in sigs:
            entry_ts, exit_ts = r["_ts"], r["_ts"] + HORIZON_MS
            if entry_ts < first_ts or exit_ts > last_ts:
                unresolved += 1
                continue
            p0 = hd.price_at(candles, entry_ts)
            p1 = hd.price_at(candles, exit_ts)
            if not p0 or not p1:
                unresolved += 1
                continue
            r["_entry"], r["_exit"] = p0, p1
            r["_change_pct"] = (p1 - p0) / p0 * 100.0
            r["_resolved_at"] = exit_ts
            resolved.append(r)
    return resolved, unlisted, unresolved


def strategy_key(strategy):
    for prefix in ("META: ", "CONFIRMED: ", "QUALITY: "):
        if strategy.startswith(prefix):
            strategy = strategy[len(prefix):]
    return strategy.replace(" + S4", "")


def signal_lenses(resolved):
    def correct(r):
        a = r.get("action")
        ch = r["_change_pct"]
        if a == "BUY":
            return ch > MOVE_THRESHOLD_PCT
        if a == "SELL":
            return ch < -MOVE_THRESHOLD_PCT
        return None

    lenses = collections.defaultdict(list)
    for r in resolved:
        lenses[strategy_key(r.get("strategy", "?"))].append(r)
        for tag in r.get("active_strategies", []):
            act = r.get(f"{tag.lower()}_action")
            if act:
                sub = dict(r)
                sub["action"] = act
                lenses[f"{tag} (sub-signal)"].append(sub)

    def collapse(rs):
        return dedup_runs(rs,
                          group_fn=lambda r: (r.get("symbol"), r.get("action")),
                          time_fn=lambda r: r["_ts"])

    out = {}
    for label, rows in lenses.items():
        out[label] = {"raw": accuracy(rows, correct),
                      "dedup": accuracy(collapse(rows), correct)}
    out["ALL SIGNALS (blended)"] = {"raw": accuracy(resolved, correct),
                                    "dedup": accuracy(collapse(resolved), correct)}
    return out


def print_table(title, results):
    print(f"\n{title}")
    print("=" * 104)
    print(f"{'Lens':<34} {'raw n':>7} {'raw acc':>9} {'epi n':>7} {'epi acc':>9} "
          f"{'inflation':>10} {'delta':>9}")
    print("-" * 104)
    for label in sorted(results, key=lambda k: -results[k]["dedup"]["graded"]):
        raw, ded = results[label]["raw"], results[label]["dedup"]
        ra = f"{raw['accuracy']:.1f}%" if raw["accuracy"] is not None else "n/a"
        da = f"{ded['accuracy']:.1f}%" if ded["accuracy"] is not None else "n/a"
        infl = f"{raw['graded']/ded['graded']:.2f}x" if ded["graded"] else "n/a"
        delta = (f"{ded['accuracy'] - raw['accuracy']:+.1f}pp"
                 if raw["accuracy"] is not None and ded["accuracy"] is not None else "n/a")
        print(f"{label:<34} {raw['graded']:>7} {ra:>9} {ded['graded']:>7} {da:>9} "
              f"{infl:>10} {delta:>9}")
    print("-" * 104)


def main():
    print("EPISODE DEDUPLICATION -- every strategy lens")
    print(f"Horizon: {HORIZON_MS // 3600000}h   "
          f"Episode = maximal run of (symbol, direction) with gaps < horizon   "
          f"Representative: first observation")

    k = kronos_lenses()
    if k:
        print_table("A. kronos_outcomes.jsonl", k)

    sig_rows = load_jsonl("signal_log.jsonl")
    print(f"\nB. signal_log.jsonl -- {len(sig_rows)} raw rows, resolving against OKX 1H OHLCV")
    resolved, unlisted, unresolved = resolve_signals(sig_rows)
    print(f"\n    resolved: {len(resolved)}   "
          f"dropped (window not closed / no data): {unresolved}   "
          f"not listed on OKX: {len(unlisted)}")
    if unlisted:
        print(f"    excluded symbols: {', '.join(sorted(unlisted))}")

    sig_res = signal_lenses(resolved) if resolved else {}
    if sig_res:
        print_table("B. signal_log.jsonl strategy lenses", sig_res)

    out = {"kronos": k, "signals": sig_res,
           "meta": {"unresolved": unresolved, "unlisted": sorted(unlisted),
                    "resolved": len(resolved), "raw_signal_rows": len(sig_rows)}}
    dest = os.path.join(REPO, "analysis", "episode_dedup_results.json")
    json.dump(out, open(dest, "w"), indent=2, default=str)
    print(f"\nWrote {dest}")


if __name__ == "__main__":
    main()
