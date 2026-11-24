# アーキテクチャ

このドキュメントは、Flow App Template の現行実装の構造と設計判断を説明します。利用手順と本番運用は
[README.md](../README.md) と [OPERATIONS.md](OPERATIONS.md)、実装規約は [AGENTS.md](../AGENTS.md) を
参照してください。

## 1. 全体像

Flow App Template は、単一の Go バイナリで完結する HTTP API サーバーです。

- HTTP: 標準ライブラリ `net/http` の `http.ServeMux`（Go 1.22 以降のメソッド付きパターン）。
- 永続化: SQLite（`modernc.org/sqlite`、純 Go ドライバ）。ORM は使わず生 SQL のみ。
- ID: `github.com/oklog/ulid/v2` の 26 文字 ULID。
- 配信物: API、Web コンソール、SQL マイグレーションを 1 つのバイナリに埋め込む。
- 運用: `systemd` で常駐し、Litestream v0.5 が SQLite の WAL を Cloudflare R2 に複製する。

Docker、PostgreSQL、Redis、外部 HTTP ルーター、ORM は使いません。これらは現在の要件に対する
依存ではなく、意図的な非依存です。単一ノードの SQLite で読み取り並行性と障害復旧を確保する構成を
採用しています。本番の配備資材は
[deploy/systemd/flow-api.service](../deploy/systemd/flow-api.service)、
[deploy/systemd/litestream.service](../deploy/systemd/litestream.service)、
[deploy/litestream/litestream.yml](../deploy/litestream/litestream.yml)、
[deploy/litestream/restore.sh](../deploy/litestream/restore.sh) にあります。

## 2. レイヤーと依存方向

### 2.1 レイヤー構成

```text
┌──────────────────────────────────────────────────────────────────────┐
│ cmd/server                                                           │
│ composition root: config → sqlite → hotpath → batcher → usecase →    │
│                   httpapi → http.Server                              │
└───────────────┬──────────────────────────────────┬───────────────────┘
                │                                  │
┌───────────────▼──────────────────┐  ┌────────────▼───────────────────┐
│ internal/presentation/httpapi    │  │ internal/infra/                │
│ + web (embed.FS)                 │  │   config, sqlite, hotpath,     │
│ JSON API、middleware、console     │  │   batcher                      │
└───────────────┬──────────────────┘  └────────────┬───────────────────┘
                │                                  │
                │        port を実装／利用          │
                ▼                                  ▼
┌──────────────────────────────────────────────────────────────────────┐
│ internal/contract/port ・ internal/contract/message                  │
│ 永続化、ホットパス、書き込みキュー、監査クエリのインターフェース       │
└──────────────────────────────┬───────────────────────────────────────┘
                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│ internal/domain                                                      │
│ 集約、値オブジェクト、状態遷移、検証。I/O も context も知らない         │
└──────────────────────────────────────────────────────────────────────┘
```

依存は常に外側から内側へ向かいます。`domain` は何にも依存せず、`contract` は `domain` だけに、
`usecase` は `domain` と `contract` だけに依存します。`infra` と `presentation` は内側の契約を
実装または利用し、`cmd/server` だけがすべてを組み立てます。

### 2.2 `internal/arch_test.go` が強制する規則

`TestArchitectureDependencies` は `internal/` 配下の `*_test.go` 以外の Go ファイルを
`go/parser` の `ImportsOnly` で解析し、import パスを検査します。`go list` の出力には依存しません。

- `internal/` 配下の各パッケージは、下表のいずれかの規則に一致しなければなりません。規則のない
  ディレクトリに Go ファイルを追加するとテストは失敗します。
- import 先が禁止リストにあれば、その時点で拒否します。
- 禁止リストになく、自モジュール内なら `allowInternal`、標準ライブラリなら許可、それ以外の
  外部パッケージなら `allowExternal` で判定します。標準ライブラリかどうかは、import パスの
  最初のセグメントに `.` を含むかで判定します。
- 禁止 import はパッケージごとの `forbiddenImports` で明示的に拒否します。
- 検査対象は `internal/` のみです。`cmd/server` と `web` は検査対象外ですが、`cmd/server` は
  composition root として全パッケージを import できます。

| パッケージ | import 可能な内部パッケージ | 外部依存 | 明示的な禁止 import |
| --- | --- | --- | --- |
| `internal/domain` | なし | `github.com/oklog/ulid/v2` のみ | `context`、`database/sql`、`net/http` |
| `internal/contract/port` | `internal/domain` のみ | なし | `database/sql`、`net/http` |
| `internal/contract/message` | `internal/domain` のみ | なし | `database/sql`、`net/http` |
| `internal/usecase` と配下 | `internal/domain`、`internal/contract/*` | なし | `database/sql`、`net/http` |
| `internal/infra/{config,sqlite,hotpath,batcher}` と配下 | `internal/domain`、`internal/contract/*`、`migrations` | `github.com/oklog/ulid/v2`、`modernc.org/libc`（配下含む）、`modernc.org/sqlite`（配下含む） | なし |
| `internal/presentation/httpapi` と配下 | `internal/domain`、`internal/contract/*`、`internal/usecase`（配下含む）、`web`（配下含む） | なし | `database/sql` |

