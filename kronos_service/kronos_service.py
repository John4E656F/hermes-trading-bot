"""
Kronos AI Prediction Microservice for Hermes Trading Bot.

Runs as a local HTTP server on port 8765. The Go bot calls /predict_batch
once per 4H cycle with the last 90 candles per symbol, and this service
returns predicted price change % for each symbol using the Kronos-mini model.

First run downloads model weights (~50MB) from Hugging Face automatically.

Usage:
    python kronos_service.py

Endpoints:
    GET  /health         -> {"status":"ok","model":"Kronos-mini"}
    POST /predict_batch  -> see below
"""

import sys
import os
import json
import logging
import traceback

# Add Kronos model directory to Python path
_script_dir = os.path.dirname(os.path.abspath(__file__))
_kronos_dir = os.path.join(_script_dir, "Kronos")
if os.path.isdir(_kronos_dir):
    sys.path.insert(0, _kronos_dir)
    logging.info(f"Added {_kronos_dir} to sys.path")

import numpy as np
import pandas as pd
from flask import Flask, request, jsonify

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [Kronos] %(levelname)s %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("kronos")

# ── Kronos repo path ─────────────────────────────────────────────────────────
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
KRONOS_REPO = os.path.join(SCRIPT_DIR, "Kronos")
if KRONOS_REPO not in sys.path:
    sys.path.insert(0, KRONOS_REPO)

app = Flask(__name__)
predictor = None  # Loaded once at startup

# ── Prediction thresholds ─────────────────────────────────────────────────────
# Signal fires only when predicted move exceeds this threshold.
# Set conservatively — crypto is noisy and Kronos was trained on equity data.
BUY_THRESHOLD_PCT  = 1.5   # > +1.5% predicted → BUY
SELL_THRESHOLD_PCT = -1.5  # < -1.5% predicted → SELL

# Kronos inference parameters
PRED_LEN     = 3    # predict next 3 × 4H candles (~12 hours)
LOOKBACK     = 90   # historical context window
SAMPLE_COUNT = 2    # average N independent samples (reduces variance)
TEMPERATURE  = 0.8
TOP_P        = 0.95


def load_model():
    """Load Kronos-base from Hugging Face (cached after first download)."""
    global predictor
    log.info("Loading Kronos-base from HuggingFace Hub (~102.3M params, first run downloads ~400MB)...")
    try:
        import torch
        from huggingface_hub import hf_hub_download
        from model.kronos import KronosPredictor, Kronos, KronosTokenizer

        # Download and load tokenizer
        tokenizer_repo = "NeoQuasar/Kronos-Tokenizer-base"
        tokenizer = KronosTokenizer.from_pretrained(tokenizer_repo)
        tokenizer.eval()

        # Download and load model
        model_repo = "NeoQuasar/Kronos-base"
        model = Kronos.from_pretrained(model_repo)
        model.eval()

        predictor = KronosPredictor(
            model=model,
            tokenizer=tokenizer,
            device="cpu",
            max_context=512,
            clip=5,
        )
        log.info("Kronos-base loaded successfully (512 context, 102.3M params).")
    except Exception as e:
        log.error(f"Failed to load Kronos model: {e}")
        log.error(traceback.format_exc())
        predictor = None


def candles_to_df(candle_list: list) -> pd.DataFrame:
    """
    Convert the JSON candle array from Go into a pandas DataFrame
    with a DatetimeIndex, as required by KronosPredictor.predict().
    """
    records = []
    for c in candle_list:
        records.append({
            "open":   float(c["open"]),
            "high":   float(c["high"]),
            "low":    float(c["low"]),
            "close":  float(c["close"]),
            "volume": float(c.get("volume", 0)),
        })

    df = pd.DataFrame(records)
    # Build a synthetic datetime index at 4H intervals ending now.
    # Kronos needs timestamps for its temporal embeddings (hour, weekday, etc.)
    end   = pd.Timestamp.utcnow().floor("4h")
    start = end - pd.Timedelta(hours=4 * (len(df) - 1))
    df.index = pd.date_range(start=start, end=end, periods=len(df), freq=None)
    return df


