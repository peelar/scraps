# OpenShell's maintained agent image supplies the users, supervisor-compatible
# layout, network tooling, common development tools, and supported agent CLIs.
# Pin by digest so `make up` is reproducible even when the community tag moves.
FROM ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e

USER root
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ripgrep \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /workspace \
    && chmod 1777 /workspace

ENV HOME=/workspace \
    TMPDIR=/workspace/.scrap/tmp \
    SCRAP_WORKSPACE_ROOT=/workspace
WORKDIR /workspace
USER sandbox
