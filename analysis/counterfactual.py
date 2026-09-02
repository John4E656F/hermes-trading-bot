#!/usr/bin/env python3
"""
Counterfactual measurement of every AI layer.

The question each layer must answer is not "is it accurate?" but "does acting
on it beat ignoring it?" A filter that is right 60% of the time still destroys
expectancy if the 40% it blocks were the large winners.

METHOD
  Every recorded signal is replayed through the SAME bracket simulator the
  backtest uses (analysis/backtest.py) -- ATR stop and target, the trailing
  stop, taker fees, funding and slippage -- regardless of whether the layer
  let it through. The trade the layer BLOCKED gets a result in R just like the
  trade it allowed. Expectancy is then compared with the layer acting as a
  filter versus not acting at all, over the episode-deduplicated set.

  A layer helps only if (expectancy of what it ALLOWED) > (expectancy of
  everything). If the set it REJECTED has higher expectancy than the set it
  allowed, the layer is costing money.

SCOPE
  Layers present in this repository: the 5-model AI Council, the Kronos
  overlay, the reflection memory and the sentiment pre-fetch. There is no
  TradingAgents-derived code in this repository -- nothing from
  github.com/TauricResearch/TradingAgents has been added -- so there are no
  TradingAgents agent verdicts to measure.
"""

import collections
import datetime as dt
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import backtest as bt
import hermes_data as hd
import strategy as sg
from episode_dedup import load_jsonl, parse_ts, dedup_episodes

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def bar_index_at(c4h, ts):
    """Index of the last 4H bar CLOSED at or before ts. None if out of range."""
    lo, hi, found = 0, len(c4h) - 1, None
    while lo <= hi:
        mid = (lo + hi) // 2
        if c4h[mid]["ts"] <= ts:
            found = mid
            lo = mid + 1
        else:
            hi = mid - 1
    if found is None or found < bt.WARMUP_4H or found >= len(c4h) - 2:
        return None
    return found


class Replayer:
    """Caches per-symbol data and bar states so repeated lookups are cheap."""

    def __init__(self):
        self.data = {}
        self.states = {}

    def get(self, symbol):
        if symbol not in self.data:
            d = bt.load_symbol(symbol, max_bars=3000)
            self.data[symbol] = d
            self.states[symbol] = bt.build_states(d) if d else None
        return self.data[symbol], self.states[symbol]

    def replay(self, symbol, ts, action, label="counterfactual"):
        """Result in R had this trade been taken, ignoring every gate."""
        data, states = self.get(symbol)
        if not data:
            return None
        i = bar_index_at(data["c4h"], ts)
        if i is None or i not in states:
            return None
        return bt.simulate_trade(data, states, i, action, label, 0, 0.0)


def summarize(trades, label):
    if not trades:
        return {"label": label, "n": 0, "expectancy_r": None,
                "win_rate": None, "profit_factor": None, "total_r": 0.0}
    rs = [t["result_r"] for t in trades]
    wins = [r for r in rs if r > 0]
    losses = [r for r in rs if r <= 0]
    gl = abs(sum(losses))
    return {
        "label": label, "n": len(rs),
        "expectancy_r": sum(rs) / len(rs),
        "win_rate": len(wins) / len(rs) * 100.0,
        "profit_factor": (sum(wins) / gl) if gl > 0 else (float("inf") if wins else 0.0),
        "total_r": sum(rs),
        "avg_winner_r": (sum(wins) / len(wins)) if wins else 0.0,
        "avg_loser_r": (sum(losses) / len(losses)) if losses else 0.0,
    }


def print_layer(title, groups, note=""):
    print(f"\n{title}")
    if note:
        print(f"  {note}")
    print("=" * 92)
    print(f"{'Set':<42} {'n':>6} {'exp R':>9} {'win%':>7} {'PF':>7} {'total R':>10}")
    print("-" * 92)
    for g in groups:
        if g["n"] == 0:
            print(f"{g['label']:<42} {0:>6} {'n/a':>9} {'n/a':>7} {'n/a':>7} {'n/a':>10}")
            continue
        pf = "inf" if g["profit_factor"] == float("inf") else f"{g['profit_factor']:.2f}"
        print(f"{g['label']:<42} {g['n']:>6} {g['expectancy_r']:>+9.4f} "
              f"{g['win_rate']:>6.1f}% {pf:>7} {g['total_r']:>+10.2f}")
    print("-" * 92)


# ─── Layer 1: Kronos ─────────────────────────────────────────────────────

