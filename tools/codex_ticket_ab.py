#!/usr/bin/env python3
"""Opt-in, serial Codex connection experiment; stdout contains summaries only."""

import argparse
import base64
import binascii
import http.client
import json
import os
import re
import ssl
import sys
import time
import uuid
from dataclasses import dataclass, field
from http.cookies import CookieError, SimpleCookie
from urllib.parse import unquote, urlsplit

HOST = "chatgpt.com"
PATH = "/backend-api/codex/responses"
MAX_BODY = 1 << 20
COOKIE_NAMES = ("__cflb", "__oailb")


class ExperimentError(Exception):
    """Only fixed, non-secret reason codes may be used as messages."""


def load_account(path, index=None):
    with open(path, encoding="utf-8") as stream:
        doc = json.load(stream)
    if not isinstance(doc, dict):
        raise ExperimentError("invalid_account_document")
    if "accounts" in doc:
        accounts = doc["accounts"]
        if not isinstance(accounts, list) or not accounts:
            raise ExperimentError("empty_account_export")
        if index is None and len(accounts) != 1:
            raise ExperimentError("account_index_required")
        selected = 0 if index is None else index
        if selected < 0 or selected >= len(accounts):
            raise ExperimentError("invalid_account_index")
        doc = accounts[selected]
    elif index is not None:
        raise ExperimentError("account_index_requires_export")
    if not isinstance(doc, dict):
        raise ExperimentError("invalid_account_record")
    creds = doc.get("credentials", doc)
    if not isinstance(creds, dict):
        raise ExperimentError("invalid_credentials")
    token = creds.get("access_token")
    account = creds.get("chatgpt_account_id") or creds.get("account_id")
    for value in (token, account):
        if not isinstance(value, str) or not value.strip() or re.search(r"[\s\x00-\x1f\x7f]", value):
            raise ExperimentError("missing_or_invalid_credentials")
    return token, account


class SingleConnectHTTPS(http.client.HTTPSConnection):
    """A connection object must never silently replace its original socket."""

    def __init__(self, proxy, timeout):
        self.connect_count = 0
        self.connected_once = False
        headers = {}
        try:
            parsed = urlsplit(proxy)
            if parsed.scheme != "http" or not parsed.hostname or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
                raise ValueError
            port = parsed.port or 80
            if not 1 <= port <= 65535:
                raise ValueError
            if parsed.username is not None:
                userpass = unquote(parsed.username) + ":" + unquote(parsed.password or "")
                headers["Proxy-Authorization"] = "Basic " + base64.b64encode(userpass.encode()).decode()
        except ValueError:
            raise ExperimentError("invalid_http_proxy") from None
        super().__init__(parsed.hostname, port, timeout=timeout, context=ssl.create_default_context())
        self.set_tunnel(HOST, 443, headers=headers)

    def connect(self):
        if self.connected_once:
            raise ExperimentError("original_connection_lost")
        self.connected_once = True
        super().connect()
        self.connect_count += 1


def ticket_valid(state, length, now):
    if len(state) != length or not state.startswith("gAAAAA"):
        return False
    core = state.rstrip("=")
    if len(state) - len(core) > 2 or not re.fullmatch(r"[A-Za-z0-9_-]+", core):
        return False
    try:
        raw = base64.b64decode(core + "=" * (-len(core) % 4), altchars=b"-_", validate=True)
    except (ValueError, binascii.Error):
        return False
    blocks = {292: 10, 332: 12}[length]
    if len(raw) != 57 + 16 * blocks or raw[0] != 0x80:
        return False
    if base64.urlsafe_b64encode(raw).decode().rstrip("=") != core:
        return False
    issued = int.from_bytes(raw[1:9], "big")
    return 1577836800 <= issued < 4102444800 and -30 <= now - issued < 3570


def completed_response(raw, model):
    """Require explicit completed status and model; reject failed/truncated SSE."""
    try:
        text = raw.decode("utf-8", errors="strict").replace("\r\n", "\n")
        if text.lstrip().startswith("{"):
            events = [json.loads(text)]
        else:
            events, data = [], []
            for line in text.splitlines():
                if not line:
                    if data:
                        payload = "\n".join(data)
                        if payload != "[DONE]":
                            events.append(json.loads(payload))
                        data = []
                elif line.startswith("data:"):
                    data.append(line[5:].removeprefix(" "))
            if data:
                raise ExperimentError("unterminated_stream")
        completed = None
        for event in events:
            if not isinstance(event, dict):
                raise ExperimentError("invalid_response")
            response = event.get("response", event)
            if not isinstance(response, dict):
                raise ExperimentError("invalid_response")
            if event.get("type") in ("error", "response.failed", "response.incomplete"):
                raise ExperimentError("response_failed")
            for value in (event, response):
                if value.get("error") or value.get("status") in ("failed", "incomplete"):
                    raise ExperimentError("response_failed")
                if value.get("model") is not None and value["model"] != model:
                    raise ExperimentError("model_mismatch")
            if event.get("type") == "response.completed" or (not event.get("type") and event.get("status") == "completed"):
                if response.get("status") != "completed" or response.get("model") != model:
                    raise ExperimentError("completion_not_verified")
                completed = response
        if completed is None:
            raise ExperimentError("completion_missing")
        return completed
    except (ValueError, UnicodeError):
        raise ExperimentError("invalid_response") from None


