"""router-shim — a transparent OpenAI-compatible sidecar for cli-proxy-api.

It forwards every request 1:1 to the upstream cli-proxy-api, with one tiny
transformation: it prefixes the assistant's reply with `[<actual-model>] `
so consumers can SEE which model the Fugu orchestrator picked, without
having to inspect the JSON `model` field.

Works for both:
  - /v1/chat/completions       (non-streaming JSON  -> patches choices[].message.content)
  - /v1/chat/completions stream (text/event-stream  -> patches the first content delta)
  - /v1/completions            (legacy completions  -> patches choices[].text)
  - everything else            (models, embeddings, images, management) passed through bytes-for-bytes

The shim is deliberately small. It does NOT touch:
  - request bodies (forwarded as-is, so all OpenAI params work)
  - headers other than Host / Content-Length (those are set by httpx)
  - the upstream `model` field (still reports the actual routed model)
  - non-chat endpoints (full passthrough)

Opt-out: send header `X-Router-Prefix: off` to disable the rewrite for one
request. Useful when an automated client breaks on the `[name]` prefix.

Env vars:
  UPSTREAM_BASE_URL   default http://cli-proxy-api:8317  -- where to forward
  LISTEN_HOST         default 0.0.0.0
  LISTEN_PORT         default 8000
"""

from __future__ import annotations

import contextlib
import json
import logging
import os
import re
from typing import AsyncIterator

import httpx
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import Response, StreamingResponse
from starlette.routing import Route

UPSTREAM_BASE_URL = os.environ.get("UPSTREAM_BASE_URL", "http://cli-proxy-api:8317").rstrip("/")
LISTEN_HOST = os.environ.get("LISTEN_HOST", "0.0.0.0")
LISTEN_PORT = int(os.environ.get("LISTEN_PORT", "8000"))

# Headers we strip before forwarding upstream (hop-by-hop + content-length,
# which httpx will recompute).
HOP_BY_HOP = {
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "te", "trailers", "transfer-encoding", "upgrade", "host", "content-length",
}

# Headers we strip from the upstream response before returning to the client.
# We MUST drop content-length when we rewrite the body (length changes).
RESPONSE_STRIP = {"content-length", "transfer-encoding", "connection"}

# Shared async HTTP client. timeout=None means "stream forever" — important
# for long-running chat completions and SSE streams.
client: httpx.AsyncClient | None = None

logger = logging.getLogger("router-shim")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")


def _filter_request_headers(headers) -> dict[str, str]:
    return {k: v for k, v in headers.items() if k.lower() not in HOP_BY_HOP}


def _filter_response_headers(headers) -> list[tuple[str, str]]:
    return [(k, v) for k, v in headers.items() if k.lower() not in RESPONSE_STRIP]


def _prefix_for(model: str | None) -> str:
    """Build the `[model] ` prefix; empty string when model is unknown."""
    return f"[{model}] " if model else ""


def _rewrite_chat_completion_json(payload: bytes) -> bytes:
    """Patch a non-streaming chat completion: prefix every choice's content."""
    try:
        body = json.loads(payload)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return payload  # upstream returned non-JSON (error?), pass through

    model = body.get("model")
    prefix = _prefix_for(model)
    if not prefix:
        return payload

    choices = body.get("choices") or []
    for choice in choices:
        msg = choice.get("message")
        if isinstance(msg, dict):
            content = msg.get("content")
            if isinstance(content, str):
                msg["content"] = prefix + content
            elif isinstance(content, list):
                # Vision / multi-part content: prefix the first text part.
                for part in content:
                    if isinstance(part, dict) and part.get("type") == "text" and isinstance(part.get("text"), str):
                        part["text"] = prefix + part["text"]
                        break

        # Legacy completions style (/v1/completions) — has `text` not `message`.
        if isinstance(choice.get("text"), str):
            choice["text"] = prefix + choice["text"]

    return json.dumps(body, ensure_ascii=False).encode("utf-8")


