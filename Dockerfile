# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

# BuildKit consumes this predefined build argument to normalize image config
# and history timestamps when release automation supplies the tag commit epoch.
ARG SOURCE_DATE_EPOCH

FROM node:22-alpine@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS web-build
WORKDIR /src/webapp
COPY webapp/package.json webapp/package-lock.json ./
RUN npm ci
COPY webapp/ ./
RUN npm run build

FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS server-build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ENV GOTOOLCHAIN=local
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w \
      -X github.com/hkjang/moyro/server/internal/buildinfo.Version=${VERSION} \
      -X github.com/hkjang/moyro/server/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/hkjang/moyro/server/internal/buildinfo.BuildDate=${BUILD_DATE}" \
    -o /out/moyro ./cmd/moyro
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/fakeoidc ./cmd/fakeoidc
RUN mkdir -p /runtime/var/lib/moyro/files /runtime/var/lib/moyro/plugins

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS fake-oidc
COPY --from=server-build --chown=65532:65532 /out/fakeoidc /usr/local/bin/fakeoidc
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fakeoidc"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="moyro" \
      org.opencontainers.image.description="Self-hosted Mattermost-compatible collaboration service" \
      org.opencontainers.image.source="https://github.com/hkjang/moyro" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"
COPY --from=server-build --chown=65532:65532 /out/moyro /usr/local/bin/moyro
COPY --from=server-build --chown=65532:65532 /runtime/var/lib/moyro /var/lib/moyro
COPY --from=web-build --chown=65532:65532 /src/webapp/dist /opt/moyro/web
WORKDIR /var/lib/moyro
USER 65532:65532
EXPOSE 8065
VOLUME ["/var/lib/moyro"]
ENTRYPOINT ["/usr/local/bin/moyro"]
