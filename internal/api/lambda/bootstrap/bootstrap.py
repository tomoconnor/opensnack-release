# Lambda bootstrap for opensnack's executors: a Runtime API client. It
# imports the handler named by _HANDLER from LAMBDA_TASK_ROOT, then loops on
# /invocation/next, calling the handler with each event and a context and
# posting back /response or /error, as the AWS runtime does. Output the
# function writes (including from child processes) is captured per
# invocation and posted to opensnack's /logs extension.
import http.client
import importlib
import json
import os
import resource
import sys
import threading
import time
import traceback

API = os.environ["AWS_LAMBDA_RUNTIME_API"]
TOKEN = os.environ.pop("_OPENSNACK_RUNTIME_TOKEN", "")
PREFIX = "/2018-06-01/runtime"
LOG_CHUNK = 64 * 1024

# The runtime's own diagnostics go to the original stderr; fds 1 and 2
# become a pipe so everything the function prints can be captured.
_diag = os.fdopen(os.dup(2), "w", buffering=1)


def request(method, path, body=None, headers=None):
    host, _, port = API.rpartition(":")
    conn = http.client.HTTPConnection(host or API, int(port) if host else 80, timeout=None)
    hdrs = {"X-Opensnack-Runtime-Token": TOKEN}
    hdrs.update(headers or {})
    if isinstance(body, str):
        body = body.encode()
    conn.request(method, PREFIX + path, body=body, headers=hdrs)
    resp = conn.getresponse()
    data = resp.read()
    conn.close()
    return resp.status, resp.headers, data


def max_rss_kb():
    own = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    kids = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
    rss = max(own, kids)
    return rss // 1024 if sys.platform == "darwin" else rss


class Capture:
    """Reads fds 1 and 2 through a pipe and posts what arrives to the
    current invocation's log, in chunks."""

    def __init__(self):
        r, w = os.pipe()
        sys.stdout.flush()
        sys.stderr.flush()
        os.dup2(w, 1)
        os.dup2(w, 2)
        os.close(w)
        sys.stdout = os.fdopen(1, "w", buffering=1, closefd=False)
        sys.stderr = os.fdopen(2, "w", buffering=1, closefd=False)
        self.reader = os.fdopen(r, "rb", buffering=0)
        self.lock = threading.Lock()
        self.post_lock = threading.Lock()
        self.buf = bytearray()
        self.path = "/init/logs"
        self.marks = {}
        self.seq = 0
        threading.Thread(target=self.read, daemon=True).start()
        threading.Thread(target=self.tick, daemon=True).start()

    def read(self):
        while True:
            chunk = self.reader.read(LOG_CHUNK)
            if not chunk:
                return
            with self.lock:
                self.buf += chunk
                for seq, (mark, ev) in list(self.marks.items()):
                    i = self.buf.find(mark)
                    if i >= 0:
                        del self.buf[i:i + len(mark)]
                        del self.marks[seq]
                        ev.set()
                full = len(self.buf) >= LOG_CHUNK
            if full:
                self.flush()

    def tick(self):
        while True:
            time.sleep(1)
            self.flush()

    def flush(self):
        with self.post_lock:
            with self.lock:
                data, path = bytes(self.buf), self.path
                self.buf.clear()
            if data:
                try:
                    request("POST", path, data, {"Content-Type": "text/plain"})
                except Exception as e:  # the invocation is gone; drop the output
                    _diag.write("log delivery failed: %s\n" % e)

    def sync(self):
        """Wait until everything written so far has been read."""
        sys.stdout.flush()
        sys.stderr.flush()
        with self.lock:
            self.seq += 1
            mark = b"\0opensnack-sync-%d\0" % self.seq
            ev = threading.Event()
            self.marks[self.seq] = (mark, ev)
        os.write(1, mark)
        ev.wait(2)

    def switch(self, path):
        self.sync()
        self.flush()
        with self.lock:
            self.path = path


def error_doc(e, skip=1):
    stack = traceback.format_list(traceback.extract_tb(e.__traceback__)[skip:])
    return {"errorMessage": str(e), "errorType": type(e).__name__, "requestId": "", "stackTrace": stack}