### 2.3 `internal/infra/*` の相互 import 禁止

`internal/infra/*` の許可リストには、他の `internal/infra/*` が含まれていません。そのため
`sqlite` が `hotpath` を、`hotpath` が `batcher` を import すると `arch_test` が失敗します。

- `sqlite` はインメモリ状態を知りません。
- `hotpath` は SQLite も Batcher も知りません。
- `batcher` は SQLite を知りません。
- 結合は `cmd/server` が行います。

この分離により、永続化方式、admission の実装、書き込みキューを独立してテストできます。

### 2.4 `port.BatchSink` による Batcher と SQLite の分離

非同期 Batcher は `internal/infra/sqlite` を import しません。`internal/contract/port` の
`BatchSink` だけに依存します。

```go
type BatchSink interface {
    WriteBatch(ctx context.Context, payload BatchPayload) error
}
```

- `internal/infra/batcher` は `BatchSink` にイベントを渡すだけです。
- `internal/infra/sqlite.Store` が `WriteBatch` を実装し、1 つの `WithWriteTx` で
  `billing_events`、`account_quotas`、`request_logs` をまとめて書き込みます。
- `cmd/server` が `batcher.New(store, ...)` として `*sqlite.Store` を注入します。

もし Batcher が `sqlite` を直接 import すると、インフラ同士の結合が生まれ、`arch_test` にも
違反します。`BatchSink` はその結合を契約に置き換えるための境界です。

## 3. ドメインモデル

`internal/domain` は純粋な型、値オブジェクト、業務ルールだけを持ちます。`context`、`database/sql`、
`net/http` を import しません。

### 3.1 集約とレコード

| 型 | 役割 | 主な不変条件 |
| --- | --- | --- |
| `User` | ユーザー集約のルート | `DisplayName` は 1〜100 ルーン、前後の空白なし。`Email` は 254 文字以内で検証済み |
| `Team` | チーム集約のルート | `TeamName` は 1〜100 ルーン、前後の空白なし |
| `TeamMembership` | メンバーシップ期間の集約 | ロールは `OWNER` / `ADMIN` / `MEMBER`。状態は `PENDING` / `ACTIVE` / `LEAVED` |
| `AccountQuota` | アカウントの残高とレート上限 | `AccountID`、`Balance`、`RateLimitRPM`、`UpdatedAt` |
| `BillingEvent` | 追記専用の残高変動 | `Amount` は符号付き。クレジットは正、チャージ・リクエスト課金は負 |
| `RequestLog` | 追記専用の API リクエスト履歴 | メソッド、パス、ステータス、レイテンシ、発生時刻 |

値オブジェクトとして `DisplayName`、`Email`、`TeamName`、`Version` を持ちます。`Version` は
楽観的ロックの世代番号で、負の値を受け付けません。

`RuntimeConfig`、`AccountState`、`HotPathDecision`、`HotPathStats`、`BatcherStats`、`DBHealth` は
実行時の読み取りモデルです。`RuntimeConfig` は `Clone` で `Routes` マップを深くコピーします。

### 3.2 ULID と `ParseStrict`

- すべての集約 ID は 26 文字の ULID を SQLite の `TEXT` にそのまま保存します。バイナリ保存や
  別表現への変換はしません。
- 外部から受け取った ID は `ulid.ParseStrict` で検証します。`ulid.Parse` は短縮形や曖昧な文字を
  受け付けるため使いません。
- `UserID`、`TeamID`、`MembershipID`、`EventID` はそれぞれ専用の型で、`ParseUserID` などが
  `ParseStrict` を通します。`User`、`Team`、`TeamMembership`、`BillingEvent` の ID がこの契約の
  対象です。`AccountQuota.AccountID` は ULID ではなく任意のアカウント識別子です。
- `NewRequestID` はリクエスト相関用の ULID を生成します。受信した `X-Request-ID` はそのまま
  引き継ぎ、ULID として再検証はしません。`BillingEvent.RequestID` と `RequestLog.RequestID` は
  任意の文字列を保持できます。

### 3.3 `TeamMembership` の状態機械

`TeamMembership` は「メンバーシップ期間」を表します。脱退後も `LEAVED` 行を残し、再参加は
新しい ULID の期間として記録します。

```text
             approve
  PENDING ─────────────▶ ACTIVE
     │                      │
     │ leave                │ leave
     ▼                      ▼
  LEAVED ◀─────────────────┘

  ACTIVE / PENDING: change role 可能
  LEAVED:           approve / leave / change role はすべて拒否
```

| 操作 | 遷移 | 拒否条件 |
| --- | --- | --- |
| `Approve` | `PENDING` → `ACTIVE` | `PENDING` 以外 |
| `Leave` | `PENDING` / `ACTIVE` → `LEAVED` | `LEAVED` |
| `ChangeRole` | ロールのみ変更 | `LEAVED` |

`JoinTeam` は `ACTIVE` かつ `MEMBER` の期間を作り、`RequestJoinTeam` は `PENDING` かつ `MEMBER` の
期間を作ります。どちらも書き込みトランザクション内でチームとユーザーの存在を確認します。

