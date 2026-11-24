# Flow App Template

## 概要

**Flow App Template** は、Go の Clean Architecture で実装した単一バイナリの API サーバーです。
SQLite をアプリケーションに埋め込み、Web コンソールと SQL マイグレーションを `embed.FS` で
バイナリに同梱します。`-trimpath -ldflags "-s -w"` でビルドした Linux バイナリは約 11MB です。

- HTTP は標準ライブラリ `net/http` の `http.ServeMux` のみ。外部ルーターは使いません。
- 永続化は SQLite (`modernc.org/sqlite`)。ORM は使わず、生 SQL のみです。
- ホットパスはインメモリで判定し、監査履歴は非同期のグループコミットで永続化します。
- Docker、PostgreSQL、Redis を必要としません。`systemd` と `litestream` による単一バイナリ運用を前提とします。
- ID は 26 文字の ULID を `TEXT` として保存します。

設計の詳細は [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)、本番運用と災害復旧は
[docs/OPERATIONS.md](docs/OPERATIONS.md)、実装規約は [AGENTS.md](AGENTS.md) を参照してください。

## 主な特徴

| 領域 | 内容 |
| --- | --- |
| アーキテクチャ | `domain` → `contract` → `usecase` → `presentation` / `infra` の依存方向を `internal/arch_test.go` が検査 |
| 永続化 | WAL モードの SQLite。書き込み用 1 接続と読み取り専用プールを分離 |
| ホットパス | `atomic.Pointer[domain.RuntimeConfig]` とアカウント単位の `sync.Mutex` でディスク I/O なしに判定 |
| 監査履歴 | 8,192 スロットのチャネルと単一ライターによる 15ms / 64 件のグループコミット |
| コンソール | `/` で配信する埋め込み Web コンソール。ホットパス、キュー、監査履歴を表示 |
| マイグレーション | `migrations/*.sql` を起動時に自動適用し、`schema_migrations` で追跡 |
| 運用 | `systemd` unit、Litestream v0.5 による Cloudflare R2 への継続レプリケーション、復旧スクリプト |

## ディレクトリ構成

```text
.
├── cmd/server/                  # composition root とプロセス起動・終了
│   ├── main.go
│   └── main_test.go
├── internal/
│   ├── domain/                  # 集約、値オブジェクト、業務ルール（I/O なし）
│   ├── contract/
│   │   ├── port/                # 永続化・ホットパス・キューのインターフェース
│   │   └── message/             # レイヤーを越える共有 DTO・トピック
│   ├── usecase/                 # ユースケースと入出力 DTO
│   ├── infra/
│   │   ├── config/              # 環境変数の読み込みと検証
│   │   ├── sqlite/              # 生 SQL リポジトリ、二重プール、マイグレーション適用
│   │   ├── hotpath/             # インメモリの admission・残高・テレメトリ
│   │   └── batcher/             # 非同期グループコミットキュー
│   ├── presentation/httpapi/    # JSON API、ミドルウェア、埋め込みコンソール配信
│   └── arch_test.go             # 依存方向の検査
├── migrations/                  # 埋め込み SQL マイグレーション
│   ├── 0001_init.sql
│   └── embed.go
├── web/                         # 埋め込み Web コンソール
│   ├── embed.go
│   └── static/
├── deploy/
│   ├── systemd/                 # flow-api.service、litestream.service、環境変数例
│   └── litestream/              # litestream.yml、restore.sh
├── docs/
│   ├── ARCHITECTURE.md
│   └── OPERATIONS.md
├── .github/
│   ├── pull_request_template.md
│   └── workflows/ci.yml         # Go の CI
├── .env.example                 # ローカル開発用の環境変数例
├── .gitignore
├── go.mod
├── go.sum
├── LICENSE
├── AGENTS.md                    # 実装規約
└── README.md
```

各パッケージのテストは `*_test.go` として同じディレクトリに置きます。

## クイックスタート

必要なものは Go 1.26 以上です。CGO は不要で、SQLite ドライバは純 Go 実装です。

```bash
git clone https://github.com/aiagate/flow-app-template.git
cd flow-app-template

cp .env.example .env
go run ./cmd/server
```

起動すると `./data/flow.db` が作成され、未適用のマイグレーションが自動で適用されます。

- Web コンソール: <http://localhost:8080/>
- ヘルスチェック: <http://localhost:8080/healthz>

