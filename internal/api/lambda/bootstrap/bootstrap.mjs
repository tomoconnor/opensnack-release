// Dev-only Lambda bootstrap for opensnack's LocalExecutor: loads the
// handler from the task root, calls it with the event and a context
// (async or callback style), and writes {"ok": true, "result": ...} or
// {"ok": false, "error": {...}} to the result file. Everything the function
// prints is the invocation's log.
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { format } from "node:util";

const spec = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const writeResult = (doc) => fs.writeFileSync(spec.result_path, JSON.stringify(doc));

function fail(errorType, errorMessage, trace = []) {
  writeResult({ ok: false, error: { errorType, errorMessage, trace } });
  process.stderr.write(`ERROR\t${errorType}: ${errorMessage}\n`);
}

function errorDoc(err) {
  if (err instanceof Error) {
    return { errorType: err.name, errorMessage: err.message, trace: String(err.stack || "").split("\n") };
  }
  return { errorType: "Error", errorMessage: typeof err === "string" ? err : JSON.stringify(err), trace: [] };
}

async function load() {
  const handler = spec.handler;
  const slash = handler.lastIndexOf("/");
  const base = handler.slice(slash + 1);
  const dot = base.indexOf(".");
  if (dot < 0) {
    fail("Runtime.MalformedHandlerName", `Bad handler ${handler}`);
    return null;
  }
  const modulePath = path.join(spec.task_root, handler.slice(0, slash + 1) + base.slice(0, dot));
  const funcPath = base.slice(dot + 1).split(".");
  const file = [".js", ".mjs", ".cjs"].map((ext) => modulePath + ext).find((f) => fs.existsSync(f));
  if (!file) {
    fail("Runtime.ImportModuleError", `Error: Cannot find module '${base.slice(0, dot)}'`);
    return null;
  }
  let mod;
  try {
    mod = await import(pathToFileURL(file).href);
  } catch (err) {
    const doc = errorDoc(err);
    fail(err instanceof SyntaxError ? "Runtime.UserCodeSyntaxError" : "Runtime.ImportModuleError", doc.errorMessage, doc.trace);
    return null;
  }
  let fn = mod;
  for (const part of funcPath) {
    fn = fn?.[part] ?? fn?.default?.[part];
  }
  if (typeof fn !== "function") {
    fail("Runtime.HandlerNotFound", `${handler} is undefined or not exported`);
    return null;
  }
  return fn;
}

const c = spec.context;
const context = {
  functionName: c.function_name,
  functionVersion: c.function_version,
  invokedFunctionArn: c.invoked_function_arn,
  memoryLimitInMB: String(c.memory_limit_in_mb),
  awsRequestId: c.aws_request_id,
  logGroupName: c.log_group_name,
  logStreamName: c.log_stream_name,
  clientContext: spec.client_context,
  callbackWaitsForEmptyEventLoop: true,
  getRemainingTimeInMillis: () => Math.max(spec.deadline_ms - Date.now(), 0),
};

for (const level of ["log", "info", "warn", "error", "debug"]) {
  const orig = console[level].bind(console);
  const tag = level === "log" ? "INFO" : level.toUpperCase();
  console[level] = (...args) => orig(`${new Date().toISOString()}\t${c.aws_request_id}\t${tag}\t${format(...args)}`);
}

const fn = await load();
if (fn) {
  const event = JSON.parse(fs.readFileSync(spec.event_path, "utf8"));
  try {
    const result = await new Promise((resolve, reject) => {
      const callback = (err, value) => (err ? reject(err) : resolve(value));
      context.done = callback;
      context.succeed = (value) => resolve(value);
      context.fail = (err) => reject(err);
      const ret = fn(event, context, callback);
      if (ret && typeof ret.then === "function") ret.then(resolve, reject);
      else if (fn.length < 3) resolve(ret);
    });
    writeResult({ ok: true, result: result === undefined ? null : result });
  } catch (err) {
    const doc = errorDoc(err);
    process.stderr.write(`ERROR\tInvoke Error \t${JSON.stringify(doc)}\n`);
    writeResult({ ok: false, error: doc });
  }
}
process.exit(0);
