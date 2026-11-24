# 運用

このドキュメントは、Flow App Template を Linux の単一バイナリとしてビルドし、`systemd` で運用し、
Litestream v0.5 で Cloudflare R2 に継続レプリケーションし、障害時に復旧する手順をまとめます。
アプリケーションの構造は [ARCHITECTURE.md](ARCHITECTURE.md)、利用手順は
[README.md](../README.md)、実装規約は [AGENTS.md](../AGENTS.md) を参照してください。

## 1. 前提

- 対応 OS: `systemd` を持つ Linux（amd64 または arm64）。
- アプリケーション: Go 1.26 以上でビルドした静的バイナリ。
- データベース: SQLite。`modernc.org/sqlite` は純 Go 実装のため CGO は不要です。
- レプリケーション: Litestream v0.5.x と Cloudflare R2。
- リバースプロキシ: アプリケーション自身は TLS を終端しません。必要に応じて前段のプロキシで
  TLS を終端し、`HTTP_ADDR=127.0.0.1:8080` のようにループバックへバインドしてください。

Docker、PostgreSQL、Redis は使いません。`systemd` の unit と Litestream だけで運用します。

## 2. ビルドと配置

### 2.1 Linux 向け静的バイナリ

`CGO_ENABLED=0` でクロスコンパイルすると、C ライブラリに依存しない静的バイナリになります。

```bash
# amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o flow-api-linux-amd64 ./cmd/server

# arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w" -o flow-api-linux-arm64 ./cmd/server
```

確認します。

```bash
file flow-api-linux-amd64
# ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped

sha256sum flow-api-linux-amd64 flow-api-linux-arm64
```

ストリップ後のバイナリは約 11MB です。`-trimpath` はビルド環境のパスをバイナリに残さず、
`-ldflags "-s -w"` はシンボルと DWARF 情報を削除します。

### 2.2 リリース手順

1. CI（`.github/workflows/ci.yml`）で `gofmt`、`go vet`、race テスト、CGO 無効テスト、ビルド、
   `restore.sh` の構文と実行権限を確認する。
2. 対象アーキテクチャのバイナリをビルドし、チェックサムを記録する。
3. ステージング環境で `/healthz` と `/api/v1/runtime/stats` を確認する。
4. 本番のメンテナンスウィンドウでバイナリを差し替える。手順は「9.2 バイナリの更新」を参照。

## 3. 初回セットアップ

以下は amd64 の例です。arm64 ではバイナリ名を読み替えてください。

### 3.1 ユーザーとディレクトリ

```bash
sudo groupadd --system flow-api 2>/dev/null || true
sudo useradd --system \
  --gid flow-api \
  --home-dir /var/lib/flow-api \
  --shell /usr/sbin/nologin \
  flow-api 2>/dev/null || true

sudo install -d -o root -g flow-api -m 0750 /etc/flow-api
sudo install -d -o flow-api -g flow-api -m 0750 /var/lib/flow-api
sudo install -d -o root -g root -m 0700 /var/backups/flow-api
```

`flow-api.service` の `StateDirectory=flow-api` も `/var/lib/flow-api` を管理しますが、初回から
所有権とモードを確定させるために明示的に作成します。

### 3.2 バイナリ

```bash
sudo install -o root -g root -m 0755 \
  flow-api-linux-amd64 /usr/local/bin/flow-api
```

Litestream v0.5.x の公式リリースバイナリを取得し、同じく `/usr/local/bin/litestream` に配置します。

```bash
sudo install -o root -g root -m 0755 litestream /usr/local/bin/litestream
litestream version
```

### 3.3 環境変数ファイル

```bash
sudo install -o root -g flow-api -m 0640 \
  deploy/systemd/flow-api.env.example /etc/flow-api/flow-api.env
sudoedit /etc/flow-api/flow-api.env
```

環境変数例は [deploy/systemd/flow-api.env.example](../deploy/systemd/flow-api.env.example) です。

`/etc/flow-api/flow-api.env` は `root:flow-api`、モード `0640` のままにします。R2 の秘密鍵を
含めるため、リポジトリや world-readable な場所へ置いてはいけません。`flow-api.service` と
`litestream.service` の両方がこのファイルを読みます。

最低限、次の値を設定します。

