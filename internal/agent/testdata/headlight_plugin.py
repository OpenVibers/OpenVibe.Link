#!/usr/bin/env python3
"""A tiny headlight driver for the Agent tests: describes the headlight kind, records each command line it receives
(config.record), answers with the LOOI accepted-not-confirmed data, and reports p7_headlight telemetry."""
import json
import sys

record = None


def send(obj):
    print(json.dumps(obj, separators=(",", ":")), flush=True)


def note(line):
    if not record:
        return
    with open(record, "a") as f:
        f.write(json.dumps(line, separators=(",", ":")) + "\n")


for raw in sys.stdin:
    try:
        msg = json.loads(raw)
    except ValueError:
        continue
    op = msg.get("op")
    if op == "hello":
        config = msg.get("config") or {}
        record = config.get("record")
        send({"op": "describe", "driver": "headlight", "version": "1", "capabilities": {"headlight": {}}})
        if msg.get("probe"):
            continue
        send({"op": "ready"})
        send({"op": "telemetry", "sensors": {"p7_headlight": {
            "commanded": None, "pending": False, "physical_state_verified": False}}})
    elif op == "command":
        on = (msg.get("value") or {}).get("on")
        note({"id": msg.get("id"), "kind": msg.get("kind"), "value": msg.get("value"),
              "deadline_ms": msg.get("deadline_ms"), "expires_at_mono_ns": msg.get("expires_at_mono_ns"),
              "operator": msg.get("operator"), "turn_epoch": msg.get("turn_epoch"),
              "provenance": msg.get("provenance")})
        send({"op": "ack", "id": msg.get("id"),
              "data": {"stage": "headlight_pending", "admitted": True}})
        send({"op": "telemetry", "sensors": {"p7_headlight": {
            "commanded": on, "pending": True, "physical_state_verified": False}}})