@dataclass
class Reply:
    status: int = 0
    reason: str = ""
    state: str = field(default="", repr=False)
    cookies: str = field(default="", repr=False)
    response: dict = field(default_factory=dict, repr=False)

    def public(self):
        return {"http_status": self.status, "reason": self.reason,
                "completed_model_match": bool(self.response), "ticket_length": len(self.state),
                "cookie_pair": bool(self.cookies)}


def exchange(conn, headers, body, model):
    result = Reply()
    try:
        conn.request("POST", PATH, json.dumps(body).encode(), headers)
        resp = conn.getresponse()
        result.status = resp.status
        result.state = (resp.getheader("X-Codex-Turn-State") or "").strip()
        cookies = SimpleCookie()
        for line in resp.headers.get_all("Set-Cookie", []):
            cookies.load(line)
        if all(name in cookies and cookies[name].value for name in COOKIE_NAMES):
            result.cookies = "; ".join(name + "=" + cookies[name].coded_value for name in COOKIE_NAMES)
        raw = resp.read(MAX_BODY + 1)
        if len(raw) > MAX_BODY:
            raise ExperimentError("response_too_large")
        if resp.status != 200:
            raise ExperimentError("http_error")
        result.response = completed_response(raw, model)
    except ExperimentError as exc:
        result.reason = str(exc)
    except CookieError:
        result.reason = "invalid_cookie"
    except (OSError, http.client.HTTPException):
        result.reason = "transport_error"
    return result


def seed_request(model, scenario):
    tool = {"type": "function", "name": "connection_probe", "description": "Return a local diagnostic marker.",
            "parameters": {"type": "object", "properties": {}, "additionalProperties": False}}
    prompt = "Call connection_probe once. After its result, reply with OK." if scenario == "tool-continuation" else "Reply with OK. Do not call tools."
    return {"model": model, "instructions": prompt, "input": [{"role": "user", "content": prompt}],
            "tools": [tool], "tool_choice": {"type": "function", "name": "connection_probe"} if scenario == "tool-continuation" else "none",
            "stream": True, "store": False, "parallel_tool_calls": False,
            "include": ["reasoning.encrypted_content"]}


def followup_request(seed, response, scenario):
    body = dict(seed)
    body["tool_choice"] = "none"
    if scenario == "tool-continuation":
        output = response.get("output")
        if not isinstance(output, list) or any(not isinstance(item, dict) for item in output):
            raise ExperimentError("tool_output_missing")
        calls = [item for item in output if item.get("type") == "function_call"]
        if len(calls) != 1 or calls[0].get("name") != "connection_probe" or not isinstance(calls[0].get("call_id"), str) or not calls[0]["call_id"]:
            raise ExperimentError("tool_call_missing_or_unexpected")
        body["input"] = seed["input"] + output + [{"type": "function_call_output", "call_id": calls[0]["call_id"], "output": "local probe complete"}]
    return body


def identity_headers(token, account, client_version):
    return {"Authorization": "Bearer " + token, "ChatGPT-Account-ID": account,
            "Content-Type": "application/json", "Accept": "text/event-stream",
            "Originator": "codex_cli_rs", "User-Agent": "codex_cli_rs/" + client_version,
            "Version": client_version, "OpenAI-Beta": "responses_websockets=2026-02-06",
            "X-OpenAI-Internal-Codex-Responses-Lite": "true"}