```bash
HTTP_ADDR=:8080
DB_PATH=/var/lib/flow-api/flow.db
LITESTREAM_DB_PATH=/var/lib/flow-api/flow.db

R2_ACCOUNT_ID=<Cloudflare アカウント ID>
R2_BUCKET=<R2 バケット名>
R2_PREFIX=flow-api
R2_ACCESS_KEY_ID=<R2 アクセスキー ID>
R2_SECRET_ACCESS_KEY=<R2 シークレットアクセスキー>
```

`DB_PATH` と `LITESTREAM_DB_PATH` は同じファイルを指します。

### 3.4 Litestream 設定

```bash
sudo install -o root -g flow-api -m 0640 \
  deploy/litestream/litestream.yml /etc/flow-api/litestream.yml
```

設定ファイルは [deploy/litestream/litestream.yml](../deploy/litestream/litestream.yml) です。
内容は「6. Litestream v0.5 と Cloudflare R2」で説明します。

### 3.5 systemd unit

```bash
sudo install -o root -g root -m 0644 \
  deploy/systemd/flow-api.service /etc/systemd/system/flow-api.service
sudo install -o root -g root -m 0644 \
  deploy/systemd/litestream.service /etc/systemd/system/litestream.service

sudo systemctl daemon-reload
sudo systemctl enable flow-api.service litestream.service
```

unit は [deploy/systemd/flow-api.service](../deploy/systemd/flow-api.service) と
[deploy/systemd/litestream.service](../deploy/systemd/litestream.service) です。

起動順は `flow-api.service` の `Before=litestream.service` と `litestream.service` の
`After=flow-api.service` で API が先になります。

```bash
sudo systemctl start flow-api.service
sudo systemctl start litestream.service
```

### 3.6 復旧スクリプト

```bash
sudo install -o root -g root -m 0755 \
  deploy/litestream/restore.sh /usr/local/bin/flow-api-restore
```

スクリプトは [deploy/litestream/restore.sh](../deploy/litestream/restore.sh) です。
`restore.sh` は単体では動作せず、`litestream` と設定ファイル、環境変数を必要とします。本番では
`--config /etc/flow-api/litestream.yml` と `--env-file /etc/flow-api/flow-api.env` を明示します。

### 3.7 起動確認

```bash
systemctl status flow-api.service litestream.service --no-pager
journalctl -u flow-api.service -u litestream.service -n 100 --no-pager

curl -fsS http://127.0.0.1:8080/healthz
```

`/healthz` は HTTP 200 を返しても `status` が `degraded` になることがあります。ボディの
`status` と `db.ok` まで確認してください。

## 4. systemd の構成とハードニング

### 4.1 `flow-api.service`

```ini
[Unit]
Description=Flow API single-binary HTTP service
Documentation=https://github.com/aiagate/flow-app-template
After=network-online.target
Wants=network-online.target
Before=litestream.service

[Service]
Type=simple
User=flow-api
Group=flow-api
WorkingDirectory=/var/lib/flow-api
StateDirectory=flow-api
StateDirectoryMode=0750
UMask=0027
EnvironmentFile=/etc/flow-api/flow-api.env
ExecStart=/usr/local/bin/flow-api
Restart=on-failure
RestartSec=3
TimeoutStopSec=30
KillSignal=SIGTERM
# ... ハードニング設定 ...
```

| 設定 | 意図 |
| --- | --- |
| `User` / `Group` | 専用の非特権ユーザーで実行 |
| `WorkingDirectory` | DB の相対パスと `.env` 探索の基準。本番は絶対パスを使う |
| `StateDirectory` | `/var/lib/flow-api` を systemd 管理下に置く。モードは `0750` |
| `UMask=0027` | 新規ファイルをグループ読み取り可、その他は不可に |
| `EnvironmentFile` | R2 認証情報を含む環境変数ファイルを読む。モード `0640` |
| `Restart=on-failure` | 異常終了時に 3 秒後に再起動 |
| `TimeoutStopSec=30` | `SHUTDOWN_TIMEOUT`（既定 10 秒）より長くする |
| `KillSignal=SIGTERM` | アプリケーションの graceful shutdown を起動 |
| `NoNewPrivileges` | setuid/setgid による権限昇格を禁止 |
| `PrivateTmp` | 専用の一時ディレクトリ |
| `ProtectSystem=strict` | `/usr`、`/boot`、`/etc` などを読み取り専用に |
| `ProtectHome` | ホームディレクトリを隠す |
| `ProtectKernelTunables` / `ProtectKernelModules` / `ProtectControlGroups` | カーネル設定の変更を禁止 |
| `ReadWritePaths=/var/lib/flow-api` | DB と WAL の書き込みだけを許可 |
| `LimitNOFILE=65536` | 接続とファイルディスクリプタの上限 |
| `MemoryMax=512M` | アプリケーションのメモリ上限。負荷に応じて調整 |
| `LockPersonality` / `RestrictSUIDSGID` / `RestrictRealtime` | 実行環境の制限 |
| `SystemCallArchitectures=native` | 異なる ABI のシステムコールを禁止 |

