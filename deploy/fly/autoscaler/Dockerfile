# fly-autoscaler built from source, with the Prometheus-collector timeout fix.
#
# Why not the official image: flyio/fly-autoscaler:0.3 ignores the reconcile
# timeout in its Prometheus collector (prometheus/prometheus.go:48 queries with
# context.Background()) and its HTTP client has no response timeout, so one
# Prometheus request that is accepted but never answered blocks the reconcile
# loop forever. Observed in prod on 2026-09-28/29: 37 h without a single
# "reconciling" line while the Machine stayed "started", including during an
# exam whose load called for 6 workers. Fix + regression test upstream:
#   https://github.com/superfly/fly-autoscaler/pull/46
# Until that ships in a release, build the patched fork pinned to the fix
# commit. Once released, drop this file and go back to
#   [build] image = "flyio/fly-autoscaler:<version>"
FROM golang:1.22-alpine AS build
ARG FAS_REPO=https://github.com/ferkrodriguez98/fly-autoscaler.git
ARG FAS_COMMIT=689a233eb87b850e39f9d4d9ba5c3ede06c4833f
RUN apk add --no-cache git \
 && git clone --quiet "$FAS_REPO" /src \
 && git -C /src checkout --quiet "$FAS_COMMIT"
WORKDIR /src
RUN CGO_ENABLED=0 go build \
      -ldflags "-X 'main.Version=0.3.2+pr46' -X 'main.Commit=${FAS_COMMIT}'" \
      -o /usr/local/bin/fly-autoscaler ./cmd/fly-autoscaler

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /usr/local/bin/fly-autoscaler /usr/local/bin/fly-autoscaler
ENTRYPOINT ["fly-autoscaler"]
CMD ["serve"]
