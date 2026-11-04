# テストスイート（tests/）

このディレクトリには、アプリケーションの自動テストコードが含まれています。
機能追加時のテスト作成手順や全体フローは、**[機能追加ガイド（ADDING_FEATURE.md）](../docs/development/ADDING_FEATURE.md)** を参照してください。

---

## ディレクトリ構成とテストの役割

- **`domain/`**: ドメイン層（集約・Value Object）の単体テスト。
  - `aggregates/`: 集約の状態遷移・不変条件・同一性テスト（例: [`test_team_membership.py`](domain/aggregates/test_team_membership.py) に許可・拒否の遷移表を定義）。
  - `value_objects/`: 値オブジェクトの検証と等価性テスト。
  - [`aggregate_cases.py`](domain/aggregate_cases.py): 4集約サンプル・状態列挙・例外理由の定義。
  - [`test_aggregate_contracts.py`](domain/test_aggregate_contracts.py): 全集約が同一性・可変性・hash契約を満たすかを共通assertで検証。
- **`usecases/`**: ユースケース層（アプリケーションロジック・Mediator）のテスト。
  - 入力DTOからドメイン操作、UoWを通じた保存、結果DTO・Result型の返却フローを検証。
- **`infrastructure/`**: インフラストラクチャ層（DB・リポジトリ・外部連携）の統合テスト。
  - [`test_repositories.py`](infrastructure/test_repositories.py): 汎用リポジトリの保存、Version管理（楽観ロック競合）、削除、ロールバックの検証。
  - [`test_unit_of_work.py`](infrastructure/test_unit_of_work.py): トランザクション境界、コミット、ロールバック伝播、専用Repository取得の検証。
  - [`test_domain_mappings.py`](infrastructure/test_domain_mappings.py): ドメイン ↔ ORMマッパーの復元状態の完全性と不正データの拒否検証。
  - [`test_registration_completeness.py`](infrastructure/test_registration_completeness.py): ORMモデル・明示的マッピング・Handlerの登録漏れやVersion設定漏れを検出。
  - [`test_aggregate_persistence.py`](infrastructure/test_aggregate_persistence.py)（モデル定義: [`helpers/aggregate_persistence.py`](helpers/aggregate_persistence.py)）: 親子集約の完全スナップショット保存、実race競合検出とロールバック、専用RepositoryとUoWのトランザクション共有・ライフサイクル検証。
- **`presentation/`**: プレゼンテーション層（Discord Bot Cog、Web API Router、Worker、LINE）のテスト。
  - [`bot/test_intents.py`](presentation/bot/test_intents.py): Bot が最小権限の Gateway Intents のみを使うことの回帰テスト。
  - [`test_entrypoint_environment.py`](presentation/test_entrypoint_environment.py): 各エントリポイントが共通環境ローダー（`app.environment`）を使うことの検証。
- **`test_environment.py`** / **`test_alembic_env.py`**: 共通環境ローダーと Alembic が隔離ルート・疑似クレデンシャルだけで環境変数を解決することの検証。
- **[`conftest.py`](conftest.py)**: pytest共通フィクスチャ定義（テスト用DBエンジン `database_engine`, アプリDBバインド `test_db_engine`, `session_factory`, `uow`, `chat_history_query`, モックイベントバス `event_bus`, dotenv汚染防止 `isolated_environment`）。※Mediatorフィクスチャは提供していません。

---

## データベーステスト環境（TEST_DATABASE_URL）

データベースを伴うテストは、環境変数 `TEST_DATABASE_URL` により接続先と分離方式を制御します。

- **未指定、または `sqlite+aiosqlite://`（デフォルト）**:
  - テストケースごとに一時ファイルSQLite（`test.db`）が作成されます。
  - 接続ごとに外部キー制約（`PRAGMA foreign_keys=ON`）が有効化され、独立した接続でテストされます。
- **`postgresql+asyncpg://...`**:
  - テストケースごとにUUIDを用いた独立スキーマ（`test_<uuid>`）が作成され、テスト終了時に自動的に削除（`DROP SCHEMA CASCADE`）されます。
  - CI環境ではPostgreSQL 16サービス上で同一の全テストスイートが実行されます。

> [!CAUTION]
> PostgreSQLを指定する場合、必ず**テスト専用のデータベースURL**を指定してください。開発用・実運用のデータベースに向けて実行してはなりません。

---

## テスト実行コマンド

プロジェクトの標準コマンド（`uv` 経由）でテストを実行します。

```bash
# 全テスト実行（SQLite一時ファイル、デフォルト）
TEST_DATABASE_URL=sqlite+aiosqlite:// uv run --frozen --all-extras pytest

# PostgreSQL環境での実行例（テスト用DB）
TEST_DATABASE_URL=postgresql+asyncpg://postgres:password@localhost:5432/test_db uv run --frozen --all-extras pytest

# カバレッジ付きで実行
uv run --frozen --all-extras pytest --cov=app --cov-report=term-missing

# 特定のディレクトリやファイルのみ実行
uv run --frozen --all-extras pytest tests/domain/
uv run --frozen --all-extras pytest tests/infrastructure/test_domain_mappings.py
```
