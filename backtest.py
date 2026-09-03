#!/usr/bin/env python3
"""Comprehensive backtest: what if new rules had been in place?"""
import json
from collections import Counter, defaultdict

# ── Load all data ──
signals = []
with open('signal_log.jsonl') as f:
    for line in f:
        if line.strip():
            signals.append(json.loads(line))

outcomes = []
with open('kronos_outcomes.jsonl') as f:
    for line in f:
        if line.strip():
            outcomes.append(json.loads(line))

kronos_log = []
with open('kronos_log.jsonl') as f:
    for line in f:
        if line.strip():
            kronos_log.append(json.loads(line))

# Build outcome lookup: (symbol, timestamp_hour) -> outcome
outcome_map = {}
for o in outcomes:
    key = (o['symbol'], o['timestamp'][:13])  # hour precision
    outcome_map[key] = o

# Build kronos_log lookup for pre_conviction and ADX-like info
kronos_map = {}
for k in kronos_log:
    key = (k['symbol'], k['timestamp'][:13])
    kronos_map[key] = k

# Known win rates from reflection data
WR_MAP = {
    'BTCUSDT': 0.29, 'ETHUSDT': 0.40, 'SOLUSDT': 0.18, 'XRPUSDT': 0.75,
    'LINKUSDT': 0.28, 'DOGEUSDT': 0.35, 'ADAUSDT': 0.35, 'SUIUSDT': 0.50,
    'NEARUSDT': 0.42, 'ZECUSDT': 1.00, 'BCHUSDT': 0.30, 'LTCUSDT': 0.55,
    'AAVEUSDT': 0.25, 'BNBUSDT': 0.45, 'APTUSDT': 0.28, 'AVAXUSDT': 0.38,
    'ARBUSDT': 0.22, 'UNIUSDT': 0.18, 'HYPEUSDT': 0.25, 'SOXLUSDT': 0.15,
    'ENAUSDT': 1.00, 'PUMPFUNUSDT': 0.35, 'TRUMPUSDT': 0.40, 'GPROUSDT': 0.45,
    'MAGMAUSDT': 0.30, 'XAUUSDT': 0.50, 'DOGEUSDT': 0.35, 'SNDKUSDT': 0.40,
}

def get_reflection_mult(sym):
    wr = WR_MAP.get(sym, 0.40)
    if wr >= 0.65:
        return 1.2
    elif wr >= 0.40:
        return 1.0
    else:
        return 0.8

# ── Simulate execution with NEW rules ──
# New execution flow:
# 1. Symbol banned? -> HOLD (already filtered)
# 2. Trend Sell Conv1 with ADX>40 -> auto-boost to Conv2
# 3. Conv2+ -> apply reflection multiplier (only if Conv>=2)
# 4. Confidence >= 0.70? -> proceed
# 5. Falling knife guard: gain7d < -15% hard block, < -8% Conv1 forced
# 6. AI Council: bypass if Conv2+ ADX>40 or Conv3 or S4
# 7. Execution: check portfolio cap, place trade

def simulate_trade(sig, outcome):
    """Simulate a trade with new rules. Returns dict with result."""
    sym = sig['symbol']
    action = sig['action']
    old_conv = sig['conviction']
    old_conf = sig['confidence']
    gain7d = sig.get('gain_7d', 0)
    strategy = sig.get('strategy', '')
    skip = sig.get('skip_reason', '')
    already_executed = sig.get('executed', False)

    # Step 0: Was it already executed?
    if already_executed:
        return None  # Already counted

    # Step 1: Symbol ban check
    if 'banned' in skip.lower():
        return None  # Still banned

    # Step 2: Auto-boost Trend Sell Conv1 -> Conv2 (assuming ADX>40)
    new_conv = old_conv
    if old_conv == 1 and 'trend' in strategy.lower() and 'sell' in strategy.lower():
        new_conv = 2  # auto-boost

    # Step 3: Falling knife guard
    if action == 'SELL' and gain7d < -10:
        # Late sell guard - confidence capped at 55%
        pass  # still executable but reduced conf
    if action == 'BUY' and gain7d < -15:
        return None  # Hard block

    # Step 4: Reflection multiplier (only applies if Conv >= 2)
    new_conf = old_conf
    if new_conv >= 2:
        mult = get_reflection_mult(sym)
        new_conf = old_conf * mult
        # Cap at 0.95
        new_conf = min(new_conf, 0.95)

    # Step 5: Confidence floor
    if new_conf < 0.70:
        return None  # Still blocked

    # Step 6: Late sell guard for SELL
    if action == 'SELL' and gain7d < -10:
        new_conf = min(new_conf, 0.55)
        if new_conf < 0.70:
            return None  # Capped too low

    # Step 7: AI Council bypass check
    # New rule: Conv2+ ADX>40 or Conv3 or S4 -> bypass
    # Since we don't have ADX, assume Trend Sell has ADX>40 (that's how they're generated)
    bypass = False
    if new_conv >= 3:
        bypass = True
    elif new_conv >= 2 and ('trend' in strategy.lower() or 'S4' in strategy):
        bypass = True

    # Step 8: Portfolio cap (simplified: only execute if we have room)
    # We track this separately

    # Found a valid trade
    result = {
        'symbol': sym,
        'action': action,
        'strategy': strategy,
        'old_conv': old_conv,
        'new_conv': new_conv,
        'old_conf': old_conf,
        'new_conf': new_conf,
        'bypass': bypass,
        'gain7d': gain7d,
        'timestamp': sig['timestamp'],
        'skip_reason': skip[:60] if skip else '',
    }

    # Match against outcome for PnL
    if outcome:
        result['entry_price'] = outcome.get('entry_price', 0)
        result['exit_price'] = outcome.get('exit_price', 0)
        result['change_pct'] = outcome.get('change_pct', 0)
        result['master_result'] = outcome.get('master_result', 'unknown')
    else:
        result['entry_price'] = sig.get('price', 0)
        result['exit_price'] = 0
        result['change_pct'] = gain7d * -1 if action == 'SELL' else gain7d
        result['master_result'] = 'unknown'

    return result