### 3.4 部分ユニークインデックス

`team_memberships` には次の部分ユニークインデックスがあります。

```sql
CREATE UNIQUE INDEX uq_team_memberships_current_period
    ON team_memberships(team_id, user_id)
    WHERE status IN ('PENDING', 'ACTIVE');
```

これにより、同じチームとユーザーの「現在の期間」は 1 つだけになります。`LEAVED` は対象外なので、
履歴は保持したまま再参加できます。`PENDING` 中に `join` や `request` を重ねると、SQLite の
`SQLITE_CONSTRAINT_UNIQUE` が `port.ErrAlreadyExists` に変換され、HTTP 409 `CONFLICT` になります。

### 3.5 楽観的ロック

`User`、`Team`、`TeamMembership` は `version` を持ちます。リポジトリの `UPDATE` は次の形で、
読み込み時の version が変わっていれば更新しません。

```sql
UPDATE users
SET display_name = ?, email = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?;
```

- 更新行数が 1 なら成功です。
- 0 行の場合は存在確認を行い、存在しなければ `NOT_FOUND`、存在すれば
  `port.ErrVersionConflict`（HTTP 409 `CONCURRENCY_CONFLICT`）です。
- API の `expected_version` は任意です。省略した場合は書き込みトランザクション内で読み込んだ
  version を使うため、同時更新は version 衝突として検出されます。
- 集約側の `Version` は読み取り専用の値で、実際の加算はリポジトリの SQL が行います。

### 3.6 時刻

永続化する時刻は UTC の RFC 3339（ナノ秒精度、`time.RFC3339Nano`）を `TEXT` に保存します。
読み込み時は `time.Parse(time.RFC3339Nano)` で UTC に戻します。

## 4. 永続化: SQLite の二重プール

### 4.1 なぜプールを分けるか

SQLite の WAL モードでは、読み取りは書き込みと並行できます。一方、書き込みは単一ライターに
直列化する必要があります。そこで `*sql.DB` を 2 つに分けます。

- `WriteDB`: 書き込み専用。最大 1 接続。
- `ReadDB`: 読み取り専用。`mode=ro` と `_query_only=on` で防御し、最大 4〜6 接続。

読み取り専用プールを分けることで、書き込みトランザクションの待ち時間が一覧取得や監査クエリに
波及しません。読み取り側は WAL のスナップショットを読むため、書き込み中でもブロックされません。

### 4.2 `file:` URI の必須性

パスは次の順で組み立てます。

```text
DB_PATH
  → filepath.Abs
  → filepath.ToSlash
  → "file:" + path
  → DSN のクエリを付加
```

`file:` プレフィックスがないと、SQLite ドライバは `mode=ro` や `_query_only=on` を
**黙って無視**します。読み取り専用のつもりが読み書き可能な接続になるため、`file:` は必須です。

### 4.3 起動順序

`sqlite.Open` は次の順序を守ります。

1. `DB_PATH` を絶対パスに変換し、親ディレクトリを `os.MkdirAll` で作成する。
2. `WriteDB` を開き、`PingContext` で接続を確認する。
3. `PRAGMA wal_autocheckpoint = 0` を明示的に実行する。
4. `migrate` で未適用のマイグレーションを適用する。
5. その後に `ReadDB` を開き、`PingContext` で確認する。

DB ファイルと `-wal` / `-shm` が存在しない状態で読み取り専用接続を開くと失敗します。先に
`WriteDB` を開いてマイグレーションを完了させるのは、このためです。

`Store.Close` は読み取りプールを先に閉じ、その後に書き込みプールを閉じます。進行中の読み取りが
最後の WAL 状態と競合しないようにするためです。

### 4.4 DSN と PRAGMA

`WriteDB` の DSN は次の設定を持ちます。

```text
file:<absolute-path>?_txlock=immediate
  &_pragma=busy_timeout(5000)
  &_pragma=journal_mode(WAL)
  &_pragma=synchronous(NORMAL)
  &_pragma=foreign_keys(ON)
  &_pragma=mmap_size(134217728)
  &_pragma=cache_size(-32000)
  &_pragma=wal_autocheckpoint(0)
```

`ReadDB` の DSN は次の設定を持ちます。

```text
file:<absolute-path>?mode=ro&_query_only=on&_txlock=deferred
  &_pragma=busy_timeout(5000)
  &_pragma=synchronous(NORMAL)
  &_pragma=foreign_keys(ON)
  &_pragma=mmap_size(134217728)
  &_pragma=cache_size(-32000)
```