`ProtectSystem=strict` と `ReadWritePaths` の組み合わせにより、アプリケーションは
`/var/lib/flow-api` 以外へ書き込めません。`/etc/flow-api` は読み取り専用です。

### 4.2 `litestream.service`

`litestream.service` は同じ `User`、`Group`、`EnvironmentFile`、`ReadWritePaths` を使います。

```ini
[Unit]
Description=Litestream continuous replication for Flow API SQLite
Documentation=https://litestream.io/
After=network-online.target flow-api.service
Wants=network-online.target

[Service]
Type=simple
User=flow-api
Group=flow-api
WorkingDirectory=/var/lib/flow-api
StateDirectory=flow-api
StateDirectoryMode=0750
UMask=0027
EnvironmentFile=/etc/flow-api/flow-api.env
ExecStart=/usr/local/bin/litestream replicate -config /etc/flow-api/litestream.yml
Restart=always
RestartSec=5
TimeoutStopSec=30
KillSignal=SIGTERM
# ... 同じハードニング設定 ...
```

| 設定 | 意図 |
| --- | --- |
| `After=flow-api.service` | API が DB と WAL を初期化してからレプリケーションを開始 |
| `Restart=always` | レプリケーションは止まると RPO が悪化するため常に再起動 |
| `RestartSec=5` | R2 の一時障害時に過度な再試行をしない |
| `MemoryMax=256M` | Litestream のメモリ上限 |
| `ReadWritePaths=/var/lib/flow-api` | WAL の読み取りとチェックポイントのため書き込みを許可 |

### 4.3 停止と再起動の順序

通常の停止は Litestream を先に止め、次に API を止めます。

```bash
sudo systemctl stop litestream.service
sudo systemctl stop flow-api.service
```

起動は API を先にします。

```bash
sudo systemctl start flow-api.service
sudo systemctl start litestream.service
```

`systemctl restart flow-api.service` だけなら Litestream は動き続けます。バイナリ更新やスキーマ
変更を伴う場合は「9.2 バイナリの更新」の順序を使ってください。

### 4.4 シャットダウンのタイミング

`SHUTDOWN_TIMEOUT`（既定 `10s`）は、HTTP の停止、最終 quota スナップショット、Batcher の
Drain に使う期限です。`TimeoutStopSec=30` は systemd が SIGKILL を送るまでの猶予で、必ず
`SHUTDOWN_TIMEOUT` より大きくします。

Drain では 8,192 スロットのキューを 64 件ずつコミットします。停止が間に合わない場合は、
`SHUTDOWN_TIMEOUT` と `TimeoutStopSec` を負荷に合わせて延ばします。

## 5. 設定リファレンス

### 5.1 アプリケーション

| 環境変数 | 既定値 | 制約 | 説明 |
| --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | 空でない `host:port` | HTTP の待ち受けアドレス |
| `DB_PATH` | `./data/flow.db` | 空でないパス | SQLite ファイル。親ディレクトリは自動作成 |
| `DB_READ_CONNS` | `5` | 未設定は既定、明示値は `[4, 6]` にクランプ | 読み取りプールの接続数 |
| `DEFAULT_INITIAL_BALANCE` | `1000` | 0 以上 | 新規アカウントの初期残高 |
| `DEFAULT_RATE_LIMIT_RPM` | `120` | 正の整数 | 既定の 1 分あたり上限 |
| `DEFAULT_REQUEST_COST` | `1` | 0 以上 | 課金対象ルートの既定コスト |
| `SHUTDOWN_TIMEOUT` | `10s` | 正の duration | 停止処理の期限 |

`config.Load` は `.env.local`、`.env` の順に読み、既存のプロセス環境変数を上書きしません。
`systemd` の `EnvironmentFile` はプロセス環境変数として渡されるため、`.env` より優先されます。

### 5.2 Litestream と R2

