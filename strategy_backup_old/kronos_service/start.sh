#!/usr/bin/env bash
# Kronos AI Prediction Service — setup and start
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Clone Kronos repo if not present
if [ ! -d "Kronos" ]; then
    echo "[setup] Cloning Kronos repository..."
    git clone https://github.com/shiyu-coder/Kronos
fi

# Install Python dependencies
echo "[setup] Installing Python dependencies..."
pip install -q -r requirements.txt

echo "[start] Launching Kronos service on http://localhost:8765"
echo "[start] First run will download model weights (~50MB) from HuggingFace..."
python kronos_service.py
