# データベースマイグレーションガイド

このドキュメントは、Alembicを使用したデータベースマイグレーションの作成・管理・検証方法を説明します。
集約・機能追加の一連の手順全体は、正本ガイドである **[機能追加ガイド（ADDING_FEATURE.md）](../development/ADDING_FEATURE.md)** を参照してください。

## Membershipの現在期間一意性

`team_memberships` には、`status` が `PENDING` または `ACTIVE` の行について
`(team_id, user_id)` を一意にする部分インデックスを作成します。`LEAVED` 行は加入期間の
履歴なので、同じ組み合わせで複数行を保持できます。再加入は新しいMembership行を作ります。

前方マイグレーションはインデックス作成前に現在期間の重複を検出します。重複がある場合は
エラーで停止し、行の削除・自動統合・勝手な勝者選択を行いません。運用者が業務判断で
重複を解消してから、同じマイグレーションを再実行してください。

---

## 目次

- [概要](#概要)
- [マイグレーション作成と検証の基本フロー](#マイグレーション作成と検証の基本フロー)
- [マイグレーションパターン](#マイグレーションパターン)
- [よくあるケース](#よくあるケース)
- [トラブルシューティング](#トラブルシューティング)
- [ベストプラクティス](#ベストプラクティス)

---

## 概要

### Alembicとは

[Alembic](https://alembic.sqlalchemy.org/) は、SQLAlchemyのためのデータベースマイグレーションツールです。このプロジェクトでは、SQLModelと組み合わせて使用しています。

### プロジェクトの構成

```
.
├── alembic/
│   ├── env.py                # Alembic環境設定
│   ├── script.py.mako        # マイグレーションファイルのテンプレート
│   └── versions/             # マイグレーションファイル格納ディレクトリ
├── alembic.ini               # Alembic設定ファイル
└── src/app/
    └── infrastructure/
        └── orm_models/       # SQLModel ORMモデル
```

### データベース設定

- **開発環境**: SQLite (`bot.db`)
- **本番環境**: 環境変数 `DATABASE_URL` で指定
- **非同期対応**: `aiosqlite` / `asyncpg` を使用

---

## マイグレーション作成と検証の基本フロー

**基本方針**: 既存のテーブル構造や列で対応できる場合は、不要なマイグレーションを作成しません。スキーマ変更が必要な場合のみ、以下のフローで作成・検証します。

> [!WARNING]
> テスト実行時（`conftest.py`）の `SQLModel.metadata.create_all()` はモデル定義から直接テーブルを作成するため、**テストが通過してもマイグレーションスクリプトが正しく動作することの検証にはなりません**。必ず以下の独立した検証手順を実施してください。

### 1. ORMモデルの変更と読み込み正本への登録

`src/app/infrastructure/orm_models/` 配下のORMモデルを変更・追加します。
作成・変更したモデルは、必ず [`src/app/infrastructure/orm_models/__init__.py`](../../src/app/infrastructure/orm_models/__init__.py) でインポートし、`__all__` に含めてください。
Alembic（`alembic/env.py`）はこのパッケージから全モデルを一括で読み込みます。

### 2. 使い捨てデータベースでの検証フロー

既存の開発用や実運用のデータベースに影響を与えないよう、`mktemp -d` で作成した専用一時ディレクトリ内で括弧のsubshell `( ... )` を用いて検証を行います。これにより終了後に利用者のshell環境へ環境変数を残さず、既存のデータベースファイルを誤って操作・削除するリスクを防ぎます。
検証は「リビジョン生成」と「往復適用・整合性確認」の2段階に分け、生成後の目視レビューを必ず挟んで進めます。

#### フェーズ 1: 一時DBでのマイグレーション自動生成

一時DBを現在の最新headまで適用した上で、現在のORMモデルとの差分から新しいリビジョンファイルを生成します。

```bash
(
  set -e
  TMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TMP_DIR"' EXIT
  export DATABASE_URL="sqlite+aiosqlite:///$TMP_DIR/migration_gen.db"

  # 現在のheadまで適用
  uv run alembic upgrade head

  # リビジョンファイルを自動生成
  uv run alembic revision --autogenerate -m "変更内容の説明"
)
```

#### フェーズ 2: 生成されたリビジョンファイルの目視確認と手動修正

自動生成された `alembic/versions/<revision_id>_<message>.py` をエディタで開き、**必ず人の目で内容を確認・手動修正**します。

> [!IMPORTANT]
> **自動生成の限界と目視確認**:
> Alembicの自動生成はテーブル追加や列追加を検出できますが、テーブル名や列名の変更（dropとaddとして誤検出される）、一部のCHECK制約・外部キー制約、部分一意インデックスの条件などを正確に検出できない制限があります。
> 参考: [Alembic Autogenerateの対象と限界（公式ドキュメント）](https://alembic.sqlalchemy.org/en/latest/autogenerate.html)、[Alembic check コマンド（公式ドキュメント）](https://alembic.sqlalchemy.org/en/latest/api/commands.html#alembic.command.check)

また、生成されたファイル冒頭の `down_revision` の値（直前のリビジョンID文字列、例: `'0195e4e8979b'`）を確認し、次の検証フェーズで使用します。

#### フェーズ 3: 別の一時DBでの往復適用とデータ保持・整合性の検証

生成したマイグレーションが既存データを含む環境で正常に適用でき、ダウングレードやクリーンインストールでも不整合が起きないかを、**別の一時DB**を作成して検証します。

> [!CAUTION]
> 以下のスクリプトを実行する前に、必ず `DOWN_REVISION` 変数の値をフェーズ2で確認した生成ファイルの `down_revision`（例: `"0195e4e8979b"`。最初のマイグレーションなら `"base"`）に置き換えてください。架空の値のまま実行してはなりません。

```bash
(
  set -e
  TMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TMP_DIR"' EXIT
  export DATABASE_URL="sqlite+aiosqlite:///$TMP_DIR/migration_test.db"

  # ★必ず生成された alembic/versions/*.py の down_revision の値に置き換えて実行してください
  DOWN_REVISION="<生成ファイルで確認したdown_revisionの値を設定>"

  # 1. 変更直前の旧リビジョン（down_revision）まで適用
  uv run alembic upgrade "$DOWN_REVISION"

  # 2. 代表的な旧データを投入し、移行前のデータ状態を準備（必要に応じてPythonスクリプト等で実施）

  # 3. 新しいheadへアップグレードし、旧データが保持・適切に変換されているかを検証
  uv run alembic upgrade head

  # 4. 1つ前のリビジョンにロールバック（ダウングレード）
  uv run alembic downgrade -1

  # 5. 再度新headへアップグレードし、旧データが維持されていることを検証
  uv run alembic upgrade head

  # 6. ORMモデル定義とデータベーススキーマの乖離を検査
  uv run alembic check

  # 7. 別の空DBから直接新headまで適用し、新規構築（クリーンインストール）の整合性を確認
  export DATABASE_URL="sqlite+aiosqlite:///$TMP_DIR/migration_clean.db"
  uv run alembic upgrade head
  uv run alembic check
)
```

---

## マイグレーションパターン

### テーブル作成

```python
def upgrade() -> None:
    op.create_table(
        'users',
        sa.Column('id', sa.String(length=26), nullable=False),
        sa.Column('name', sa.String(length=255), nullable=False),
        sa.Column('email', sa.String(length=255), nullable=False),
        sa.PrimaryKeyConstraint('id')
    )
    op.create_index(op.f('ix_users_email'), 'users', ['email'], unique=True)

def downgrade() -> None:
    op.drop_index(op.f('ix_users_email'), table_name='users')
    op.drop_table('users')
```

### カラム追加

```python
def upgrade() -> None:
    op.add_column('users', sa.Column('phone', sa.String(length=20), nullable=True))

def downgrade() -> None:
    op.drop_column('users', 'phone')
```

### カラム名変更（SQLite対応）

**重要**: SQLiteは `ALTER COLUMN` を直接サポートしていないため、以下のパターンを使用します。

```python
import sqlalchemy as sa

def upgrade() -> None:
    """Rename name column to display_name (SQLite compatible)."""
    # Step 1: インデックスを削除（存在する場合）
    op.drop_index(op.f('ix_users_name'), table_name='users')

    # Step 2: 新しいカラムを追加
    op.add_column('users', sa.Column('display_name', sa.String(length=255), nullable=True))

    # Step 3: データをコピー
    op.execute('UPDATE users SET display_name = name')

    # Step 4: 古いカラムを削除
    op.drop_column('users', 'name')

    # Step 5: 新しいインデックスを作成
    op.create_index(op.f('ix_users_display_name'), 'users', ['display_name'], unique=False)

def downgrade() -> None:
    """Revert display_name column back to name (SQLite compatible)."""
    op.drop_index(op.f('ix_users_display_name'), table_name='users')
    op.add_column('users', sa.Column('name', sa.String(length=255), nullable=True))
    op.execute('UPDATE users SET name = display_name')
    op.drop_column('users', 'display_name')
    op.create_index(op.f('ix_users_name'), 'users', ['name'], unique=False)
```

### カラム型変更（SQLite対応）

```python
def upgrade() -> None:
    """Change timestamp columns to datetime type (SQLite compatible)."""
    # Step 1: 新しい型のカラムを追加
    op.add_column(
        'users',
        sa.Column('created_at_new', sa.DateTime(timezone=True), server_default=sa.func.now())
    )

    # Step 2: データを変換してコピー
    op.execute(
        "UPDATE users SET created_at_new = datetime(created_at)"
    )

    # Step 3: 古いカラムを削除
    op.drop_column('users', 'created_at')

    # Step 4: 新しいカラムをリネーム
    op.alter_column('users', 'created_at_new', new_column_name='created_at')

def downgrade() -> None:
    """Revert datetime columns back to string type."""
    op.add_column('users', sa.Column('created_at_new', sa.String(), nullable=True))
    op.execute("UPDATE users SET created_at_new = created_at")
    op.drop_column('users', 'created_at')
    op.alter_column('users', 'created_at_new', new_column_name='created_at')
```

### プライマリキーの変更

**警告**: プライマリキーの変更は破壊的な操作です。既存データの扱いに注意してください。

```python
def upgrade() -> None:
    """Change user id from int to ULID (string)."""
    # データを削除（開発環境のみ推奨）
    op.execute('DELETE FROM users')

    # 主キー制約を削除
    op.drop_constraint('users_pkey', 'users', type_='primary')
    op.drop_column('users', 'id')

    # 新しいカラムを追加
    op.add_column(
        'users',
        sa.Column('id', sa.String(length=26), nullable=False),
    )

    # 主キー制約を再作成
    op.create_primary_key('users_pkey', 'users', ['id'])

def downgrade() -> None:
    """Revert user id back from ULID to int."""
    op.execute('DELETE FROM users')
    op.drop_constraint('users_pkey', 'users', type_='primary')
    op.drop_column('users', 'id')
    op.add_column(
        'users',
        sa.Column('id', sa.Integer(), nullable=False, autoincrement=True),
    )
    op.create_primary_key('users_pkey', 'users', ['id'])
```

### インデックスの操作

```python
def upgrade() -> None:
    # インデックス作成
    op.create_index(op.f('ix_users_email'), 'users', ['email'], unique=True)

    # 複合インデックス
    op.create_index('ix_users_name_email', 'users', ['name', 'email'], unique=False)

def downgrade() -> None:
    op.drop_index(op.f('ix_users_email'), table_name='users')
    op.drop_index('ix_users_name_email', table_name='users')
```

---

## よくあるケース

### 新しいテーブルを追加する

1. `src/app/infrastructure/orm_models/` に新しいORMモデルを作成します。
2. `src/app/infrastructure/orm_models/__init__.py` に新しいモデルをインポートし、`__all__` に追加します（Alembicは `alembic/env.py` 経由でこのパッケージから全モデルを一括読み込みするため）。
3. [マイグレーション作成と検証の基本フロー](#マイグレーション作成と検証の基本フロー) に従って、一時DBでのリビジョン自動生成、生成ファイルの目視確認、別一時DBでの往復検証（`upgrade` / `downgrade` / `check`）を実施します。

### 既存のテーブルにカラムを追加する

1. ORMモデルにカラムを追加
2. マイグレーションを生成:

   ```bash
   uv run alembic revision -m "add phone column to users"
   ```

3. `upgrade()` と `downgrade()` を実装
4. マイグレーションを実行

### カラム名を変更する

1. ORMモデルのカラム名を変更
2. マイグレーションを生成:

   ```bash
   uv run alembic revision -m "rename user name to display name"
   ```

3. SQLite対応のリネームパターンを実装（上記参照）
4. マイグレーションを実行

---

## トラブルシューティング

### エラー: "no such index"

**原因**: 削除しようとしているインデックスがデータベースに存在しない。

**対処法**:

1. 現在のデータベーススキーマを確認:

   ```bash
   uv run python -c "import sqlite3; conn = sqlite3.connect('bot.db'); cursor = conn.cursor(); cursor.execute('PRAGMA index_list(users)'); print(cursor.fetchall()); conn.close()"
   ```

2. マイグレーションを条件付きにするか、`alembic stamp` でマイグレーション履歴をマーク:

   ```bash
   uv run alembic stamp head
   ```

### エラー: "no such column"

**原因**: 参照しているカラムがデータベースに存在しない。

**対処法**:

1. 現在のテーブル構造を確認:

   ```bash
   uv run python -c "import sqlite3; conn = sqlite3.connect('bot.db'); cursor = conn.cursor(); cursor.execute('PRAGMA table_info(users)'); print(cursor.fetchall()); conn.close()"
   ```

2. マイグレーション順序を確認:

   ```bash
   uv run alembic history
   ```

3. 必要に応じてデータベースをリセット:

   ```bash
   rm bot.db
   uv run alembic upgrade head
   ```

### マイグレーション履歴の不整合

**対処法**:

1. 現在の状態を確認:

   ```bash
   uv run alembic current
   uv run alembic heads
   ```

2. 特定のリビジョンにマーク:

   ```bash
   uv run alembic stamp <revision_id>
   ```

### 開発中にスキーマが手動で変更された

**対処法**:
マイグレーション履歴をデータベースの実際の状態に合わせる:

```bash
uv run alembic stamp head
```

---

## ベストプラクティス

### 1. マイグレーションの粒度

- **1つのマイグレーションは1つの論理的な変更のみ**を含める
- 大きな変更は複数のマイグレーションに分割する
- テーブル作成とデータ投入は別々のマイグレーションにする

### 2. ダウングレードの実装

- 必ず `downgrade()` を実装する
- ダウングレードが不可能な場合（データ削除など）はコメントで明記する

### 3. データの扱い

- **破壊的な変更には十分注意する**
- データマイグレーション（既存データの変換）が必要な場合は、慎重にテストする
- 本番環境ではバックアップを取ってから実行する

### 4. SQLite互換性

- プロジェクトはSQLiteをデフォルトで使用しているため、SQLite互換のパターンを使用する
- `ALTER COLUMN` は使用せず、カラム追加 → データコピー → カラム削除のパターンを使用

### 5. テスト

マイグレーション実行後は必ずテストを実行:

```bash
uv run --frozen --all-extras pytest
```

### 6. コミット前の確認

```bash
# マイグレーションをテスト
uv run alembic upgrade head
uv run alembic downgrade -1
uv run alembic upgrade head

# テストを実行
uv run --frozen --all-extras pytest

# コードフォーマット
uv run --frozen ruff format .
uv run --frozen ruff check . --fix
```

### 7. マイグレーションファイルの管理

- **既存のマイグレーションファイルは絶対に変更しない**
- 修正が必要な場合は新しいマイグレーションを作成する
- マイグレーションファイルはバージョン管理に含める

### 8. 命名規則

マイグレーションメッセージは明確で簡潔に:

- [OK] `"add teams table"`
- [OK] `"rename user name to display name"`
- [OK] `"change timestamp columns to datetime type"`
- [NG] `"update"`
- [NG] `"fix"`

---

## 参考資料

- [Alembic公式ドキュメント](https://alembic.sqlalchemy.org/)
- [SQLModel公式ドキュメント](https://sqlmodel.tiangolo.com/)
- [SQLAlchemy公式ドキュメント](https://docs.sqlalchemy.org/)

---

## 関連ドキュメント

- [機能追加ガイド](../development/ADDING_FEATURE.md)
- [アーキテクチャ設計](../ARCHITECTURE.md)
- [ドメイン実装ガイド](../domain/DOMAIN_IMPLEMENTATION_GUIDE.md)
