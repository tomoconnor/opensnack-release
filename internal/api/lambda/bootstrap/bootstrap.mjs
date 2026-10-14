// Lambda bootstrap for opensnack's executors: a Runtime API client. It
// loads the handler named by _HANDLER from LAMBDA_TASK_ROOT, then loops on
// /invocation/next, calling the handler with each event and a context
// (async or callback style) and posting back /response or /error, as the
// AWS runtime does. What the function writes to stdout and stderr is
// captured per invocation and posted to opensnack's /logs extension.
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { format } from "node:util";

const api = process.env.AWS_LAMBDA_RUNTIME_API;
const token = process.env._OPENSNACK_RUNTIME_TOKEN || "";
delete process.env._OPENSNACK_RUNTIME_TOKEN;
const [apiHost, apiPort] = api.includes(":") ? [api.slice(0, api.lastIndexOf(":")), api.slice(api.lastIndexOf(":") + 1)] : [api, "80"];
const agent = new http.Agent({ keepAlive: true, maxSockets: 4 });
const diag = (msg) => fs.writeSync(2, msg);

function request(method, p, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const data = body === undefined ? undefined : Buffer.isBuffer(body) ? body : Buffer.from(body);
    const req = http.request(
      {
        host: apiHost,
        port: apiPort,
        method,
        agent,
        path: "/2018-06-01/runtime" + p,
        headers: { "X-Opensnack-Runtime-Token": token, ...(data ? { "Content-Length": data.length } : {}), ...headers },
      },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
        res.on("error", reject);
      },
    );
    req.on("error", reject);
    if (data) req.write(data);
    req.end();
  });
}

// Output capture: writes to stdout and stderr go to the current
// invocation's log instead of the process's own streams.
let logPath = "/init/logs";
let logBuf = [];
let posting = Promise.resolve();
function capture(chunk, encoding, cb) {
  logBuf.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk), typeof encoding === "string" ? encoding : "utf8"));
  const done = typeof encoding === "function" ? encoding : cb;
  if (done) process.nextTick(done);
  return true;
}
process.stdout.write = capture;
process.stderr.write = capture;
function flushLogs() {
  if (logBuf.length === 0) return posting;
  const data = Buffer.concat(logBuf);
  const p = logPath;
  logBuf = [];
  posting = posting.then(() => request("POST", p, data, { "Content-Type": "text/plain" })).catch((e) => diag(`log delivery failed: ${e}\n`));
  return posting;
}
setInterval(flushLogs, 1000).unref();
async function switchLogs(p) {
  await flushLogs();
  logPath = p;
}

let requestId = "";
for (const level of ["log", "info", "warn", "error", "debug"]) {
  const tag = level === "log" ? "INFO" : level.toUpperCase();
  const stream = level === "warn" || level === "error" ? process.stderr : process.stdout;
  console[level] = (...args) => stream.write(`${new Date().toISOString()}\t${requestId}\t${tag}\t${format(...args)}\n`);
}

const maxRSSKB = () => String(process.resourceUsage().maxRSS);

function errorDoc(err) {
  if (err instanceof Error) {
    return { errorType: err.name, errorMessage: err.message, trace: String(err.stack || "").split("\n") };
  }
  return { errorType: "Error", errorMessage: typeof err === "string" ? err : JSON.stringify(err), trace: [] };
}

async function initError(errorType, errorMessage, trace = []) {
  process.stderr.write(`ERROR\t${errorType}: ${errorMessage}\n`);
  await switchLogs("/init/logs");
  await request("POST", "/init/error", JSON.stringify({ errorType, errorMessage, trace }), {
    "Lambda-Runtime-Function-Error-Type": errorType,
  });
  process.exit(1);
}