| 環境変数 | 例 | 説明 |
| --- | --- | --- |
| `R2_ACCOUNT_ID` | `0123456789abcdef...` | Cloudflare のアカウント ID。R2 のアクセスキーではない |
| `R2_BUCKET` | `flow-api-backups` | レプリケーション先のバケット |
| `R2_PREFIX` | `flow-api` | バケット内のプレフィックス |
| `R2_ACCESS_KEY_ID` | `...` | R2 の S3 互換アクセスキー ID |
| `R2_SECRET_ACCESS_KEY` | `...` | R2 のシークレットアクセスキー |
| `LITESTREAM_DB_PATH` | `/var/lib/flow-api/flow.db` | レプリケーション対象の DB。`DB_PATH` と同一にする |

R2 のエンドポイントは次の URL で表現されます。

```text
https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com
```

リージョンは `auto` です。`R2_ACCOUNT_ID`、`R2_BUCKET`、`R2_PREFIX`、アクセスキーは
`deploy/litestream/litestream.yml` の `${...}` で展開されます。

## 6. Litestream v0.5 と Cloudflare R2

### 6.1 R2 バケットと認証情報

1. Cloudflare ダッシュボードで R2 を有効化し、バケット（例: `flow-api-backups`）を作成する。
2. R2 の API トークンを作成し、対象バケットに対する **Object Read & Write** 権限だけを与える。
3. 発行されたアクセスキー ID とシークレットアクセスキーを `/etc/flow-api/flow-api.env` に設定する。
4. バケットのライフサイクルや保持期間は Litestream の `retention` と整合させる。

認証情報は環境変数からのみ渡します。`litestream.yml` やリポジトリへ実値をコミットしません。

### 6.2 設定ファイル

`deploy/litestream/litestream.yml` を `/etc/flow-api/litestream.yml` に配置します。

```yaml
sync-interval: 10s
l0-retention: 24h
l0-retention-check-interval: 15s

snapshot:
  interval: 24h
  retention: 24h

retention:
  enabled: true

logging:
  level: info
  type: json

dbs:
  - path: ${LITESTREAM_DB_PATH}
    checkpoint-interval: 1m
    monitor-interval: 1s
    busy-timeout: 5s
    replica:
      url: "s3://${R2_BUCKET}/${R2_PREFIX}?endpoint=https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com&region=auto"
      access-key-id: ${R2_ACCESS_KEY_ID}
      secret-access-key: ${R2_SECRET_ACCESS_KEY}
      sync-interval: 10s
```

Litestream v0.5 の構文に合わせ、次の点を守ります。

- レプリカは単数形の `replica:` で書く。
- `retention` は `{enabled: true}` のオブジェクトで書く。
- `snapshot` は `interval` と `retention` を持つオブジェクトで書く。
- `l0-retention` と `l0-retention-check-interval` を使う。
- 認証情報は `access-key-id` と `secret-access-key` に環境変数展開で渡す。

| キー | 値 | 意味 |
| --- | --- | --- |
| `sync-interval` | `10s` | R2 へ同期する間隔。目標 RPO は約 10 秒 |
| `l0-retention` | `24h` | L0 セグメントの保持期間 |
| `l0-retention-check-interval` | `15s` | L0 の保持期限チェック間隔 |
| `snapshot.interval` | `24h` | スナップショットの作成間隔 |
| `snapshot.retention` | `24h` | スナップショットの保持期間 |
| `retention.enabled` | `true` | 保持ポリシーを有効化 |
| `dbs[].checkpoint-interval` | `1m` | Litestream が WAL をチェックポイントする間隔 |
| `dbs[].monitor-interval` | `1s` | DB ファイルの監視間隔 |
| `dbs[].busy-timeout` | `5s` | SQLite のロック待機 |
| `dbs[].replica.url` | `s3://...` | R2 の S3 互換 URL |

### 6.3 なぜ `wal_autocheckpoint=0` なのか

SQLite の自動チェックポイントは、WAL が一定量を超えると WAL フレームを DB 本体へ書き戻し、
再利用可能にします。Litestream は WAL のフレームを読んで R2 へ複製するため、SQLite が先に
チェックポイントして WAL を切り詰めると、Litestream が読む前にフレームが失われる可能性が
あります。

アプリケーションは `PRAGMA wal_autocheckpoint = 0` で自動チェックポイントを止め、WAL の管理を
Litestream に委ねます。Litestream は `checkpoint-interval: 1m` で、複製が完了したことを確認して
からチェックポイントします。

