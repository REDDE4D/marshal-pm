# Image for the Marshal central fleet server + dashboard. The pre-built
# (CGO-free) binary is supplied by GoReleaser's build context — this Dockerfile
# only assembles the runtime.
#
# NOTE: this is the *server* component (fleet aggregation + web dashboard), not
# the per-host agent — an agent supervises host processes and belongs on the
# host, not in a container.
#
#   docker run -p 9000:9000 -p 9001:9001 -v marshal-data:/data \
#     ghcr.io/redde4d/marshal:latest
#
# First-run auth setup happens with the server stopped, e.g.:
#   docker run --rm -v marshal-data:/data ghcr.io/redde4d/marshal \
#     server passwd --data-dir /data/marshal-server
FROM alpine:3.20

# ca-certificates: outbound TLS for notification webhooks/Slack/Telegram.
# openssh-keygen: SSH deploy-credential generation in the credential store.
RUN apk add --no-cache ca-certificates openssh-keygen \
    && adduser -D -H -u 10001 marshal

COPY marshal /usr/bin/marshal

ENV XDG_DATA_HOME=/data
VOLUME /data
EXPOSE 9000 9001

ENTRYPOINT ["/usr/bin/marshal"]
CMD ["server", "--listen", ":9000", "--http-listen", ":9001"]