async function load() {
  const handler = process.env._HANDLER || "";
  const taskRoot = process.env.LAMBDA_TASK_ROOT || "/var/task";
  const slash = handler.lastIndexOf("/");
  const base = handler.slice(slash + 1);
  const dot = base.indexOf(".");
  if (dot < 0) return initError("Runtime.MalformedHandlerName", `Bad handler ${handler}`);
  const modulePath = path.join(taskRoot, handler.slice(0, slash + 1) + base.slice(0, dot));
  const funcPath = base.slice(dot + 1).split(".");
  const file = [".js", ".mjs", ".cjs"].map((ext) => modulePath + ext).find((f) => fs.existsSync(f));
  if (!file) return initError("Runtime.ImportModuleError", `Error: Cannot find module '${base.slice(0, dot)}'`);
  let mod;
  try {
    mod = await import(pathToFileURL(file).href);
  } catch (err) {
    const doc = errorDoc(err);
    return initError(err instanceof SyntaxError ? "Runtime.UserCodeSyntaxError" : "Runtime.ImportModuleError", doc.errorMessage, doc.trace);
  }
  let fn = mod;
  for (const part of funcPath) fn = fn?.[part] ?? fn?.default?.[part];
  if (typeof fn !== "function") return initError("Runtime.HandlerNotFound", `${handler} is undefined or not exported`);
  return fn;
}

function run(fn, event, context) {
  return new Promise((resolve, reject) => {
    const callback = (err, value) => (err ? reject(err) : resolve(value));
    context.done = callback;
    context.succeed = (value) => resolve(value);
    context.fail = (err) => reject(err);
    try {
      const ret = fn(event, context, callback);
      if (ret && typeof ret.then === "function") ret.then(resolve, reject);
      else if (fn.length < 3) resolve(ret);
    } catch (err) {
      reject(err);
    }
  });
}

const fn = await load();
const env = process.env;
for (;;) {
  await switchLogs("/init/logs");
  let next;
  try {
    next = await request("GET", "/invocation/next");
  } catch (e) {
    diag(`next invocation: ${e}\n`);
    await new Promise((r) => setTimeout(r, 200));
    continue;
  }
  if (next.status === 410) process.exit(0);
  if (next.status !== 200) {
    await new Promise((r) => setTimeout(r, 200));
    continue;
  }
  const h = next.headers;
  requestId = h["lambda-runtime-aws-request-id"];
  const deadline = Number(h["lambda-runtime-deadline-ms"]);
  const context = {
    functionName: env.AWS_LAMBDA_FUNCTION_NAME,
    functionVersion: env.AWS_LAMBDA_FUNCTION_VERSION,
    invokedFunctionArn: h["lambda-runtime-invoked-function-arn"],
    memoryLimitInMB: env.AWS_LAMBDA_FUNCTION_MEMORY_SIZE,
    awsRequestId: requestId,
    logGroupName: env.AWS_LAMBDA_LOG_GROUP_NAME,
    logStreamName: env.AWS_LAMBDA_LOG_STREAM_NAME,
    clientContext: h["lambda-runtime-client-context"] ? JSON.parse(h["lambda-runtime-client-context"]) : undefined,
    callbackWaitsForEmptyEventLoop: true,
    getRemainingTimeInMillis: () => Math.max(deadline - Date.now(), 0),
  };
  env._X_AMZN_TRACE_ID = h["lambda-runtime-trace-id"] || "";
  await switchLogs(`/invocation/${requestId}/logs`);
  let payload, doc;
  try {
    const result = await run(fn, JSON.parse(next.body.toString("utf8")), context);
    payload = JSON.stringify(result === undefined ? null : result);
  } catch (err) {
    doc = errorDoc(err);
    process.stderr.write(`${new Date().toISOString()}\t${requestId}\tERROR\tInvoke Error \t${JSON.stringify(doc)}\n`);
  }
  await switchLogs("/init/logs");
  if (doc) {
    await request("POST", `/invocation/${requestId}/error`, JSON.stringify(doc), {
      "Lambda-Runtime-Function-Error-Type": doc.errorType,
      "X-Opensnack-Max-Rss-Kb": maxRSSKB(),
    });
  } else {
    await request("POST", `/invocation/${requestId}/response`, payload, {
      "Content-Type": "application/json",
      "X-Opensnack-Max-Rss-Kb": maxRSSKB(),
    });
  }
}
