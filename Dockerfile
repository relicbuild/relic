# syntax=docker/dockerfile:1

ARG INDEXER_PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"

FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/ ./
RUN --mount=type=cache,id=relic-pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store && pnpm generate \
 && node scripts/third-party-notices.ts > /src/web-notices.txt

FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,id=relic-go-mod,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
COPY migrations/ migrations/
COPY --from=web /src/web/.output/public/ internal/app/ui/dist/
ARG VERSION=dev
RUN --mount=type=cache,id=relic-go-mod,target=/go/pkg/mod --mount=type=cache,id=relic-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/relic ./cmd/relic

FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS indexer
ARG INDEXER_PLATFORMS
WORKDIR /src
COPY go.mod go.sum* ./
COPY api/adapters/artifactory/xray/xraymap/ api/adapters/artifactory/xray/xraymap/
COPY api/adapters/artifactory/relic-indexer/ api/adapters/artifactory/relic-indexer/
WORKDIR /src/api/adapters/artifactory/relic-indexer
RUN --mount=type=cache,id=relic-go-mod,target=/go/pkg/mod go mod download
RUN --mount=type=cache,id=relic-go-mod,target=/go/pkg/mod --mount=type=cache,id=relic-go-build,target=/root/.cache/go-build \
    mkdir -p /out && for p in ${INDEXER_PLATFORMS}; do \
      os=${p%/*}; arch=${p#*/}; ext=; [ "$os" = windows ] && ext=.exe; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w" -o /out/relic-indexer-$os-$arch$ext . || exit 1; \
    done

FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS govulncheck
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,id=relic-go-mod,target=/go/pkg/mod --mount=type=cache,id=relic-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go install golang.org/x/vuln/cmd/govulncheck@v1.8.0 \
 && mkdir -p /out && cp "$(find /go/bin -name govulncheck -type f | head -n 1)" /out/govulncheck

FROM anchore/syft:v1.52.0@sha256:500e2d872ac019436926e8322b4fc1f39441d94d21f6f4046c6ff29b30e8cb02 AS syft
FROM anchore/grype:v0.119.0@sha256:8c2c9234a345577a6d321a4753aa3ee1276d8975c8452d2344a56b57733ecad3 AS grype
FROM zricethezav/gitleaks:v8.30.1@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f AS gitleaks
FROM aquasec/trivy:0.72.0@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f AS trivy

FROM build AS dirs
RUN mkdir -p /out/var/lib/relic/grype /out/var/lib/relic/data /out/etc/relic/tls

FROM build AS notices
ARG TARGETOS TARGETARCH INDEXER_PLATFORMS
COPY tools/thirdpartynotices/ tools/thirdpartynotices/
COPY LICENSE /out/licenses/LICENSE
COPY --from=web /src/web-notices.txt /tmp/web-notices.txt
RUN --mount=type=cache,id=relic-go-mod,target=/go/pkg/mod --mount=type=cache,id=relic-go-build,target=/root/.cache/go-build \
    go run ./tools/thirdpartynotices -output /out/licenses/THIRD_PARTY_NOTICES \
      -platforms "$TARGETOS/$TARGETARCH,$(echo ${INDEXER_PLATFORMS} | tr ' ' ',')" \
      -program syft=github.com/anchore/syft@v1.52.0 \
      -program grype=github.com/anchore/grype@v0.119.0 \
      -program gitleaks=github.com/zricethezav/gitleaks/v8@v8.30.1 \
      -program trivy=github.com/aquasecurity/trivy@v0.72.0 \
      -program govulncheck=golang.org/x/vuln@v1.8.0 \
      -include /tmp/web-notices.txt \
      ./cmd/relic ./api/adapters/artifactory/relic-indexer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/relic /relic
COPY --from=syft /syft /usr/local/bin/syft
COPY --from=grype /grype /usr/local/bin/grype
COPY --from=gitleaks /usr/bin/gitleaks /usr/local/bin/gitleaks
COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy
COPY --from=govulncheck /out/govulncheck /usr/local/bin/govulncheck
COPY --from=dirs --chown=nonroot:nonroot /out/var/lib/relic /var/lib/relic
COPY --from=dirs --chown=nonroot:nonroot /out/etc/relic /etc/relic
COPY --from=indexer /out/ /usr/local/share/relic/indexer/
COPY --from=notices /out/licenses/ /usr/share/licenses/relic/
ENV RELIC_TLS_DIRECTORY=/etc/relic/tls
EXPOSE 8090
ENTRYPOINT ["/relic"]
CMD ["server", "start"]
