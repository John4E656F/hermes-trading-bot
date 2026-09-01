#!/usr/bin/env python3
"""
Neo Oracle — Self-Improving Texas Hold'em Poker Bot
====================================================
Self-improvement system that learns from every hand:
  - Tracks win/loss by starting hand, position, street, and action
  - Adjusts aggression factor, bluff frequency, bet sizing, and fold thresholds
  - Persists to learning_db.json between benchmark runs
  - Logs lessons learned to lessons.log

Interface: function act(state) -> dict
  state: dict with keys listed below
  returns: {"action": "fold"|"call"|"raise"|"all-in", "amount": int}
"""

import json
import os
import random
import time
from collections import defaultdict
from typing import Any, Dict, List, Optional, Tuple

# ── Persistence ────────────────────────────────────────────────────────────

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
LEARNING_DB = os.path.join(BASE_DIR, "learning_db.json")
LESSONS_LOG = os.path.join(BASE_DIR, "lessons.log")

DEFAULT_PARAMS = {
    "aggression": 1.0,
    "bluff_frequency": 0.12,
    "fold_to_raise": 0.65,
    "call_threshold": 0.35,
    "value_bet_pot_pct": 0.75,
    "bluff_bet_pot_pct": 0.55,
    "raise_on_flop_pct": 0.40,
    "three_bet_freq": 0.08,
    "positional_aggression_boost": {0: 1.3, 1: 1.2, 2: 1.1, 3: 1.0, 4: 0.9, 5: 0.8},
    "hand_strength_thresholds": {
        "premium": 0.85,
        "strong": 0.70,
        "decent": 0.50,
        "speculative": 0.35,
        "trash": 0.20,
    },
    "bluff_threshold": 0.60,
    "adaptation_rate": 0.05,
    "min_hands_before_adapt": 50,
    "aggression_floor": 0.5,
    "aggression_ceiling": 2.5,
}


def log_lesson(msg: str):
    with open(LESSONS_LOG, "a") as f:
        ts = time.strftime("%Y-%m-%d %H:%M:%S")
        f.write(f"[{ts}] {msg}\n")