| 設定 | 値 | `WriteDB` | `ReadDB` | 目的 |
| --- | --- | --- | --- | --- |
| `journal_mode` | `WAL` | あり | なし | 読み取りと書き込みの並行性。DB 単位の永続設定なので書き込み接続で設定 |
| `synchronous` | `NORMAL` | あり | あり | WAL と組み合わせた fsync 回数の削減 |
| `mmap_size` | `134217728` | あり | あり | 128 MiB のメモリマップ I/O |
| `cache_size` | `-32000` | あり | あり | 約 32,000 KiB のページキャッシュ |
| `foreign_keys` | `ON` | あり | あり | 外部キー制約の有効化 |
| `busy_timeout` | `5000` | あり | あり | プロセス外（Litestream、`sqlite3`）との競合時の待機 |
| `wal_autocheckpoint` | `0` | あり + 明示 `PRAGMA` | なし | 自動チェックポイントを停止し、WAL 管理を Litestream に委ねる |
| `mode` | `ro` | なし | あり | 読み取り専用接続 |
| `_query_only` | `on` | なし | あり | SQLite レベルでの書き込み拒否 |
| `_txlock` | `immediate` / `deferred` | `immediate` | `deferred` | 書き込み開始時のロック確保／読み取りの遅延ロック |

- `_pragma=...` は接続プールの各接続に適用されます。DSN に書くことで、プールが新しい接続を
  作ったときも同じ設定になります。
- 読み取り接続に `_txlock=immediate` を使うと `SQLITE_READONLY` になります。読み取り側は
  `_txlock=deferred` です。
- `ReadDB` は `mode=ro` なので `journal_mode` を設定しません。WAL は `WriteDB` の起動時に
  DB の永続設定として確立されます。
- `wal_autocheckpoint=0` は DSN だけでなく、起動時に `PRAGMA wal_autocheckpoint = 0` としても
  明示します。Litestream が WAL を複製してからチェックポイントする運用に合わせるためです。

### 4.5 接続プールの設定

| 設定 | `WriteDB` | `ReadDB` |
| --- | --- | --- |
| `SetMaxOpenConns` | `1` | `DB_READ_CONNS`（未設定は 5、明示値は `[4, 6]` にクランプ） |
| `SetMaxIdleConns` | `1` | 同じ値 |
| `SetConnMaxLifetime` | `0` | `0` |

`DB_READ_CONNS` が未設定（または空文字）のときは既定の 5 になります。明示した値は
`config.Load` が `[4, 6]` にクランプするため、`0` や負値、4 未満は 4、6 超は 6 です。
`sqlite.Open` を直接呼ぶ場合も `[4, 6]` にクランプしますが、`0` だけは既定の 5 として扱います。

### 4.6 Go の `sync.Mutex` と `_txlock=immediate` による直列化

`Store.WithWriteTx` は次の順で書き込みを直列化します。

```text
writeMu.Lock()
  → WriteDB.BeginTx()
  → fn(ctx, repos)  // 1 つのトランザクションに束縛されたリポジトリ
  → Commit()
  → 失敗時は Rollback()
writeMu.Unlock()
```

- プロセス内の競合は Go の `sync.Mutex` で先に潰します。SQLite のロック待ちに頼りません。
- DSN の `_txlock=immediate` は、トランザクション開始時に書き込みロックを確保します。
  `deferred` のように読み取り後に書き込みへ昇格する形ではないため、`SQLITE_BUSY` の
  デッドロックパターンを避けられます。
- `WithWriteTx` のコールバックに渡す `WriteRepos` は、そのトランザクションに束縛されます。
  コールバック終了後に保持してはいけません。
- トランザクション内の読み取りは同じ `*sql.Tx` を使うため、read-your-writes が保証されます。
  トランザクション外の読み取りは `ReadDB` を使います。

### 4.7 ヘルスチェック

`Store.Health` は DSN の文字列ではなく、実際の接続から次を読みます。

- `PRAGMA journal_mode`（`WriteDB`）
- `PRAGMA wal_autocheckpoint`（`WriteDB`）
- `PRAGMA query_only`（`ReadDB`）

`journal_mode` が `wal`、`wal_autocheckpoint` が `0`、`query_only` が `1` のすべてを満たす場合に
`DBHealth.OK` が真になります。`/healthz` と `/api/v1/runtime/stats` はこの値を使います。

## 5. インメモリ Hot Path

`internal/infra/hotpath` は admission と残高の判定をメモリ上で完結させます。ホットパスで
ディスク I/O もネットワーク I/O も行いません。

### 5.1 設定スナップショット

- 実行時設定は `atomic.Pointer[domain.RuntimeConfig]` で保持します。
- 読み取りは `Load()` のみで、ロックを取らずに現在のスナップショットを参照します。
- 更新は `UpdateConfig` が `Clone()` した設定を `Swap()` します。`Routes` マップを含めて
  ディープコピーするため、呼び出し元が後からマップを変更しても公開済みスナップショットは
  変わりません。
- `CurrentConfig` が返す値は読み取り専用として扱います。

スナップショットには次が含まれます。

- `DefaultRateLimitRPM`
- `DefaultRequestCost`
- `MaintenanceMode`
- `Routes`（ルート名 → `Enabled`、`Cost`、`RateLimitRPM`）

### 5.2 アカウント単位の状態

```text
Store
├── config atomic.Pointer[domain.RuntimeConfig]
├── mu sync.RWMutex
│   └── accounts map[string]*accountBucket
└── telemetry atomic.Int64 ...

accountBucket
├── mu sync.Mutex
├── balance int64
├── rateLimitRPM int
├── rateLimitSet bool
├── windowStart time.Time
└── windowCount int64
```