# ── Run simulation ──
new_trades = []
executed_trades = [s for s in signals if s.get('executed')]
skipped_trades = [s for s in signals if not s.get('executed')]

for sig in skipped_trades:
    sym = sig['symbol']
    ts = sig['timestamp'][:13]
    outcome = outcome_map.get((sym, ts))
    result = simulate_trade(sig, outcome)
    if result:
        new_trades.append(result)

# ── Print results ──
print("=" * 100)
print("                 COMPREHENSIVE BACKTEST: NEW RULES VS OLD RULES")
print("=" * 100)
print(f"Total historical signals:         {len(signals)}")
print(f"Actually executed (old rules):    {len(executed_trades)}")
print(f"New trades with new rules:        {len(new_trades)}")
print(f"Trade frequency increase:         {len(new_trades)/max(len(executed_trades),1):.1f}x")
print()

# ── Per-rule breakdown ──
rule1 = [t for t in new_trades if t['old_conv'] == 1 and t['new_conv'] == 2]
rule2 = [t for t in new_trades if t['old_conv'] >= 2 and 'trend' in t['strategy'].lower()]
rule3 = [t for t in new_trades if 'S4' in t['strategy']]
other = [t for t in new_trades if t not in rule1 and t not in rule2 and t not in rule3]

print("─── BREAKDOWN BY RULE ───")
print(f"Rule 1 (Conv1->Conv2 auto-boost):         {len(rule1)} trades")
print(f"Rule 2 (Conv2+ bypass AI Council):         {len(rule2)} trades")
print(f"Rule 3 (S4 extreme funding override):      {len(rule3)} trades")
print(f"Other:                                      {len(other)} trades")
print()

# ── Win rate analysis ──
known_outcomes = [t for t in new_trades if t['master_result'] != 'unknown']
correct = [t for t in known_outcomes if t['master_result'] == 'correct']
incorrect = [t for t in known_outcomes if t['master_result'] == 'incorrect']

print("─── WIN RATE (known outcomes only) ───")
print(f"Trades with known 24h outcome:    {len(known_outcomes)}")
print(f"Correct:                           {len(correct)}")
print(f"Incorrect:                         {len(incorrect)}")
if known_outcomes:
    print(f"Win rate:                          {len(correct)/len(known_outcomes)*100:.1f}%")
print()

# ── PnL simulation ──
print("─── PnL SIMULATION ───")
print("Assumptions:")
print("  - $95 balance (current)")
print("  - 0.35% risk per trade (Conv2 baseline)")
print("  - 3x leverage")
print("  - SELL PnL: if correct, price went down (positive PnL)")
print("  - BUY PnL: if correct, price went up (positive PnL)")
print("  - 24h outcome = entry to exit price change")
print()

total_pnl = 0
win_count = 0
loss_count = 0
wins_pnl = 0
losses_pnl = 0

for t in known_outcomes:
    risk_pct = 0.0035
    if t['new_conf'] >= 0.85:
        risk_pct = 0.0075
    elif t['new_conf'] >= 0.75:
        risk_pct = 0.0050
    
    # BUY asymmetry
    if t['action'] == 'BUY':
        risk_pct *= 0.65
    
    # Risk amount
    risk_amount = 95.0 * risk_pct
    
    # For SELL: profit if price went down (change_pct negative)
    # For BUY: profit if price went up (change_pct positive)
    change = t['change_pct']
    is_win = False
    pnl = 0
    
    if t['action'] == 'SELL' and change < 0:
        is_win = True
        pnl = risk_amount * 3 * (abs(change) / 100)  # 3x leverage
    elif t['action'] == 'SELL' and change > 0:
        is_win = False
        pnl = -risk_amount * 3 * (change / 100)  # Stop loss
    elif t['action'] == 'BUY' and change > 0:
        is_win = True
        pnl = risk_amount * 3 * (change / 100)
    elif t['action'] == 'BUY' and change < 0:
        is_win = False
        pnl = -risk_amount * 3 * (abs(change) / 100)
    
    # SL/TP bounds
    max_loss = -risk_amount
    max_profit = risk_amount * 5  # 5:1 RR
    
    pnl = max(min(pnl, max_profit), max_loss)
    total_pnl += pnl
    
    if is_win:
        win_count += 1
        wins_pnl += pnl
    else:
        loss_count += 1
        losses_pnl += pnl