class LearningDB:
    def __init__(self, path: str = LEARNING_DB):
        self.path = path
        self.data = self._load()

    def _load(self) -> dict:
        if os.path.exists(self.path):
            with open(self.path) as f:
                try:
                    return json.load(f)
                except json.JSONDecodeError:
                    pass
        return {
            "params": DEFAULT_PARAMS.copy(),
            "hands": [],
            "hand_stats": defaultdict(lambda: {"wins": 0, "losses": 0, "total_pnl": 0, "count": 0}),
            "position_stats": defaultdict(lambda: {"wins": 0, "losses": 0, "count": 0}),
            "street_stats": defaultdict(lambda: {"aggressive": 0, "passive": 0, "won": 0, "lost": 0}),
            "bluff_attempts": 0,
            "bluff_successes": 0,
            "total_hands": 0,
            "total_bb_won": 0.0,
            "adaptation_history": [],
            "version": 2,
        }

    def save(self):
        self.data["hand_stats"] = dict(self.data["hand_stats"])
        self.data["position_stats"] = dict(self.data["position_stats"])
        self.data["street_stats"] = dict(self.data["street_stats"])
        os.makedirs(os.path.dirname(self.path) if os.path.dirname(self.path) else ".", exist_ok=True)
        with open(self.path, "w") as f:
            json.dump(self.data, f, indent=2)

    def record_hand(self, hand_record: dict):
        self.data["hands"].append(hand_record)
        self.data["total_hands"] += 1
        pnl = hand_record.get("pnl", 0)
        self.data["total_bb_won"] += pnl

        hole = hand_record.get("hole", "??")
        pos = hand_record.get("position", -1)
        street_actions = hand_record.get("street_actions", {})

        # Track by hand type
        key = hole
        hs = self.data["hand_stats"]
        if key not in hs:
            hs[key] = {"wins": 0, "losses": 0, "total_pnl": 0, "count": 0}
        hs[key]["count"] += 1
        hs[key]["total_pnl"] += pnl
        if pnl > 0:
            hs[key]["wins"] += 1
        else:
            hs[key]["losses"] += 1

        # Track by position
        pos_key = str(pos)
        ps = self.data["position_stats"]
        if pos_key not in ps:
            ps[pos_key] = {"wins": 0, "losses": 0, "count": 0}
        ps[pos_key]["count"] += 1
        if pnl > 0:
            ps[pos_key]["wins"] += 1
        else:
            ps[pos_key]["losses"] += 1

        # Track bluff success
        if hand_record.get("was_bluff", False):
            self.data["bluff_attempts"] += 1
            if pnl > 0:
                self.data["bluff_successes"] += 1

        # Adapt every min_hands_before_adapt
        if self.data["total_hands"] % self.data["params"]["min_hands_before_adapt"] == 0:
            self._adapt()

    def _adapt(self):
        p = self.data["params"]
        hs = self.data["hand_stats"]
        total = self.data["total_hands"]
        bb_won = self.data["total_bb_won"]
        win_rate = self._calculate_win_rate()

        old_agg = p["aggression"]
        old_bluff = p["bluff_frequency"]

        lesson_parts = []

        # 1. Adjust aggression based on overall win rate
        if total >= p["min_hands_before_adapt"]:
            if win_rate < 0.40:
                # Losing — tighten up
                p["aggression"] = max(p["aggression_floor"], p["aggression"] - p["adaptation_rate"])
                lesson_parts.append(f"win_rate={win_rate:.2f} < 0.40 → aggression ↓ to {p['aggression']:.2f}")
            elif win_rate > 0.55:
                # Winning — can apply more pressure
                p["aggression"] = min(p["aggression_ceiling"], p["aggression"] + p["adaptation_rate"])
                lesson_parts.append(f"win_rate={win_rate:.2f} > 0.55 → aggression ↑ to {p['aggression']:.2f}")
            else:
                lesson_parts.append(f"win_rate={win_rate:.2f} stable, aggression={p['aggression']:.2f}")

        # 2. Adjust bluff frequency
        if self.data["bluff_attempts"] > 5:
            bluff_success_rate = self.data["bluff_successes"] / max(self.data["bluff_attempts"], 1)
            if bluff_success_rate < 0.25:
                p["bluff_frequency"] = max(0.03, p["bluff_frequency"] - p["adaptation_rate"])
                lesson_parts.append(f"bluff_success={bluff_success_rate:.2f} < 0.25 → bluff ↓ to {p['bluff_frequency']:.2f}")
            elif bluff_success_rate > 0.50:
                p["bluff_frequency"] = min(0.30, p["bluff_frequency"] + p["adaptation_rate"])
                lesson_parts.append(f"bluff_success={bluff_success_rate:.2f} > 0.50 → bluff ↑ to {p['bluff_frequency']:.2f}")

        # 3. Analyze which positions are losing and tighten fold thresholds
        for pos_key, pstats in self.data["position_stats"].items():
            if pstats["count"] >= 10:
                pos_win_rate = pstats["wins"] / max(pstats["count"], 1)
                pos_int = int(pos_key)
                if pos_win_rate < 0.30 and pos_int in p["positional_aggression_boost"]:
                    p["positional_aggression_boost"][pos_int] = max(
                        0.5, p["positional_aggression_boost"][pos_int] - p["adaptation_rate"]
                    )
                    lesson_parts.append(f"position {pos_key} WR={pos_win_rate:.2f} → agg_boost ↓ to {p['positional_aggression_boost'][pos_int]:.1f}")

        # 4. Analyze strongest starting hands
        profitable_hands = []
        losing_hands = []
        for hkey, hstats in hs.items():
            if hstats["count"] >= 5:
                hwr = hstats["wins"] / max(hstats["count"], 1)
                if hwr > 0.60 and hstats["total_pnl"] > 0:
                    profitable_hands.append((hkey, hwr, hstats["total_pnl"]))
                elif hwr < 0.25:
                    losing_hands.append((hkey, hwr, hstats["total_pnl"]))

        if profitable_hands:
            lesson_parts.append(f"top winners: {', '.join(f'{h}({wr:.0%})' for h, wr, _ in profitable_hands[:3])}")
        if losing_hands:
            lesson_parts.append(f"leaks: {', '.join(f'{h}({wr:.0%})' for h, wr, _ in losing_hands[:3])}")

        # Save adaptation
        adaptation = {
            "at_hand": total,
            "old_aggression": old_agg,
            "new_aggression": p["aggression"],
            "old_bluff": old_bluff,
            "new_bluff": p["bluff_frequency"],
            "win_rate": win_rate,
            "bb_won": bb_won,
            "reason": "; ".join(lesson_parts),
        }
        self.data["adaptation_history"].append(adaptation)
        log_lesson(f"Adapt hand#{total}: " + "; ".join(lesson_parts))
        self.save()

    def _calculate_win_rate(self) -> float:
        stats = self.data["hand_stats"]
        if not stats:
            return 0.5
        total_wins = sum(s["wins"] for s in stats.values())
        total_hands = sum(s["count"] for s in stats.values())
        return total_wins / max(total_hands, 1)


