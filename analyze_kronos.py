#!/usr/bin/env python3
"""
Analyze kronos_outcomes.jsonl: each entry is a Kronos AI prediction that has
been resolved 24h after it was logged, comparing Kronos's directional call
against the bot's own master signal and the actual 24h price move.

The Go bot writes this file automatically (ResolveKronosOutcomes, run once
per cycle) — it joins kronos_log.jsonl entries that are >=24h old against the
live price at resolution time. Nothing to fetch here; just summarize.

Usage:
    python3 analyze_kronos.py
"""
import json

LOG_PATH = "kronos_outcomes.jsonl"


def main():
    try:
        with open(LOG_PATH) as f:
            lines = [json.loads(l) for l in f if l.strip()]
    except FileNotFoundError:
        print(f"{LOG_PATH} not found — no Kronos predictions have reached the "
              f"24h resolution horizon yet.")
        return

    if not lines:
        print("No resolved entries yet.")
        return

    # agreement -> {"kronos_correct":n, "kronos_incorrect":n, "master_correct":n, "master_incorrect":n, ...}
    stats = {}

    print(f"{'Symbol':<12} {'Agreement':<10} {'Master':<5} {'Kronos':<6} "
          f"{'EntryPx':>12} {'ExitPx':>12} {'Chg%':>7} {'MasterRes':<10} {'KronosRes':<10}")
    print("-" * 95)

    for entry in lines:
        agreement = entry.get("agreement", "neutral")
        bucket = stats.setdefault(agreement, {
            "kronos_correct": 0, "kronos_incorrect": 0, "kronos_no_call": 0,
            "master_correct": 0, "master_incorrect": 0, "master_no_call": 0,
        })

        master_res = entry.get("master_result", "no_call")
        kronos_res = entry.get("kronos_result", "no_call")
        bucket[f"master_{master_res}"] = bucket.get(f"master_{master_res}", 0) + 1
        bucket[f"kronos_{kronos_res}"] = bucket.get(f"kronos_{kronos_res}", 0) + 1

        print(f"{entry['symbol']:<12} {agreement:<10} {entry.get('master_action', '?'):<5} "
              f"{entry.get('kronos_direction', '?'):<6} "
              f"{entry.get('entry_price', 0):>12.4f} {entry.get('exit_price', 0):>12.4f} "
              f"{entry.get('change_pct', 0):>6.2f}% {master_res:<10} {kronos_res:<10}")

    print()
    print("=== Summary (24h-resolved predictions) ===")
    for agreement, bucket in stats.items():
        master_total = bucket.get("master_correct", 0) + bucket.get("master_incorrect", 0)
        kronos_total = bucket.get("kronos_correct", 0) + bucket.get("kronos_incorrect", 0)
        print(f"\n{agreement.upper()}:")
        if master_total:
            print(f"  Master signal correct: {bucket.get('master_correct', 0)}/{master_total} "
                  f"({bucket.get('master_correct', 0)/master_total*100:.0f}%)")
        if kronos_total:
            print(f"  Kronos correct:        {bucket.get('kronos_correct', 0)}/{kronos_total} "
                  f"({bucket.get('kronos_correct', 0)/kronos_total*100:.0f}%)")

    # Specific callout: when they disagreed, who was right more often?
    if "disagree" in stats:
        d = stats["disagree"]
        m_total = d.get("master_correct", 0) + d.get("master_incorrect", 0)
        k_total = d.get("kronos_correct", 0) + d.get("kronos_incorrect", 0)
        if m_total and k_total:
            print("\n=== When Kronos disagreed with the master signal ===")
            print(f"  Master was right {d.get('master_correct',0)}/{m_total} times "
                  f"({d.get('master_correct',0)/m_total*100:.0f}%)")
            print(f"  Kronos was right {d.get('kronos_correct',0)}/{k_total} times "
                  f"({d.get('kronos_correct',0)/k_total*100:.0f}%)")
            if d.get('kronos_correct',0)/k_total > d.get('master_correct',0)/m_total:
                print("  -> Consider trusting Kronos more on disagreement (increase confidence trim).")
            else:
                print("  -> Current confidence trim on disagreement looks justified.")


if __name__ == "__main__":
    main()