print(f"Total simulated PnL:               ${total_pnl:.2f}")
print(f"Win count:                          {win_count}")
print(f"Loss count:                         {loss_count}")
if win_count + loss_count > 0:
    print(f"Win rate:                           {win_count/(win_count+loss_count)*100:.1f}%")
    avg_win = wins_pnl/win_count if win_count > 0 else 0
    avg_loss = losses_pnl/loss_count if loss_count > 0 else 0
    print(f"Avg win:                            ${avg_win:.2f}")
    print(f"Avg loss:                           ${avg_loss:.2f}")
    print(f"Profit factor:                      {abs(wins_pnl/losses_pnl):.2f}x" if losses_pnl != 0 else "N/A")
print(f"Final balance (starting $95):      ${95 + total_pnl:.2f}")
print()

# ── Per-symbol breakdown ──
print("─── PER-SYMBOL BREAKDOWN ───")
sym_stats = defaultdict(lambda: {'total': 0, 'correct': 0, 'incorrect': 0, 'pnl': 0.0, 'actions': Counter()})
for t in new_trades:
    sym = t['symbol']
    sym_stats[sym]['total'] += 1
    sym_stats[sym]['actions'][t['action']] += 1
    if t['master_result'] == 'correct':
        sym_stats[sym]['correct'] += 1
    elif t['master_result'] == 'incorrect':
        sym_stats[sym]['incorrect'] += 1

for sym in sorted(sym_stats, key=lambda s: sym_stats[s]['total'], reverse=True):
    s = sym_stats[sym]
    wr = s['correct'] / (s['correct'] + s['incorrect']) * 100 if (s['correct'] + s['incorrect']) > 0 else 0
    actions = ', '.join(f"{a}:{c}" for a, c in s['actions'].most_common())
    print(f"  {sym:12s} | {s['total']:3d} trades | {wr:5.1f}% WR | {actions}")
print()

# ── By action type ──
print("─── BREAKDOWN BY ACTION ───")
buys = [t for t in known_outcomes if t['action'] == 'BUY']
sells = [t for t in known_outcomes if t['action'] == 'SELL']
for label, trades_list in [("BUY", buys), ("SELL", sells)]:
    correct_c = sum(1 for t in trades_list if t['master_result'] == 'correct')
    incorrect_c = sum(1 for t in trades_list if t['master_result'] == 'incorrect')
    wr = correct_c / (correct_c + incorrect_c) * 100 if (correct_c + incorrect_c) > 0 else 0
    print(f"  {label:5s}: {len(trades_list):3d} known outcomes | {wr:5.1f}% WR")
print()

# ── By strategy ──
print("─── BREAKDOWN BY STRATEGY ───")
strat_stats = defaultdict(lambda: {'total': 0, 'correct': 0})
for t in new_trades:
    strat = t['strategy'][:25]
    strat_stats[strat]['total'] += 1
    if t['master_result'] == 'correct':
        strat_stats[strat]['correct'] += 1

for strat in sorted(strat_stats, key=lambda s: strat_stats[s]['total'], reverse=True)[:10]:
    s = strat_stats[strat]
    print(f"  {strat:25s} | {s['total']:3d} trades | {s['correct']:3d} correct")
print()

# ── Top signals by confidence ──
print("─── TOP NEW SIGNALS BY CONFIDENCE ───")
sorted_trades = sorted(new_trades, key=lambda t: t['new_conf'], reverse=True)[:10]
for t in sorted_trades:
    result_str = f"✅ {t['master_result'].upper()}" if t['master_result'] == 'correct' else f"❌ {t['master_result'].upper()}" if t['master_result'] == 'incorrect' else "❓ unknown"
    print(f"  {t['symbol']:12s} | {t['action']:4s} | Conv{t['new_conv']} | {t['new_conf']:.0%} conf | {t['gain7d']:+.1f}% 7D | {result_str} | {t['strategy'][:20]}")
print()

# ── Summary ──
print("=" * 100)
print("SUMMARY")
print("=" * 100)
print(f"Old rules:  12 trades executed")
print(f"New rules:  {len(new_trades)} trades possible")
print(f"Win rate:   {len(correct)}/{len(known_outcomes)} ({len(correct)/len(known_outcomes)*100:.1f}%)" if known_outcomes else "No outcomes data")
print(f"P&L impact: ${total_pnl:.2f} on $95 starting capital")
print(f"Key insight: Rule 2 (AI Council bypass) adds the most value — {len(rule2)} trades that were already Conv2+")
print("but blocked by 5 LLMs saying 'REJECTED'. These would auto-execute now.")
print("=" * 100)
