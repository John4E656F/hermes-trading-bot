#!/bin/bash
# Hermes bot hourly scan — fast mode
cd /home/hermes/hermes-trading-bot || exit 1

# Run in scan mode with top13 (fast enough to complete under timeout)
timeout 300 ./hermes-bot --watchlist=top13 --mode=scan 2>&1

# Always exit 0 — even if timeout kills it, the partial output is still useful
exit 0