# ── Global singleton ───────────────────────────────────────────────────────

_db = None


def get_db() -> LearningDB:
    global _db
    if _db is None:
        _db = LearningDB()
    return _db


# ── Hand Strength Evaluation ───────────────────────────────────────────────

RANK_VALUES = {
    "2": 2, "3": 3, "4": 4, "5": 5, "6": 6, "7": 7,
    "8": 8, "9": 9, "T": 10, "J": 11, "Q": 12, "K": 13, "A": 14,
}
SUITS = {"h", "d", "c", "s"}


def parse_card(card: str) -> Tuple[int, str]:
    rank = card[:-1]
    suit = card[-1].lower()
    return RANK_VALUES.get(rank, 0), suit


def evaluate_hand_strength(hole: List[str], board: List[str]) -> float:
    """Returns a normalized hand strength 0.0–1.0."""
    if not hole:
        return 0.0

    r1, s1 = parse_card(hole[0])
    r2, s2 = parse_card(hole[1]) if len(hole) > 1 else (0, "")
    suited = s1 == s2 if s1 and s2 else False
    gap = abs(r1 - r2)
    high = max(r1, r2)
    low = min(r1, r2)
    paired = r1 == r2

    # Preflop hand strength estimation
    if not board:
        return _preflop_strength(r1, r2, suited, paired, gap)

    # Postflop: combine with board
    all_cards = hole + board
    return _postflop_strength(all_cards, hole, board)


def _preflop_strength(r1: int, r2: int, suited: bool, paired: bool, gap: int) -> float:
    strength = 0.0

    if paired:
        if r1 >= 14:
            strength = 0.98  # AA
        elif r1 >= 13:
            strength = 0.95  # KK
        elif r1 >= 12:
            strength = 0.91  # QQ
        elif r1 >= 11:
            strength = 0.86  # JJ
        elif r1 >= 10:
            strength = 0.81  # TT
        elif r1 >= 9:
            strength = 0.74  # 99
        elif r1 >= 7:
            strength = 0.60  # 77+
        elif r1 >= 5:
            strength = 0.45  # 55+
        else:
            strength = 0.35  # low pairs
    elif r1 >= 14 and r2 >= 13:
        strength = 0.93 if suited else 0.89  # AK
    elif r1 >= 14 and r2 >= 12:
        strength = 0.82 if suited else 0.75  # AQ
    elif r1 >= 14 and r2 >= 11:
        strength = 0.75 if suited else 0.67  # AJ
    elif r1 >= 13 and r2 >= 12:
        strength = 0.73 if suited else 0.66  # KQ
    elif r1 >= 14 and r2 >= 5:
        # Ace-x
        base = 0.50 + (r2 - 5) * 0.015
        strength = base + (0.08 if suited else 0.0)
    elif suited and gap <= 2 and r1 >= 10:
        strength = 0.45 + (r1 - 10) * 0.03  # suited connectors
    else:
        strength = max(0.05, 0.30 - gap * 0.03 - (14 - high) * 0.01)

    return min(1.0, max(0.0, strength))


def _postflop_strength(all_cards: List[Tuple[int, str]], hole, board) -> float:
    """Basic postflop strength based on hand category."""
    ranks = [c[0] for c in all_cards] if all_cards else []
    suits = [c[1] for c in all_cards] if all_cards else []

    from collections import Counter
    rank_counts = Counter(ranks)
    suit_counts = Counter(suits)

    has_pair = any(c == 2 for c in rank_counts.values())
    has_two_pair = sum(1 for c in rank_counts.values() if c == 2) >= 2
    has_trips = any(c >= 3 for c in rank_counts.values())
    has_quads = any(c >= 4 for c in rank_counts.values())
    has_flush = any(c >= 5 for c in suit_counts.values())
    has_straight = _check_straight(sorted(set(ranks), reverse=True)) if len(set(ranks)) >= 5 else False

    # Check if hole cards contribute to the best hand
    hole_ranks = [parse_card(c)[0] for c in hole]
    board_ranks = [parse_card(c)[0] for c in board]

    if has_quads:
        return 0.99
    if has_flush and has_straight:
        return 0.98
    if has_trips:
        # Check if trips use a hole card
        for r in set(hole_ranks):
            if rank_counts[r] >= 3:
                return 0.88
        return 0.60  # board trips
    if has_flush:
        # Check if hole matches flush suit
        hole_suits = [parse_card(c)[1] for c in hole]
        for s in set(hole_suits):
            if suit_counts[s] >= 5:
                return 0.82
        return 0.40  # flush on board
    if has_straight:
        return 0.72
    if has_two_pair:
        # Check if one pair is from hole
        for r in set(hole_ranks):
            if rank_counts[r] >= 2:
                return 0.65
        return 0.35
    if has_pair:
        for r in set(hole_ranks):
            if rank_counts[r] >= 2:
                return 0.55
        return 0.15  # pair on board only

    # High card
    max_hole = max(hole_ranks) if hole_ranks else 0
    return 0.05 + (max_hole - 2) * 0.02


