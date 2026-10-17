# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Lambda runtime images for opensnack's Kubernetes executor, one per
# runtime family. Build from the repository root:
#
#   docker build -f deploy/lambda-runtime/Dockerfile --target python -t opensnack-lambda-python .
#   docker build -f deploy/lambda-runtime/Dockerfile --target nodejs -t opensnack-lambda-nodejs .
#
# Push them, and configure the executor with their digests
# (OPENSNACK_LAMBDA_K8S_IMAGE_PYTHON=registry/opensnack-lambda-python@sha256:...).
#
# The images run as any non-root UID with a read-only root filesystem:
# everything they need is world-readable, and the only writable places are
# the /tmp and /var/task volumes the pod mounts.

FROM golang:1.25-alpine AS init
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/lambda-init cmd/lambda-init
COPY internal/unzip internal/unzip
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lambda-init ./cmd/lambda-init

# python3.x functions. The AWS SDK (boto3) is preinstalled, as in Lambda.
FROM python:3.12-slim AS python
RUN pip install --no-cache-dir --disable-pip-version-check "boto3>=1.35,<2" \
 && find / -xdev -name "__pycache__" -prune -exec rm -rf {} + 2>/dev/null; true
COPY --from=init /lambda-init /opt/opensnack/lambda-init
COPY internal/api/lambda/bootstrap/bootstrap.py /var/runtime/bootstrap.py
ENV PYTHONDONTWRITEBYTECODE=1
WORKDIR /var/task
ENTRYPOINT ["/opt/opensnack/lambda-init"]

# nodejs.x functions. A few AWS SDK v3 clients are preinstalled, on
# NODE_PATH (so require() finds them; ES module imports need them bundled).
FROM node:22-slim AS nodejs
WORKDIR /opt/nodejs
RUN npm install --omit=dev --no-audit --no-fund \
      @aws-sdk/client-dynamodb@3 @aws-sdk/lib-dynamodb@3 @aws-sdk/client-s3@3 \
      @aws-sdk/client-sqs@3 @aws-sdk/client-sns@3 @aws-sdk/client-sts@3 \
 && npm cache clean --force
ENV NODE_PATH=/opt/nodejs/node_modules
COPY --from=init /lambda-init /opt/opensnack/lambda-init
COPY internal/api/lambda/bootstrap/bootstrap.mjs /var/runtime/bootstrap.mjs
WORKDIR /var/task
ENTRYPOINT ["/opt/opensnack/lambda-init"]