この構成では、Litestream が停止すると WAL が成長し続けます。WAL のサイズとディスク残量を
監視し、Litestream の停止を検知したら早急に復旧してください。`-wal` / `-shm` を手動で削除しては
いけません。

### 6.4 R2 の無料枠と 10 秒同期の書き込み量

Cloudflare R2 の無料枠は、月あたり Class A オペレーション 100 万回、Class B オペレーション
1,000 万回、ストレージ 10GB です（2026 年時点の公開値。最新値は Cloudflare の料金ページを確認）。

`sync-interval: 10s` は 1 分あたり 6 回、1 日 8,640 回、30 日で約 259,200 回の同期機会になります。
1 回の同期で L0 セグメントとインデックスなど複数の Class A オペレーションが発生する場合、
月 26 万〜52 万回程度が見積もりです。24 時間ごとのスナップショットを加えても、100 万回の
無料枠に対して約 2 倍の余裕があります。

実際のオペレーション数は、WAL の更新量、セグメントの境界、Litestream のバージョンによって
変わります。R2 のメトリクスで Class A の使用量を監視し、負荷が高い場合は `sync-interval` を
延ばすか有料枠を検討してください。`sync-interval` を延ばすと RPO も延びます。

### 6.5 起動と確認

```bash
sudo systemctl start flow-api.service
sudo systemctl start litestream.service

systemctl status flow-api.service litestream.service --no-pager
journalctl -u litestream.service -n 100 --no-pager
```

R2 のバケットには `R2_PREFIX` 配下にレプリカのオブジェクトが作成されます。Litestream のログに
認証エラーや同期エラーがないことを確認してください。

## 7. 監視とヘルスチェック

### 7.1 `/healthz`

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

```json
{
  "status": "ok",
  "db": {
    "ok": true,
    "journal_mode": "wal",
    "wal_autocheckpoint": 0,
    "read_query_only": true,
    "read_connections": 5
  }
}
```

`/healthz` は DB が期待どおりでなくても HTTP 200 を返し、`status` を `degraded` にします。
監視では次のようにボディまで検証します。

```bash
curl -fsS http://127.0.0.1:8080/healthz \
  | jq -e '.status == "ok" and .db.ok == true'
```

`jq` がない環境では `status` と `db.ok` を目視または別のスクリプトで確認します。

### 7.2 ランタイム統計

```bash
curl -fsS http://127.0.0.1:8080/api/v1/runtime/stats | jq
```

- `hotpath.rate_limited`、`hotpath.insufficient_balance`、`hotpath.maintenance_rejected` で拒否理由を確認。
- `batcher.dropped` が増えている場合はキュー飽和。`queue_depth` と `queue_capacity` も確認。
- `db.ok`、`db.journal_mode`、`db.wal_autocheckpoint`、`db.read_query_only` で実効設定を確認。

### 7.3 ログ

```bash
journalctl -u flow-api.service -f
journalctl -u litestream.service -f
```

Litestream は JSON 形式で journald に出力します。認証エラー、`sync` の失敗、チェックポイントの
失敗を監視します。

### 7.4 ディスクと WAL

```bash
df -h /var/lib/flow-api
du -h /var/lib/flow-api/flow.db /var/lib/flow-api/flow.db-wal 2>/dev/null
```

`wal_autocheckpoint=0` のため、Litestream が止まると WAL が成長します。ディスク使用率と WAL の
サイズにアラートを設定してください。`-wal` / `-shm` は手動で削除せず、Litestream を復旧させて
チェックポイントを進めます。

### 7.5 監視項目の例

| 項目 | 確認方法 | 異常時の対応 |
| --- | --- | --- |
| ヘルス | `/healthz` の `status` と `db.ok` | 「10. トラブルシューティング」 |
| レイテンシ | 前段プロキシのアクセスログ | ホットパスと DB の統計を確認 |
| キュー飽和 | `batcher.dropped`、`queue_depth` | 負荷を下げる、キュー容量はコード定数 |
| WAL 成長 | `flow.db-wal` のサイズ | Litestream の復旧、ディスク拡張 |
| R2 同期 | Litestream のログ、R2 メトリクス | 認証情報、ネットワーク、R2 障害を確認 |
| ディスク | `df` | 不要なバックアップの整理、拡張 |

## 8. 5 分で復旧する災害復旧ランブック

