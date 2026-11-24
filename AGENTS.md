# 原則
1. 後方互換性を残さない。 置き換えたクラスや旧経路は削除する。互換レイヤー、互換維持のための移行スクリプト、旧実装へのフォールバックは作らない。過去の実装は Git で参照する。
1. 今の要件を満たす最も単純な実装を選ぶ。 使う予定が具体化していない抽象化、設定項目、切替機構は追加しない。
1. 最小のエンドツーエンド経路を先に通す。 実際の入口から結果まで確認し、必要な機能を段階的に足す。将来の可能性だけを理由に、今動いている経路を組み直さない。
1. 責務と依存方向を明確にする。 業務ロジック、外部サービス、永続化、UI の変更が互いに波及しないよう、既存の境界を使う。
1. ライブラリが必要なら、成熟して保守されているものを優先する。 自作する場合は、既存の選択肢で満たせない要件を示す。
1. 新規依存や自作の前に、既存コード・標準ライブラリ・導入済み依存を調べる。 要件を満たせるものがあれば再利用する。
1. 後から変えにくい設計判断は先に詰める。 保存形式、外部との契約、依存方向を決めるときは、変更がどこまで波及するか、何を守るか、破れたときにどう検出するかを明確にする。容易に差し替えられる内部実装には第2項を適用する。
1. 同じ設計課題を解いた成熟製品の実装を調べる。 自分たちの要件と制約に合うパターンを採用する。

# アーキテクチャ

## 技術スタック

- 言語: Go 1.26 (`go.mod` の `go` ディレクティブは `1.26.0`)。ビルド・検証は Go 1.27.4 で行う。
- HTTP: 標準ライブラリ `net/http` の `http.ServeMux`(Go 1.22+ のメソッド付きパターン)。外部ルータは使わない。
- 永続化: SQLite (`modernc.org/sqlite`、CGO 不要)。ORM は使わない。生 SQL のみ。
- ID: `github.com/oklog/ulid/v2`。
- 禁止: Docker、PostgreSQL、Redis、ORM、外部 HTTP ルータ。デプロイは `systemd` + `litestream` による単一バイナリ運用。

## パッケージ境界と依存方向

依存は常に外側から内側へ。内側のパッケージが外側を import してはならない。この境界は `internal/arch_test.go` が `internal/` 配下の Go ファイルを `go/parser` で解析し、import を検査して強制する。検査を緩める変更(許可リストへの追加)は、原則に照らして正当化できる場合のみ行う。

| パッケージ | import 可能なもの |
| --- | --- |
| `internal/domain/` | 標準ライブラリ + `github.com/oklog/ulid/v2` のみ |
| `internal/contract/port/` | 標準ライブラリ + `internal/domain` のみ |
| `internal/contract/message/` | 標準ライブラリ + `internal/domain` のみ |
| `internal/usecase/` | 標準ライブラリ + `internal/domain` + `internal/contract/*` のみ |
| `internal/infra/{config,sqlite,hotpath,batcher}` | 標準ライブラリ + 許可された外部依存 + `internal/domain` + `internal/contract/*` + `migrations` のみ |
| `internal/presentation/httpapi/` | 標準ライブラリ + `internal/domain` + `internal/contract/*` + `internal/usecase` + `web` のみ |
| `cmd/server/` | 手動の composition root。DI フレームワークは使わない。 |

- `internal/infra/*` 同士の相互 import は禁止。`sqlite` は `hotpath` を知らず、`hotpath` は `sqlite` を知らない。両者の結合は `cmd/server` が行う。
- `internal/infra/batcher` は `internal/infra/sqlite` を import しない。永続化能力は `internal/contract/port` の `BatchSink` インターフェースとして受け取り、`cmd/server` が `*sqlite.Store` を注入する。この分離により、Batcher は書き込み先の実装を知らない。
- `internal/presentation/httpapi` は `internal/infra` を import しない。必要な能力は `internal/contract/port` のインターフェースとして受け取る。
- `internal/domain` は I/O もコンテキストも知らない純粋な型・値オブジェクト・業務ルールのみ。
- 境界を越える共有 DTO・イベント定義は `internal/contract/message` に置く。`domain` を汎用契約置き場にしない。
- `migrations/` は `embed.FS` で `*.sql` を埋め込む。`web/` は `embed.FS` で `static/*` を埋め込む。`deploy/` は `systemd` unit、`litestream` 設定、`restore.sh` を置く。