def init_error(cap, error_type, message, stack=None):
    sys.stderr.write("[ERROR] %s: %s\n" % (error_type, message))
    cap.switch("/init/logs")
    doc = {"errorMessage": message, "errorType": error_type, "requestId": "", "stackTrace": stack or []}
    request("POST", "/init/error", json.dumps(doc), {"Lambda-Runtime-Function-Error-Type": error_type})
    sys.exit(1)


class Context:
    def __init__(self, headers):
        env = os.environ
        self.function_name = env.get("AWS_LAMBDA_FUNCTION_NAME", "")
        self.function_version = env.get("AWS_LAMBDA_FUNCTION_VERSION", "")
        self.memory_limit_in_mb = int(env.get("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "128"))
        self.log_group_name = env.get("AWS_LAMBDA_LOG_GROUP_NAME", "")
        self.log_stream_name = env.get("AWS_LAMBDA_LOG_STREAM_NAME", "")
        self.aws_request_id = headers.get("Lambda-Runtime-Aws-Request-Id", "")
        self.invoked_function_arn = headers.get("Lambda-Runtime-Invoked-Function-Arn", "")
        self.identity = None
        cc = headers.get("Lambda-Runtime-Client-Context")
        self.client_context = json.loads(cc) if cc else None
        self._deadline_ms = int(headers.get("Lambda-Runtime-Deadline-Ms", "0"))

    def get_remaining_time_in_millis(self):
        return max(int(self._deadline_ms - time.time() * 1000), 0)


def load_handler(cap):
    handler = os.environ.get("_HANDLER", "")
    sys.path.insert(0, os.environ.get("LAMBDA_TASK_ROOT", "/var/task"))
    if "." not in handler:
        init_error(cap, "Runtime.MalformedHandlerName", "Bad handler '%s': not enough values to unpack (expected 2, got 1)" % handler)
    module_name, func_name = handler.rsplit(".", 1)
    module_name = module_name.replace("/", ".")
    try:
        module = importlib.import_module(module_name)
    except ImportError as e:
        init_error(cap, "Runtime.ImportModuleError", "Unable to import module '%s': %s" % (module_name, e))
    except SyntaxError as e:
        init_error(cap, "Runtime.UserCodeSyntaxError", "Unable to import module '%s': %s" % (module_name, e))
    except Exception as e:
        init_error(cap, type(e).__name__, "Unable to import module '%s': %s" % (module_name, e), error_doc(e)["stackTrace"])
    func = getattr(module, func_name, None)
    if func is None:
        init_error(cap, "Runtime.HandlerNotFound", "Handler '%s' missing on module '%s'" % (func_name, module_name))
    return func


def main():
    cap = Capture()
    func = load_handler(cap)
    while True:
        cap.switch("/init/logs")
        status, headers, body = request("GET", "/invocation/next")
        if status == 410:
            return
        if status != 200:
            time.sleep(0.2)
            continue
        context = Context(headers)
        rid = context.aws_request_id
        cap.switch("/invocation/%s/logs" % rid)
        doc = None
        try:
            result = func(json.loads(body), context)
        except Exception as e:
            doc = error_doc(e)
        else:
            try:
                payload = json.dumps(result)
            except (TypeError, ValueError) as e:
                doc = {"errorMessage": "Unable to marshal response: %s" % e, "errorType": "Runtime.MarshalError", "stackTrace": []}
        rss = {"X-Opensnack-Max-Rss-Kb": str(max_rss_kb())}
        if doc is not None:
            doc["requestId"] = rid
            sys.stderr.write("[ERROR] %s: %s\nTraceback (most recent call last):\n%s" % (doc["errorType"], doc["errorMessage"], "".join(doc["stackTrace"])))
            cap.switch("/init/logs")
            request("POST", "/invocation/%s/error" % rid, json.dumps(doc), dict(rss, **{"Lambda-Runtime-Function-Error-Type": doc["errorType"]}))
            continue
        cap.switch("/init/logs")
        request("POST", "/invocation/%s/response" % rid, payload, dict(rss, **{"Content-Type": "application/json"}))


if __name__ == "__main__":
    main()