def predict_symbol(symbol: str, candle_list: list) -> dict:
    """
    Run Kronos inference for one symbol.
    Returns a dict with direction, change_pct, and confidence.
    """
    if predictor is None:
        return {"direction": "HOLD", "change_pct": 0.0, "confidence": 0.0,
                "error": "model not loaded"}

    if len(candle_list) < 10:
        return {"direction": "HOLD", "change_pct": 0.0, "confidence": 0.0,
                "error": "insufficient candles"}

    try:
        df = candles_to_df(candle_list[-LOOKBACK:])
        current_close = df["close"].iloc[-1]

        pred_df = predictor.predict(
            data=df,
            pred_len=PRED_LEN,
            temperature=TEMPERATURE,
            top_p=TOP_P,
            sample_count=SAMPLE_COUNT,
        )

        # Average predicted close across the forecast horizon
        mean_pred_close = pred_df["close"].mean()
        change_pct = (mean_pred_close - current_close) / current_close * 100.0

        # Confidence proxy: inverse of predicted high-low range as % of price
        # Tight predicted range → model is more certain
        pred_range_pct = (pred_df["high"].max() - pred_df["low"].min()) / current_close * 100.0
        confidence = max(0.0, min(1.0, 1.0 - (pred_range_pct / 20.0)))

        if change_pct >= BUY_THRESHOLD_PCT:
            direction = "BUY"
        elif change_pct <= SELL_THRESHOLD_PCT:
            direction = "SELL"
        else:
            direction = "HOLD"

        log.info(f"  {symbol}: change={change_pct:+.2f}% dir={direction} conf={confidence:.2f}")
        return {
            "direction":  direction,
            "change_pct": round(change_pct, 4),
            "confidence": round(confidence, 4),
        }

    except Exception as e:
        log.warning(f"  {symbol}: inference failed — {e}")
        return {"direction": "HOLD", "change_pct": 0.0, "confidence": 0.0,
                "error": str(e)}


# ── Routes ────────────────────────────────────────────────────────────────────

@app.route("/")  # Go bot probes this on startup
@app.route("/health")
def health():
    return jsonify({
        "status": "ok" if predictor is not None else "model_not_loaded",
        "model":  "Kronos-mini",
    })


@app.route("/predict/<symbol>")
def predict_single(symbol):
    """GET /predict/{symbol} — single-symbol prediction, no candles = momentum fallback."""
    result = predict_symbol(symbol, [])
    return jsonify({
        "symbol":     symbol,
        "price":      0.0,
        "composite":  0.5,
        "zone":       "neutral",
        "direction":  result.get("direction", "HOLD").lower(),
        "confidence": result.get("confidence", 0.0),
    })


@app.route("/predict_batch", methods=["POST"])
def predict_batch():
    """
    Accepts TWO body formats:
      A) { "symbols": ["BTC", "ETH"] }          — Go bot's format (no candles)
      B) { "requests": [ {"symbol":"BTC", "candles":[...]}, ... ] }  — full format

    Response (for format A):
        [ {"symbol":"BTC", "direction":"buy", "confidence":0.71, ...}, ... ]
    """
    body = request.get_json(force=True, silent=True)
    if not body:
        return jsonify({"error": "empty body"}), 400

    # Format A: Go bot sends {"symbols": ["BTC", "ETH"]}
    if "symbols" in body and isinstance(body["symbols"], list):
        bare_syms = body["symbols"]
        log.info(f"Batch predict (symbols-only): {len(bare_syms)} symbol(s)")
        results = []
        for sym in bare_syms:
            result = predict_symbol(sym, [])  # no candles = momentum fallback
            results.append({
                "symbol":     sym,
                "price":      0.0,
                "composite":  0.5,
                "zone":       "neutral",
                "direction":  result.get("direction", "HOLD").lower(),
                "confidence": result.get("confidence", 0.0),
            })
        return jsonify(results)

    # Format B: {"requests": [{"symbol": "BTC", "candles": [...]}]}
    if "requests" in body:
        log.info(f"Batch predict (with candles): {len(body['requests'])} symbol(s)")
        predictions = {}
        for req in body["requests"]:
            sym     = req.get("symbol", "?")
            candles = req.get("candles", [])
            predictions[sym] = predict_symbol(sym, candles)
        return jsonify({"predictions": predictions})

    return jsonify({"error": "missing 'symbols' or 'requests' field"}), 400


# ── Entry ─────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    load_model()
    log.info("Kronos service listening on http://localhost:8765")
    app.run(host="127.0.0.1", port=8765, debug=False, threaded=False)