- アカウントの map は `sync.RWMutex` で保護します。
- 各バケットは自身の `sync.Mutex` を持ち、残高とレート制限のウィンドウをバケット単位で
  直列化します。
- map のロックはバケットのロックを保持したまま取りません。異なるアカウントの操作は独立して
  進みます。
- 未知のアカウントは `bucketFor` が遅延作成します。初期残高は `Options.InitialBalance`、
  初期レート上限は `Options.DefaultRateLimitRPM` です。

### 5.3 admission の順序

`AcquireAndDebit` と `Authorize` は次の順で判定します。

1. メンテナンスモード（`RuntimeConfig.MaintenanceMode`）
2. ルートポリシー（`Routes[route].Enabled == false` なら拒否）
3. レート制限（1 分間の固定ウィンドウ）
4. 残高（`AcquireAndDebit` のみ）

コストはルートポリシーがあれば `policy.Cost`、なければ `DefaultRequestCost` です。負のコストは
0 として扱います。

レート上限の決定順は次のとおりです。

1. ルートポリシーの `RateLimitRPM`（0 より大きい場合）
2. 永続化されたアカウント固有の `RateLimitRPM`（`Hydrate` 済みの場合）
3. `RuntimeConfig.DefaultRateLimitRPM`
4. `Store` の既定値

`RateLimitRPM` が 0 以下の場合は無制限です。ウィンドウは最初のリクエスト時刻を起点とする
1 分間の固定ウィンドウで、スライディングウィンドウではありません。

拒否時の残高とレートの扱いは次のとおりです。

- メンテナンス、ルート無効、レート制限で拒否された場合、残高は変わりません。
- レート制限を通過した後に残高不足で拒否された場合、レート枠は消費されたままです。試行が
  admission に到達したことを記録する意図的な挙動です。
- レート制限で拒否された場合、残高は消費されません。

### 5.4 メソッドの役割

| メソッド | 用途 | 残高 | レート制限・ポリシー |
| --- | --- | --- | --- |
| `AcquireAndDebit` | 通常の課金対象ルート | 事前引き落とし | 適用 |
| `Authorize` | `/charge` の事前確認 | 変更しない | 適用 |
| `Debit` | `/charge` の明示的な引き落とし | 指定額を引く | 適用しない |
| `Refund` | 5xx 時の返金、失敗時の巻き戻し | 加算 | 適用しない |
| `Credit` | 残高の加算 | 加算 | 適用しない |
| `GetAccountState` | アカウント状態の参照 | 変更しない | 適用しない |
| `Hydrate` | 起動時の永続値の読み込み | 置換 | 適用しない |
| `SnapshotQuotas` | 終了時の最終スナップショット | 変更しない | 適用しない |

### 5.5 `/charge` の単一 Debit パターン

`/api/v1/accounts/{account_id}/charge` だけは、ハンドラが金額を指定して引き落とすため、
ミドルウェアの事前引き落としを使いません。

```text
POST /charge
  → withGuard(route, preDebit=false)
      → HotPath.Authorize(accountID, route)
          - メンテナンス、ルートポリシー、レート制限を確認
          - 残高は変更しない
  → ChargeAccount
      → HotPath.Debit(accountID, amount)
          - 残高を確認して 1 回だけ引き落とす
      → BillingEvent(-amount) と AccountQuota を enqueue
  → レスポンスの X-Account-Balance をチャージ後の残高で上書き
```

`withGuard` は `preDebit=false` の場合に `Authorize` を呼びます。ここで `AcquireAndDebit` を
呼ぶと、ハンドラの `Debit` と合わせて二重引き落としになります。この経路では
`Authorize` → `Debit` の順序を守ります。

### 5.6 通常の課金対象ルート

`withGuard(route, preDebit=true)` は `AcquireAndDebit` でコストを先に引き落とし、ハンドラの
レスポンスを見て精算します。

- ステータスが 500 以上: `Refund` でコストを戻し、最新の quota を enqueue する。
- ステータスが 500 未満: `BillingEvent(-cost)` と最新の quota を enqueue する。
  4xx でもハンドラに到達していればコストは確定する。
- `decision.Cost` が 0 の場合: 課金イベントも返金も行わない。
- ガード自体が拒否した場合（メンテナンス、ルート無効、レート制限、残高不足）: ハンドラに
  到達せず、引き落としもイベント記録も行わない。

### 5.7 起動時のハイドレート

`cmd/server` の起動時に `QuotaRepository.ListAll` が `ReadDB` から全アカウントの quota を読み、
`hot.Hydrate` に渡します。

1. 永続化済みの quota を `Hydrate` する。
2. `default` アカウントが存在しなければ、`DEFAULT_INITIAL_BALANCE` と
   `DEFAULT_RATE_LIMIT_RPM` で作成する。
3. `DEFAULT_RATE_LIMIT_RPM` と `DEFAULT_REQUEST_COST` を含む `RuntimeConfig` を
   `UpdateConfig` で設定する。

ハイドレートは起動経路だけで呼びます。ホットパスから呼ぶとディスク I/O が発生します。

### 5.8 テレメトリ

`Store` は `atomic.Int64` で次を数えます。