def kronos_layer(rep):
    """
    kronos_outcomes.jsonl records the master action AND the Kronos call on the
    same signal, so the counterfactual is direct: replay the master action
    everywhere, then partition by what Kronos said.
    """
    rows = load_jsonl("kronos_outcomes.jsonl")
    episodes = dedup_episodes(
        rows,
        key_fn=lambda r: (r.get("symbol"), r.get("master_action"), r.get("resolved_at")),
        order_fn=lambda r: r.get("timestamp") or "")
    print(f"  kronos_outcomes.jsonl: {len(rows)} rows -> {len(episodes)} episodes")

    replayed, skipped = [], 0
    for n, r in enumerate(episodes):
        action = r.get("master_action")
        if action not in (sg.BUY, sg.SELL):
            continue
        ts = parse_ts(r.get("timestamp"))
        if ts is None:
            continue
        t = rep.replay(r["symbol"], ts, action)
        if not t:
            skipped += 1
            continue
        t["_kronos_dir"] = r.get("kronos_direction")
        t["_agreement"] = r.get("agreement")
        replayed.append(t)
        if (n + 1) % 200 == 0:
            print(f"    replayed {n+1}/{len(episodes)} episodes "
                  f"({len(replayed)} tradeable)", flush=True)

    print(f"  replayed {len(replayed)} tradeable episodes "
          f"({skipped} not tradeable: outside data range or blocked by the fee gate)")

    kdir = lambda t: (t["_kronos_dir"] or "hold").lower()
    agrees = [t for t in replayed if kdir(t) == t["side"].lower()]
    disagrees = [t for t in replayed if kdir(t) in ("buy", "sell") and kdir(t) != t["side"].lower()]
    nocall = [t for t in replayed if kdir(t) == "hold"]
    called = agrees + disagrees

    return [
        summarize(replayed, "WITHOUT Kronos (trade every signal)"),
        summarize(agrees, "WITH Kronos filter (only when it agrees)"),
        summarize(disagrees, "  -> what the filter BLOCKS (disagrees)"),
        summarize(nocall, "  -> Kronos had no call (hold)"),
        summarize(called, "  -> Kronos made any directional call"),
    ], replayed


# ─── Layer 2: AI Council ─────────────────────────────────────────────────

def council_layer(rep):
    """
    signal_log.jsonl is the only record of Council verdicts on historical
    signals, and only as free text in skip_reason. The sample is tiny; it is
    reported with its n so nobody mistakes it for a measurement.
    """
    rows = load_jsonl("signal_log.jsonl")
    rejected_rows, passed_rows = [], []
    for r in rows:
        reason = r.get("skip_reason") or ""
        verdict = r.get("council_verdict")
        if verdict == "REJECTED" or "AI GATE" in reason or "AI Council rejected" in reason:
            rejected_rows.append(r)
        elif r.get("executed") or verdict in ("PASS", "CONFIRMED"):
            passed_rows.append(r)

    print(f"  signal_log.jsonl: {len(rejected_rows)} Council rejections, "
          f"{len(passed_rows)} Council passes recorded")

    def replay_set(rs, tag):
        out = []
        for r in rs:
            ts = parse_ts(r.get("timestamp"))
            act = r.get("action")
            if ts is None or act not in (sg.BUY, sg.SELL):
                continue
            t = rep.replay(r["symbol"], ts, act, tag)
            if t:
                out.append(t)
        return dedup_episodes(out, key_fn=lambda t: (t["symbol"], t["side"], t["entry_ts"]),
                              order_fn=lambda t: t["entry_ts"])

    rejected = replay_set(rejected_rows, "council_rejected")
    passed = replay_set(passed_rows, "council_passed")

    return [
        summarize(passed + rejected, "WITHOUT Council (trade both sets)"),
        summarize(passed, "WITH Council filter (it allowed)"),
        summarize(rejected, "  -> what the Council BLOCKED"),
    ]


# ─── Layer 3: conviction stacking (a non-AI baseline for comparison) ─────

def conviction_layer(replayed_kronos):
    """
    Not an AI layer, but the natural control: if the AI layers move expectancy
    less than simply requiring more indicator agreement does, that is worth
    seeing side by side.
    """
    by_regime = collections.defaultdict(list)
    for t in replayed_kronos:
        by_regime[t["regime"]].append(t)
    return [summarize(v, f"regime = {k}") for k, v in sorted(by_regime.items())]


def main():
    print("COUNTERFACTUAL MEASUREMENT OF EVERY AI LAYER")
    print("Every signal is replayed through the same bracket simulator, gated or not.")
    print("Costs modelled: taker fees both sides, funding per 8h settlement, "
          f"{bt.SLIPPAGE_BPS:.0f}bps slippage per side.\n")

    rep = Replayer()

    print("LAYER 1 — Kronos AI overlay")
    kronos_groups, replayed = kronos_layer(rep)
    print_layer("Kronos: expectancy WITH vs WITHOUT the layer", kronos_groups)

    print("\nLAYER 2 — 5-model AI Council")
    council_groups = council_layer(rep)
    print_layer("AI Council: expectancy WITH vs WITHOUT the layer", council_groups,
                note="Historical Council verdicts survive only as free text in "
                     "signal_log.skip_reason; the sample is very small.")

    print("\nLAYER 3 — TradingAgents-derived agents")
    print("  No TradingAgents-derived code exists in this repository. "
          "Nothing to measure.")

    print("\nCONTROL — same replayed set, split by market regime")
    print_layer("Regime split (not an AI layer; shown for scale)",
                conviction_layer(replayed))

    out = {"kronos": kronos_groups, "council": council_groups,
           "regime": conviction_layer(replayed)}
    dest = os.path.join(REPO, "analysis", "counterfactual_results.json")
    json.dump(out, open(dest, "w"), indent=2, default=str)
    print(f"\nWrote {dest}")


if __name__ == "__main__":
    main()
