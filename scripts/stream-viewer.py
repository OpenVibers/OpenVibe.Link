#!/usr/bin/env python3
"""Headless viewer for the Agent Streams S1 latency probe.

It joins a WebRTC stream over WHEP (the design's ``openvibe-node stream --probe`` publishes a QR of the millisecond
clock it drew each frame), decodes that QR from every video frame, and reports the glass-to-glass latency:
``now_ms - clock_in_the_QR``. The pass line is p95 under 1 s over 60 s.

    scripts/stream-viewer.sh --whep https://openvibe.live/whep/agent/ags_… --seconds 60

The view ticket is a secret: it comes from OPENVIBE_VIEW_TICKET (or ``--ticket-stdin``), never from argv. A WHEP
endpoint is what this needs; Live's S1 player speaks mediasoup over /ws/broadcast, so until Live exposes WHEP the
number is measured through a relay that does (the design allows a later WHEP endpoint for third-party players).

Requires aiortc and OpenCV (scripts/stream-viewer.sh puts them in a cache venv). ``--self-test`` checks the statistics
without a network.
"""
import argparse
import asyncio
import json
import os
import sys
import time

PASS_P95_MS = 1000.0


class LatencyReport:
    def __init__(self):
        self.samples = []

    def add(self, sent_ms, now_ms=None):
        now_ms = time.time() * 1000.0 if now_ms is None else now_ms
        delta = now_ms - sent_ms
        if 0 <= delta <= 60_000:
            self.samples.append(delta)
            return delta
        return None

    def summary(self):
        if not self.samples:
            return {"frames": 0, "p50_ms": None, "p95_ms": None, "max_ms": None}
        s = sorted(self.samples)
        def pct(p):
            return s[min(len(s) - 1, int(round(p / 100.0 * (len(s) - 1))))]
        return {"frames": len(s), "p50_ms": round(pct(50), 1), "p95_ms": round(pct(95), 1),
                "max_ms": round(s[-1], 1)}


def self_test():
    r = LatencyReport()
    for ms in range(1, 101):
        r.add(0, ms)
    got = r.summary()
    assert got["frames"] == 100 and got["p95_ms"] == 95.0, got
    assert r.add(10_000, 1_000) is None, "a QR from the future is not a sample"
    try:
        import cv2  # noqa: F401
        import numpy  # noqa: F401
    except ImportError as e:
        print("self-test: statistics ok, QR decoder missing: %s" % e)
        return 1
    print("self-test: statistics and QR decoder ok")
    return 0


async def run_whep(args):
    try:
        import cv2
        import numpy as np
        from aiortc import RTCPeerConnection, RTCSessionDescription
    except ImportError as e:
        print("missing a viewer dependency (%s): run scripts/stream-viewer.sh" % e, file=sys.stderr)
        return 2
    import aiohttp

    ticket = args.ticket
    headers = {"Content-Type": "application/sdp", "Accept": "application/sdp"}
    if ticket:
        headers["Authorization"] = "Bearer " + ticket

    pc = RTCPeerConnection()
    report = LatencyReport()
    decoder = cv2.QRCodeDetector()
    seen = {"frames": 0, "qr": 0}

    @pc.on("track")
    def on_track(track):
        if track.kind != "video":
            return

        async def consume():
            while True:
                try:
                    frame = await track.recv()
                except Exception:
                    return
                seen["frames"] += 1
                try:
                    img = frame.to_ndarray(format="bgr24")
                except Exception:
                    continue
                # detectAndDecode returns (decoded_info, points, straight_qrcode): points is an array, so test the string.
                decoded, *_ = decoder.detectAndDecode(img)
                if not decoded:
                    continue
                try:
                    sent = int(str(decoded).strip())
                except ValueError:
                    continue
                seen["qr"] += 1
                report.add(sent)
        asyncio.ensure_future(consume())

    pc.addTransceiver("video", direction="recvonly")
    offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    # Wait for the ICE gathering to finish (a few seconds), then send the complete offer.
    deadline = time.monotonic() + 10
    while pc.iceGatheringState != "complete" and time.monotonic() < deadline:
        await asyncio.sleep(0.1)
    async with aiohttp.ClientSession() as session:
        async with session.post(args.whep, data=pc.localDescription.sdp, headers=headers) as resp:
            body = await resp.text()
            if resp.status not in (200, 201):
                print("WHEP answered HTTP %d: %s" % (resp.status, body[:200]), file=sys.stderr)
                await pc.close()
                return 3
    await pc.setRemoteDescription(RTCSessionDescription(body, "answer"))
    print("viewer: joined %s; measuring for %ds" % (args.whep, args.seconds), file=sys.stderr)
    try:
        await asyncio.sleep(args.seconds)
    finally:
        await pc.close()
    out = report.summary()
    out.update({"seconds": args.seconds, "video_frames": seen["frames"], "qr_frames": seen["qr"],
                "p95_under_1s": out["p95_ms"] is not None and out["p95_ms"] < args.max_p95_ms})
    if args.json:
        print(json.dumps(out))
    else:
        if out["frames"] == 0:
            print("no QR frame decoded in %ds: is --probe publishing to this stream?" % args.seconds, file=sys.stderr)
        else:
            print("glass-to-glass latency over %ds: p50 %.1f ms, p95 %.1f ms, max %.1f ms (%d frames)" %
                  (args.seconds, out["p50_ms"], out["p95_ms"], out["max_ms"], out["frames"]))
            print("S1 pass line: p95 under %.0f ms → %s" % (args.max_p95_ms, "PASS" if out["p95_under_1s"] else "FAIL"))
    return 0 if out["p95_under_1s"] else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--whep", help="the WHEP endpoint of the stream")
    ap.add_argument("--seconds", type=int, default=60)
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--self-test", action="store_true")
    ap.add_argument("--max-p95-ms", type=float, default=PASS_P95_MS)
    ap.add_argument("--ticket-stdin", action="store_true", help="read the view ticket from one line of stdin")
    args = ap.parse_args()
    if args.self_test:
        return self_test()
    if not args.whep:
        ap.error("--whep is required (or --self-test)")
    args.ticket = os.environ.get("OPENVIBE_VIEW_TICKET", "")
    if not args.ticket and args.ticket_stdin:
        args.ticket = sys.stdin.readline().strip()
    return asyncio.run(run_whep(args))


if __name__ == "__main__":
    sys.exit(main())
