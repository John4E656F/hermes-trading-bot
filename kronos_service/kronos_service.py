"""
Kronos AI Prediction Microservice for Hermes Trading Bot.

Runs as a local HTTP server on port 8765. The Go bot calls /predict/{symbol}
or /predict_batch with bare ticker symbols (e.g. "BTC"), and this service:
  1. Fetches the last LOOKBACK 4H candles for that symbol from Bybit's public
     kline API (no auth required).
  2. Runs Kronos-base inference to forecast the next PRED_LEN candles.
  3. Returns a direction/confidence/zone summary.

First run downloads model weights (~400MB) from Hugging Face automatically.

Usage:
    python kronos_service.py

Endpoints:
    GET  /                -> health check (alias of /health)
    GET  /health          -> {"status":"ready"|"model_not_loaded","model":"Kronos-base"}
    GET  /predict/<sym>   -> {"symbol","price","composite","zone","direction","confidence"}
    POST /predict_batch   -> {"symbols":["BTC","ETH"]} -> [ {...}, {...} ]
"""

import sys
import os
import json
import logging
import traceback
import urllib.request

# Add Kronos model directory to Python path
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
KRONOS_REPO = os.path.join(SCRIPT_DIR, "Kronos")
if KRONOS_REPO not in sys.path:
    sys.path.insert(0, KRONOS_REPO)

import numpy as np
import pandas as pd
from flask import Flask, request, jsonify

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [Kronos] %(levelname)s %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("kronos")

app = Flask(__name__)
predictor = None       # KronosPredictor instance, set by load_model()
MODEL_NAME = "Kronos-base"

# ── Prediction thresholds ─────────────────────────────────────────────────────
# Signal fires only when predicted move exceeds this threshold.
# Set conservatively — crypto is noisy and Kronos was trained on equity data.
BUY_THRESHOLD_PCT  = 1.5   # > +1.5% predicted -> BUY
SELL_THRESHOLD_PCT = -1.5  # < -1.5% predicted -> SELL

# Kronos inference parameters
PRED_LEN     = 3    # predict next 3 x 4H candles (~12 hours)
LOOKBACK     = 90   # historical context window (candles)
SAMPLE_COUNT = 2    # average N independent samples (reduces variance)
TEMPERATURE  = 0.8
TOP_P        = 0.95

BYBIT_KLINE_URL = "https://api.bybit.com/v5/market/kline"


def load_model():
    """Load Kronos-base + tokenizer from Hugging Face (cached after first download)."""
    global predictor
    log.info("Loading %s from HuggingFace Hub (~102.3M params, first run downloads ~400MB)...", MODEL_NAME)
    try:
        from model.kronos import KronosPredictor, Kronos, KronosTokenizer

        tokenizer = KronosTokenizer.from_pretrained("NeoQuasar/Kronos-Tokenizer-base")
        tokenizer.eval()

        model = Kronos.from_pretrained("NeoQuasar/Kronos-base")
        model.eval()

        predictor = KronosPredictor(
            model=model,
            tokenizer=tokenizer,
            device="cpu",
            max_context=512,
            clip=5,
        )
        log.info("%s loaded successfully (512 context, 102.3M params).", MODEL_NAME)
    except Exception as e:
        log.error("Failed to load Kronos model: %s", e)
        log.error(traceback.format_exc())
        predictor = None


def to_bybit_symbol(symbol: str) -> str:
    """Normalize a bare ticker ("BTC") or pair ("BTCUSDT") to a Bybit linear symbol."""
    sym = symbol.upper()
    if not sym.endswith("USDT"):
        sym += "USDT"
    return sym


def fetch_candles(symbol: str, interval: str = "240", limit: int = LOOKBACK) -> pd.DataFrame:
    """
    Fetch the last `limit` candles for `symbol` from Bybit's public kline API
    (category=linear, no authentication required). Returns a DataFrame indexed
    by UTC timestamp, ascending order, with open/high/low/close/volume columns.
    """
    bybit_symbol = to_bybit_symbol(symbol)
    url = (f"{BYBIT_KLINE_URL}?category=linear&symbol={bybit_symbol}"
           f"&interval={interval}&limit={limit}")

    with urllib.request.urlopen(url, timeout=15) as resp:
        data = json.loads(resp.read().decode("utf-8"))

    if data.get("retCode") != 0:
        raise RuntimeError(f"Bybit kline error [{data.get('retCode')}]: {data.get('retMsg')}")

    rows = data.get("result", {}).get("list", [])
    if not rows:
        raise RuntimeError(f"no kline data returned for {bybit_symbol}")

    # Bybit returns most-recent-first: [startTime, open, high, low, close, volume, turnover]
    rows = list(reversed(rows))
    records = []
    timestamps = []
    for r in rows:
        timestamps.append(pd.to_datetime(int(r[0]), unit="ms", utc=True))
        records.append({
            "open":   float(r[1]),
            "high":   float(r[2]),
            "low":    float(r[3]),
            "close":  float(r[4]),
            "volume": float(r[5]),
        })

    df = pd.DataFrame(records)
    df.index = pd.DatetimeIndex(timestamps).tz_localize(None)
    return df


