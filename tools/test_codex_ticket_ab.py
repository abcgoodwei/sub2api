import base64
import contextlib
import io
import json
import select
import socket
import socketserver
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import mock_open, patch

import codex_ticket_ab as ab


MODEL = "gpt-6-astra"


def ticket(blocks=10, issued=None):
    raw = b"\x80" + int(time.time() if issued is None else issued).to_bytes(8, "big")
    return base64.urlsafe_b64encode(raw + b"\0" * (57 + blocks * 16 - len(raw))).decode()


def event(response, kind="response.completed"):
    return "data: " + json.dumps({"type": kind, "response": response}) + "\n\n"


def completion(model=MODEL):
    return {"status": "completed", "model": model, "output": []}


class ValidationTests(unittest.TestCase):
    def test_reject_failed_truncated_missing_or_wrong_model(self):
        created = event({"model": MODEL, "status": "in_progress"}, "response.created")
        cases = [created, created + event({"status": "failed"}, "response.failed"),
                 event(completion("gpt-5.6-luna")), event(completion()).rstrip(),
                 "data: [DONE]\n\n", event({"status": "completed"}),
                 event({"status": "incomplete", "model": MODEL}), "data: []\n\n",
                 event(completion()) + "data: invalid\n\n"]
        for body in cases:
            with self.subTest(body=body), self.assertRaises(ab.ExperimentError):
                ab.completed_response(body.encode(), MODEL)
        self.assertEqual(ab.completed_response((created + event(completion())).encode(), MODEL), completion())
        self.assertEqual(ab.completed_response(json.dumps(completion()).encode(), MODEL), completion())

    def test_ticket_shape_and_freshness(self):
        now = int(time.time())
        self.assertTrue(ab.ticket_valid(ticket(10, now), 292, now))
        self.assertTrue(ab.ticket_valid(ticket(12, now), 332, now))
        for value, length in [("x" * 292, 292), (ticket(12, now), 292), (ticket(11, now), 292),
                              (ticket(10, now - 3570), 292), (ticket(10, now + 31), 292)]:
            self.assertFalse(ab.ticket_valid(value, length, now))

    def test_account_selection_never_combines_records(self):
        data = {"accounts": [{"credentials": {"access_token": "token_A"}},
                             {"credentials": {"access_token": "token_B", "chatgpt_account_id": "id_B"}}]}
        with patch("builtins.open", mock_open(read_data=json.dumps(data))):
            with self.assertRaisesRegex(ab.ExperimentError, "account_index_required"):
                ab.load_account("unused")
            with self.assertRaisesRegex(ab.ExperimentError, "missing_or_invalid_credentials"):
                ab.load_account("unused", 0)
            self.assertEqual(ab.load_account("unused", 1), ("token_B", "id_B"))
            with self.assertRaises(ab.ExperimentError):
                ab.load_account("unused", -1)

    def test_account_shapes(self):
        creds = {"access_token": "dummy", "account_id": "account"}
        for data in (creds, {"credentials": creds}, {"accounts": [{"credentials": creds}]}):
            with patch("builtins.open", mock_open(read_data=json.dumps(data))):
                self.assertEqual(ab.load_account("unused"), ("dummy", "account"))

    def test_tool_context_is_preserved(self):
        seed = ab.seed_request(MODEL, "tool-continuation")
        response = completion()
        response["output"] = [{"type": "reasoning", "encrypted_content": "mock-encrypted"},
                              {"type": "function_call", "name": "connection_probe", "call_id": "call-1", "arguments": "{}"}]
        body = ab.followup_request(seed, response, "tool-continuation")
        self.assertEqual(body["input"][:-1], seed["input"] + response["output"])
        self.assertEqual(body["input"][-1]["call_id"], "call-1")
        self.assertEqual(body["tool_choice"], "none")
        with self.assertRaises(ab.ExperimentError):
            ab.followup_request(seed, completion(), "tool-continuation")

    def test_cli_requires_live_and_redacts_input_errors(self):
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            with self.assertRaises(SystemExit) as exit_result:
                ab.main(["--account", "unused", "--ticket-length", "292"])
            self.assertEqual(exit_result.exception.code, 2)
            with patch("builtins.open", side_effect=OSError("SECRET_FILENAME")):
                self.assertEqual(ab.main(["--live", "--account", "unused", "--ticket-length", "292"]), 2)
        self.assertNotIn("SECRET_FILENAME", stdout.getvalue() + stderr.getvalue())

    def test_proxy_validation_and_auth_redaction(self):
        for proxy in ("", "socks5://localhost:80", "http://localhost:99999", "http://localhost/path?secret=x"):
            with self.assertRaisesRegex(ab.ExperimentError, "invalid_http_proxy"):
                ab.SingleConnectHTTPS(proxy, 1)
        reply = ab.Reply(state="SECRET_STATE", cookies="SECRET_COOKIE", response={"output": "SECRET_OUTPUT"})
        self.assertNotIn("SECRET", repr(reply))
        self.assertNotIn("SECRET", json.dumps(reply.public()))