def _check_straight(ranks_sorted_desc) -> bool:
    if len(ranks_sorted_desc) < 5:
        return False
    for i in range(len(ranks_sorted_desc) - 4):
        if ranks_sorted_desc[i] - ranks_sorted_desc[i + 4] == 4:
            return True
    # Ace-low straight (A-2-3-4-5)
    if 14 in ranks_sorted_desc and 2 in ranks_sorted_desc and 3 in ranks_sorted_desc and 4 in ranks_sorted_desc and 5 in ranks_sorted_desc:
        return True
    return False


# ── Decision Engine ────────────────────────────────────────────────────────

def make_decision(
    hole: List[str],
    board: List[str],
    pot: int,
    current_bet: int,
    stack: int,
    position: int,
    num_players: int,
    street: str,
    call_amount: int,
    min_raise_to: int,
    params: dict,
) -> Dict[str, Any]:
    """Core decision logic — returns {"action": str, "amount": int}"""
    strength = evaluate_hand_strength(hole, board)
    pot_odds = call_amount / max(pot + call_amount, 1)
    # Adjust pot odds by aggression
    agg = params["aggression"]
    pos_boost = params["positional_aggression_boost"].get(position, 1.0)
    effective_agg = agg * pos_boost

    # Determine hand category
    thresholds = params["hand_strength_thresholds"]
    is_premium = strength >= thresholds["premium"]
    is_strong = strength >= thresholds["strong"]
    is_decent = strength >= thresholds["decent"]
    is_bluff = strength < thresholds["speculative"] and random.random() < params["bluff_frequency"] * effective_agg
    is_speculative = thresholds["speculative"] <= strength < thresholds["decent"]

    result = {"action": None, "amount": 0, "was_bluff": False}

    # No call needed — can check (free to see next card)
    if call_amount == 0:
        if is_premium or is_strong:
            # Value bet
            bet_size = int(pot * params["value_bet_pot_pct"] * effective_agg)
            bet_size = max(min_raise_to, min(bet_size, stack))
            if bet_size > 0:
                result["action"] = "raise"
                result["amount"] = bet_size
            else:
                result["action"] = "call"
        elif is_decent or is_speculative:
            result["action"] = "call"
        elif is_bluff:
            bet_size = int(pot * params["bluff_bet_pot_pct"] * effective_agg)
            bet_size = max(min_raise_to, min(bet_size, stack))
            if bet_size > 0:
                result["action"] = "raise"
                result["amount"] = bet_size
                result["was_bluff"] = True
            else:
                result["action"] = "call"
        else:
            result["action"] = "call"
        return result

    # Facing a bet
    if pot_odds >= strength * effective_agg:
        # Pot odds justify a call or better
        if is_premium and random.random() < params["raise_on_flop_pct"] * effective_agg:
            # Raise for value
            raise_size = int(pot * params["value_bet_pot_pct"] * effective_agg)
            raise_size = max(min_raise_to, min(raise_size, stack))
            if raise_size > 0:
                result["action"] = "raise"
                result["amount"] = raise_size
            else:
                result["action"] = "call"
        elif is_decent or is_strong:
            # Standard call
            result["action"] = "call"
        elif is_speculative and call_amount <= stack * 0.15:
            # Cheap call with speculative hand
            result["action"] = "call"
        elif is_bluff and random.random() < 0.3:
            # Bluff raise
            raise_size = int(pot * params["bluff_bet_pot_pct"])
            raise_size = max(min_raise_to, min(raise_size, stack))
            if raise_size > 0:
                result["action"] = "raise"
                result["amount"] = raise_size
                result["was_bluff"] = True
            else:
                result["action"] = "fold"
        else:
            result["action"] = "fold"
    else:
        # Pot odds don't justify — fold unless premium hand or bluff
        if is_premium or is_strong:
            result["action"] = "call"
        elif is_bluff and random.random() < 0.15:
            raise_size = int(pot * params["bluff_bet_pot_pct"] * 0.7)
            raise_size = max(min_raise_to, min(raise_size, stack))
            if raise_size > 0:
                result["action"] = "raise"
                result["amount"] = raise_size
                result["was_bluff"] = True
            else:
                result["action"] = "fold"
        else:
            result["action"] = "fold"

    return result