def predict_symbol(symbol: str) -> dict:
    """
    Fetch live candles for `symbol` and run Kronos inference.
    Returns a dict with direction, change_pct, confidence, composite, zone, price.
    """
    if predictor is None:
        return {"direction": "hold", "change_pct": 0.0, "confidence": 0.0,
                "composite": 0.5, "zone": "neutral", "price": 0.0,
                "error": "model not loaded"}

    try:
        df = fetch_candles(symbol)
    except Exception as e:
        log.warning("  %s: candle fetch failed - %s", symbol, e)
        return {"direction": "hold", "change_pct": 0.0, "confidence": 0.0,
                "composite": 0.5, "zone": "neutral", "price": 0.0,
                "error": f"candle fetch failed: {e}"}

    if len(df) < 10:
        return {"direction": "hold", "change_pct": 0.0, "confidence": 0.0,
                "composite": 0.5, "zone": "neutral", "price": 0.0,
                "error": "insufficient candles"}

    try:
        current_close = float(df["close"].iloc[-1])

        x_timestamp = pd.Series(df.index)
        last_ts = df.index[-1]
        y_timestamp = pd.Series(pd.date_range(
            start=last_ts + pd.Timedelta(hours=4), periods=PRED_LEN, freq="4h"))

        pred_df = predictor.predict(
            df=df[["open", "high", "low", "close", "volume"]],
            x_timestamp=x_timestamp,
            y_timestamp=y_timestamp,
            pred_len=PRED_LEN,
            T=TEMPERATURE,
            top_p=TOP_P,
            sample_count=SAMPLE_COUNT,
            verbose=False,
        )

        # Average predicted close across the forecast horizon
        mean_pred_close = float(pred_df["close"].mean())
        change_pct = (mean_pred_close - current_close) / current_close * 100.0

        # Confidence proxy: inverse of predicted high-low range as % of price.
        # Tight predicted range -> model is more certain.
        pred_range_pct = (pred_df["high"].max() - pred_df["low"].min()) / current_close * 100.0
        confidence = max(0.0, min(1.0, 1.0 - (pred_range_pct / 20.0)))

        # composite: 0..1 sentiment score derived from predicted change.
        # 0.5 = neutral, >0.5 = bullish, <0.5 = bearish. +/-10% change saturates it.
        composite = max(0.0, min(1.0, 0.5 + change_pct / 20.0))
        if composite < 0.4:
            zone = "fear"
        elif composite > 0.6:
            zone = "greed"
        else:
            zone = "neutral"

        if change_pct >= BUY_THRESHOLD_PCT:
            direction = "buy"
        elif change_pct <= SELL_THRESHOLD_PCT:
            direction = "sell"
        else:
            direction = "hold"

        log.info("  %s: price=%.4f change=%+.2f%% dir=%s conf=%.2f zone=%s",
                 symbol, current_close, change_pct, direction, confidence, zone)

        return {
            "direction":  direction,
            "change_pct": round(change_pct, 4),
            "confidence": round(confidence, 4),
            "composite":  round(composite, 4),
            "zone":       zone,
            "price":      current_close,
        }

    except Exception as e:
        log.warning("  %s: inference failed - %s", symbol, e)
        log.warning(traceback.format_exc())
        return {"direction": "hold", "change_pct": 0.0, "confidence": 0.0,
                "composite": 0.5, "zone": "neutral", "price": 0.0,
                "error": str(e)}


# ── Routes ────────────────────────────────────────────────────────────────────

@app.route("/")  # Go bot probes this on startup
@app.route("/health")
def health():
    return jsonify({
        "status": "ready" if predictor is not None else "model_not_loaded",
        "model":  MODEL_NAME,
    })


@app.route("/predict/<symbol>")
def predict_single(symbol):
    """GET /predict/{symbol} -> single-symbol prediction using live candles."""
    result = predict_symbol(symbol)
    return jsonify({
        "symbol":     symbol,
        "price":      result.get("price", 0.0),
        "composite":  result.get("composite", 0.5),
        "zone":       result.get("zone", "neutral"),
        "direction":  result.get("direction", "hold"),
        "confidence": result.get("confidence", 0.0),
    })


@app.route("/predict_batch", methods=["POST"])
def predict_batch():
    """
    POST /predict_batch
    Body: {"symbols": ["BTC", "ETH"]}
    Response: [ {"symbol":"BTC","price":...,"composite":...,"zone":...,"direction":...,"confidence":...}, ... ]
    """
    body = request.get_json(force=True, silent=True)
    if not body or "symbols" not in body or not isinstance(body["symbols"], list):
        return jsonify({"error": "expected JSON body {\"symbols\": [\"BTC\", ...]}"}), 400

    symbols = body["symbols"]
    log.info("Batch predict: %d symbol(s)", len(symbols))
    results = []
    for sym in symbols:
        result = predict_symbol(sym)
        results.append({
            "symbol":     sym,
            "price":      result.get("price", 0.0),
            "composite":  result.get("composite", 0.5),
            "zone":       result.get("zone", "neutral"),
            "direction":  result.get("direction", "hold"),
            "confidence": result.get("confidence", 0.0),
        })
    return jsonify(results)


# ── Entry ─────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    load_model()
    log.info("Kronos service listening on http://localhost:8765")
    app.run(host="127.0.0.1", port=8765, debug=False, threaded=False)
