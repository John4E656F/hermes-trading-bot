package main

import (
	"fmt"
	"math"
	"testing"
)

// Payloads below use Bybit V5's exact /v5/position/list shape: every numeric
// field is a STRING, flat rows come back with size "0", and an unset stop
// loss is "" or "0".

func approxEq(a, b, tol float64) bool { return math.Abs(a-b) < tol }

// Two protected positions. Risk is |entry-stop| * size on each.
const twoProtected = `{"retCode":0,"retMsg":"OK","result":{"list":[
 {"symbol":"NEARUSDT","side":"Buy","size":"5.9","avgPrice":"2.4353",
  "positionValue":"14.368","stopLoss":"2.0889","takeProfit":"3.3063"},
 {"symbol":"ICPUSDT","side":"Buy","size":"4.2","avgPrice":"2.775",
  "positionValue":"11.655","stopLoss":"2.307","takeProfit":"3.946"}
]}}`

func TestPortfolioRiskTwoProtectedPositions(t *testing.T) {
	// NEAR: (2.4353-2.0889)*5.9 = 2.04376
	// ICP : (2.775 -2.307 )*4.2 = 1.96560
	// total 4.00936 / 100.00 equity = 4.00936%
	got, err := portfolioRiskFromPositions([]byte(twoProtected), 100.0)
	if err != nil {
		t.Fatal(err)
	}
	want := ((2.4353-2.0889)*5.9 + (2.775-2.307)*4.2) / 100.0
	if !approxEq(got, want, 1e-9) {
		t.Fatalf("risk = %.6f, want %.6f", got, want)
	}
	t.Logf("two protected positions on $100 equity -> %.4f%% open risk", got*100)
}

// An unprotected position must be charged its FULL position value, not zero.
func TestUnprotectedPositionChargedFullValue(t *testing.T) {
	for _, sl := range []string{`""`, `"0"`, `"0.0"`} {
		payload := fmt.Sprintf(`{"retCode":0,"result":{"list":[
		 {"symbol":"XRPUSDT","side":"Buy","size":"100","avgPrice":"0.50",
		  "positionValue":"50.00","stopLoss":%s}]}}`, sl)
		got, err := portfolioRiskFromPositions([]byte(payload), 100.0)
		if err != nil {
			t.Fatal(err)
		}
		if !approxEq(got, 0.50, 1e-9) {
			t.Errorf("stopLoss=%s gave risk %.6f, want 0.50 (full $50 value on $100 equity)", sl, got)
		}
	}
}