# ── Agent Interface ────────────────────────────────────────────────────────

def act(state: dict) -> dict:
    """
    Entry point called by the sandbox.

    Expected state keys:
      - hole (list[str]): Your hole cards, e.g. ["Ah", "Kd"]
      - board (list[str]): Community cards
      - pot (int): Total pot chips
      - current_bet (int): Current bet on this street
      - stack (int): Your remaining chips
      - position (int): Seat number (0-indexed)
      - num_players (int): Players still in the hand
      - street (str): "Preflop"|"Flop"|"Turn"|"River"
      - call_amount (int): Chips needed to call
      - min_raise_to (int): Minimum raise amount
      - hand_id (str): Unique hand identifier (for tracking)

    Returns {"action": "fold"|"call"|"raise"|"all-in", "amount": int}
    """
    db = get_db()
    params = db.data["params"]

    hole = state.get("hole", [])
    board = state.get("board", [])
    pot = state.get("pot", 0)
    current_bet = state.get("current_bet", 0)
    stack = state.get("stack", 1000)
    position = state.get("position", 0)
    num_players = state.get("num_players", 2)
    street = state.get("street", "Preflop")
    call_amount = state.get("call_amount", 0)
    min_raise_to = state.get("min_raise_to", 0)
    hand_id = state.get("hand_id", "")

    # Check for all-in scenario
    if call_amount >= stack:
        strength = evaluate_hand_strength(hole, board)
        if strength >= params["hand_strength_thresholds"]["strong"]:
            return {"action": "call", "amount": stack}
        return {"action": "fold"}

    decision = make_decision(
        hole=hole, board=board, pot=pot, current_bet=current_bet,
        stack=stack, position=position, num_players=num_players,
        street=street, call_amount=call_amount, min_raise_to=min_raise_to,
        params=params,
    )

    # Clamp amounts
    action = decision["action"]
    amount = decision["amount"]

    if action == "raise" and amount > stack:
        action = "all-in"
    elif action == "all-in":
        amount = stack
    if action == "call" and call_amount >= stack:
        action = "all-in"
        amount = stack
    if action in ("call", "all-in"):
        amount = call_amount if action == "call" else stack

    # Log decision for learning
    record = {
        "hand_id": hand_id,
        "hole": "".join(hole),
        "board": "".join(board) if board else "",
        "pot": pot,
        "street": street,
        "position": position,
        "action": action,
        "amount": amount,
        "strength": round(evaluate_hand_strength(hole, board), 3),
        "was_bluff": decision.get("was_bluff", False),
        "timestamp": time.time(),
    }
    db.data.setdefault("decisions", []).append(record)

    return {"action": action, "amount": amount}


def report_result(hand_result: dict):
    """
    Called after a hand settles to feed results back to the learning system.

    Expected keys:
      - hand_id (str): Matching the hand_id from act()
      - pnl (int): Chips won/lost this hand
      - hole (list[str]): Your hole cards
      - position (int): Seat
      - street_actions (dict): Actions by street (optional)
    """
    db = get_db()
    db.record_hand({
        "hole": "".join(hand_result.get("hole", [])),
        "pnl": hand_result.get("pnl", 0),
        "position": hand_result.get("position", -1),
        "street_actions": hand_result.get("street_actions", {}),
        "was_bluff": hand_result.get("was_bluff", False),
    })
    db.save()


def get_stats() -> dict:
    """Return current learning stats for the owner."""
    db = get_db()
    wr = db._calculate_win_rate()
    return {
        "total_hands": db.data["total_hands"],
        "win_rate": round(wr, 3),
        "total_bb_won": round(db.data["total_bb_won"], 1),
        "aggression": db.data["params"]["aggression"],
        "bluff_frequency": db.data["params"]["bluff_frequency"],
        "adaptations": len(db.data["adaptation_history"]),
    }


# ── Diagnostic Entry Point (when run directly) ─────────────────────────────

if __name__ == "__main__":
    print("Neo Oracle Poker Bot — Self-Improving Engine v2")
    print(f"Total hands learned: {get_db().data['total_hands']}")
    print(f"Current params: aggression={get_db().data['params']['aggression']:.2f}, "
          f"bluff_freq={get_db().data['params']['bluff_frequency']:.2f}")
    if get_db().data['adaptation_history']:
        last = get_db().data['adaptation_history'][-1]
        print(f"Last adaptation: hand#{last['at_hand']} — {last['reason']}")