- `requests`
- `allowed`
- `rate_limited`
- `insufficient_balance`
- `route_disabled`
- `maintenance_rejected`

`Stats()` はこれら 6 つを返します。カウンタはロックフリーで更新され、`/api/v1/runtime/stats` の
`hotpath` に現れます。内部では `total_debited`、`total_credited`、`total_refunded` に相当する
合計も更新しますが、`Stats()` には含めません。

## 6. 非同期グループコミット Batcher

`internal/infra/batcher` は、ホットパスの判定と SQLite のコミットを分離します。課金イベント、
quota、リクエストログはメモリ上のキューに積まれ、SQLite の fsync を待ちません。ディレクトリ操作
（users、teams、memberships）はユースケース内で同期的に書き込みます。

### 6.1 定数

| 定数 | 値 | 意味 |
| --- | --- | --- |
| `QueueCapacity` | `8192` | バッファ付きチャネルの容量 |
| `BatchSize` | `64` | 即時フラッシュの閾値、および Drain 時のチャンクサイズ |
| `FlushInterval` | `15ms` | 部分バッチをコミットするまでの最大待機時間 |

### 6.2 キューへの投入

`EnqueueBilling`、`EnqueueQuotaSync`、`EnqueueRequestLog` は共通の `enqueue` を通ります。

- `sync.RWMutex` の読み取りロックを保持し、`closed` を確認してからチャネルへ送ります。
- 送信は `select` の `default` を使った非ブロッキングです。キューが満杯なら
  `port.ErrQueueFull` を返し、`dropped` カウンタを増やします。
- `queueLen` は送信前に加算し、満杯時は減算します。ライターが未計上のイベントを減算して
  `QueueDepth` が一時的に負になることを防ぎます。
- `Drain` 後に投入すると `port.ErrQueueClosed` を返します。

キューが満杯でも HTTP のレイテンシを守るため、呼び出し側は待機しません。この設計では、
飽和時にリクエストログや課金イベントの一部が失われる可能性があります。`CreditAccount` と
`ChargeAccount` は enqueue 失敗時にメモリ上の残高を巻き戻してエラーを返しますが、メータード
ルートの精算とリクエストログは enqueue エラーを無視します。残高そのものは終了時の
`SnapshotQuotas` で最終スナップショットを enqueue して永続化を試みます。

### 6.3 単一ライターとグループコミット

`New` は `run` を 1 つの goroutine として起動します。ライターは次を行います。

1. `15ms` の ticker とチャネルからの受信を `select` で待つ。
2. 受信したイベントをメモリ上の `pending` に追加する。
3. `pending` が 64 件以上になり、リトライ中でなければ、先頭 64 件をコミットする。
4. ticker が発火したら、`pending` 全体をコミットする。
5. コミットに失敗したら `retrying = true` にし、`pending` を保持したまま次の ticker で再試行する。

1 回のコミットは `BatchSink.WriteBatch` の呼び出しで、`sqlite.Store` では 1 つの
`WithWriteTx` になります。`billing_events`、`account_quotas`、`request_logs` は同じトランザクションで
書き込まれるため、途中のイベントだけが残ることはありません。

### 6.4 quota の重複排除

同じバッチに同じアカウントの quota が複数ある場合、最後の値だけを残します。

- `buildPayload` は `account_id` ごとに最新の quota を保持します。課金イベントとリクエストログは
  キューの順序を保ちます。
- `sqlite.Store.WriteBatch` も `dedupeQuotas` で再度 `account_id` ごとに最後の値を残します。
  契約側と実装側の両方で重複を排除します。

`ON CONFLICT(account_id) DO UPDATE` の upsert で `balance`、`rate_limit_rpm`、`updated_at` を
更新します。

### 6.5 `Enqueue` と `Drain` の競合安全

- `Enqueue` は `RWMutex.RLock` を保持したまま `closed` を確認し、チャネルへ送ります。
- `Drain` は `RWMutex.Lock` を取得して `closed = true` にし、チャネルを閉じます。
- この排他により、`Drain` がチャネルを閉じた瞬間に別の goroutine が閉じたチャネルへ送信して
  panic することはありません。
- `Drain` は冪等です。最初の呼び出しだけがチャネルを閉じ、以降の呼び出しは同じ `done` と
  結果を観測します。
- 最初の `Drain` 呼び出しの context が残りのコミットに使われます。後から待つ呼び出しは自分の
  context で待機を中断できますが、コミットの期限は変えられません。

### 6.6 正常終了の順序

`cmd/server` の `App.Shutdown` は次の順序を守ります。

```text
SIGINT / SIGTERM
  → http.Server.Shutdown(ctx)                 // 新規受付を停止し、実行中ハンドラの完了を待つ
  → hotpath.SnapshotQuotas(now)               // 全アカウントの最終残高を取得
  → batcher.EnqueueQuotaSync(quota) × アカウント数
  → batcher.Drain(ctx)                        // キューを閉じ、残りをコミット
  → store.Close()                             // 読み取りプール → 書き込みプールの順に閉じる
```