// A stop on the WRONG side of entry protects nothing.
func TestNonProtectiveStopChargedFullValue(t *testing.T) {
	// Long with a stop ABOVE entry, and short with a stop BELOW entry.
	payload := `{"retCode":0,"result":{"list":[
	 {"symbol":"AAAUSDT","side":"Buy","size":"10","avgPrice":"1.00",
	  "positionValue":"10.00","stopLoss":"1.20"},
	 {"symbol":"BBBUSDT","side":"Sell","size":"10","avgPrice":"1.00",
	  "positionValue":"10.00","stopLoss":"0.80"}]}}`
	got, err := portfolioRiskFromPositions([]byte(payload), 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(got, 0.20, 1e-9) {
		t.Fatalf("risk = %.6f, want 0.20 (both positions charged full value)", got)
	}
}

// Risk can never exceed the position's own value.
func TestRiskCappedAtPositionValue(t *testing.T) {
	// Stop far below zero-equivalent: |1.00-(-)|*10 would exceed $10 value.
	payload := `{"retCode":0,"result":{"list":[
	 {"symbol":"CCCUSDT","side":"Buy","size":"10","avgPrice":"1.00",
	  "positionValue":"10.00","stopLoss":"0.0001"}]}}`
	got, err := portfolioRiskFromPositions([]byte(payload), 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if got > 0.10+1e-9 {
		t.Fatalf("risk %.6f exceeds the position's own $10 value on $100 equity", got)
	}
}

// Flat rows (size "0") are returned by Bybit and must not count.
func TestFlatRowsIgnored(t *testing.T) {
	payload := `{"retCode":0,"result":{"list":[
	 {"symbol":"DDDUSDT","side":"Buy","size":"0","avgPrice":"1.00",
	  "positionValue":"0","stopLoss":""},
	 {"symbol":"EEEUSDT","side":"","size":"0","avgPrice":"0","positionValue":"0","stopLoss":""}]}}`
	got, err := portfolioRiskFromPositions([]byte(payload), 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("flat rows produced %.6f risk, want 0", got)
	}
}

// positionValue is preferred, but size*avgPrice is the fallback when absent.
func TestPositionValueFallback(t *testing.T) {
	payload := `{"retCode":0,"result":{"list":[
	 {"symbol":"FFFUSDT","side":"Buy","size":"20","avgPrice":"2.00","stopLoss":""}]}}`
	got, err := portfolioRiskFromPositions([]byte(payload), 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(got, 0.40, 1e-9) {
		t.Fatalf("risk = %.6f, want 0.40 (20*2.00 = $40 on $100 equity)", got)
	}
}

// A Bybit-level error must surface, not silently read as zero risk — zero
// would hand the caller a clean bill of health it never earned.
func TestBybitErrorSurfaces(t *testing.T) {
	payload := `{"retCode":10001,"retMsg":"params error","result":{"list":[]}}`
	if _, err := portfolioRiskFromPositions([]byte(payload), 100.0); err == nil {
		t.Fatal("expected an error for retCode 10001, got nil")
	}
	if _, err := portfolioRiskFromPositions([]byte(`not json`), 100.0); err == nil {
		t.Fatal("expected a parse error for malformed JSON, got nil")
	}
}

// THE BUG THIS FIXES: the number must track positions opening and closing
// across cycles, which an in-memory counter reset to 0.0 each process could
// never do.
func TestRiskTracksPositionsOpeningAndClosing(t *testing.T) {
	const equity = 100.0
	steps := []struct {
		name    string
		payload string
		wantPct float64
	}{
		{"cycle 1: flat", `{"retCode":0,"result":{"list":[]}}`, 0.0},
		{"cycle 2: one position carried in",
			`{"retCode":0,"result":{"list":[
			 {"symbol":"AUSDT","side":"Buy","size":"10","avgPrice":"1.00",
			  "positionValue":"10.00","stopLoss":"0.90"}]}}`, 1.0},
		{"cycle 3: a second added",
			`{"retCode":0,"result":{"list":[
			 {"symbol":"AUSDT","side":"Buy","size":"10","avgPrice":"1.00",
			  "positionValue":"10.00","stopLoss":"0.90"},
			 {"symbol":"BUSDT","side":"Sell","size":"20","avgPrice":"2.00",
			  "positionValue":"40.00","stopLoss":"2.10"}]}}`, 3.0},
		{"cycle 4: first one closed",
			`{"retCode":0,"result":{"list":[
			 {"symbol":"AUSDT","side":"Buy","size":"0","avgPrice":"1.00",
			  "positionValue":"0","stopLoss":"0.90"},
			 {"symbol":"BUSDT","side":"Sell","size":"20","avgPrice":"2.00",
			  "positionValue":"40.00","stopLoss":"2.10"}]}}`, 2.0},
		{"cycle 5: flat again", `{"retCode":0,"result":{"list":[]}}`, 0.0},
	}

	for _, st := range steps {
		// Each iteration is a FRESH process: the global starts at its zero
		// value and is seeded from the live query, exactly as main() does.
		globalPortfolioRiskPct = 0.0
		got, err := portfolioRiskFromPositions([]byte(st.payload), equity)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		globalPortfolioRiskPct = got
		if !approxEq(globalPortfolioRiskPct*100, st.wantPct, 1e-6) {
			t.Errorf("%s: seeded %.4f%%, want %.4f%%", st.name,
				globalPortfolioRiskPct*100, st.wantPct)
		}
		t.Logf("%-34s seeded globalPortfolioRiskPct = %.2f%%", st.name,
			globalPortfolioRiskPct*100)
	}
	globalPortfolioRiskPct = 0.0
}

// The scenario from the bug report: positions carried in from earlier cycles
// consume the cap, so a fresh max-risk trade is refused rather than stacked
// on top of exposure the old counter could not see.
func TestCarriedInPositionsConsumeTheCap(t *testing.T) {
	// Five 0.75%-risk positions opened over five earlier cycles = 3.75%.
	payload := `{"retCode":0,"result":{"list":[
	 {"symbol":"P1USDT","side":"Buy","size":"75","avgPrice":"1.00","positionValue":"75.00","stopLoss":"0.99"},
	 {"symbol":"P2USDT","side":"Buy","size":"75","avgPrice":"1.00","positionValue":"75.00","stopLoss":"0.99"},
	 {"symbol":"P3USDT","side":"Buy","size":"75","avgPrice":"1.00","positionValue":"75.00","stopLoss":"0.99"},
	 {"symbol":"P4USDT","side":"Buy","size":"75","avgPrice":"1.00","positionValue":"75.00","stopLoss":"0.99"},
	 {"symbol":"P5USDT","side":"Buy","size":"75","avgPrice":"1.00","positionValue":"75.00","stopLoss":"0.99"}]}}`
	const equity = 100.0

	seeded, err := portfolioRiskFromPositions([]byte(payload), equity)
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(seeded*100, 3.75, 1e-6) {
		t.Fatalf("seeded %.4f%%, want 3.75%%", seeded*100)
	}

	// OLD behaviour: counter reset to 0, so a fresh 0.75% trade sees 0 + 0.75
	// = 0.75% against a 4% cap and is approved. Real exposure becomes 4.50%.
	oldTotal := 0.0 + 0.0075
	if oldTotal > MAX_PORTFOLIO_RISK {
		t.Fatal("pre-fix behaviour should have approved the trade — test premise is wrong")
	}
	t.Logf("PRE-FIX : counter 0.00%% + new 0.75%% = 0.75%% vs %.0f%% cap -> APPROVED, "+
		"real exposure would be %.2f%%", MAX_PORTFOLIO_RISK*100, (seeded+0.0075)*100)

	// NEW behaviour: seeded from live positions, the same trade breaches.
	newTotal := seeded + 0.0075
	if newTotal <= MAX_PORTFOLIO_RISK {
		t.Fatalf("post-fix: %.4f%% should exceed the %.0f%% cap", newTotal*100, MAX_PORTFOLIO_RISK*100)
	}
	t.Logf("POST-FIX: counter %.2f%% + new 0.75%% = %.2f%% vs %.0f%% cap -> REFUSED",
		seeded*100, newTotal*100, MAX_PORTFOLIO_RISK*100)
}