## 外部依存

許可する外部依存は次の 2 つ(と推移的に固定された 1 つ)のみ。

- `modernc.org/sqlite@v1.63.0`(純 Go SQLite ドライバ)
- `modernc.org/libc@v1.80.1`(`modernc.org/sqlite` が使う。バージョンを固定する)
- `github.com/oklog/ulid/v2@v2.1.2`

新しい外部依存を追加する前に、標準ライブラリ・導入済み依存で要件を満たせないか必ず確認する。追加する場合は、なぜ既存の選択肢では足りないかを説明できる状態にする。

## SQLite の二重プールと PRAGMA

書き込み用と読み取り用で `*sql.DB` を分ける。読み取りは WAL により書き込みと並行できる。

- パスの組み立て: `filepath.Abs` → `filepath.ToSlash` → `file:` URI プレフィックスを付ける。`file:` を付け忘れると `mode=ro` は**黙って無視される**(読み取り専用のつもりが読み書き可能になる)。
- 起動順序: 先に `WriteDB` を開き、埋め込みマイグレーションを実行してから `ReadDB` を開く。DB ファイルと `-wal` / `-shm` が存在しない状態で読み取り専用接続を開くと失敗する。
- `WriteDB`:
  - `SetMaxOpenConns(1)`、`SetMaxIdleConns(1)`、`SetConnMaxLifetime(0)`
  - DSN に `_txlock=immediate`
  - すべての書き込みを Go レベルの `sync.Mutex` で直列化する(`WithWriteTx`)。SQLite のロックに頼らず、プロセス内の競合を先に潰す。
  - 初期化時に明示的に `PRAGMA wal_autocheckpoint = 0`。WAL のチェックポイントは Litestream に委ねる。
- `ReadDB`:
  - DSN は `mode=ro&_query_only=on&_txlock=deferred`。`_txlock=immediate` を読み取り接続に使うと `SQLITE_READONLY` になる。**絶対に使わない。**
  - `SetMaxOpenConns` は `[4, 6]` の範囲(既定 5)。設定値 `DB_READ_CONNS` で変更する場合もこの範囲にクランプする。
- 接続ごとの PRAGMA は DSN の `_pragma=...` で与える(接続プールの各接続に適用される): `journal_mode=WAL`、`synchronous=NORMAL`、`mmap_size=134217728`、`cache_size=-32000`、`foreign_keys=ON`、`busy_timeout=5000`。
- `schema_migrations` で適用済みファイル名を記録し、起動時に未適用の `migrations/*.sql` を 1 つの書き込みトランザクションで適用する。適用済みファイルは編集しない。

## インメモリ Hot Path (`internal/infra/hotpath`)

- ホットパスでディスク I/O をしてはならない。参照・判定・カウンタ更新はメモリ上で完結させる。
- 設定スナップショットとルーティング状態は `atomic.Pointer[domain.RuntimeConfig]` で保持する。読み取りはロックフリーな `Load()`、更新は `Swap()`。ポインタを差し替えるだけで、読み取り側をブロックしない。
- アカウント単位の状態は `sync.RWMutex` で保護した map。各アカウントのバケットは自身の `sync.Mutex` を持ち、レート制限と残高の事前引き当てをバケット単位で直列化する。map 全体のロックをホットパスで保持しない。
- テレメトリのカウンタは `atomic.Int64`。
- `/charge` は `withGuard` が `Authorize`(メンテナンス・ルートポリシー・レート制限のみ)を実行し、ハンドラの `ChargeAccount` が `Debit` で 1 回だけ明示的に引き落とす。他の課金ルートは `AcquireAndDebit` で事前引き落としする。`Authorize` と `Debit` を併用する経路で `AcquireAndDebit` を呼ぶと二重引き落としになる。
- 起動時に `ReadDB` から状態をハイドレートする。ハイドレートは起動経路のみで、ホットパスからは呼ばない。