- `http.Server.Shutdown` を最初に置くことで、新しいリクエストがキューへ入り続けることを止めます。
- ハンドラ停止後に `SnapshotQuotas` を実行するのは、ハンドラの完了順が入れ替わっても、古い残高が
  最後の書き込みになるのを防ぐためです。各アカウントについて、停止時点の権威ある残高を 1 件
  enqueue します。
- `batcher.Drain` は閉じたチャネルに残ったイベントとメモリ上の `pending` を、64 件ずつ
  コミットします。
- 最後に `store.Close` を呼びます。順序を逆にすると、受付中のリクエストや Batcher が閉じた
  ストアへ書き込もうとします。

`App.Shutdown` は `sync.Once` で 1 回だけ実行され、発生したエラーは `errors.Join` でまとめて
返します。`SHUTDOWN_TIMEOUT` はこの一連の処理に使う context の期限です。

### 6.7 統計

`Batcher.Stats` は次を返します。

| フィールド | 意味 |
| --- | --- |
| `enqueued` | キューへ正常に投入されたイベント数 |
| `written` | sink へのコミットに成功したイベント数 |
| `dropped` | キュー満杯で破棄されたイベント数 |
| `queue_depth` | キューにあるが、まだライターのメモリバッチへ移っていないイベント数 |
| `queue_capacity` | チャネル容量（8192） |
| `flush_commits` | 成功したコミット回数 |

## 7. HTTP 境界

### 7.1 ルーティング

`httpapi.NewHandler` は `http.NewServeMux` にルートを登録します。

- 埋め込みコンソール: `GET /{$}` と `GET /assets/{path...}`
- 非課金の制御・台帳: `/healthz`、`/api/v1/runtime/*`、`/api/v1/accounts/*`、
  `/api/v1/billing/events`、`/api/v1/logs`
- 課金対象: users、teams、memberships
- `/api/v1/accounts/{account_id}/charge` は `Authorize` のみを行う特別なガード
- それ以外は JSON の 404 エラー

メソッド付きパターンに一致しないパスやメソッドは、最後の `/` ハンドラが JSON の 404 を返します。

### 7.2 ミドルウェアの順序

外側から内側へ次の順で適用されます。

```text
withRequestLog
  → withRequestID
    → withBodyLimit
      → withAccountID
        → withPanicRecovery
          → http.ServeMux
            → withGuard（課金対象のみ）
              → withPanicRecovery
                → ハンドラ
```

- `withRequestLog` は `/api/` で始まるパスだけを対象に、ステータスとレイテンシを非同期キューへ
  積みます。コンソール、アセット、`/healthz` は記録しません。
- `withRequestID` は `X-Request-ID` を引き継ぐか ULID を生成し、リクエストとレスポンスの両方に
  設定します。
- `withBodyLimit` は `http.MaxBytesReader` でボディを 1 MiB に制限します。
- `withAccountID` は `X-Account-ID` を解決し、空なら `default` を使います。実効値はレスポンス
  ヘッダにも設定します。
- `withPanicRecovery` は panic を JSON の 500 エラーに変換します。すでにヘッダを書き終えている
  場合は何もしません。
- `withGuard` はホットパスの admission を行い、`X-RateLimit-Limit`、
  `X-RateLimit-Remaining`、`X-Account-Balance` を設定します。

### 7.3 エラー分類

ユースケースは `usecase.ErrorCode` に分類し、HTTP 層が一元的にステータスへ変換します。

| `ErrorCode` | HTTP | 発生例 |
| --- | --- | --- |
| `VALIDATION_ERROR` | 400 | 不正な ULID、空の名前、1 MiB 超のボディ |
| `INSUFFICIENT_BALANCE` | 402 | 残高不足 |
| `NOT_FOUND` | 404 | 存在しない ID、未知のルート |
| `CONFLICT` | 409 | 現在のメンバーシップ期間の重複、不正な状態遷移 |
| `CONCURRENCY_CONFLICT` | 409 | `expected_version` の不一致 |
| `RATE_LIMITED` | 429 | 1 分間のレート上限超過 |
| `ROUTE_DISABLED` | 503 | メンテナンスモード、無効化されたルート |
| `UNEXPECTED` | 500 | 分類できない内部エラー |

### 7.4 サーバータイムアウト

`cmd/server` は次の `http.Server` タイムアウトを設定します。

| 設定 | 値 |
| --- | --- |
| `ReadHeaderTimeout` | 5 秒 |
| `ReadTimeout` | 15 秒 |
| `WriteTimeout` | 30 秒 |
| `IdleTimeout` | 60 秒 |

### 7.5 埋め込みコンソール

`web/embed.go` は `//go:embed static/*` で `web/static/` を埋め込み、`StaticFS` が
`fs.Sub` で `static` をルートにした読み取り専用ファイルシステムを返します。`httpapi` は
`index.html` を読み込み、`/` では `Cache-Control: no-cache` を付けて配信します。
`/assets/{path...}` は `http.FileServerFS` で配信し、存在しないアセットは JSON の 404 を返します。

## 8. マイグレーション

### 8.1 埋め込みと適用

`migrations/embed.go` は `//go:embed *.sql` で SQL ファイルをバイナリに埋め込みます。
`sqlite.Store.migrate` は起動時に次を行います。

