# Running Lambda functions in Kubernetes

This guide covers the Kubernetes Lambda executor (`OPENSNACK_LAMBDA_EXECUTOR=kubernetes`): how it works, how to deploy it, how to run it, and where its security boundaries are. The README's [Lambda execution](../README.md#lambda-execution) section lists every setting; this guide explains them.

## How it works

```
                 AWS API (:4566)                       Runtime API (:9001)
  client ──────► opensnack ◄──────────────────────────────── sandbox pod
   Invoke          │  │                                      lambda-init
                   │  └── creates/deletes pods ──► kube API     └─ bootstrap
                   │      (Role in the sandbox                     └─ handler
                   │       namespace only)
                   └── STS: role credentials for the function
```

1. An `Invoke` (or an event source, or API Gateway) reaches OpenSnack. The Handler applies the concurrency caps and payload limits, then hands the invocation to the executor.
2. The executor looks for a warm pod for that exact function version: same code, same configuration. If there is none, it creates one, and the invocation waits in a queue for it. The function's `Timeout` doesn't start yet.
3. The pod starts `lambda-init`, the image's entrypoint. Using its token, it fetches the function's environment from `GET /2018-06-01/runtime/opensnack/init`. The environment includes the role credentials OpenSnack's STS has just issued. `lambda-init` then fetches the deployment package from `GET /2018-06-01/runtime/opensnack/code` and unpacks it into `/var/task`. Finally it sets the process limits and execs the language bootstrap.
4. The bootstrap is a standard [Runtime API](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html) client. It loads the handler and calls `GET /invocation/next`. The invocation is handed over at that point, and the function's timeout starts.
5. The handler's result comes back through `POST /invocation/{id}/response` or `/error`. Output is posted to `/invocation/{id}/logs` while it runs. OpenSnack writes START, END and REPORT around it in CloudWatch Logs.
6. The pod then calls `/next` again and stays warm. When it has been idle for `OPENSNACK_LAMBDA_K8S_IDLE_TTL`, or has reached `OPENSNACK_LAMBDA_K8S_MAX_POD_LIFETIME`, OpenSnack deletes it and revokes its credentials.

Each pod runs one invocation at a time, so a function with two invocations in flight has two pods. A pod is only reused for the version it was started for. Updating a function's code or configuration, or deleting the function, retires its pods once their current invocations finish.

### What counts as a function error

`Invoke` fails with an error only when OpenSnack couldn't run the function at all. When no image is configured for the runtime, that is a 502 `InvalidRuntimeException`, as in AWS. When the pod never started (its image can't be pulled, or it didn't start within `OPENSNACK_LAMBDA_K8S_START_TIMEOUT`), it is a 500 `ServiceException` that gives the reason. Everything the function itself does wrong is returned as a normal invocation result with `X-Amz-Function-Error` set, as Lambda does:

