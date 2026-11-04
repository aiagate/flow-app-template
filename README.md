# Flow App Template

## 概要

**※現在製作中のプロジェクトです。予期せぬバグ、不具合などが含まれる可能性があります。**

設計の一貫性を保ち、業務ロジックに集中するためのPythonアプリケーションテンプレート。

開発者はユースケース・業務ルールの実装に集中できます。クリーンアーキテクチャと
プロジェクトの規約、契約テストが設計の一貫性を保つ助けになります。Discord Bot /
HTTP API / LINE / Worker は任意のエントリポイントであり、このプロジェクトの
アイデンティティではありません。新機能の追加手順は
[機能追加ガイド](docs/development/ADDING_FEATURE.md) を参照してください。

## 特徴

### 1. **非同期処理の活用**

- `asyncio`を使用して非同期処理を実現。
- 高速かつ効率的な処理を可能にする設計。

### 2. **依存性注入 (DI)**

- `injector`ライブラリを使用して依存性注入を実現。
- テスト容易性とモジュール間の疎結合を実現。

### 3. **Mediatorパターンの採用**

- `flow-med`ライブラリを使用してMediatorパターンを実現。
- リクエストとハンドラーの分離により、コードの可読性と拡張性を向上。

### 4. **クリーンアーキテクチャ**

- ユースケース層 (`usecases/`) とドメイン層 (`domain/`) を明確に分離。
- ビジネスロジックとインフラストラクチャの独立性を確保。
- ドメイン層 (`domain/`) とインフラストラクチャ層 (`infrastructure/`) を完全分離。

### 5. **データベース統合**

- **SQLModel + Alembic**: 型安全なORM とマイグレーション管理。
- **非同期データベース**: aiosqlite による非同期SQLite操作。
- **クリーンアーキテクチャ準拠**: ORMモデルとドメイン集約を分離。
- **自動マイグレーション**: Alembicによるスキーマバージョン管理。

### 6. **コグ (Cog) によるコマンド管理**

- `discord.ext.commands`のCogを使用してコマンドをモジュール化。
- Botの機能を簡単に拡張可能。

### 7. **FastAPI 統合 (Web API)**

- Discord Bot を起動せずに単体で動作するREST API（Bot と並行しての起動も可能）。
- 共通のユースケースを再利用し、外部システムとの連携が可能。
- Swagger UI によるインタラクティブなドキュメント。

### 8. **型安全性**

- Pythonの型ヒントを活用し、静的解析ツールによるエラー検出を強化。

### 9. **テスト環境の整備**

- `pytest`と`anyio`を使用し、`asyncio`バックエンドで非同期テストを実行。
- `pytest-cov`によるコードカバレッジ測定。
- テストごとに独立した一時ファイルSQLite（外部キー制約有効）を使用。

### 10. **コード品質管理**

- `Ruff`: 高速なコードフォーマッターとリンター。
- `Pyright`: 厳格な型チェック (strict モード)。
- `pre-commit`: Git コミット前の自動チェック。

## ディレクトリ構成

```text
.
├── src/app/                      # アプリケーション本体
│   ├── application/              # Mediatorの構成・アプリケーション境界
│   ├── contracts/                # 複数レイヤーで共有する境界契約
│   │   ├── messages/             # イベント・共有メッセージ
│   │   └── ports/                # UoW・Query・イベントバスのPort
│   ├── domain/                   # 業務ルール（ORM・入口に依存しない）
│   │   ├── aggregates/           # ドメイン集約
│   │   ├── interfaces/           # ドメイン固有の抽象
│   │   ├── repositories/         # リポジトリ契約
│   │   └── value_objects/        # 値オブジェクト
│   ├── usecases/                 # ユースケース・入出力DTO・Handler
│   │   ├── users/                # ユーザー関連
│   │   ├── teams/                # チーム関連
│   │   ├── memberships/          # メンバーシップ関連
│   │   └── chat/                 # チャット保存
│   ├── infrastructure/           # Port・永続化の実装
│   │   ├── mappings/             # ドメイン集約とORMの明示的変換
│   │   ├── orm_models/           # SQLModelによるテーブル定義
│   │   ├── repositories/         # リポジトリ実装
│   │   └── unit_of_work.py       # トランザクションとRepositoryの寿命
│   ├── presentation/             # 必要な入口のみを利用
│   │   ├── api/                  # HTTP API (start-api)
│   │   ├── bot/                  # Discord Bot (start-bot)
│   │   ├── line/                 # LINE (start-line)
│   │   └── worker/               # プロセスローカルのイベント処理
│   ├── container.py              # DIコンテナ設定
│   └── environment.py            # 全入口共通の環境変数ローダー
├── alembic/                       # Alembicマイグレーション
│   └── versions/                  # マイグレーションファイル
├── docs/                          # ドキュメント
├── tests/                         # テストコード
├── .pre-commit-config.yaml        # Pre-commit設定
├── pyproject.toml                 # プロジェクト設定
└── README.md                      # このファイル
```