class Origin(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        with self.server.lock:
            self.server.connection_count += 1
            self.connection_number = self.server.connection_count

    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        is_seed = not self.headers.get("X-Codex-Turn-State")
        self.server.seen.append((self.connection_number, dict(self.headers), body))
        response = completion()
        if is_seed and body["tool_choice"] != "none":
            response["output"] = [{"type": "reasoning", "encrypted_content": "test-reasoning"},
                                  {"type": "function_call", "name": "connection_probe", "call_id": "test-call", "arguments": "{}"}]
        status, state = 200, ticket()
        if is_seed and self.server.mode == "bad_seed":
            response["model"] = "gpt-5.6-luna"
        if is_seed and self.server.mode == "no_cookies":
            pass
        elif not is_seed and self.server.mode == "http_error":
            status = 429
        elif not is_seed and self.server.mode == "http_500":
            status = 500
        elif not is_seed and self.server.mode == "degraded_state":
            state = ticket(11)
        raw = event(response)
        if not is_seed and self.server.mode == "failed_stream":
            raw = event({"model": MODEL, "status": "in_progress"}, "response.created") + event({"status": "failed"}, "response.failed")
        if not is_seed and self.server.mode == "oversized":
            raw += " " * (ab.MAX_BODY + 1)
        payload = raw.encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("X-Codex-Turn-State", state)
        if self.server.mode != "no_cookies":
            self.send_header("Set-Cookie", "__cflb=" + ("seed-cookie" if is_seed else "new-cookie") + "; Path=/")
            self.send_header("Set-Cookie", "__oailb=test-cookie; Path=/")
        if is_seed and self.server.mode == "close_seed":
            self.send_header("Connection", "close")
            self.close_connection = True
        self.end_headers()
        self.wfile.write(payload)


class ConnectProxy(socketserver.StreamRequestHandler):
    def handle(self):
        request = self.rfile.readline().decode().strip()
        headers = {}
        while True:
            line = self.rfile.readline().decode().strip()
            if not line:
                break
            key, value = line.split(":", 1)
            headers[key.lower()] = value.strip()
        self.server.seen.append((request, headers))
        with socket.create_connection(self.server.origin, timeout=2) as upstream:
            self.wfile.write(b"HTTP/1.1 200 Connection established\r\n\r\n")
            self.wfile.flush()
            try:
                while True:
                    ready, _, _ = select.select([upstream, self.connection], [], [], 2)
                    if not ready:
                        return
                    for source in ready:
                        data = source.recv(65536)
                        if not data:
                            return
                        (upstream if source is self.connection else self.connection).sendall(data)
            except OSError:
                return


class ProxyServer(socketserver.ThreadingTCPServer):
    daemon_threads = True


class TransportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory()
        cls.cert = str(Path(cls.directory.name) / "cert.pem")
        cls.key = str(Path(cls.directory.name) / "key.pem")
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                        "-subj", "/CN=chatgpt.com", "-addext", "subjectAltName=DNS:chatgpt.com",
                        "-keyout", cls.key, "-out", cls.cert], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def setUp(self):
        self.origin = ThreadingHTTPServer(("127.0.0.1", 0), Origin)
        self.origin.seen, self.origin.connection_count = [], 0
        self.origin.lock, self.origin.mode = threading.Lock(), "ok"
        server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server_context.load_cert_chain(self.cert, self.key)
        self.origin.socket = server_context.wrap_socket(self.origin.socket, server_side=True)
        self.proxy = ProxyServer(("127.0.0.1", 0), ConnectProxy)
        self.proxy.origin, self.proxy.seen = self.origin.server_address, []
        for server in (self.origin, self.proxy):
            threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()
        self.context = ssl.create_default_context(cafile=self.cert)

    def tearDown(self):
        for server in (self.proxy, self.origin):
            server.shutdown()
            server.server_close()

    def run_trial(self, trials=1, scenario="tool-continuation"):
        rows = []
        proxy_url = "http://mock-user:mock-password@127.0.0.1:" + str(self.proxy.server_address[1])
        with patch.object(ab.ssl, "create_default_context", return_value=self.context):
            summary = ab.run_experiment(lambda: ab.SingleConnectHTTPS(proxy_url, 2),
                                        ab.identity_headers("SECRET_TOKEN", "SECRET_ACCOUNT", "0.155.0"),
                                        MODEL, 292, scenario, trials, rows.append)
        public = json.dumps(rows)
        for secret in ("SECRET_TOKEN", "SECRET_ACCOUNT", "seed-cookie", "new-cookie", "mock-password", "test-reasoning", ticket()):
            self.assertNotIn(secret, public)
        return summary, rows

    def test_real_tls_connect_ab_ba_connection_and_context(self):
        summary, rows = self.run_trial(trials=2)
        self.assertEqual(summary["valid_pairs"], 2)
        self.assertEqual(summary["same_connection_successes"], 2)
        self.assertEqual(summary["new_connection_successes"], 2)
        self.assertEqual([row["arm"] for row in rows if row["kind"] == "arm"], ["same", "new", "new", "same"])
        self.assertEqual([request[0] for request in self.origin.seen], [1, 1, 2, 3, 4, 3])
        self.assertEqual(len(self.proxy.seen), 4)
        for request, headers in self.proxy.seen:
            self.assertTrue(request.startswith("CONNECT chatgpt.com:443 HTTP/1."))
            self.assertEqual(headers["proxy-authorization"], "Basic " + base64.b64encode(b"mock-user:mock-password").decode())
        for start in (0, 3):
            seed, first, second = self.origin.seen[start:start + 3]
            self.assertNotIn("Cookie", seed[1])
            self.assertNotIn("X-Codex-Turn-State", seed[1])
            self.assertNotIn("Proxy-Authorization", seed[1])
            self.assertEqual(first[1], second[1])
            self.assertEqual(first[2], second[2])
            self.assertIn("seed-cookie", second[1]["Cookie"])
            self.assertIn("function_call_output", [item.get("type") for item in first[2]["input"]])

    def test_closed_seed_never_silently_reconnects(self):
        self.origin.mode = "close_seed"
        summary, rows = self.run_trial()
        self.assertEqual(summary["valid_pairs"], 0)
        same = next(row for row in rows if row.get("arm") == "same")
        self.assertEqual(same["reason"], "original_connection_lost")
        self.assertFalse(same["transport_verified"])
        self.assertEqual(len(self.proxy.seen), 2)
        self.assertEqual(len(self.origin.seen), 2)

    def test_http_errors_stop_entire_run(self):
        for mode in ("http_error", "http_500"):
            self.origin.mode = mode
            summary, rows = self.run_trial(trials=2)
            self.assertEqual(summary["stop"], "http_error")
            self.assertEqual(summary["trials"], 1)
            self.assertEqual(summary["valid_pairs"], 0)
            self.assertFalse(next(row for row in rows if row["kind"] == "arm")["success"])

    def test_failed_stream_is_not_success(self):
        self.origin.mode = "failed_stream"
        summary, rows = self.run_trial()
        self.assertEqual(summary["valid_pairs"], 1)
        self.assertEqual(summary["same_connection_successes"], 0)
        self.assertEqual(summary["new_connection_successes"], 0)
        self.assertEqual([row["reason"] for row in rows if row["kind"] == "arm"], ["response_failed"] * 2)

    def test_degraded_returned_state_is_not_success(self):
        self.origin.mode = "degraded_state"
        summary, _ = self.run_trial()
        self.assertEqual(summary["valid_pairs"], 1)
        self.assertEqual(summary["same_connection_successes"], 0)
        self.assertEqual(summary["new_connection_successes"], 0)

    def test_unqualified_seed_sends_no_followup(self):
        for mode in ("bad_seed", "no_cookies"):
            self.origin.mode = mode
            summary, rows = self.run_trial()
            self.assertEqual(summary["qualified_seeds"], 0)
            self.assertFalse(any(row["kind"] == "arm" for row in rows))

    def test_message_scenario(self):
        summary, _ = self.run_trial(scenario="message")
        self.assertEqual(summary["valid_pairs"], 1)
        self.assertEqual(self.origin.seen[0][2], self.origin.seen[1][2])

    def test_expired_cookie_bundle_sends_no_followup(self):
        with patch.object(ab.time, "monotonic", side_effect=[0, 241]):
            summary, rows = self.run_trial()
        self.assertEqual(summary["valid_pairs"], 0)
        self.assertEqual(len(self.origin.seen), 1)
        self.assertEqual(next(row for row in rows if row["kind"] == "skip")["reason"], "seed_expired")

    def test_tls_verification_cannot_be_skipped(self):
        conn = ab.SingleConnectHTTPS("http://127.0.0.1:" + str(self.proxy.server_address[1]), 2)
        try:
            result = ab.exchange(conn, ab.identity_headers("dummy", "dummy", "0.155.0"),
                                 ab.seed_request(MODEL, "message"), MODEL)
        finally:
            conn.close()
        self.assertEqual(result.reason, "transport_error")
        self.assertEqual(conn.connect_count, 0)
        self.assertFalse(self.origin.seen)

    def test_oversize_fails(self):
        self.origin.mode = "oversized"
        summary, rows = self.run_trial()
        self.assertEqual(summary["same_connection_successes"], 0)
        self.assertTrue(all(row["reason"] == "response_too_large" for row in rows if row["kind"] == "arm"))


if __name__ == "__main__":
    unittest.main()