### 8.1 前提

- `litestream` が `/usr/local/bin/litestream` にある。
- `sqlite3` がインストールされている。ない場合、`restore.sh` は整合性チェックをスキップして
  警告するため、復旧前に導入する。
- `/etc/flow-api/flow-api.env` に R2 と `LITESTREAM_DB_PATH` が設定されている。
- `/etc/flow-api/litestream.yml` が配置されている。
- 復旧スクリプトが `/usr/local/bin/flow-api-restore` にある。

### 8.2 タイムライン

| 時刻 | 作業 |
| --- | --- |
| T+0 | 障害を宣言し、書き込みを止める判断をする |
| T+1 | Litestream と API を停止し、必要なら現行 DB を退避する |
| T+2 | `flow-api-restore` で最新または指定時刻のレプリカを復元する |
| T+3 | `PRAGMA integrity_check` と件数確認を行う |
| T+4 | API と Litestream を起動し、ヘルスチェックを行う |
| T+5 | トラフィックを再開し、監視を強化する |

### 8.3 T+0: 障害の確認

- `/healthz` が `degraded`、DB ファイルの破損、ディスク障害、誤操作などを確認する。
- アプリケーションが書き込みを続けると復旧対象が変わるため、早めにサービスを止める。
- 破損が疑われる場合は、復元前に現行ファイルを退避する。

### 8.4 T+1: サービス停止と現行 DB の退避

```bash
sudo systemctl stop litestream.service
sudo systemctl stop flow-api.service
```

`restore.sh` は `flow-api.service` または `litestream.service` が動いていると復旧を拒否します。
必ず停止してください。

必要に応じて現行 DB を退避します。

```bash
sudo install -d -o root -g root -m 0700 /var/backups/flow-api
sudo cp -a /var/lib/flow-api/flow.db \
  "/var/backups/flow-api/flow.db.pre-restore.$(date +%Y%m%dT%H%M%SZ)"
```

`-wal` / `-shm` も保全したい場合は、DB と同じディレクトリで `flow.db-wal`、`flow.db-shm` を
個別にコピーします。稼働中に WAL だけをコピーすると一貫性が崩れるため、停止後に行います。

### 8.5 T+2: 最新レプリカから復元

```bash
sudo /usr/local/bin/flow-api-restore \
  --config /etc/flow-api/litestream.yml \
  --env-file /etc/flow-api/flow-api.env \
  --force
```

`--force` は既存の DB を置き換え、古い `-wal` / `-shm` を削除します。既存 DB がある場合に
`--force` を省略すると、スクリプトは上書きを拒否します。

スクリプトは次を行います。

1. 環境変数ファイルを読み込み、必須変数を検証する。
2. `litestream restore` で `LITESTREAM_DB_PATH` の最新レプリカを一時ファイルへ復元する。
3. `sqlite3` があれば `PRAGMA integrity_check` を実行する。
4. 一時ファイルを `LITESTREAM_DB_PATH` へ `mv` で置き換える。
5. モードを `0640` にし、root 実行時は `flow-api` 所有へ変更する。

### 8.6 特定時刻へ復元する（PITR）

直近のレプリカに問題がある場合は、RFC 3339 の時刻を指定します。

```bash
sudo /usr/local/bin/flow-api-restore \
  --config /etc/flow-api/litestream.yml \
  --env-file /etc/flow-api/flow-api.env \
  --timestamp 2026-05-01T12:30:00Z \
  --force
```

`--timestamp` は「その時刻の状態に戻す」指定です。障害発生時刻の直前を選びます。保持期間は
`l0-retention: 24h` と `snapshot.retention: 24h` に依存するため、それより古い時刻は復元できません。
要件に応じて保持期間を延ばしてください。

別のパスへ復元して中身を確認したい場合は `--output` を使います。

```bash
sudo /usr/local/bin/flow-api-restore \
  --config /etc/flow-api/litestream.yml \
  --env-file /etc/flow-api/flow-api.env \
  --timestamp 2026-05-01T12:30:00Z \
  --output /tmp/flow-restore-check.db \
  --force
```

`--output` を使った場合は、確認後に本番パスへ戻す手順を別途行います。通常の復旧では
`--output` を省略し、`LITESTREAM_DB_PATH` へ直接戻します。

### 8.7 T+3: 整合性とデータの確認

スクリプトが `integrity_check: ok` を出力していても、業務データの件数を確認します。