async def _stream_with_prefix(
    upstream: AsyncIterator[bytes],
    is_legacy_completions: bool,
) -> AsyncIterator[bytes]:
    """Patch the first content delta of an OpenAI SSE stream with `[model] `.

    The OpenAI SSE wire format is a sequence of `data: <json>\\n\\n` events.
    Every event carries `model` and a `delta` (chat) or `text` (legacy).
    We:
      1. parse each event,
      2. on the FIRST event whose delta has non-empty content, prepend the
         prefix to that content,
      3. write the (possibly modified) event back out.
    Untouched bytes (comments, keep-alive blank lines) pass through.
    """
    buf = b""
    prefixed = False

    async for chunk in upstream:
        if not chunk:
            continue
        buf += chunk

        # SSE events are separated by blank lines (\n\n). Process complete
        # events; leave the trailing partial event in the buffer.
        while b"\n\n" in buf:
            event, buf = buf.split(b"\n\n", 1)
            event_text = event.decode("utf-8", errors="replace")

            if prefixed or not event_text.startswith("data:"):
                # Either we've already prefixed, or this is a comment / keep-alive — pass through.
                yield event + b"\n\n"
                continue

            # Parse the data: portion(s) of this event. SSE allows multiple
            # data: lines per event — concatenate them.
            data_lines = [ln[len("data:"):].lstrip() for ln in event_text.splitlines() if ln.startswith("data:")]
            data_str = "\n".join(data_lines)

            if data_str.strip() == "[DONE]":
                yield event + b"\n\n"
                continue

            try:
                obj = json.loads(data_str)
            except json.JSONDecodeError:
                yield event + b"\n\n"
                continue

            model = obj.get("model")
            prefix = _prefix_for(model)
            if not prefix:
                yield event + b"\n\n"
                continue

            choices = obj.get("choices") or []
            for choice in choices:
                if is_legacy_completions:
                    txt = choice.get("text")
                    if isinstance(txt, str) and txt:
                        choice["text"] = prefix + txt
                        prefixed = True
                        break
                else:
                    delta = choice.get("delta") or {}
                    content = delta.get("content")
                    if isinstance(content, str) and content:
                        delta["content"] = prefix + content
                        prefixed = True
                        break
                    if isinstance(content, list):
                        for part in content:
                            if isinstance(part, dict) and part.get("type") == "text" and isinstance(part.get("text"), str) and part["text"]:
                                part["text"] = prefix + part["text"]
                                prefixed = True
                                break
                        if prefixed:
                            break

            if prefixed:
                new_event = f"data: {json.dumps(obj, ensure_ascii=False)}".encode("utf-8")
                yield new_event + b"\n\n"
            else:
                yield event + b"\n\n"

    # Flush any trailing bytes.
    if buf:
        yield buf


def _should_rewrite(path: str, opt_out: str | None) -> tuple[bool, bool]:
    """Return (rewrite?, is_legacy_completions?)."""
    if (opt_out or "").lower() in ("off", "false", "0", "no"):
        return (False, False)
    # Match /v1/chat/completions, /v1/responses (just in case), and /v1/completions.
    if re.fullmatch(r"/v\d+/chat/completions/?", path):
        return (True, False)
    if re.fullmatch(r"/v\d+/completions/?", path):
        return (True, True)
    return (False, False)


async def proxy(request: Request) -> Response:
    assert client is not None
    raw_path = request.url.path
    if request.url.query:
        target = f"{UPSTREAM_BASE_URL}{raw_path}?{request.url.query}"
    else:
        target = f"{UPSTREAM_BASE_URL}{raw_path}"

    body = await request.body()
    forward_headers = _filter_request_headers(request.headers)
    opt_out = request.headers.get("x-router-prefix")
    rewrite, is_legacy = _should_rewrite(raw_path, opt_out)

    # Determine streaming intent by inspecting the request body JSON.
    is_streaming = False
    if rewrite and body:
        try:
            req_obj = json.loads(body)
            is_streaming = bool(req_obj.get("stream"))
        except json.JSONDecodeError:
            pass

    upstream_req = client.build_request(
        request.method,
        target,
        headers=forward_headers,
        content=body if body else None,
    )

    if is_streaming and rewrite:
        upstream_resp = await client.send(upstream_req, stream=True)
        out_headers = _filter_response_headers(upstream_resp.headers)
        # Force text/event-stream content-type if upstream already had it (shouldn't drift).
        body_iter = _stream_with_prefix(
            upstream_resp.aiter_raw(),
            is_legacy_completions=is_legacy,
        )

        async def _aclose(stream):
            try:
                async for chunk in stream:
                    yield chunk
            finally:
                await upstream_resp.aclose()

        return StreamingResponse(
            _aclose(body_iter),
            status_code=upstream_resp.status_code,
            headers=dict(out_headers),
            media_type=upstream_resp.headers.get("content-type", "text/event-stream"),
        )

    # Non-streaming path: read the whole body, maybe rewrite, return.
    upstream_resp = await client.send(upstream_req)
    payload = upstream_resp.content
    if rewrite:
        payload = _rewrite_chat_completion_json(payload)

    out_headers = _filter_response_headers(upstream_resp.headers)
    return Response(
        content=payload,
        status_code=upstream_resp.status_code,
        headers=dict(out_headers),
        media_type=upstream_resp.headers.get("content-type"),
    )


async def health(_: Request) -> Response:
    """Local health probe — does NOT hit upstream, used by Docker."""
    return Response(b'{"status":"ok"}', media_type="application/json")


@contextlib.asynccontextmanager
async def lifespan(_app: Starlette):
    """Open/close the shared httpx client across the app lifetime."""
    global client
    client = httpx.AsyncClient(timeout=httpx.Timeout(None, connect=30.0))
    logger.info("router-shim ready, upstream=%s", UPSTREAM_BASE_URL)
    try:
        yield
    finally:
        if client is not None:
            await client.aclose()


# Route ordering: health first (specific), then catch-all proxy.
routes = [
    Route("/_shim/health", health, methods=["GET"]),
    Route("/{path:path}", proxy, methods=["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"]),
]

app = Starlette(
    debug=False,
    routes=routes,
    lifespan=lifespan,
)


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=LISTEN_HOST, port=LISTEN_PORT, log_level="info", access_log=False)
