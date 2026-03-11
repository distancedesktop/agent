#!/usr/bin/env python3
"""
Distance Agent — reads encoded frames from shared memory and relays them
to connected web clients over WebSocket.

Wire protocol (matches web/src/components/canvas/Canvas.tsx):
  CONFIG  0x01  [u8 type][u8 reserved][u16be width][u16be height][6x reserved][u8 codec][u8 nameLen][utf8 name]
  FRAME   0x02  [u8 type][u32le frameSize][bytes frame]
"""

import asyncio
import ctypes
import ctypes.util
import mmap
import os
import struct
import sys
import logging

import websockets
from websockets.asyncio.server import serve, ServerConnection

# ── Shared memory constants ───────────────────────────────────────────────────

SHM_NAME  = "/distance_frames"          # POSIX shm_open name (macOS + Linux)
SHM_MAGIC = 0xD157CAFE


def _shm_open(name: str) -> int:
    """
    Call POSIX shm_open(name, O_RDONLY, 0) via ctypes.
    On macOS the shm objects live in a kernel namespace, not /dev/shm,
    so we cannot use os.open() on a filesystem path.
    Returns a file descriptor on success, raises OSError on failure.
    """
    libc_name = ctypes.util.find_library("c")
    libc = ctypes.CDLL(libc_name, use_errno=True)
    libc.shm_open.restype = ctypes.c_int
    libc.shm_open.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.c_uint]

    O_RDONLY = 0x0000
    fd = libc.shm_open(name.encode(), O_RDONLY, 0)
    if fd < 0:
        errno = ctypes.get_errno()
        raise OSError(errno, os.strerror(errno), name)
    return fd

# struct shm_header_t { u32 magic, sequence, width, height, stride, codec, data_size; }
HEADER_FMT    = struct.Struct("<IIIIIII")  # 7 × uint32 little-endian = 28 bytes
HEADER_SIZE   = HEADER_FMT.size           # 28
FRAME_OFFSET  = HEADER_SIZE               # frame data starts immediately after

CODEC_NAMES = {0: "H.264", 1: "H.265"}
ENCODER_NAME = b"distance"

# ── WebSocket server constants ────────────────────────────────────────────────

WS_HOST = "0.0.0.0"
WS_PORT = 58472

# ── Global state ──────────────────────────────────────────────────────────────

clients: set[ServerConnection] = set()
ws_shm: mmap.mmap | None = None

log = logging.getLogger("distance-agent")


# ── Message builders ──────────────────────────────────────────────────────────

def build_config(width: int, height: int, codec: int) -> bytes:
    name_len = len(ENCODER_NAME)
    # >BxHH6xBB  = big-endian: u8, pad, u16, u16, 6 pad bytes, u8, u8
    header = struct.pack(">BxHH6xBB", 0x01, width, height, codec, name_len)
    return header + ENCODER_NAME


def build_frame(data: bytes) -> bytes:
    return struct.pack(">BI", 0x02, len(data)) + data


# ── Shared memory ─────────────────────────────────────────────────────────────

async def open_shm() -> mmap.mmap:
    """Open the shared memory segment; wait until the encoder creates it."""
    while True:
        try:
            fd = _shm_open(SHM_NAME)
            size = os.fstat(fd).st_size
            shm = mmap.mmap(fd, size, access=mmap.ACCESS_READ)
            os.close(fd)
            # Validate magic
            magic, = struct.unpack_from("<I", shm, 0)
            if magic == SHM_MAGIC:
                log.info("Opened shared memory '%s'", SHM_NAME)
                return shm
            shm.close()
            log.warning("Bad magic 0x%08X (expected 0x%08X), retrying…", magic, SHM_MAGIC)
        except OSError as e:
            log.info("Waiting for shared memory '%s'… (%s)", SHM_NAME, e)

        await asyncio.sleep(1)


def read_header(shm: mmap.mmap) -> tuple[int, int, int, int, int, int, int]:
    """Return (magic, sequence, width, height, stride, codec, data_size)."""
    shm.seek(0)
    return HEADER_FMT.unpack(shm.read(HEADER_SIZE))


def read_frame(shm: mmap.mmap, data_size: int) -> bytes:
    shm.seek(FRAME_OFFSET)
    return shm.read(data_size)


# ── WebSocket handler ─────────────────────────────────────────────────────────

async def handler(ws: ServerConnection) -> None:
    clients.add(ws)
    addr = ws.remote_address
    log.info("Client connected: %s", addr)
    try:
        # Send current config immediately (if encoder is already running)
        try:
            if ws_shm is not None:
                _, _, width, height, _, codec, _ = read_header(ws_shm)
                await ws.send(build_config(width, height, codec))
                log.debug("Sent CONFIG to %s (%dx%d codec=%s)", addr, width, height, CODEC_NAMES.get(codec, codec))
            else:
                log.info("Encoder not ready yet; CONFIG will be sent on first frame")
        except Exception as e:
            log.warning("Could not send CONFIG to %s: %s", addr, e)

        # Hold until disconnect
        await ws.wait_closed()
    finally:
        clients.discard(ws)
        log.info("Client disconnected: %s", addr)


# ── Frame polling loop ────────────────────────────────────────────────────────

async def poll_frames(shm: mmap.mmap) -> None:
    last_seq = 0

    while True:
        try:
            _, seq, width, height, _, codec, data_size = read_header(shm)
        except Exception as e:
            log.error("SHM read error: %s", e)
            await asyncio.sleep(0.1)
            continue

        if seq != last_seq and seq != 0 and data_size > 0:
            try:
                frame_bytes = read_frame(shm, data_size)
                msg = build_frame(frame_bytes)
                last_seq = seq

                if clients:
                    await asyncio.gather(
                        *[ws.send(msg) for ws in list(clients)],
                        return_exceptions=True,
                    )
            except Exception as e:
                log.warning("Frame send error: %s", e)

        # Yield to the event loop; encoder runs at ~60 fps so we don't need to sleep long
        await asyncio.sleep(0)


# ── Entry point ───────────────────────────────────────────────────────────────

async def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s",
    )

    log.info("Starting WebSocket server on ws://%s:%d", WS_HOST, WS_PORT)

    async with serve(handler, WS_HOST, WS_PORT) as server:
        log.info("Listening — waiting for clients and frames…")

        shm = await open_shm()
        global ws_shm
        ws_shm = shm

        await asyncio.gather(
            server.serve_forever(),
            poll_frames(shm),
        )


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        log.info("Shutting down.")
        sys.exit(0)
