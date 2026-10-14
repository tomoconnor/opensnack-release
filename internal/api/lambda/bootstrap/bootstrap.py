# Dev-only Lambda bootstrap for opensnack's LocalExecutor: imports the
# handler from the task root, calls it with the event and a context, and
# writes {"ok": true, "result": ...} or {"ok": false, "error": {...}} to the
# result file. Everything the function prints is the invocation's log.
import importlib
import json
import sys
import time
import traceback


def write_result(path, doc):
    with open(path, "w") as f:
        json.dump(doc, f)


def fail(path, error_type, message, stack=None):
    err = {"errorMessage": message, "errorType": error_type, "requestId": "", "stackTrace": stack or []}
    write_result(path, {"ok": False, "error": err})
    sys.stderr.write("[ERROR] %s: %s\n" % (error_type, message))


class Context:
    def __init__(self, ctx, deadline_ms, client_context):
        self.function_name = ctx["function_name"]
        self.function_version = ctx["function_version"]
        self.invoked_function_arn = ctx["invoked_function_arn"]
        self.memory_limit_in_mb = int(ctx["memory_limit_in_mb"])
        self.aws_request_id = ctx["aws_request_id"]
        self.log_group_name = ctx["log_group_name"]
        self.log_stream_name = ctx["log_stream_name"]
        self.identity = None
        self.client_context = client_context
        self._deadline_ms = deadline_ms

    def get_remaining_time_in_millis(self):
        return max(int(self._deadline_ms - time.time() * 1000), 0)


def main():
    with open(sys.argv[1]) as f:
        spec = json.load(f)
    result_path = spec["result_path"]
    handler = spec["handler"]
    sys.path.insert(0, spec["task_root"])

    if "." not in handler:
        return fail(result_path, "Runtime.MalformedHandlerName", "Bad handler '%s': not enough values to unpack (expected 2, got 1)" % handler)
    module_name, func_name = handler.rsplit(".", 1)
    module_name = module_name.replace("/", ".")
    try:
        module = importlib.import_module(module_name)
    except ImportError as e:
        return fail(result_path, "Runtime.ImportModuleError", "Unable to import module '%s': %s" % (module_name, e))
    except Exception as e:
        return fail(result_path, "Runtime.UserCodeSyntaxError" if isinstance(e, SyntaxError) else type(e).__name__,
                    "Unable to import module '%s': %s" % (module_name, e))
    func = getattr(module, func_name, None)
    if func is None:
        return fail(result_path, "Runtime.HandlerNotFound", "Handler '%s' missing on module '%s'" % (func_name, module_name))

    with open(spec["event_path"]) as f:
        event = json.load(f)
    context = Context(spec["context"], spec["deadline_ms"], spec.get("client_context"))
    try:
        result = func(event, context)
    except Exception as e:
        stack = traceback.format_list(traceback.extract_tb(e.__traceback__)[1:])
        err = {
            "errorMessage": str(e),
            "errorType": type(e).__name__,
            "requestId": context.aws_request_id,
            "stackTrace": stack,
        }
        sys.stdout.flush()
        sys.stderr.write("[ERROR] %s: %s\nTraceback (most recent call last):\n%s" % (type(e).__name__, e, "".join(stack)))
        write_result(result_path, {"ok": False, "error": err})
        return
    try:
        payload = json.dumps(result)
    except (TypeError, ValueError) as e:
        return fail(result_path, "Runtime.MarshalError", "Unable to marshal response: %s" % e)
    with open(result_path, "w") as f:
        f.write('{"ok": true, "result": ' + payload + "}")


if __name__ == "__main__":
    main()