```bash
curl -fsS http://localhost:8080/healthz
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

`/healthz` は DB の実効 PRAGMA が期待どおりでない場合も HTTP 200 を返し、`status` を
`degraded` にします。監視では HTTP ステータスだけでなく `status` と `db.ok` を確認してください。

### 設定の読み込み

`config.Load` はプロジェクトルート（`go.mod` のあるディレクトリ）の `.env.local`、次に `.env` を
読みます。すでに設定済みのプロセス環境変数が常に優先されます。本番の `systemd` では
`EnvironmentFile=/etc/flow-api/flow-api.env` を使い、`.env` ファイルは配置しません。

| 環境変数 | 既定値 | 説明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP の待ち受けアドレス。`host:port` 形式 |
| `DB_PATH` | `./data/flow.db` | SQLite ファイル。親ディレクトリは自動作成 |
| `DB_READ_CONNS` | `5` | 読み取りプールの接続数。`[4, 6]` にクランプ |
| `DEFAULT_INITIAL_BALANCE` | `1000` | 新規アカウントの初期残高。0 以上 |
| `DEFAULT_RATE_LIMIT_RPM` | `120` | 既定の 1 分あたりリクエスト上限。正の整数 |
| `DEFAULT_REQUEST_COST` | `1` | 課金対象ルートの既定コスト。0 以上 |
| `SHUTDOWN_TIMEOUT` | `10s` | HTTP 停止、最終スナップショット、キュー排出の期限 |

Cloudflare R2 と Litestream の環境変数はアプリケーションではなく Litestream が使います。
一覧と設定方法は [docs/OPERATIONS.md](docs/OPERATIONS.md) を参照してください。

## REST API

### 共通仕様

- リクエストとレスポンスは JSON です。リクエストボディの上限は 1 MiB です。
- `X-Account-ID` で課金アカウントを選択します。省略または空の場合は `default` を使います。
- `X-Request-ID` が指定されればその値を引き継ぎ、なければ ULID を生成します。実効値はレスポンスヘッダにも返します。
- 課金対象ルートでは `X-RateLimit-Limit`、`X-RateLimit-Remaining`、`X-Account-Balance` を返します。
- エラーは共通のエンベロープです。

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "email: must be a valid email address"
  }
}
```

| HTTP | エラーコード |
| --- | --- |
| 400 | `VALIDATION_ERROR` |
| 402 | `INSUFFICIENT_BALANCE` |
| 404 | `NOT_FOUND` |
| 409 | `CONFLICT`、`CONCURRENCY_CONFLICT` |
| 429 | `RATE_LIMITED` |
| 500 | `UNEXPECTED` |
| 503 | `ROUTE_DISABLED` |

### エンドポイント一覧

「課金」列の「対象」は、ハンドラ実行前にホットパスがコストを事前引き落としするルートです。
`/charge` だけは事前引き落としをせず、ハンドラが明示的に 1 回だけ引き落とします。

| Method | Path | 用途 | 課金 |
| --- | --- | --- | --- |
| GET | `/` | 埋め込み Web コンソール | なし |
| GET | `/assets/{path...}` | コンソールの静的アセット | なし |
| GET | `/healthz` | ヘルスチェックと実効 PRAGMA | なし |
| GET | `/api/v1/runtime/config` | 実行時設定の取得 | なし |
| PUT | `/api/v1/runtime/config` | 実行時設定の更新（メンテナンス、ルートポリシー） | なし |
| GET | `/api/v1/runtime/stats` | ホットパス、Batcher、DB の統計 | なし |
| GET | `/api/v1/accounts/{account_id}` | 残高とレート上限の取得 | なし |
| POST | `/api/v1/accounts/{account_id}/credit` | 残高の加算と監査イベント記録 | なし |
| POST | `/api/v1/accounts/{account_id}/charge` | 明示的な残高の引き落とし | 単一 Debit |
| GET | `/api/v1/billing/events` | 課金イベントの一覧 | なし |
| GET | `/api/v1/logs` | リクエストログの一覧 | なし |
| POST | `/api/v1/users` | ユーザー作成 | 対象 |
| GET | `/api/v1/users` | ユーザー一覧 | 対象 |
| GET | `/api/v1/users/{id}` | ユーザー取得 | 対象 |
| PUT | `/api/v1/users/{id}/email` | メールアドレス変更 | 対象 |
| POST | `/api/v1/teams` | チーム作成 | 対象 |
| GET | `/api/v1/teams` | チーム一覧 | 対象 |
| GET | `/api/v1/teams/{id}` | チーム取得 | 対象 |
| PUT | `/api/v1/teams/{id}` | チーム名変更 | 対象 |
| POST | `/api/v1/teams/{team_id}/memberships/join` | 即時参加（`ACTIVE`） | 対象 |
| POST | `/api/v1/teams/{team_id}/memberships/request` | 参加リクエスト（`PENDING`） | 対象 |
| GET | `/api/v1/teams/{team_id}/memberships` | チームのメンバーシップ履歴 | 対象 |
| POST | `/api/v1/memberships/{id}/approve` | 参加リクエストの承認 | 対象 |
| POST | `/api/v1/memberships/{id}/leave` | 脱退 | 対象 |
| PUT | `/api/v1/memberships/{id}/role` | ロール変更 | 対象 |
| * | 上記以外 | JSON の 404 エラー | なし |