```bash
sudo -u flow-api sqlite3 \
  'file:/var/lib/flow-api/flow.db?mode=ro' \
  "PRAGMA integrity_check;"

sudo -u flow-api sqlite3 \
  'file:/var/lib/flow-api/flow.db?mode=ro' \
  "SELECT COUNT(*) AS users FROM users;
   SELECT COUNT(*) AS teams FROM teams;
   SELECT COUNT(*) AS memberships FROM team_memberships;
   SELECT COUNT(*) AS billing_events FROM billing_events;
   SELECT COUNT(*) AS request_logs FROM request_logs;"
```

`file:...?mode=ro` で開くことで、確認用の接続が WAL や DB を変更しないようにします。残高は
`account_quotas` と監査イベントを突き合わせます。API を起動した後（T+4）なら、
`GET /api/v1/accounts/{account_id}` でも確認できます。

### 8.8 T+4: サービス起動

```bash
sudo systemctl start flow-api.service
sudo systemctl start litestream.service

curl -fsS http://127.0.0.1:8080/healthz \
  | jq -e '.status == "ok" and .db.ok == true'
```

`flow-api.service` を先に起動し、マイグレーションと WAL の初期化が終わってから Litestream を
起動します。

### 8.9 T+5: トラフィック再開と監視

- 前段のプロキシやロードバランサーを戻す。
- `journalctl -u flow-api.service -u litestream.service -f` でエラーを監視する。
- `/api/v1/runtime/stats` で `batcher.dropped`、`db.ok`、キュー深度を確認する。
- R2 の同期が再開していることを Litestream のログで確認する。
- 復旧後の監査イベントと残高の整合性を確認する。

### 8.10 復旧できない場合

- `--timestamp` を障害直前の別の時刻にして再試行する。
- それでも失敗する場合は、保持期間内のスナップショットを確認し、Litestream の
  `litestream restore` を手動で実行してエラー内容を確認する。
- R2 の認証情報、バケット、プレフィックス、エンドポイントを再確認する。
- 破損した DB を上書きする前に、必ず退避したファイルを保全する。

### 8.11 RPO と RTO

- RPO: `sync-interval: 10s` のため、最悪約 10 秒分のコミットが R2 に届いていない可能性があります。
- RTO: 本ランブックの範囲では数分です。復元するスナップショットのサイズと R2 からの転送速度に
  依存します。
- 監査履歴は Batcher が非同期に書き込むため、強制終了時には直近のイベントが失われる可能性が
  あります。

## 9. 定期作業

### 9.1 復旧訓練

月次または四半期ごとに、メンテナンスウィンドウで次を実施します。

1. `litestream.service` と `flow-api.service` を停止する。
2. `flow-api-restore --output /tmp/flow-restore-drill.db --force` で一時パスへ復元する。
3. `sqlite3 /tmp/flow-restore-drill.db "PRAGMA integrity_check;"` と件数を確認する。
4. 一時ファイルを削除する。
5. サービスを起動し、`/healthz` を確認する。

本番 DB を置き換えずに復旧手順と所要時間を検証できます。

### 9.2 バイナリの更新

スキーマ変更を伴う可能性があるため、次の順序で行います。

```bash
sudo systemctl stop litestream.service
sudo systemctl stop flow-api.service

# WAL を含めた一貫したバックアップ（sqlite3 を使う場合）
sudo sqlite3 /var/lib/flow-api/flow.db \
  ".backup '/var/backups/flow-api/flow.db.pre-upgrade.$(date +%Y%m%dT%H%M%SZ)'"

sudo install -o root -g root -m 0755 \
  flow-api-linux-amd64 /usr/local/bin/flow-api

sudo systemctl start flow-api.service
sudo systemctl start litestream.service

curl -fsS http://127.0.0.1:8080/healthz
```

`sqlite3` を使わない場合は、`flow.db` だけでなく `flow.db-wal` と `flow.db-shm` も同じ退避先へ
コピーします。`flow.db` だけをコピーすると、WAL に残った直近のコミットが失われる可能性があります。

新しいバイナリは起動時に未適用のマイグレーションを自動適用します。適用済みのマイグレーション
ファイルは編集しません。ロールバックが必要な場合は、変更前の DB バックアップまたは Litestream の
復元ポイントを使います。

### 9.3 OS 再起動

`flow-api.service` と `litestream.service` は `enable` 済みであれば自動起動します。
`flow-api.service` の `Before=litestream.service` により API が先に起動します。

