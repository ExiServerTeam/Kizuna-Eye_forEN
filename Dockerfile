# Kizuna-Eye reproducible build environment
#
# 目的: ホストに Go を入れず、固定ツールチェーン（go1.27.2）で host 4 バイナリを
# 再現可能にビルドする。CI や E2E の土台として使う。
#
#   docker build -t kizuna-eye-build .
#   docker run --rm kizuna-eye-build            # /out にビルド済みバイナリ
#
# 注意: Go プラグイン (.so) は別リポジトリ（Kizuna-Security / Kizuna-Backup LITE）
# のソースが要るため、この Dockerfile は host のみを対象とする。プラグインを
# 含める場合は build-all.sh を combined go.work 下で実行すること（ABI 一致）。
FROM golang:1.27-bookworm AS build

# CGO は plugin パッケージに必須（go build のみなら不要だが、host は plugin を
# 扱うため揃える）。build-essential は cgo に必要。
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends build-essential ca-certificates git \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY . .

# -trimpath / -buildvcs=false / -s -w を全バイナリで統一（build.sh と同じ方針）。
# GOTOOLCHAIN=local でイメージ内の Go を使う（ネット取得を避ける）。
ENV CGO_ENABLED=1 \
    GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false

RUN mkdir -p /out \
 && go build -trimpath -ldflags "-s -w -X Kizuna-Eye/internal/api.Version=$(cat VERSION)" -o /out/agent_linux ./cmd/agent \
 && go build -trimpath -ldflags "-s -w -X Kizuna-Eye/internal/api.Version=$(cat VERSION)" -o /out/dashboard_linux ./cmd/dashboard \
 && go build -trimpath -ldflags "-s -w -X Kizuna-Eye/internal/api.Version=$(cat VERSION)" -o /out/plugin-inspect ./cmd/plugin-inspect \
 && go build -trimpath -ldflags "-s -w -X Kizuna-Eye/internal/api.Version=$(cat VERSION)" -o /out/plugin-sign ./cmd/plugin-sign

FROM debian:bookworm-slim
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ /opt/kizuna-eye/bin/
COPY --from=build /src/web/static /opt/kizuna-eye/web/static
COPY --from=build /src/VERSION /opt/kizuna-eye/VERSION
WORKDIR /opt/kizuna-eye
# 既定はバージョン表示のみ（サービス起動は設定が必要）。
ENTRYPOINT ["/opt/kizuna-eye/bin/dashboard_linux", "--help"]
