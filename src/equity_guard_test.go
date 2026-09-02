package main

import (
	"os"
	"testing"
)

func withTempState(t *testing.T, fn func()) {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	fn()
}

func TestDrawdownTiers(t *testing.T) {
	cases := []struct {
		name          string
		peak, equity  float64
		wantTier      string
		wantRiskMult  float64
		wantMinConv   int
		wantBlocked   bool
		wantHardHalt  bool
	}{
		{"at peak", 1000, 1000, "NORMAL", 1.00, 2, false, false},
		{"-4.9% just above tier1", 1000, 951, "NORMAL", 1.00, 2, false, false},
		{"-5% risk 75", 1000, 950, "RISK_75", 0.75, 2, false, false},
		{"-7.9%", 1000, 921, "RISK_75", 0.75, 2, false, false},
		{"-8% risk 50", 1000, 920, "RISK_50", 0.50, 2, false, false},
		{"-10% conv3 only", 1000, 900, "CONVICTION_3_ONLY", 0.50, 3, false, false},
		{"-12% no new entries", 1000, 880, "NO_NEW_ENTRIES", 0.50, 3, true, false},
		{"-15% hard halt", 1000, 850, "HARD_HALT", 0.00, 99, true, true},
		{"-40% hard halt", 1000, 600, "HARD_HALT", 0.00, 99, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempState(t, func() {
				// Seed the high-water mark, then drop equity to the test level.
				EvaluateDrawdownGuard(tc.peak, false)
				got := EvaluateDrawdownGuard(tc.equity, false)

				if got.Tier != tc.wantTier {
					t.Errorf("tier = %q, want %q (dd=%.2f%%)", got.Tier, tc.wantTier, got.DrawdownPct)
				}
				if got.RiskMultiplier != tc.wantRiskMult {
					t.Errorf("riskMult = %.2f, want %.2f", got.RiskMultiplier, tc.wantRiskMult)
				}
				if got.MinConviction != tc.wantMinConv {
					t.Errorf("minConviction = %d, want %d", got.MinConviction, tc.wantMinConv)
				}
				if got.BlockNewEntries != tc.wantBlocked {
					t.Errorf("blockNewEntries = %v, want %v", got.BlockNewEntries, tc.wantBlocked)
				}
				if got.HardHalt != tc.wantHardHalt {
					t.Errorf("hardHalt = %v, want %v", got.HardHalt, tc.wantHardHalt)
				}
			})
		})
	}
}

// The 15-minute cron restarts the process constantly. A peak held only in
// memory would reset every run and make the guard permanently inert.
func TestPeakSurvivesRestart(t *testing.T) {
	withTempState(t, func() {
		EvaluateDrawdownGuard(1000, false) // peak established
		EvaluateDrawdownGuard(970, false)  // -3%, peak must not follow equity down

		// Simulate a fresh process: nothing in memory, only the file on disk.
		st := LoadEquityState()
		if st.PeakEquity != 1000 {
			t.Fatalf("persisted peak = %.2f, want 1000", st.PeakEquity)
		}

		got := EvaluateDrawdownGuard(880, false)
		if got.Tier != "NO_NEW_ENTRIES" {
			t.Errorf("after restart tier = %q, want NO_NEW_ENTRIES (dd=%.2f%%)", got.Tier, got.DrawdownPct)
		}
		if got.PeakEquity != 1000 {
			t.Errorf("after restart peak = %.2f, want 1000", got.PeakEquity)
		}
	})
}

// A hard halt must latch: a bounce back above the -15% line does not resume
// trading on its own, it requires a human to clear the state file.
func TestHardHaltLatches(t *testing.T) {
	withTempState(t, func() {
		EvaluateDrawdownGuard(1000, false)
		if got := EvaluateDrawdownGuard(840, false); !got.HardHalt {
			t.Fatalf("expected hard halt at -16%%, got %q", got.Tier)
		}
		// Equity recovers to only -2% from peak. Still halted.
		got := EvaluateDrawdownGuard(980, false)
		if !got.HardHalt {
			t.Errorf("halt did not latch: tier=%q after recovery", got.Tier)
		}
		if got.Tier != "HALTED" {
			t.Errorf("tier = %q, want HALTED", got.Tier)
		}
	})
}

// Scan mode must never write the high-water mark: it falls back to a dummy
// $100 balance when the wallet fetch fails, which would destroy a real peak.
func TestScanModeDoesNotPersist(t *testing.T) {
	withTempState(t, func() {
		EvaluateDrawdownGuard(1000, false)
		EvaluateDrawdownGuard(100, true) // scan-mode dummy balance
		if st := LoadEquityState(); st.PeakEquity != 1000 || st.LastEquity == 100 {
			t.Errorf("scan mode mutated state: peak=%.2f last=%.2f", st.PeakEquity, st.LastEquity)
		}
	})
}

// The absolute-dollar floor is a secondary net, not a replacement: a large
// account deep in drawdown is still restricted by the percentage tiers.
func TestAbsoluteFloorIsSecondary(t *testing.T) {
	withTempState(t, func() {
		EvaluateDrawdownGuard(1000, false)
		act := ApplyAbsoluteFloor(EvaluateDrawdownGuard(900, false), 900)
		if act.Tier != "CONVICTION_3_ONLY" || act.BlockNewEntries {
			t.Errorf("large account at -10%%: tier=%q blocked=%v — floor should not have fired",
				act.Tier, act.BlockNewEntries)
		}

		// Tiny account at its own peak: percentage tier is NORMAL, but the
		// floor blocks because no compliant order can be funded.
		small := ApplyAbsoluteFloor(DrawdownAction{Tier: "NORMAL", RiskMultiplier: 1, MinConviction: 2}, 8.0)
		if !small.BlockNewEntries {
			t.Errorf("$8.00 equity should be blocked by the absolute floor")
		}
	})
}