## 必要な環境

- Python 3.13 以上
- パッケージ管理 [uv](https://github.com/astral-sh/uv)
- 必要な依存関係は`pyproject.toml`に記載されています。

## セットアップ

1. 仮想環境を作成:

   ```bash
   uv venv -p 3.13 .venv
   source .venv/bin/activate  # Windows(PS)の場合は .venv\Scripts\activate
   ```

2. 依存関係をインストール（以下は全入口の開発用）:

   APIだけ等の選択導入は[入口ごとの依存管理](docs/development/OPTIONAL_ENTRYPOINTS.md)を参照してください。
   本番で不要なSDKを省く場合は、`--no-dev --extra api`等で必要な入口を指定します。

   ```bash
   uv sync --frozen --all-extras
   ```

3. Pre-commit フックをインストール:

   ```bash
   uv run pre-commit install
   ```

4. 環境変数を設定:

   `.env.example`を`.env`または`.env.local`にコピーして編集:

   ```bash
   # .env.local を作成
   cp .env.example .env.local
   ```

   `.env.local`の内容を編集:

   ```bash
   # Discord Bot を使う場合のみ必須（HTTP API単体では不要）
   DISCORD_BOT_TOKEN=your_discord_bot_token_here

   # データベースURL（オプション、デフォルト: sqlite+aiosqlite:///./bot.db）
   DATABASE_URL=sqlite+aiosqlite:///./bot.db
   ```

   環境変数の読み込みは全エントリポイント（Bot / API / Worker / LINE /
   Alembic）で共通です（`src/app/environment.py`）。優先順位は次のとおりです:

   1. 既に設定されたプロセス環境変数（外部提供分は上書きしない）
   2. プロジェクトルートの `.env.local`（開発環境用）
   3. プロジェクトルートの `.env`（`.env.local` に無いキーのフォールバック）

   読み込み対象は明示的に解決したプロジェクトルートのみで、
   カレントディレクトリやその親ディレクトリは探索しません。
   ファイルが存在しない場合は無視されます。

5. データベースマイグレーションを実行:

   ```bash
   # マイグレーション適用
   uv run alembic upgrade head

   # マイグレーション状態確認
   uv run alembic current
   ```

6. アプリケーションを起動:

   Discord Botを起動:

   ```bash
   uv run --frozen --extra discord start-bot
   ```

   または、Web APIサーバーを起動:

   ```bash
   uv run --frozen --extra api start-api
   ```

## データベース管理

### マイグレーション操作

```bash
# スキーマ変更後、マイグレーションを自動生成
uv run alembic revision --autogenerate -m "Add new field"

# マイグレーション適用
uv run alembic upgrade head

# 1つ前に戻す
uv run alembic downgrade -1

# 現在のマイグレーション確認
uv run alembic current

# マイグレーション履歴表示
uv run alembic history
```

### データベース構造

- **使用DB**: SQLite (開発時) / PostgreSQL (本番推奨)
- **ORM**: SQLModel
- **マイグレーション**: Alembic
- **非同期対応**: aiosqlite

## テスト実行

データベースを伴うテストは環境変数 `TEST_DATABASE_URL` で接続先を制御できます。
未指定または `sqlite+aiosqlite://` の場合はテストごとに独立した一時ファイルSQLite（外部キー制約有効）が使用されます。
`postgresql+asyncpg://...` を指定した場合はUUIDによる独立したスキーマで隔離実行されます。

```bash
# 全テスト実行（SQLite一時ファイル、デフォルト）
TEST_DATABASE_URL=sqlite+aiosqlite:// uv run --frozen --all-extras pytest

# PostgreSQL環境での実行例（※テスト用DBを指定すること）
TEST_DATABASE_URL=postgresql+asyncpg://postgres:password@localhost:5432/test_db uv run --frozen --all-extras pytest

# カバレッジ付きで実行
uv run --frozen --all-extras pytest --cov=app --cov-report=term-missing

# 特定のテストファイルのみ実行
uv run --frozen --all-extras pytest tests/infrastructure/test_repositories.py
```

## コード品質チェック

```bash
# フォーマット
uv run ruff format .

# リントチェック
uv run ruff check .

# リント自動修正
uv run ruff check . --fix

# 型チェック
uv run --all-extras pyright

# 全チェック実行
uv run ruff format . && \
uv run ruff check . --fix && \
uv run --all-extras pyright && \
uv run --all-extras pytest
```

## Discord Developer Portal とサーバー権限の設定（最小権限）

現行機能は最小限の Gateway Intents のみを有効化します
（`src/app/presentation/bot/intents.py`）:

| Intent               | 必要な理由                                             |
| -------------------- | ------------------------------------------------------ |
| `guilds`             | ギルド（サーバー）メタデータ。コマンドのコンテキスト取得 |
| `guild_messages`     | ギルド内メッセージイベント。`!` プレフィックスコマンドの受信 |
| `dm_messages`        | DM メッセージイベント。DM 履歴の永続化                  |
| `message_content`    | **特権 Intent（Message Content Intent）**。`!` コマンドの引数解析と DM 本文の保存 |

`message_content` は特権 Intent のため、Developer Portal の Bot 設定で
Message Content Intent を明示的に有効化する必要があります。
有効化しない Intent: メンバー一覧（members）、プレゼンス（presences）、
タイピング、音声、リアクション、BAN、招待、Webhook、自動モデレーション等は
現行機能で不要のため無効です。

Discord サーバー側で付与する権限は次の最小集合です:

- チャンネルの閲覧（View Channels）
- メッセージ送信（Send Messages）

Administrator / Manage Roles / Manage Server は不要です。
なお、ドメイン層の「チームロール（membership role）」はアプリ内データであり、
Discord のサーバーロール操作とは無関係です。

## 利用可能なDiscordコマンド

### ユーザー管理 (Users)

- `!users get <user_id>`: ユーザー情報を取得
- `!users create <name> <email>`: 新規ユーザーを作成

### チーム管理 (Teams)

- `!teams get <team_id>`: チーム情報を取得
- `!teams create <name>`: 新規チームを作成
- `!teams update <team_id> <new_name>`: チーム名を更新
- `!teams join <team_id> <user_id>`: チームに参加 (即時)
- `!teams request <team_id> <user_id>`: チーム参加リクエストを送信

### メンバーシップ管理 (Memberships)

- `!memberships approve <membership_id>`: 参加リクエストを承認
- `!memberships leave <membership_id>`: チームから脱退
- `!memberships role <membership_id> <role>`: メンバーのロールを変更

## アーキテクチャ

このテンプレートは以下のレイヤーで構成されています：

```text
┌─────────────────────────────────────┐  ┌─────────────────────────────────────┐
│    Presentation Layer (Web API)     │  │  Presentation Layer (Discord Bot)   │
│           (FastAPI)                 │  │          (discord.py)               │
└──────────────────┬──────────────────┘  └──────────────────┬──────────────────┘
                   │                                        │
┌──────────────────▼────────────────────────────────────────▼──────────────────┐
│                        Application Layer (UseCases)                          │
├──────────────────────────────────────────────────────────────────────────────┤
│                         Domain Layer (Aggregates)                            │
├──────────────────────────────────────────────────────────────────────────────┤
│                     Infrastructure Layer (Repository)                        │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 依存関係の方向

- 上位層から下位層への依存のみ許可
- ドメイン層はインフラストラクチャに依存しない
- リポジトリパターンで永続化を抽象化

### イベント配信について

イベントバスは `InMemoryEventBus`（`IEventBus` の実装）のみを使用し、
Worker を含む全プロセスで**プロセスローカルなイベント配信**を行います。
**プロセスをまたいだイベント配信（Redis 等の外部バックエンド）は行いません**。

### 詳細ドキュメント

プロジェクトの詳細なドキュメントは `docs/` ディレクトリにあります：

- **[機能追加ガイド](docs/development/ADDING_FEATURE.md)** - 新機能・集約の追加手順（正本）
- **[アーキテクチャ設計](docs/ARCHITECTURE.md)** - システム全体のアーキテクチャ詳細
- **[Domain層実装ガイド](docs/domain/DOMAIN_IMPLEMENTATION_GUIDE.md)** - ドメインモデルの実装方法
- **[データベースマイグレーション](docs/infrastructure/DATABASE_MIGRATIONS.md)** - Alembicによるマイグレーション管理

## TODO

- Domain層の拡充（より多くの集約の追加）
- 複数データベースソリューションへの対応（PostgreSQL、MySQL等）
- 認証、認可の仕組みの導入
- イベントソーシング対応

## ライセンス

このプロジェクトは[MITライセンス](LICENSE)の下で公開されています。
