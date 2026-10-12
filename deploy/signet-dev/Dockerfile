# syntax=docker/dockerfile:1
#
# Disposable Signet for Bahia's dev compose stack and the joined assistant e2e
# harness: signetd and signetctl built from a nostrc checkout (build context
# "nostrc", Signet >= 0.7.0; build context "scripts" is Bahia's scripts/), a private relay (nak serve) and
# scripts/signet_dev_seed.py, which generates every key and seeds the owner,
# the requester's delivery grant and the daemon's system credentials. It
# holds throwaway keys only; never use it for real credentials.
#
#   docker build -f deploy/signet-dev/Dockerfile \
#     --build-context nostrc=<nostrc checkout> --build-context scripts=scripts \
#     -t bahia-signet-dev:local deploy/signet-dev
#
# Signet's own Dockerfile also runs its Meson suite; that is Signet's CI, not
# a prerequisite for a disposable dev instance, so this build does not.

FROM golang:1.26-bookworm AS nak
RUN GOBIN=/out go install github.com/fiatjaf/nak@v0.19.7

FROM debian:bookworm-slim AS signet
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake ninja-build pkg-config python3-pip git \
    libglib2.0-dev libjson-glib-dev libjansson-dev libssl-dev libsqlite3-dev libsqlcipher-dev \
    libsodium-dev libmicrohttpd-dev libsecp256k1-dev libcurl4-openssl-dev libwebsockets-dev \
    libnsync-dev libgit2-dev ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && pip3 install --no-cache-dir --break-system-packages "cmake>=3.30,<3.31"
COPY --from=nostrc . /src
RUN cmake -S /src -B /build -G Ninja -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=OFF \
      -DSIGNET_ENABLE=ON -DSIGNET_USE_SQLCIPHER=ON -DSIGNET_ENABLE_PASSKEYS=OFF -DBUILD_NOSTR_GTK=OFF \
      -DBUILD_APPS=OFF -DBUILD_LIBMARMOT=OFF -DBUILD_MARMOT_GOBJECT=OFF \
      -DWITH_NOSTRDB=OFF -DLIBNOSTR_WITH_NOSTRDB=OFF \
  && cmake --build /build --target signetd signetctl \
  && mkdir -p /out/lib \
  && cp /build/signet/signetd /build/signet/signetctl /out/ \
  && ldd /out/signetd /out/signetctl | awk '$3 ~ "^/build/" {print $3}' | sort -u | xargs -r cp -L -t /out/lib/

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libglib2.0-0 libjson-glib-1.0-0 libjansson4 libssl3 libsqlite3-0 libsqlcipher0 libsodium23 \
    libmicrohttpd12 libsecp256k1-1 libnsync1 libwebsockets17 libcurl4 libcap2 ca-certificates python3 \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd -r signet && useradd -r -g signet -d /data -s /usr/sbin/nologin signet
COPY --from=signet /out/signetd /out/signetctl /usr/local/bin/
COPY --from=signet /out/lib/ /usr/local/lib/
COPY --from=nak /out/nak /usr/local/bin/nak
COPY --from=scripts signet_dev_seed.py signet_live_interop.py /opt/bahia/
RUN ldconfig && if ldd /usr/local/bin/signetd /usr/local/bin/signetctl | grep 'not found'; then exit 1; fi
VOLUME ["/data", "/shared"]
HEALTHCHECK --interval=2s --timeout=2s --retries=60 CMD ["test", "-f", "/shared/ready"]
ENTRYPOINT ["python3", "/opt/bahia/signet_dev_seed.py", "serve"]
CMD ["--state", "/data", "--shared", "/shared"]
