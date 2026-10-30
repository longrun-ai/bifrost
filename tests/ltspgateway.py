#!/usr/bin/env python3
"""Exercise the actual Bifrost binary against task-owned local upstreams.

No real credentials or provider charges. The proxy and upstream exist only for
this process; all state is in an explicit ignored evidence directory.
"""
import argparse
import contextlib
import datetime
import http.server
import json
import os
from pathlib import Path
import selectors
import socket
import sqlite3
import subprocess
import threading
import time
import urllib.error
import urllib.request


def request(url, body=None, headers=None, method=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=55) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


class Upstream(http.server.BaseHTTPRequestHandler):
    depleted = False
    depleted_anthropic = False
    calls = []
    lock = threading.Lock()

    def log_message(self, *_):
        pass

    def send_json(self, status, value):
        b = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        self.send_json(200, {"object": "list", "data": [{"id": "gpt-6.1-sol", "object": "model", "created": 0, "owned_by": "openai"}]})

    def do_POST(self):
        b = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        key = self.headers.get("x-api-key") or self.headers.get("Authorization", "").removeprefix("Bearer ")
        with self.lock:
            self.calls.append({"key": key, "path": self.path, "body": b})
        if key.startswith("fixture-c"):
            if self.depleted_anthropic:
                self.send_json(400,{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}})
            else:
                self.send_json(200,{"id":"msg_fixture","type":"message","role":"assistant","model":b["model"],"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":None,"usage":{"input_tokens":12,"output_tokens":2}})
            return
        if self.depleted and key.startswith("fixture-a"):
            self.send_json(429, {"error": {"message": "credit balance exhausted", "type": "credit_balance_exhausted", "code": "credit_balance_exhausted"}})
            return
        if self.path.endswith("/responses"):
            response = {
                "id": "resp_fixture", "object": "response", "created_at": 0, "status": "completed", "model": b["model"],
                "output": [{"type": "message", "role": "assistant", "id": "msg_fixture", "status": "completed",
                            "content": [{"type": "output_text", "text": "ok", "annotations": []}]}],
                "usage": {"input_tokens": 20, "output_tokens": 2, "total_tokens": 22,
                          "input_tokens_details": {"cached_tokens": 10, "cache_write_tokens": 5,
                                                   "cache_write_tokens_details": {"ttl_12h_tokens": 5, "ttl_30m_tokens": 0}}}}
            if b.get("stream"):
                self.send_response(200)
                self.send_header("Content-Type","text/event-stream")
                self.end_headers()
                item=response["output"][0]
                events=[
                    {"type":"response.created","response":{**response,"status":"in_progress","output":[],"usage":None}},
                    {"type":"response.output_item.added","output_index":0,"item":{**item,"status":"in_progress","content":[]}},
                    {"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_fixture","part":{"type":"output_text","text":"","annotations":[]}},
                    {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_fixture","delta":"ok"},
                    {"type":"response.completed","response":response},
                ]
                for number,event in enumerate(events):
                    event["sequence_number"]=number
                    self.wfile.write(("event: "+event["type"]+"\ndata: "+json.dumps(event)+"\n\n").encode())
                self.wfile.flush()
            else:
                self.send_json(200,response)
            return
        base = {"id": "chatcmpl_fixture", "created": 0, "model": b["model"]}
        if b.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for value in (
                {**base, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": {"role": "assistant", "content": "ok"}, "finish_reason": None}]},
                {**base, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                 "usage": {"prompt_tokens": 12, "completion_tokens": 2, "total_tokens": 14}},
            ):
                self.wfile.write(b"data: " + json.dumps(value).encode() + b"\n\n")
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        else:
            self.send_json(200, {**base, "object": "chat.completion", "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
                                 "usage": {"prompt_tokens": 12, "completion_tokens": 2, "total_tokens": 14}})


class Proxy(http.server.BaseHTTPRequestHandler):
    target = None
    connections = 0

    def log_message(self, *_):
        pass

    def do_CONNECT(self):
        # Fixture only: every tunnel must target this process's exact upstream.
        if self.path != f"127.0.0.1:{self.target[1]}":
            self.send_error(403)
            return
        type(self).connections += 1
        with socket.create_connection(self.target, timeout=5) as remote:
            self.send_response(200, "Connection established")
            self.end_headers()
            with selectors.DefaultSelector() as selector:
                selector.register(self.connection, selectors.EVENT_READ, remote)
                selector.register(remote, selectors.EVENT_READ, self.connection)
                while True:
                    events = selector.select(30)
                    if not events:
                        return
                    for event, _ in events:
                        data = event.fileobj.recv(65536)
                        if not data:
                            return
                        event.data.sendall(data)


@contextlib.contextmanager
def serve(handler):
    s = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    s.daemon_threads = True
    thread = threading.Thread(target=s.serve_forever, daemon=True)
    thread.start()
    try:
        yield s.server_address
    finally:
        s.shutdown()
        s.server_close()
        thread.join()


def run(binary, evidence):
    evidence.mkdir(parents=True, exist_ok=False)
    app = evidence / "app"
    app.mkdir(mode=0o700)
    prices={model:{"provider":provider,"mode":"chat","base_model":model,"input_cost_per_token":2e-6,
                   "output_cost_per_token":1e-5,"cache_read_input_token_cost":1e-7,"cache_creation_input_token_cost":2.5e-6,
                   "max_input_tokens":128000,"max_output_tokens":16000,"model_parameters":[]}
            for model,provider in (("gpt-6.1-sol","openai"),("claude-test","anthropic"))}
    (app/"fixture-pricing.json").write_text(json.dumps(prices))
    # Resolve an ephemeral listener without assuming a free fixed production port.
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    url = f"http://127.0.0.1:{port}"
    ids = {name: f"00000000-0000-4000-8000-{n:012d}" for n, name in enumerate(("a1", "a2", "b1"), 1)}
    anthropic_ids={name:f"00000000-0000-4000-8000-{n:012d}" for n,name in enumerate(("c1","c2"),4)}
    with serve(Upstream) as target:
        Proxy.target = target
        with serve(Proxy) as proxy:
            initial = {
                "$schema":"https://www.getbifrost.ai/schema",
                "framework":{"pricing":{"pricing_url":(app/"fixture-pricing.json").as_uri(),
                    "model_parameters_url":(app/"fixture-pricing.json").as_uri(),"mcp_library_sync_interval":0,"live_models_sync_interval":0}},
                "client": {"enable_logging": True, "disable_content_logging": True,
                           "enforce_auth_on_inference": False,
                           "allow_per_request_content_storage_override": False, "allow_direct_keys": False},
                "config_store": {"enabled": True, "type": "sqlite", "config": {"path": str(app / "config.db")}},
                "logs_store": {"enabled": True, "type": "sqlite", "config": {"path": str(app / "logs.db")}},
                "providers": {"openai": {"keys": [{"id": ids[name], "name": name, "value": "fixture-"+name,
                                                    "models": ["*"], "weight": 1, "enabled": True} for name in ids],
                    "network_config": {"base_url": f"http://{target[0]}:{target[1]}/v1", "allow_private_network": True, "max_retries": 0},
                    # Must lose to the fixed deployment proxy.
                    "proxy_config": {"type": "http", "url": "http://127.0.0.1:9"}},
                    "anthropic":{"keys":[{"id":id,"name":name,"value":"fixture-"+name,"models":["*"],"weight":1,"enabled":True} for name,id in anthropic_ids.items()],
                      "network_config":{"base_url":f"http://{target[0]}:{target[1]}","allow_private_network":True,"max_retries":0},
                      "proxy_config":{"type":"http","url":"http://127.0.0.1:9"}}},
            }
            provider_configs=initial["providers"]
            # Only the operator's fixture file selects private local upstreams.
            # Exercise unauthenticated key registration without requesting the
            # native API's separately protected private-endpoint capability.
            initial["providers"]={provider:{**cfg,"keys":[]} for provider,cfg in provider_configs.items()}
            (app / "config.json").write_text(json.dumps(initial))
            env = dict(os.environ, LTSP_GATEWAY_STATE=str(app / "accounts.json"), LTSP_GATEWAY_PROXY=f"http://{proxy[0]}:{proxy[1]}",
                       BIFROST_ENCRYPTION_KEY="fixture-only-encryption-passphrase", BIFROST_HOST="127.0.0.1")
            log = (evidence / "gateway.log").open("wb")
            p = subprocess.Popen([binary, "-app-dir", str(app), "-port", str(port), "-log-level", "warn"], env=env, stdout=log, stderr=subprocess.STDOUT)
            try:
                for _ in range(90):
                    if p.poll() is not None:
                        raise AssertionError(f"gateway exited {p.returncode}; see gateway.log")
                    try:
                        status, raw = request(url + "/api/ltsp/accounts")
                        if status == 200 and "accounts" in json.loads(raw):
                            break
                    except (OSError, ValueError):
                        pass
                    time.sleep(1)
                else:
                    raise AssertionError("gateway did not start")
                status,raw=request(url+"/api/session/is-auth-enabled")
                auth=json.loads(raw)
                assert status==200 and not auth.get("is_auth_enabled") and not auth.get("setup_required"),auth
                status, home = request(url + "/")
                assert status == 200 and "客户端接入" in home.decode(), "root did not serve the LTSP account home"
                assert request(url + "/ltsp/accounts")[1] == home, "home alias differs from root"
                for provider,cfg in provider_configs.items():
                    for key in cfg["keys"]:
                        status,raw=request(url+"/api/providers/"+provider+"/keys",key)
                        assert status in (200,201),("API key registration",status,raw[:400])
                assert request(url+"/api/ltsp/accounts",headers={"Origin":"https://unrelated.example"})[0]==403
                today = datetime.date.today().isoformat()
                accounts = [{"id": "account-a", "name": "A", "provider": "openai", "key_ids": [ids["a1"], ids["a2"]],
                             "cycle": "monthly", "refresh_date": today, "probe_model": "gpt-6.1-sol"},
                            {"id": "account-b", "name": "B", "provider": "openai", "key_ids": [ids["b1"]],
                             "cycle": "monthly", "refresh_date": today, "probe_model": "gpt-6.1-sol"},
                            {"id":"account-c","name":"C","provider":"anthropic","key_ids":list(anthropic_ids.values()),
                             "cycle":"monthly","refresh_date":today,"probe_model":"claude-test"}]
                assert request(url + "/api/ltsp/accounts", {"accounts": accounts}, method="PUT")[0] == 200
                payload = {"model": "openai/gpt-6.1-sol", "messages": [{"role": "user", "content": "fixture-sensitive-prompt-do-not-log"}]}

                def inference(extra=None, body=None, path="/v1/chat/completions"):
                    status, raw = request(url + path, body or payload, extra)
                    assert status == 200, (status, raw[:400])
                    return raw

                # Same source IP uses one native key; explicit sessions still pass through.
                start = len(Upstream.calls)
                for _ in range(4):
                    inference()
                assert len({c["key"] for c in Upstream.calls[start:]}) == 1, "IP affinity was not honored"
                inference({"Authorization":"Bearer unused"})
                for placeholder in ("dummy-api-key", "arbitrary-lan-placeholder"):
                    inference({"Authorization":"Bearer "+placeholder}, body={**payload,"model":"gpt-6.1-sol"}, path="/openai/chat/completions")
                inference({"Authorization":"Bearer dummy-api-key"}, body={"model":"gpt-6.1-sol","input":"Hi"}, path="/openai/responses")
                inference({"x-bf-session-id": "native-user-session"})
                assert b'"ok"' in inference(body={**payload, "stream": True})
                assert Proxy.connections > 0, "gateway did not use fixed proxy"

                for options, expected in ((None, "12h"), ({"ttl": "30m", "mode": "explicit"}, "30m")):
                    body = {"model": "openai/gpt-6.1-sol", "input": [{"role":"user", "content":[{
                        "type":"input_text", "text":"Hi", "prompt_cache_breakpoint":{"mode":"explicit", "ttl":"30m"}}]}]}
                    if options is not None:
                        body["prompt_cache_options"] = options
                    response = json.loads(inference(body=body, path="/v1/responses"))
                    assert Upstream.calls[-1]["body"]["prompt_cache_options"]["ttl"] == expected
                    assert Upstream.calls[-1]["body"]["input"][0]["content"][0]["prompt_cache_breakpoint"]["ttl"] == "30m"
                    assert response["usage"]["input_tokens_details"]["cache_write_tokens_details"]["ttl_12h_tokens"] == 5

                Upstream.depleted = True
                assert request(url + "/v1/chat/completions", payload, {"x-bf-api-key-id": ids["a1"]})[0] != 200
                state = json.loads(request(url + "/api/ltsp/accounts")[1])
                assert state["accounts"][0]["exhausted"]
                before = len(Upstream.calls)
                assert request(url + "/v1/chat/completions", payload, {"x-bf-api-key-id": ids["a2"]})[0] != 200
                assert len(Upstream.calls) == before, "sibling key escaped account hold"
                inference()
                assert Upstream.calls[-1]["key"] == "fixture-b1"

                before = len(Upstream.calls)
                status, raw = request(url + "/api/ltsp/accounts/account-a/probe", {})
                assert status == 200 and not json.loads(raw)["recovered"], raw
                assert len(Upstream.calls) == before + 1, "manual probe was delayed or retried"
                Upstream.depleted = False
                before = len(Upstream.calls)
                status, raw = request(url + "/api/ltsp/accounts/account-a/probe", {})
                assert status == 200 and json.loads(raw)["recovered"], raw
                assert len(Upstream.calls) == before + 1
                assert not json.loads((app / "accounts.json").read_text())["accounts"][0]["exhausted"]
                claude={"model":"anthropic/claude-test","max_completion_tokens":16,"messages":[{"role":"user","content":"Reply OK."}]}
                inference(body=claude)
                Upstream.depleted_anthropic=True
                assert request(url+"/v1/chat/completions",claude,{"x-bf-api-key-id":anthropic_ids["c1"]})[0]!=200
                before=len(Upstream.calls)
                assert request(url+"/v1/chat/completions",claude,{"x-bf-api-key-id":anthropic_ids["c2"]})[0]!=200
                assert len(Upstream.calls)==before,"Anthropic sibling escaped hold"
                before=len(Upstream.calls)
                status,raw=request(url+"/api/ltsp/accounts/account-c/probe",{})
                assert status==200 and not json.loads(raw)["recovered"] and len(Upstream.calls)==before+1,raw
                Upstream.depleted_anthropic=False
                before=len(Upstream.calls)
                status,raw=request(url+"/api/ltsp/accounts/account-c/probe",{})
                assert status==200 and json.loads(raw)["recovered"] and len(Upstream.calls)==before+1,raw
                # A real process restart must preserve UI-registered keys and a hold.
                Upstream.depleted=True
                assert request(url+"/v1/chat/completions",payload,{"x-bf-api-key-id":ids["a1"]})[0]!=200
                p.terminate()
                p.wait(timeout=15)
                p=subprocess.Popen([binary,"-app-dir",str(app),"-port",str(port),"-log-level","warn"],env=env,stdout=log,stderr=subprocess.STDOUT)
                for _ in range(90):
                    if p.poll() is not None:raise AssertionError("restarted gateway exited")
                    try:
                        status,raw=request(url+"/api/ltsp/accounts")
                        if status==200 and json.loads(raw)["accounts"][0]["exhausted"]:break
                    except (OSError,ValueError):pass
                    time.sleep(1)
                else:raise AssertionError("persisted hold not restored")
                before=len(Upstream.calls)
                assert request(url+"/v1/chat/completions",payload,{"x-bf-api-key-id":ids["a2"]})[0]!=200
                assert len(Upstream.calls)==before
                inference()
                stored=b"".join(f.read_bytes() for f in app.glob("config.db*"))
                assert b"fixture-a1" not in stored,"native configuration key was stored without encryption"
                # An actual native SQLite log must contain metadata without request content.
                for _ in range(30):
                    db = sqlite3.connect(f"file:{app / 'logs.db'}?mode=ro", uri=True)
                    tables = [row[0] for row in db.execute("select name from sqlite_master where type='table'")]
                    rows = {table: db.execute('select * from "'+table.replace('"', '""')+'"').fetchall() for table in tables}
                    db.close()
                    text = repr(rows)
                    if "native-user-session" in text and "ltsp-ip:127.0.0.1" in text:
                        assert "fixture-sensitive-prompt-do-not-log" not in text
                        break
                    time.sleep(1)
                else:
                    raise AssertionError("native usage/session metadata was not recorded")
                (evidence / "result.json").write_text(json.dumps({"passed": True, "inference_calls": len(Upstream.calls),
                    "proxy_connections": Proxy.connections, "checks": ["HTTP forwarding", "SSE", "fixed proxy", "IP/native session affinity",
                    "12h default/30m explicit", "account hold", "healthy pool fallback", "immediate single probe", "native usage without prompt content"]}, indent=2))
                print("Bifrost real-binary local-upstream integration passed")
            finally:
                (evidence/"upstream-calls.json").write_text(json.dumps(Upstream.calls,indent=2))
                p.terminate()
                try:
                    p.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait()
                log.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    run(args.binary, args.evidence)
