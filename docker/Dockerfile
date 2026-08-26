FROM debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       bash ca-certificates coreutils findutils git procps python3 ripgrep \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home --shell /bin/bash scrap

USER 1000:1000
WORKDIR /workspace
ENV HOME=/workspace \
    PATH=/usr/local/bin:/usr/bin:/bin \
    SHELL=/bin/bash \
    TMPDIR=/workspace/.scrap/tmp \
    SCRAP_WORKSPACE_ROOT=/workspace

CMD ["sleep", "infinity"]