```bash
sudo systemctl reboot
```

再起動後に `systemctl status` と `/healthz` を確認します。

### 9.4 ログとディスク

- journald の保持期間はホストの設定に従います。必要に応じて `journalctl --vacuum-time=14d` などで
  古いログを削除します。
- `/var/lib/flow-api` のディスク使用率、`flow.db`、`flow.db-wal` のサイズを監視します。
- `/var/backups/flow-api` の退避ファイルは、保持期間を決めて定期的に整理します。

### 9.5 セキュリティ

- `/etc/flow-api/flow-api.env` は `root:flow-api`、`0640` を維持する。
- R2 の API トークンは対象バケットの Object Read & Write のみに制限する。
- R2 の認証情報を `litestream.yml`、リポジトリ、チャットへ書かない。
- 公開する場合は前段で TLS を終端し、`HTTP_ADDR` をループバックに限定する。
- `systemd` のハードニング設定を緩める場合は、理由と影響を記録する。

## 10. トラブルシューティング

| 症状 | 主な原因 | 対応 |
| --- | --- | --- |
| `/healthz` が `degraded` | `journal_mode` が `wal` でない、`wal_autocheckpoint` が 0 でない、`query_only` が 1 でない | ログと `db` オブジェクトを確認。DB の権限、読み取りプールの起動順、DSN を確認 |
| 起動時に `unable to open database file` | DB または WAL がない、権限不足、読み取り専用接続を先に開いた | `/var/lib/flow-api` の所有者とモード、`DB_PATH`、`WriteDB` → `ReadDB` の順序を確認 |
| `database is locked` | Litestream や `sqlite3` が書き込みロックを保持、長いトランザクション | `lsof` で DB を開いているプロセスを確認。Litestream の `busy-timeout` と負荷を確認 |
| 読み取りで `SQLITE_READONLY` | 読み取り接続に `_txlock=immediate` を使っている | `ReadDB` は `mode=ro&_query_only=on&_txlock=deferred` であることを確認 |
| Litestream が 401/403 | R2 の認証情報、バケット、エンドポイントの誤り | `R2_ACCOUNT_ID`、`R2_BUCKET`、`R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY` を確認 |
| Litestream が同期しない | `LITESTREAM_DB_PATH` の不一致、設定ファイルのパス誤り、ネットワーク障害 | `systemctl status litestream`、`journalctl -u litestream`、`litestream version` を確認 |
| WAL が成長し続ける | Litestream の停止、チェックポイント失敗、ディスク不足 | Litestream を復旧し、`df` とログを確認。`-wal` / `-shm` を手動削除しない |
| `batcher.dropped` が増える | 8,192 スロットのキュー飽和 | 負荷を下げる、書き込み頻度を調整する。容量はコード定数で環境変数ではない |
| 停止に時間がかかり SIGKILL される | `SHUTDOWN_TIMEOUT` が `TimeoutStopSec` より長い、Drain が間に合わない | `SHUTDOWN_TIMEOUT` を `TimeoutStopSec` より短くし、必要なら両方を延ばす |
| 復元が拒否される | サービスが稼働中、出力先が存在、必須環境変数が未設定 | サービスを停止し、`--force` を付け、`--env-file` を確認 |
| `address already in use` | 別プロセスが `HTTP_ADDR` を使用 | `ss -ltnp` で確認し、ポートまたはプロセスを調整 |
| DB の権限エラー | `flow-api` 以外の所有、モードが `0640` でない | `chown -R flow-api:flow-api /var/lib/flow-api`、`chmod 0640 flow.db` |

### 10.1 ログの確認例

```bash
journalctl -u flow-api.service --since "30 min ago" --no-pager
journalctl -u litestream.service --since "30 min ago" --no-pager
systemctl status flow-api.service litestream.service --no-pager
```

### 10.2 設定の確認例

```bash
sudo systemctl show flow-api.service \
  -p User -p Group -p WorkingDirectory -p TimeoutStopUSec
systemctl cat flow-api.service | grep -E '^(EnvironmentFile|ExecStart|ReadWritePaths)='

sudo -u flow-api sh -c 'cd /var/lib/flow-api && ls -l flow.db*'
```

`/healthz` の `db` オブジェクトは、DSN の文字列ではなく実際の接続から読んだ値です。設定の
最終確認にはこの値を使います。