| What happened | `errorType` | `errorMessage` |
|---|---|---|
| The handler raised or threw | the exception's type | the exception's message |
| The handler ran past `Timeout` | `Sandbox.Timedout` | `Task timed out after N.00 seconds` |
| The process ran out of memory | `Runtime.ExitError` | `Runtime exited with error: signal: killed` |
| The process exited | `Runtime.ExitError` | `Runtime exited with error: exit status N` |
| Initialisation failed (the handler can't be imported, say) | the bootstrap's type, such as `Runtime.ImportModuleError` | the reason |

After a timeout, the pod is deleted rather than reused, and the next invocation gets a fresh one.

### The Runtime API

OpenSnack serves the Runtime API on its own listener (`OPENSNACK_LAMBDA_RUNTIME_LISTEN_ADDR`, default `:9001`), separate from the AWS API. Besides the four standard endpoints, it serves the extensions the bundled images use:

| Request | Purpose |
|---|---|
| `GET /2018-06-01/runtime/invocation/next` | standard |
| `POST /2018-06-01/runtime/invocation/{id}/response` | standard |
| `POST /2018-06-01/runtime/invocation/{id}/error` | standard |
| `POST /2018-06-01/runtime/init/error` | standard |
| `GET /2018-06-01/runtime/opensnack/init` | `{"env": {...}}`: the function's environment, with `AWS_*` credentials and Lambda's own variables |
| `GET /2018-06-01/runtime/opensnack/code` | the deployment package, as a zip |
| `POST /2018-06-01/runtime/invocation/{id}/logs` | output during an invocation, appended |
| `POST /2018-06-01/runtime/init/logs` | output before the first invocation |

Each request must carry `X-Opensnack-Runtime-Token: <id>.<secret>`. OpenSnack generates the token when it creates the pod and passes it in the pod's environment as `_OPENSNACK_RUNTIME_TOKEN`. A token is good only for its own pod. With `OPENSNACK_LAMBDA_K8S_BIND_POD_IP` on (the default), it is also accepted only from that pod's IP. Other requests get a 403. Each pod can have at most 8 requests in progress at once.

Responses can optionally carry `X-Opensnack-Max-Rss-Kb` with the process's peak memory. The bundled bootstraps send it, and OpenSnack reports it as `Max Memory Used` in the REPORT line.

## Deploying

You need:

- a cluster that enforces NetworkPolicy (k3s does; check your CNI);
- a sandboxing RuntimeClass for untrusted code: a microVM runtime or a user-space kernel. The cluster's default runtime is a container, not a sandbox;
- a registry the nodes can pull from.

### 1. Build the runtime images

There is one image per runtime family. Both are built from [deploy/lambda-runtime/Dockerfile](../deploy/lambda-runtime/Dockerfile):

```bash
docker build -f deploy/lambda-runtime/Dockerfile --target python -t REGISTRY/opensnack-lambda-python:TAG .
docker build -f deploy/lambda-runtime/Dockerfile --target nodejs -t REGISTRY/opensnack-lambda-nodejs:TAG .
docker push REGISTRY/opensnack-lambda-python:TAG
docker push REGISTRY/opensnack-lambda-nodejs:TAG
```

Build them for the nodes' architecture (`--platform linux/amd64`, say). Configure the executor with the pushed digests, not the tags:

```bash
docker inspect --format '{{index .RepoDigests 0}}' REGISTRY/opensnack-lambda-python:TAG
```

The Python image is Python 3.12 with boto3, and serves every `python3.x` runtime. The Node.js image is Node 22 with the DynamoDB, S3, SQS, SNS and STS clients of SDK v3 on `NODE_PATH`, and serves every `nodejs*.x` runtime. `require()` finds the preinstalled clients, but ES module `import` doesn't, so functions written as ES modules must bundle their SDK. The Dockerfile pins only major versions of the SDKs, so the digest is what makes a build reproducible.

To run a different language version, change the base image. Each image may serve any runtime of its family: the executor doesn't check versions within a family.

### 2. Apply the manifests

[deploy/k8s/lambda/](../deploy/k8s/lambda/) is a kustomization:

| File | What it creates |
|---|---|
| `namespace.yaml` | `opensnack-lambda`, with the `restricted` Pod Security Standard enforced |
| `serviceaccount.yaml` | the sandboxes' service account, with no token mounted and no role bindings |
| `rbac.yaml` | a Role and RoleBinding: OpenSnack's service account may create, get, list, watch and delete pods in `opensnack-lambda`, and nothing else |
| `networkpolicy.yaml` | sandboxes accept no connections, and may connect only to OpenSnack (4566, 9001) and to cluster DNS |
| `opensnack.yaml` | OpenSnack's service account, the `opensnack-runtime` Service, a policy that admits only sandboxes to port 9001, and a single-replica `Recreate` Deployment with the executor settings |

`opensnack.yaml` is a sketch of the OpenSnack side. Merge its settings into your own manifests, which hold the database, the credentials and the AWS API Service. Then change, at least:

- `OPENSNACK_LAMBDA_K8S_RUNTIME_CLASS` to your RuntimeClass;
- `OPENSNACK_LAMBDA_K8S_IMAGE_PYTHON` and `OPENSNACK_LAMBDA_K8S_IMAGE_NODEJS` to your digests;
- the namespace names in `rbac.yaml` and the policies, if OpenSnack doesn't run in `opensnack`.

```bash
kubectl apply -k deploy/k8s/lambda
```

[deploy/k8s/lambda-it/](../deploy/k8s/lambda-it/) is the overlay the integration tests use. It is a complete working example, with Postgres and test credentials.

### 3. Point functions at OpenSnack

Functions call AWS through the SDK, so their AWS host names must reach OpenSnack. There are two ways to do that.

- **DNS.** Have the sandboxes resolve `*.amazonaws.com` to OpenSnack, for example with a CoreDNS rewrite or a stub zone. They then connect over HTTPS, so whatever answers needs a certificate for those names. If it comes from your own CA, put the CA in a ConfigMap in the sandbox namespace and name it:

  ```bash
  kubectl -n opensnack-lambda create configmap opensnack-ca --from-file=ca.crt=ca.pem
  ```

  ```yaml
  - { name: OPENSNACK_LAMBDA_K8S_CA_CONFIGMAP, value: opensnack-ca }
  ```

  The pod mounts it, `lambda-init` appends it to the system CAs, and the function's environment points `AWS_CA_BUNDLE`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE` and `NODE_EXTRA_CA_CERTS` at the bundle.

  If the names resolve to an ingress controller rather than to OpenSnack's Service, the sandboxes' NetworkPolicy must allow that controller. `networkpolicy.yaml` has a commented example. The controller must pass `Host` through unchanged, because SigV4 signs it.

- **An endpoint override.** Set `OPENSNACK_LAMBDA_AWS_ENDPOINT_URL`, for example to `http://opensnack.opensnack.svc.cluster.local:4566`. Every function then gets `AWS_ENDPOINT_URL`, which current SDKs honour. This is simpler, but code that builds its own endpoints, or uses an old SDK, won't follow it. Use it for development.

### 4. Check it

Create a function and invoke it twice. The first call waits for a cold start; the second should take milliseconds.

```bash
kubectl -n opensnack-lambda get pods -L opensnack/runtime,opensnack/tenant
```

The [integration tests](#testing) exercise everything below, including escape attempts, and are the quickest way to check a cluster's policies.

## Sizing and tuning

**Memory and CPU.** A pod's memory request and limit are both the function's `MemorySize`. That gives the pod Guaranteed QoS, and running out of memory kills it, as in Lambda. The CPU limit is `MemorySize ÷ OPENSNACK_LAMBDA_K8S_MB_PER_VCPU` vCPUs (1769 MB is one vCPU, as in Lambda). A 128 MB function therefore gets 72 millicores, which makes interpreter start-up slow under a microVM runtime. `OPENSNACK_LAMBDA_K8S_MIN_CPU_MILLIS` sets a floor, for example 250. Add the per-pod overhead of your RuntimeClass (its `overhead.podFixed`) when you work out how many pods a node holds.

**Guest overhead.** A microVM runtime that sizes the guest from the pod (Kata with `static_sandbox_resource_mgmt = true`) spends part of the pod's memory on the guest kernel and agent: a 128 MiB pod leaves about 66 MB, too little for Python with boto3, and the guest's default process limit (248) is under the usual `OPENSNACK_LAMBDA_K8S_MAX_PROCS`. `OPENSNACK_LAMBDA_K8S_GUEST_OVERHEAD_MB` adds that much to every pod's memory so the function keeps its `MemorySize`; 128 gives a 128 MB function a 197 MB guest. The CPU share still follows `MemorySize`. lambda-init never raises a limit, so a process limit above the guest's is capped rather than failing the start.

**Pod count.** `OPENSNACK_LAMBDA_K8S_MAX_PODS` caps sandboxes across all tenants, warm or busy. At the cap, OpenSnack deletes the least recently used idle pod to make room. If no pod is idle, the invocation is throttled (429 `TooManyRequestsException`). Size the cap from node memory: with 128 MB functions plus runtime overhead, a 60 GB node holds a few hundred pods. Pod start-up is usually limited by CPU first.

**Concurrency.** `OPENSNACK_LAMBDA_CONCURRENCY_PER_NAMESPACE` (default 2) caps one tenant's invocations in flight, so no single tenant can take every pod. A function's `ReservedConcurrentExecutions` caps it further, and 0 disables it. The throttle response carries `Reason`, as Lambda's does: `ConcurrentInvocationLimitExceeded` or `ReservedFunctionConcurrentInvocationLimitExceeded`.

**What a throttle does to each caller:**

- A synchronous invoke gets a 429. AWS SDKs retry it.
- An asynchronous invoke is queued, up to `OPENSNACK_LAMBDA_ASYNC_QUEUE_PER_NAMESPACE` per tenant. It is retried with backoff until `MaximumEventAgeInSeconds`. Function errors are retried `MaximumRetryAttempts` times (default 2), after one minute and then two.
- An SQS event source leaves the messages on the queue. They come back after the visibility timeout.

**Cold starts.** A cold start is pod scheduling, sandbox boot, image start, fetching the code and loading the handler. It doesn't count against the function's `Timeout`. It does count against API Gateway's integration timeout (30 seconds by default), which covers the whole call. `OPENSNACK_LAMBDA_K8S_START_TIMEOUT` (default 1 minute) is how long a cold start may take before the waiting invocation fails. Pods use `imagePullPolicy: IfNotPresent`, so pull the images onto the nodes ahead of time, and a cold start never includes a pull.

Measured under runc, in k3d on an arm64 laptop:

| Invoke | Time |
|---|---|
| Python, cold | 4.7–8.2 s |
| Python, warm | 5–95 ms |
| Node.js, cold | 2.9–3.4 s |
| Node.js, warm | 4–69 ms |
| API Gateway → Lambda, cold | 4.9–6.2 s |

A microVM runtime adds its boot time to every cold start.

**Idle pods.** A warm pod holds its memory until `OPENSNACK_LAMBDA_K8S_IDLE_TTL` (default 5 minutes). A shorter TTL frees memory sooner but means more cold starts. Pods are also replaced after `OPENSNACK_LAMBDA_K8S_MAX_POD_LIFETIME` (default 1 hour), which bounds the lifetime of their credentials.

**Processes.** Kubernetes has no per-pod process limit, so each pod runs as a random UID from `OPENSNACK_LAMBDA_K8S_UID_BASE`/`_UID_COUNT`, and `lambda-init` sets `RLIMIT_NPROC` to `OPENSNACK_LAMBDA_K8S_MAX_PROCS`. Also set the kubelet's `podPidsLimit` (k3s: `--kubelet-arg=pod-max-pids=1024`) as a node-wide backstop against fork bombs.

## Operating

**One replica.** Run OpenSnack as a single replica with the `Recreate` strategy. At start-up it deletes every sandbox pod with its `opensnack/instance` label that it didn't create, so two replicas would delete each other's pods. Each would also enforce its own caps. To run two OpenSnacks against one cluster, give them different `OPENSNACK_LAMBDA_K8S_INSTANCE` values, or different sandbox namespaces.

**Restarts.** Restarting OpenSnack deletes all sandbox pods, loses queued asynchronous invocations and in-memory STS sessions, and fails invocations in flight.

**Finding a tenant's pods.** Pods carry hashes, not names:

| Label | Value |
|---|---|
| `app.kubernetes.io/managed-by` | `opensnack` |
| `app.kubernetes.io/component` | `lambda-sandbox` |
| `opensnack/instance` | `OPENSNACK_LAMBDA_K8S_INSTANCE` |
| `opensnack/tenant` | first 16 hex digits of SHA-256 of `tenant\0<namespace>\0` |
| `opensnack/function` | first 16 hex digits of SHA-256 of `function\0<namespace>\0<region>\0<function name>\0` |
| `opensnack/runtime` | `python` or `nodejs` |

```bash
t=$(printf 'tenant\0%s\0' "$NAMESPACE" | shasum -a 256 | cut -c1-16)
kubectl -n opensnack-lambda get pods -l opensnack/tenant=$t
```

Deleting a namespace through the inspection API (`DELETE /_opensnack/v1/namespaces/{ns}`) kills its pods at once. Deleting a pod by hand is safe: its invocation fails with `Runtime.ExitError`, and the next invocation starts a new pod.

**Logs.** A function's output goes to its CloudWatch log group (`/aws/lambda/<name>`) and, for `LogType: Tail`, into the invoke response. Up to `OPENSNACK_LAMBDA_MAX_LOG_BYTES` is kept per invocation. OpenSnack never reads pod logs (its Role has no `pods/log`). Output that never reaches the Runtime API, such as a crash inside `lambda-init`, is only in `kubectl logs`.

**Troubleshooting.**

| Symptom | Likely cause |
|---|---|
| OpenSnack doesn't start: `OPENSNACK_LAMBDA_K8S_RUNTIME_API is required` | Set it to the `opensnack-runtime` Service's `host:port`. |
| `lambda sandboxes: listing pods ... forbidden` at start-up | The RoleBinding doesn't name OpenSnack's service account, or names the wrong namespace. |
| Pods are rejected with `violates PodSecurity` | A webhook or policy is changing the pod spec. The executor's pods satisfy `restricted` as built. |
| Every cold start fails after `START_TIMEOUT` | Pods can't reach the Runtime API. Check the NetworkPolicies, the Service, and `kubectl logs` of a sandbox. |
| Runtime API returns 403 for a live pod | Traffic to OpenSnack is source-NATed, so requests don't come from the pod's IP. Fix the routing, or set `OPENSNACK_LAMBDA_K8S_BIND_POD_IP=false`. |
| Functions' SDK calls fail with certificate errors | The CA ConfigMap is missing from the sandbox namespace, or holds the wrong CA. |
| Functions' SDK calls time out | Their AWS host names don't resolve to something the NetworkPolicy allows. |
| `ServiceException` with `ErrImagePull` | The node can't pull the configured digest. |
| Frequent 429s | `OPENSNACK_LAMBDA_K8S_MAX_PODS` or the per-namespace cap is too low for the load. |

## Security model

The boundary between tenants is the RuntimeClass. Everything else is defence in depth. With the cluster's default runtime (runc), function code shares the node's kernel, which is fine for CI and trusted code only.

**What a function can reach:**

- the AWS API, as its own role, in its own namespace and account. Its credentials come from OpenSnack's STS and can't reach another namespace. `AssumeRole` from inside a function finds roles only in the caller's own namespace and account;
- the Runtime API, as its own pod only. It can read its own token, and with it take its own function's later invocations. It can't touch any other pod's;
- whatever else is public on OpenSnack, such as other tenants' unauthenticated API Gateway or CloudFront endpoints, as anyone on the internet could in AWS.

**What it can't reach**, provided the NetworkPolicy is enforced: the internet, the Kubernetes API, other pods, and node services. It has no service account token, no capabilities, no writable root filesystem, and no host mounts. Its code and environment are fetched from the Runtime API, not stored in the pod object.

**Known gaps:**

- The Runtime API token sits in the pod spec, so anyone who can read pods in the sandbox namespace can read it. With the IP binding, a token alone is not enough to use it.
- Without NetworkPolicy enforcement, every pod in the cluster can reach the Runtime API port. The token still guards it, but the pods also lose their egress restrictions. Check enforcement before relying on it.
- `AssumeRole` doesn't evaluate trust policies, so a function can assume any role in its own account. That doesn't cross tenants, but it isn't AWS's behaviour.
- Asynchronous queues and STS sessions are in memory.
- `RLIMIT_NPROC` is set with the Linux syscall numbers for amd64 and arm64. Other architectures fall back to the kubelet's `podPidsLimit`.

## Testing

Unit tests run against a fake Kubernetes client (`internal/kube.Fake`) and need no cluster:

```bash
go test ./internal/api/lambda/ ./internal/kube/
```

The integration tests need Docker, k3d and kubectl. They create a throwaway k3d cluster called `opensnack-lambda-it` with its own kubeconfig. They never use `~/.kube/config`.

```bash
deploy/k8s/lambda-it/run.sh
KEEP_CLUSTER=1 WORK=/some/dir deploy/k8s/lambda-it/run.sh   # keep the cluster for reruns
```

The script builds the OpenSnack and runtime images and imports them into the cluster. It deploys the `lambda-it` overlay, which runs OpenSnack in strict auth mode with the NetworkPolicies, then runs the tests tagged `k8sintegration`. They cover:

- Python and Node.js payloads, and cold versus warm timings;
- `FunctionError: Unhandled`;
- a timeout that replaces the pod;
- running out of memory;
- SQS → Lambda → DynamoDB with role credentials, and SNS → Lambda;
- API Gateway → Lambda;
- escape attempts: the internet, the Kubernetes API, another pod's Runtime API token, and the function's own credentials against another tenant;
- pods being removed when a namespace or function is deleted.

CI runs them in the `lambda-kubernetes` job.