def run_experiment(factory, headers, model, length, scenario, trials, emit):
    summary = {"kind": "summary", "trials": 0, "qualified_seeds": 0, "valid_pairs": 0,
               "same_connection_successes": 0, "new_connection_successes": 0, "stop": "budget_exhausted"}
    for trial in range(1, trials + 1):
        summary["trials"] += 1
        original = factory()
        try:
            request_headers = dict(headers)
            for key in ("session_id", "thread_id", "turn_id"):
                request_headers[key] = str(uuid.uuid4())
            seed_body = seed_request(model, scenario)
            started = time.monotonic()
            seed = exchange(original, request_headers, seed_body, model)
            qualified = not seed.reason and bool(seed.cookies) and ticket_valid(seed.state, length, time.time())
            emit({"kind": "seed", "trial": trial, **seed.public(), "qualified": qualified})
            if seed.status >= 400:
                summary["stop"] = "http_error"
                break
            if not qualified:
                continue
            try:
                body = followup_request(seed_body, seed.response, scenario)
            except ExperimentError as exc:
                emit({"kind": "skip", "trial": trial, "reason": str(exc)})
                continue
            summary["qualified_seeds"] += 1
            request_headers.update({"Cookie": seed.cookies, "X-Codex-Turn-State": seed.state})
            # Both arms send the original bundle. Updating it after the first arm
            # would change a second variable and prevent a paired comparison.
            order = ("same", "new") if trial % 2 else ("new", "same")
            pair = {}
            for position, arm in enumerate(order, 1):
                if time.monotonic() - started >= 240 or not ticket_valid(seed.state, length, time.time()):
                    emit({"kind": "skip", "trial": trial, "arm": arm, "reason": "seed_expired"})
                    break
                conn = original if arm == "same" else factory()
                try:
                    result = exchange(conn, request_headers, body, model)
                    verified = conn.connect_count == 1 and result.status != 0
                    acceptable_state = not result.state or ticket_valid(result.state, length, time.time())
                    success = verified and not result.reason and acceptable_state
                    pair[arm] = (verified, success)
                    emit({"kind": "arm", "trial": trial, "arm": arm, "position": position,
                          **result.public(), "transport_verified": verified,
                          "connections_opened": conn.connect_count,
                          "bundle_age_seconds": round(time.monotonic() - started, 3),
                          "returned_state_acceptable": acceptable_state, "success": success})
                    if result.status >= 400:
                        summary["stop"] = "http_error"
                        break
                finally:
                    if arm == "new":
                        conn.close()
            if len(pair) == 2 and all(value[0] for value in pair.values()):
                summary["valid_pairs"] += 1
                summary["same_connection_successes"] += int(pair["same"][1])
                summary["new_connection_successes"] += int(pair["new"][1])
            if summary["stop"] == "http_error":
                break
        finally:
            original.close()
    emit(summary)
    return summary


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--live", action="store_true", help="send real upstream requests (up to 3 per trial)")
    parser.add_argument("--account", required=True, help="local account JSON; contents are never printed")
    parser.add_argument("--account-index", type=int, help="zero-based index; required for multi-account exports")
    parser.add_argument("--proxy-env", default="CODEX_AB_PROXY", help="environment variable containing an HTTP CONNECT proxy URL")
    parser.add_argument("--model", default="gpt-6-astra", choices=("gpt-6-astra", "gpt-5.6-sol"))
    parser.add_argument("--ticket-length", type=int, choices=(292, 332), required=True)
    parser.add_argument("--scenario", choices=("tool-continuation", "message"), default="tool-continuation")
    parser.add_argument("--trials", type=int, default=2)
    parser.add_argument("--timeout", type=int, default=20, help="socket timeout in seconds")
    parser.add_argument("--client-version", default="0.155.0")
    args = parser.parse_args(argv)
    if not args.live:
        parser.error("real requests require --live; see tools/test_codex_ticket_ab.py for offline tests")
    if not 1 <= args.trials <= 10 or not 1 <= args.timeout <= 60 or not re.fullmatch(r"\d+\.\d+\.\d+", args.client_version):
        parser.error("invalid trials, timeout or client version")
    emit = lambda row: print(json.dumps(row, sort_keys=True), flush=True)
    try:
        token, account = load_account(args.account, args.account_index)
        proxy = os.environ.get(args.proxy_env, "")
        factory = lambda: SingleConnectHTTPS(proxy, args.timeout)
        # Validate the proxy before reporting the start of the experiment.
        factory().close()
        emit({"kind": "config", "model": args.model, "ticket_length": args.ticket_length,
              "scenario": args.scenario, "trials": args.trials, "client_version": args.client_version,
              "cookie_policy": "frozen_pair_240s", "egress_ip_verified": False})
        summary = run_experiment(factory, identity_headers(token, account, args.client_version),
                                 args.model, args.ticket_length, args.scenario, args.trials, emit)
        return 0 if summary["valid_pairs"] and summary["stop"] != "http_error" else 2
    except (ExperimentError, OSError, ValueError, http.client.HTTPException):
        # JSON decode errors, filenames, proxy addresses and upstream messages
        # can contain secrets; never render exception text or a traceback.
        emit({"kind": "error", "reason": "input_or_transport_error"})
        return 2
    except KeyboardInterrupt:
        emit({"kind": "error", "reason": "cancelled"})
        return 130


if __name__ == "__main__":
    sys.exit(main())