`POST /api/v1/users`、`POST /api/v1/teams`、`POST /api/v1/teams/{team_id}/memberships/join`、
`POST /api/v1/teams/{team_id}/memberships/request` は成功時に 201 を返します。それ以外の成功は
200 です。

### 主なリクエストボディ

| エンドポイント | ボディ |
| --- | --- |
| `POST /api/v1/users` | `{"display_name":"...","email":"..."}` |
| `PUT /api/v1/users/{id}/email` | `{"email":"...","expected_version":1}`（`expected_version` は任意） |
| `POST /api/v1/teams` | `{"name":"..."}` |
| `PUT /api/v1/teams/{id}` | `{"name":"...","expected_version":1}`（`expected_version` は任意） |
| `POST /api/v1/teams/{team_id}/memberships/join` | `{"user_id":"..."}` |
| `POST /api/v1/teams/{team_id}/memberships/request` | `{"user_id":"..."}` |
| `PUT /api/v1/memberships/{id}/role` | `{"role":"OWNER"}`（`OWNER` / `ADMIN` / `MEMBER`） |
| `POST /api/v1/accounts/{account_id}/credit` | `{"amount":100,"reason":"..."}` |
| `POST /api/v1/accounts/{account_id}/charge` | `{"amount":10,"reason":"..."}` |
| `PUT /api/v1/runtime/config` | `{"default_rate_limit_rpm":120,"default_request_cost":1,"maintenance_mode":false,"routes":{...}}` |

`GET /api/v1/billing/events` は任意の `account_id` と `limit`（1〜1000、既定 100）を受け付けます。
`GET /api/v1/logs` は `limit` のみを受け付けます。

### 課金の扱い

- 課金対象ルートは、ハンドラ実行前に `AcquireAndDebit` でコストを引き落とします。
- ハンドラが 5xx を返した場合はコストを返金します。5xx 未満のレスポンスではコストが確定し、課金イベントが記録されます。
- メンテナンス、ルート無効、レート制限、残高不足で拒否されたリクエストは引き落としません。
- `/charge` は `Authorize` でメンテナンス、ルートポリシー、レート制限だけを確認し、ハンドラの `Debit` が金額を 1 回だけ引き落とします。

## ビルドと検証

変更後は次のコマンドを順に実行し、すべて通ることを確認します。

```bash
gofmt -s -w .
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
CGO_ENABLED=0 go test -count=1 ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" ./cmd/server
```

- `CGO_ENABLED=1` の race テストには C コンパイラが必要です。
- `modernc.org/sqlite` は CGO 不要のため、`CGO_ENABLED=0` でも全テストが通る状態を保ちます。
- Linux 向けの静的バイナリの作成と配置は [docs/OPERATIONS.md](docs/OPERATIONS.md) を参照してください。

## ドキュメント

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — レイヤー、ドメイン、SQLite 二重プール、ホットパス、Batcher、マイグレーション
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — クロスコンパイル、`systemd`、Litestream v0.5 と R2、5 分で復旧するランブック
- [AGENTS.md](AGENTS.md) — 実装原則、依存方向、ビルドと検証、Git 規約

### デプロイ資材

- [deploy/systemd/flow-api.service](deploy/systemd/flow-api.service) — API の systemd unit
- [deploy/systemd/litestream.service](deploy/systemd/litestream.service) — Litestream の systemd unit
- [deploy/systemd/flow-api.env.example](deploy/systemd/flow-api.env.example) — 本番環境変数の例
- [deploy/litestream/litestream.yml](deploy/litestream/litestream.yml) — Litestream v0.5 の設定
- [deploy/litestream/restore.sh](deploy/litestream/restore.sh) — 復旧スクリプト

## ライセンス

このプロジェクトは [MIT ライセンス](LICENSE) の下で公開されています。