## 非同期グループコミット Batcher (`internal/infra/batcher`)

- バッファ付きチャネルの容量は 8192。
- 単一のバックグラウンドライター goroutine が、15ms 経過ごと、または 64 件溜まった時点で `BatchSink.WriteBatch` を呼び出す。`sqlite.Store` 側では `WithWriteTx` による 1 つの SQLite トランザクションでまとめて書き込む。
- `Enqueue` / `Drain` は `sync.RWMutex` で保護する。HTTP のシャットダウンと書き込みが競合しても、クローズ済みチャネルへの送信で panic しないようにする。
- 終了順序を守る: `http.Server.Shutdown` → `hotpath.SnapshotQuotas` の結果を `EnqueueQuotaSync` → `batcher.Drain` → `store.Close`。ハンドラ停止後に最新の残高スナップショットを enqueue することで、完了順の逆転で古い残高が最後の書き込みになるのを防ぐ。逆順にすると、受付中のリクエストが閉じたストアへ書き込む。

## Litestream v0.5 と災害復旧

- Litestream v0.5.x の YAML 構文を使う:
  - 単数形の `replica:`
  - `sync-interval: 10s`
  - `l0-retention: 24h`
  - `snapshot: {interval: 24h, retention: 24h}`
  - `retention: {enabled: true}`
- Cloudflare R2 のレプリカ URL:
  `s3://${R2_BUCKET}/${R2_PREFIX}?endpoint=https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com&region=auto`
- `restore.sh` は 1 コマンドで復旧できるスクリプトとして維持する。`systemd` unit で動かし、Docker は使わない。
- 認証情報は環境変数から渡す。設定ファイルやリポジトリにコミットしない。

## ULID

- SQLite には 26 文字の `TEXT` として厳密に保存する。バイナリ保存や別表現への変換はしない。
- 外部から受け取った ID は `ulid.ParseStrict` で検証する。`Parse` は長さや曖昧な文字を許すため使わない。

## 設定

- 設定は環境変数から読み、`internal/infra/config` で検証してから composition root に渡す。既定値は `.env.example` と一致させる。
- アプリケーションが読む主要な環境変数: `HTTP_ADDR`、`DB_PATH`、`DB_READ_CONNS`、`DEFAULT_INITIAL_BALANCE`、`DEFAULT_RATE_LIMIT_RPM`、`DEFAULT_REQUEST_COST`、`SHUTDOWN_TIMEOUT`。
- `R2_ACCOUNT_ID`、`R2_BUCKET`、`R2_PREFIX`、`R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY`、`LITESTREAM_DB_PATH` は Litestream が読む。認証情報はリポジトリへコミットしない。

# ビルドと検証

変更後は以下を順に実行し、すべて通す。

```bash
gofmt -s -w .
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
CGO_ENABLED=0 go test -count=1 ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" ./cmd/server
```

- `CGO_ENABLED=1` の race テストと `CGO_ENABLED=0` のテストの両方を回す。`modernc.org/sqlite` は CGO 不要なので、CGO 無効でもテストが通る状態を保つ。
- 新機能にはテストを追加する。バグ修正にはリグレッションテストを追加する。
- `go.mod` / `go.sum` はコミットする。依存を追加したら `go mod tidy` で整理する。

# ドキュメント

- 現行の仕様・構成・利用手順・設計理由のみを記述する。
- 過去の名称、完了した移行、作業経緯だけの注記や計画書は残さない。必要な場合は Git 履歴を参照する。
- 現在の制約、業務上の履歴管理、DB マイグレーションの手順と注意事項は維持する。

# Git

- コミット前に `git status` と `git diff` を確認する。
- ユーザー報告に基づくバグ修正・機能追加では、次の trailer を付ける:

  ```bash
  git commit --trailer "Reported-by:<name>"
  ```

- GitHub Issue に関連するコミットでは、次の trailer を付ける:

  ```bash
  git commit --trailer "Github-Issue:#<number>"
  ```

- `co-authored-by` やそれに類する記述を絶対に含めない。コミットメッセージ・PR 本文で、使用した AI ツール(LLM 等)に一切言及しない。
