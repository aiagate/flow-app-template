# 入口ごとのオプション依存（Optional Extras）

このドキュメントは、Flow App Template のコア依存と入口（API / Discord Bot / LINE /
Worker）ごとのオプション依存の分け方、開発・CI での扱いを定めます。
依存関係の正本は [`pyproject.toml`](../../pyproject.toml) と `uv.lock` です。

## 方針

- コア（`[project.dependencies]`）は、どの入口でも使う基盤だけを持つ。
  具体的には DB・マイグレーション・DI・Mediator・Result 型・環境変数ローダー。
- 入口固有の SDK は `[project.optional-dependencies]` の extra に置く。
  使わない入口の SDK はインストールされない。
- extra 名は入口名に対応させる（`api` / `discord` / `line`）。
- Worker は追加 SDK を持たないため extra を持たず、コアのみで動作する。

## extra 一覧

| extra     | インストールされる SDK                     | 対応する入口・起動コマンド |
| :-------- | :----------------------------------------- | :------------------------- |
| （なし）  | なし（コアのみ）                           | Worker                     |
| `api`     | `fastapi`, `uvicorn`                       | `uv run --extra api start-api`         |
| `discord` | `discord-py`                               | `uv run --extra discord start-bot`         |
| `line`    | `line-bot-sdk`, `fastapi`, `uvicorn`       | `uv run --extra line start-line`        |

LINE の Webhook は FastAPI アプリケーションとして実装しているため、`line` extra は
`api` extra と同じ Web スタックもインストールします。`api` extra と `line` extra の
両方に `fastapi` / `uvicorn` を明示しているのは、extra を単独で指定しても起動できる
ようにするためです（extra 間の暗黙依存を作らない）。

## インストール

コアのみ:

```bash
uv sync --frozen --no-dev
```

API のみ:

```bash
uv sync --frozen --no-dev --extra api
uv run --frozen --no-dev --extra api start-api
```

Discord Bot のみ:

```bash
uv sync --frozen --no-dev --extra discord
uv run --frozen --no-dev --extra discord start-bot
```

LINE のみ:

```bash
uv sync --frozen --no-dev --extra line
uv run --frozen --no-dev --extra line start-line
```

全入口をまとめて（開発・テスト用）:

```bash
uv sync --frozen --all-extras
```

`uv run --extra <name> ...` は実行時にその extra を同期します。複数の入口を同時に
動かす場合は `--all-extras` を使用します。

## 開発と CI

- リポジトリのテストは API / Discord / LINE の各 SDK を import するため、
  開発環境と CI のフルテストは `--all-extras` で依存をインストールする。
- CI の `lint-and-test` ジョブは `uv sync --frozen --all-extras` を使用する。
- CI の `optional-extras` ジョブは、コア・`api`・`discord`・`line` の
  使い捨て環境を uv で作り、不要な SDK が入っていないことと、コア import と
  API lifespan が動くことを検証する。
- 新しい入口を追加するときは、その SDK をコアへ追加せず extra を追加する。
  追加は `uv add --optional <extra> <package>` を使用する（`pyproject.toml` の
  直接編集は行わない）。

```bash
uv add --optional api "fastapi>=0.124.4"
```

既存のコア依存を extra へ移す場合は、先に `uv remove` でコアから外し、
`uv add --optional` で extra へ追加する。`uv.lock` は `uv lock` で必要最小限の
差分に留める。

## DB ドライバを分割しない理由

`aiosqlite`（SQLite）と `asyncpg`（PostgreSQL）はコア依存のままとする。
DB アクセスは全入口が使うインフラ層の責務であり、ドライバを extra に分けると
どの入口でも「どの DB を使うか」をインストール時に選ばせることになる。
現行のテストは SQLite と PostgreSQL の両方で同じスイートを実行するため、
ドライバ分割は今回の入口SDK分離とは別の判断とする。単一DBのみを使うアプリでは
導入量を減らせる利点があるため、依存量と選択・検証経路の維持費を比較して再検討する。

## 検証

構造（extra の定義と `uv.lock` の一致、コアに SDK が無いこと）は常時実行される
テストで検証する:

```bash
uv run --frozen --all-extras pytest tests/tooling/test_optional_dependencies.py -p no:cacheprovider --no-cov
```

使い捨て環境での分離検証は、明示的に有効化して実行する。テストは
`UV_PROJECT_ENVIRONMENT` を一時ディレクトリへ向け、`PYTHONPATH` /
`PYTHONHOME` を引き継がずに `uv sync --frozen --no-dev` を実行する:

```bash
FLOW_APP_OPTIONAL_EXTRAS_INTEGRATION=1 \
  uv run --frozen --all-extras pytest tests/tooling/test_optional_dependencies.py \
  -p no:cacheprovider --no-cov
```

検証内容:

- コア環境: `app.domain` / `app.infrastructure.database` / `app.container` が
  import でき、Worker のエントリポイントが import できる。
  `fastapi` / `discord` / `linebot` は存在しない。
- `api` 環境: `fastapi` / `uvicorn` が存在し、API の lifespan が起動して
  Mediator を初期化できる。`discord` / `linebot` は存在しない。
- `discord` 環境: `discord` のみ存在し、Bot のエントリポイントが import できる。
  Web SDK と LINE SDK は存在しない。
- `line` 環境: `linebot` と Web スタックが存在し、LINE のエントリポイントが
  import できる。`discord` は存在しない。