1. `schema_migrations` テーブルを `CREATE TABLE IF NOT EXISTS` で用意する。

   ```sql
   CREATE TABLE schema_migrations (
       version TEXT PRIMARY KEY,
       applied_at TEXT NOT NULL
   );
   ```

2. `migrations.FS` から `*.sql` を列挙し、ファイル名で昇順にソートする。
3. `schema_migrations` の `version` を読み、適用済みの集合を作る。
4. 未適用のファイルを順に `ExecContext` で実行し、`version`（ファイル名）と `applied_at`
   （UTC の RFC3339Nano）を記録する。
5. ここまでの処理を 1 つの `WithWriteTx` でコミットする。

`schema_migrations` の作成、未適用マイグレーションの実行、適用記録は同じトランザクションです。
途中で失敗すればすべてロールバックされ、サーバーは起動しません。

### 8.2 運用規則

- 適用済みのマイグレーションファイルは編集しません。チェックサムは記録していないため、編集しても
  既存環境には再適用されず、環境間でスキーマが黙って分岐します。
- スキーマ変更は新しいファイル（例: `0002_add_index.sql`）として追加します。
- ファイル名は辞書順で適用されるため、ゼロ埋めした連番を使います。
- ダウンマイグレーションはありません。ロールバックが必要な場合は、変更前のバックアップまたは
  Litestream の復元ポイントから戻します。
- 1 つのファイルに複数の DDL を書けます。ファイル全体が 1 つの書き込みトランザクションで
  実行されます。

### 8.3 現在のスキーマ

`0001_init.sql` は次を作成します。

| テーブル | 主な列 | インデックス |
| --- | --- | --- |
| `users` | `id`、`display_name`、`email`、`version`、`created_at`、`updated_at` | `email` の UNIQUE、`idx_users_display_name` |
| `teams` | `id`、`name`、`version`、`created_at`、`updated_at` | `idx_teams_name` |
| `team_memberships` | `id`、`team_id`、`user_id`、`role`、`status`、`version`、`created_at`、`updated_at` | `idx_team_memberships_team_id`、`idx_team_memberships_user_id`、`uq_team_memberships_current_period` |
| `account_quotas` | `account_id`、`balance`、`rate_limit_rpm`、`updated_at` | `account_id` の PRIMARY KEY |
| `billing_events` | `id`、`account_id`、`amount`、`reason`、`request_id`、`occurred_at` | `idx_billing_events_account_occurred` |
| `request_logs` | `id`、`request_id`、`account_id`、`method`、`path`、`status_code`、`latency_us`、`occurred_at` | `idx_request_logs_occurred` |

## 9. 設計判断

### 9.1 単一バイナリと SQLite

- 配布物が 1 つで済み、`systemd` の unit と環境変数だけで運用できます。追加のコンテナ基盤や
  外部 DB のオーケストレーションは不要です。
- SQLite の WAL は単一ノードで読み取りと書き込みを並行させられます。このテンプレートの
  ワークロードは 1 プロセス・1 ノードを前提としており、追加の外部データストアを導入する理由が
  ありません。
- バックアップと PITR は Litestream に任せ、アプリケーションは WAL のチェックポイントを
  Litestream に譲ります。

### 9.2 二重プール

書き込みを 1 接続に絞り、読み取りを別プールに分けることで、書き込みの直列化と読み取りの並行性を
両立します。`mode=ro` と `_query_only=on` の二重の防御により、読み取り経路の書き込み事故を
防ぎます。

### 9.3 ホットパス

残高とレート制限の判定をメモリ上で行うことで、API のレイテンシから SQLite の fsync を除外します。
設定はイミュータブルなスナップショットとして `atomic.Pointer` で差し替え、アカウント単位の
ロックだけで競合を局所化します。

### 9.4 グループコミット

1 リクエストごとに SQLite へ書き込むと、fsync がボトルネックになります。Batcher は 15ms または
64 件を単位に 1 トランザクションへまとめ、スループットを確保します。quota は最後の値だけを
残すことで、同じアカウントの更新を圧縮します。

### 9.5 契約による分離

`internal/contract/port` のインターフェースにより、`usecase` と `presentation` は SQLite や
ホットパスの実装を知りません。`BatchSink` は Batcher と SQLite の結合を契約に置き換え、
`internal/infra/*` の相互 import を禁止する規則と合わせて、交換可能性とテスト容易性を確保します。

### 9.6 ULID

ULID は時刻順にソートでき、26 文字の固定長 `TEXT` として扱えます。`ParseStrict` で外部入力を
厳密に検証し、短縮形や曖昧な表現を永続化層へ通しません。

### 9.7 トレードオフ

- 単一ノードの SQLite であるため、水平スケールや複数ライターの分散は想定していません。
- Batcher は非ブロッキングなので、飽和時には監査イベントが破棄される可能性があります。
  残高の最終値は終了時のスナップショットで永続化を試みますが、監査履歴の完全性を最優先する場合は
  キュー容量と負荷を監視してください。
- 監査履歴は非同期に永続化されるため、強制終了時には直近のイベントが失われる可能性があります。
  復旧ポイントは Litestream の同期間隔（10 秒）に依存します。
