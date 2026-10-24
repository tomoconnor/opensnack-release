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
#
# Both are distroless: the runtime, its SDK and lambda-init, with no shell
# or package manager. The python and node -slim images carry a whole Debian
# base (util-linux, perl, ncurses, systemd libraries) that a function never
# uses and that scanners flag.

FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS init
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/lambda-init cmd/lambda-init
COPY internal/unzip internal/unzip
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lambda-init ./cmd/lambda-init

# python3.x functions: the official CPython 3.12 build, with the AWS SDK
# (boto3) preinstalled as in Lambda, copied onto distroless/cc (glibc and
# OpenSSL). Extension modules a function has no use for (curses, readline,
# dbm, tkinter, _uuid; uuid works without it) are dropped, and with them
# their libraries. The Debian libraries that remain are copied with their
# dpkg records, so image scanners still see them.
FROM python:3.12-slim@sha256:a0f57064cbdcd6285fa45f51237ab0b35ea8ebaed06a1f5973e89e19dfb86704 AS python-build
RUN pip install --no-cache-dir --disable-pip-version-check "boto3>=1.35,<2"
RUN set -eu; \
    py=/usr/local/lib/python3.12; \
    multiarch=$(python3 -c 'import sysconfig; print(sysconfig.get_config_var("MULTIARCH"))'); \
    rm -rf $py/ensurepip $py/idlelib $py/tkinter $py/turtledemo $py/test $py/site-packages/pip $py/site-packages/pip-* \
      $py/lib-dynload/_curses* $py/lib-dynload/readline.* $py/lib-dynload/_dbm.* $py/lib-dynload/_gdbm.* \
      $py/lib-dynload/_tkinter.* $py/lib-dynload/_uuid.*; \
    find / -xdev -name __pycache__ -prune -exec rm -rf {} +; \
    mkdir -p /out/usr/local/bin /out/usr/local/lib /out/usr/lib/$multiarch /out/var/lib/dpkg/status.d; \
    cp -a /usr/local/bin/python3 /usr/local/bin/python3.12 /out/usr/local/bin/; \
    cp -a $py /out/usr/local/lib/; \
    cp -a /usr/local/lib/libpython3.12.so* /out/usr/lib/$multiarch/; \
    # Libraries the interpreter and its modules load, less what distroless/cc has.
    for lib in $(ldd /usr/local/bin/python3.12 $py/lib-dynload/*.so | awk '$2 == "=>" {print $3}' | sort -u); do \
      case $(basename $lib) in \
        libc.so*|libm.so*|libdl.so*|libpthread.so*|librt.so*|ld-linux*|libgcc_s.so*|libstdc++.so*|libssl.so*|libcrypto.so*|libpython3.12.so*) continue ;; \
      esac; \
      cp -L $lib /out/usr/lib/$multiarch/; \
      pkg=$(dpkg -S "$(readlink -f $lib)" | cut -d: -f1); \
      dpkg -s $pkg >/out/var/lib/dpkg/status.d/$pkg; \
    done; \
    ls /out/usr/lib/$multiarch /out/var/lib/dpkg/status.d

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:3ee00291cf386ec52a552e6a858d555debd74776eb4bfa53fa3df2cd6677515e AS python
COPY --from=python-build /out/ /
COPY --from=init /lambda-init /opt/opensnack/lambda-init
COPY internal/api/lambda/bootstrap/bootstrap.py /var/runtime/bootstrap.py
ENV PYTHONDONTWRITEBYTECODE=1
WORKDIR /var/task
ENTRYPOINT ["/opt/opensnack/lambda-init"]

# nodejs.x functions. A few AWS SDK v3 clients are preinstalled, on
# NODE_PATH (so require() finds them; ES module imports need them bundled).
# They're installed with npm in node:22-slim and copied onto distroless's
# Node 22 (the same release), which has no npm.
FROM node:22-slim@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d AS nodejs-build
WORKDIR /opt/nodejs
RUN npm install --omit=dev --no-audit --no-fund \
      @aws-sdk/client-dynamodb@3 @aws-sdk/lib-dynamodb@3 @aws-sdk/client-s3@3 \
      @aws-sdk/client-sqs@3 @aws-sdk/client-sns@3 @aws-sdk/client-sts@3 \
 && npm cache clean --force

FROM gcr.io/distroless/nodejs22-debian13:nonroot@sha256:10747a1323be68f206aed893599d43e6697ad67257e7c42703553f99fbce3d33 AS nodejs
COPY --from=nodejs-build /opt/nodejs /opt/nodejs
# lambda-init finds node on PATH; distroless keeps it in /nodejs/bin.
ENV NODE_PATH=/opt/nodejs/node_modules \
    PATH=/nodejs/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
COPY --from=init /lambda-init /opt/opensnack/lambda-init
COPY internal/api/lambda/bootstrap/bootstrap.mjs /var/runtime/bootstrap.mjs
WORKDIR /var/task
ENTRYPOINT ["/opt/opensnack/lambda-init"]
