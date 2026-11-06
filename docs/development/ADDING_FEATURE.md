# 機能追加ガイド

このドキュメントは、ドメインモデルを使う新しい集約や機能を追加する際の正本ガイドです。
単純な業務処理や読み取りだけの機能では、先に[実装方式の選び方](./IMPLEMENTATION_CHOICES.md)を確認してください。すべての機能に以下の全手順を要求するものではありません。
ドメイン設計からORM定義、明示的マッピング、Handler登録、Presentation層の接続、マイグレーション検証、テストまでの手順を案内します。

## 全体対応表（編集先・登録先・テスト）

実在するチーム機能（`Team`）を主例とした、各開発ステップの編集先・登録先・検証テストの対応表です。

| 開発ステップ | 主な作成・編集ファイル（例: Team） | 登録先・接続先ファイル | 確認コマンド / テストファイル |
| :--- | :--- | :--- | :--- |
| **1. Domain** | [`src/app/domain/aggregates/team.py`](../../src/app/domain/aggregates/team.py)<br>[`src/app/domain/value_objects/team_id.py`](../../src/app/domain/value_objects/team_id.py)<br>[`src/app/domain/value_objects/team_name.py`](../../src/app/domain/value_objects/team_name.py) | [`src/app/domain/aggregates/__init__.py`](../../src/app/domain/aggregates/__init__.py)<br>[`tests/domain/aggregate_cases.py`](../../tests/domain/aggregate_cases.py)<br>[`tests/domain/test_aggregate_contracts.py`](../../tests/domain/test_aggregate_contracts.py)<br>[`tests/infrastructure/test_registration_completeness.py`](../../tests/infrastructure/test_registration_completeness.py) | [`tests/domain/aggregates/test_team.py`](../../tests/domain/aggregates/test_team.py)<br>[`tests/domain/aggregates/test_team_membership.py`](../../tests/domain/aggregates/test_team_membership.py)<br>[`tests/domain/test_aggregate_contracts.py`](../../tests/domain/test_aggregate_contracts.py) |
| **2. ORM & マッピング** | [`src/app/infrastructure/orm_models/team_orm.py`](../../src/app/infrastructure/orm_models/team_orm.py)<br>[`src/app/infrastructure/mappings/team.py`](../../src/app/infrastructure/mappings/team.py) | [`src/app/infrastructure/orm_models/__init__.py`](../../src/app/infrastructure/orm_models/__init__.py)<br>[`src/app/infrastructure/orm_registry.py`](../../src/app/infrastructure/orm_registry.py) | [`tests/infrastructure/test_domain_mappings.py`](../../tests/infrastructure/test_domain_mappings.py)<br>[`tests/infrastructure/test_registration_completeness.py`](../../tests/infrastructure/test_registration_completeness.py) |
| **3. Repository & UoW** | （特殊クエリ・ロード制御が必要な場合のみ）<br>Port: `src/app/domain/repositories/...`<br>Impl: `src/app/infrastructure/repositories/...` | [`src/app/container.py`](../../src/app/container.py)<br>（UoWの `repository_factories`） | [`tests/infrastructure/test_team_repositories.py`](../../tests/infrastructure/test_team_repositories.py)<br>[`tests/infrastructure/test_unit_of_work.py`](../../tests/infrastructure/test_unit_of_work.py)<br>[`tests/infrastructure/test_aggregate_persistence.py`](../../tests/infrastructure/test_aggregate_persistence.py) |
| **4. UseCase** | [`src/app/usecases/teams/create_team.py`](../../src/app/usecases/teams/create_team.py)<br>[`src/app/usecases/teams/get_team.py`](../../src/app/usecases/teams/get_team.py)<br>[`src/app/usecases/teams/update_team.py`](../../src/app/usecases/teams/update_team.py) | [`src/app/application/mediator.py`](../../src/app/application/mediator.py)<br>（`_HANDLER_TYPES` タプル） | [`tests/usecases/teams/test_create_team.py`](../../tests/usecases/teams/test_create_team.py)<br>[`tests/usecases/teams/test_get_team.py`](../../tests/usecases/teams/test_get_team.py)<br>[`tests/usecases/teams/test_update_team.py`](../../tests/usecases/teams/test_update_team.py) |
| **5. Presentation** | （必要な入口のみ選択）<br>Bot: [`src/app/presentation/bot/cogs/teams_cog.py`](../../src/app/presentation/bot/cogs/teams_cog.py)<br>API: [`src/app/presentation/api/routers/teams.py`](../../src/app/presentation/api/routers/teams.py) | Bot: `src/app/presentation/bot/__main__.py`<br>API: `src/app/presentation/api/__main__.py` | 各プレゼンテーション層テスト<br>（例: `tests/presentation/api/` など） |
| **6. Migration** | `alembic/versions/<rev>_<desc>.py`<br>（※DBスキーマ変更がある場合のみ） | `alembic/` | 使い捨てDBでの検証コマンド群<br>`uv run alembic check` |
| **7. 品質検証** | リポジトリ全体 | CI / Pre-commit | `TEST_DATABASE_URL=sqlite+aiosqlite:// uv run --frozen --all-extras pytest`<br>`uv run --frozen ruff format --check .`<br>`uv run --frozen ruff check .`<br>`uv run --frozen --all-extras pyright` |

## 検証方法の選び方（必要な場合のみ）

業務上の約束が状態遷移、入力の組合せ、操作順、複数行にまたがる整合性に関わる
場合、例ベースのテストだけでは抜けやすい箇所があります。次を目安に手法を選びます。

- 代表例で決まる入力・エラー分岐: 例ベースのテスト（従来どおり）
- 入力の組合せ・境界値・操作順・履歴の保持: Property-based test
- 実装から独立した不変条件、境界内の全状態: 実行可能な仕様モデルと全探索
- 検査が壊れた実装を検出するか: 手動の変異確認（常用しない）

具体例（TeamMembershipの加入・承認・退会・再加入）と実行コマンド、保証範囲、
業務ルール変更の演習は [業務ルールを検証へつなぐ手順](./VERIFICATION.md) を参照して
ください。すべての機能に形式モデルやPBTを要求するものではありません。

## 各要素が存在する理由と設計原則

- **ORMの標準保存（`merge(load=True)` / `flush`）とドメイン変換の分離**:
  Repositoryが手作業でSQL UPDATE/DELETEを発行すると、関連オブジェクトの追加・更新・削除順序や依存関係の制御が複雑化しやすくなります。SQL生成と発行順の制御はSQLAlchemy ORMに任せ、アプリケーション側はドメイン集約とORMの明示的変換に専念します。
- **楽観ロック（Version検査）**:
  複数プロセスやリクエストによる同時更新の競合を検知するために使用します。親属性を変更せず子要素のみを変更した場合でも親集約のVersionを進めることで、集約全体の変更を保護します。
- **Unit of Work（UoW）とSession共有**:
  複数リポジトリ操作を同一トランザクションとして原子性（All or Nothing）をもって確定または破棄するために使用します。専用Repositoryにも同一Sessionを渡すことで、意図しないトランザクション分離を防ぎます。
- **明示的な登録リスト（モデル・マッパー・Handler）**:
  モジュールの動的探索による暗黙の結合や起動順序依存の不具合を回避し、静的解析ツールや登録完全性テストによって起動前・CI時に設定漏れを検知しやすくします。

## 機能追加のステップ

### Step 1: Domain層の実装（境界・不変条件・テスト）

ビジネスルールと状態遷移をDomain層に実装します。Domain層はORMや外部I/Oに依存せず、業務ルールと状態遷移を表現します。

1. **集約境界と不変条件の決定**:
   整合性を保つべき境界を決定します（例: `Team` はチーム名・Version・監査日時を持ち、`TeamMembership` は一回の加入期間を表す集約）。
2. **Value ObjectとEntity/Aggregateの実装**:
   - 不変な値はValue Objectとして実装（例: [`TeamId`](../../src/app/domain/value_objects/team_id.py), [`TeamName`](../../src/app/domain/value_objects/team_name.py)）。
   - 集約は生成ファクトリ（`form()` 等）と業務メソッド（`change_name()` 等）を提供し、Pythonの命名慣習（`_field`）により内部フィールドの直接参照ではなく業務メソッドを操作経路として明示します（例: [`Team`](../../src/app/domain/aggregates/team.py)）。
   - 同一性比較: 集約の `__eq__` は同一具象型かつ同一IDで判定します（属性やVersionが異なっても同一Entity）。可変集約はハッシュ化不可（`__hash__ = None`）とします。
3. **公開集約パッケージへの登録**:
   作成した集約を [`src/app/domain/aggregates/__init__.py`](../../src/app/domain/aggregates/__init__.py) でインポートし、`__all__` に追加します。[`tests/infrastructure/test_registration_completeness.py`](../../tests/infrastructure/test_registration_completeness.py) により、定義された集約が公開されているかが検証されます。
4. **ドメインテストの作成と契約テストの登録**:
   - 共通契約ケース定義の追加（[`tests/domain/aggregate_cases.py`](../../tests/domain/aggregate_cases.py)）:
     - `Aggregate` 型alias（`type Aggregate = ... | NewAggregate`）に対象集約を追加します。
     - `AGGREGATE_CASES` 辞書に新規集約のテストケース（復元値、変更値、ID引数名、保持フィールド、可変性、無効値）を追加します（具体例: [Teamの定義例](../../tests/domain/aggregate_cases.py#L65-L78)）。
   - 状態遷移の検証と登録:
     - 状態遷移を持つ集約（例: `TeamMembership`）: 状態遷移テスト（許可遷移および禁止遷移で状態維持）を作成し、[`tests/domain/test_aggregate_contracts.py`](../../tests/domain/test_aggregate_contracts.py) の `test_every_public_aggregate_has_contract_cases` の遷移テスト対象集合（`set(TRANSITION_EXEMPTIONS) | {TeamMembership, ...} == public_types`）に対象集約を追加します。
     - 状態遷移を持たない集約（例: `User`, `Team`, `ChatMessage`）: `aggregate_cases.py` の `TRANSITION_EXEMPTIONS` にその理由を明記して追加します。
   - Version適用外の集約（例: 追記専用の `ChatMessage`）:
     - `aggregate_cases.py` の `VERSION_EXEMPTIONS` に理由を明記して追加し、[`tests/infrastructure/test_registration_completeness.py`](../../tests/infrastructure/test_registration_completeness.py) 側の期待する例外定義（`VERSION_EXEMPTIONS`）とも整合させます。
   - 非永続集約の扱い（DBに永続化しない集約を追加する場合）:
     - [`tests/infrastructure/test_registration_completeness.py`](../../tests/infrastructure/test_registration_completeness.py) の `NON_PERSISTED_AGGREGATES` に理由を明記して追加します。
     - ※注意: `AGGREGATE_CASES` は ORM へのマッピング往復（`ORMMappingRegistry`）を前提とした共通テスト（[`tests/infrastructure/test_domain_mappings.py`](../../tests/infrastructure/test_domain_mappings.py) など）でも参照されます。非永続集約を実際に追加する場合は、これらの共通テスト側の適用範囲（対象の除外やスキップ処理）も実装に沿って調整が必要です（現行の実装では非永続集約を自動的にスキップしません）。
   - 契約テストの実行:
     - [`tests/domain/test_aggregate_contracts.py`](../../tests/domain/test_aggregate_contracts.py) を実行し、同一性・可変性・hash契約が満たされていることを確認します（※共通契約テストは基本契約を検証するものであり、業務不変条件や状態遷移の網羅性を自動保証するものではありません）。

### Step 2: ORMモデル・マッピング・永続化の設計

集約の永続化が必要な場合、ORMモデルと双方向の明示的変換関数を作成します。

1. **ORMモデルの作成**:
   - [`src/app/infrastructure/orm_models/team_orm.py`](../../src/app/infrastructure/orm_models/team_orm.py) を作成します。
   - テーブル定義には `SQLModel(table=True)` を使用します。
   - 楽観ロック対応: Versionを持つテーブルには `version: int = Field(default=0)` を定義し、SQLAlchemy Mapperの `version_id_col` を設定します。Repository側で明示的にインクリメントを制御するため、`version_id_generator=False` とします。
2. **モデル読み込み正本への登録**:
   - 作成したORMモデルを [`src/app/infrastructure/orm_models/__init__.py`](../../src/app/infrastructure/orm_models/__init__.py) でインポートし、`__all__` に追加します。
   - Alembic（[`alembic/env.py`](../../alembic/env.py)）はこのパッケージから全モデルを一括読み込みするため、`env.py` を個別に編集する必要はありません。
3. **明示的マッパーの作成**:
   - [`src/app/infrastructure/mappings/team.py`](../../src/app/infrastructure/mappings/team.py) に `team_to_orm(team)` と `team_from_orm(orm)` を実装します。
   - 復元時は `Team.restore(...)` などの専用復元メソッドを呼び出し、保存されているID、Version、監査日時をそのまま復元します。
4. **マッピングレジストリへの登録**:
   - [`src/app/infrastructure/orm_registry.py`](../../src/app/infrastructure/orm_registry.py) の `init_orm_mappings()` 内で `register_orm_mapping(Team, TeamORM, to_orm=team_to_orm, from_orm=team_from_orm)` を呼び出します。
   - 同一内容の再登録は冪等（idempotent）ですが、異なるマッピングによる競合登録はエラーとして拒否されます。
5. **親子集約・リレーション設計のルール**:
   - 完全スナップショット: 保存時（`update`）は集約全体のスナップショットを受け取ります。コレクションが空の場合は全削除として扱われます。
   - 子IDの維持: 変換処理（`to_orm`）では既存の子要素のIDを保持します。
   - Cascade設定: 集約が所有する子要素に対してのみ `save-update, merge, delete, delete-orphan` を設定します。別集約への参照に削除cascadeを設定してはなりません。
   - 暗黙I/Oの防止: `AsyncSession` では lazy load が `MissingGreenlet` エラーを引き起こします。`from_orm` を呼ぶ前に、ORM設定（`lazy="selectin"`）や明示的なクエリ（`selectinload`）で必要な関連オブジェクトを先行読み込みしてください。
   - 親子保存の検証実例: [`tests/infrastructure/test_aggregate_persistence.py`](../../tests/infrastructure/test_aggregate_persistence.py) および [`tests/helpers/aggregate_persistence.py`](../../tests/helpers/aggregate_persistence.py) を参照してください。
6. **マッピングテストの確認**:
   - [`tests/infrastructure/test_domain_mappings.py`](../../tests/infrastructure/test_domain_mappings.py) にテストを追加し、相互変換で属性・Version・日時が欠落・改変されないこと、不正データが適切に拒否されることを検証します。

### Step 3: リポジトリとUnit of Workの利用・登録

永続化処理は `GenericRepository` または必要に応じて作成する専用Repositoryを `IUnitOfWork` 経由で取得して行います。

1. **GenericRepositoryの基本仕様**:
   - 通常のCRUD操作には `uow.GetRepository(Team, TeamId)` を使用します。
   - 保存方式: `merge(load=True)` および `flush()` を使用し、SQLの発行順序制御はSQLAlchemy ORMに委ねます。
   - Version管理: 初期Versionは0、`update()` のたびに+1されます（子要素のみの変更や同値更新を含む）。
   - 事前Version照合: `session.get()` または `merge(load=True)` 前に入力集約のVersionとDB上のVersionを照合します。古いVersionの場合は `flush()` を待たずに即座に `VERSION_CONFLICT` を返します。通常の事前Version不一致はDBトランザクションを壊さず、無関係な先行書き込みを取り消しません。
   - `merge()` には入力元のVersionを渡し、merge後の管理対象オブジェクト側でVersionを進めます（`managed.version = entity.version.to_primitive() + 1`）。
2. **専用Repositoryの作成と登録（必要な場合のみ）**:
   - 単純なID取得や追加・更新・削除だけなら専用Repositoryを作らず、複雑な集約クエリや親子ロード制御が必要な場合のみ作成します。
   - Port定義: `src/app/domain/repositories/` 配下に抽象インターフェースを定義。
   - 実装クラス: `src/app/infrastructure/repositories/` 配下に実装。専用Repository内で独自に `AsyncSession` を生成したり、成功を確定する `commit()` を呼び出してはなりません（commitはUoWが行います）。flush失敗時にはRepositoryが同じSessionをrollbackしてErrを返し、そのrollbackでUoWは失敗を記録し以後のcommitを拒否します。必ずUoWから渡される現在の `AsyncSession` を使用してください。
   - UoWファクトリ登録: [`src/app/container.py`](../../src/app/container.py) で `SQLAlchemyUnitOfWork` を構築する際、`repository_factories` マッピング（`Mapping[type, Callable[[AsyncSession], object]]`）に登録します。
   - 取得: UseCaseからは `uow.GetCustomRepository(ITeamCustomRepository)` で型安全に取得します。未登録のPortを要求した場合は即座に例外が発生します。
3. **トランザクションとエラー処理の仕様**:
   - 正常な `uow.rollback()` の呼び出しは未確定の書き込みを破棄し、同一UoWスコープのまま処理を継続できます（その後に新たな操作を行ってコミット可能です）。
   - 既にスコープが失敗状態の場合は、`rollback()` を呼んでも失敗状態は解除されません。
   - `flush()` 時に `StaleDataError` や `IntegrityError` などの例外が発生した場合、またはRepository内部で直接 `rollback()` が呼ばれた場合は、Sessionの `after_soft_rollback` イベントによってUoWに失敗状態（`_is_failed = True`）が記録されます。
   - 失敗状態となったスコープでは以降の `commit()` が拒否（`Err(RepositoryError)`）されます。再試行は新しいUoWスコープを作成して行う必要があります。
   - 検証テスト: [`tests/infrastructure/test_aggregate_persistence.py`](../../tests/infrastructure/test_aggregate_persistence.py)（親子保存、実race競合、専用Repositoryのライフサイクル、ロールバック挙動を網羅）。

### Step 4: UseCaseとHandlerの実装・登録

ビジネスユースケースを実装し、Mediatorにハンドラーを登録します。

1. **ユースケースモジュールの実装**:
   - [`src/app/usecases/teams/`](../../src/app/usecases/teams/) 配下にリクエスト（Command/Query）、結果DTO、Handlerを実装します。
   - 入力値をドメインのValue Objectへ変換し、UoWを開いてドメイン操作と保存を調停します。
   - 戻り値は `Result[ResultDTO, UseCaseResultError]` とし、ドメインエラーや検証エラーは `UseCaseError` に変換し、`RepositoryError` はそのまま返します。
2. **Handlerの登録**:
   - [`src/app/application/mediator.py`](../../src/app/application/mediator.py) の `_HANDLER_TYPES` タプルに作成したHandlerクラスを追加します。
3. **UseCaseテストの実装**:
   - [`tests/usecases/teams/test_create_team.py`](../../tests/usecases/teams/test_create_team.py) 等を作成し、正常系および各種エラーケース（NOT_FOUND, VERSION_CONFLICT, ALREADY_EXISTS など）のフローを検証します。

### Step 5: 入口（Presentation層）への接続

ユースケースを呼び出すインターフェース（Discord Bot Cog、FastAPI Router など）を接続します。

- 必要な入口のみ接続します。機能の目的に応じて必要なインターフェースのみを選択します。
- Presentation層は直接リポジトリやUoWを触らず、DIコンテナから注入された `ApplicationMediator` 経由でユースケースを呼び出します（`mediator.send_async(command)`）。
- 実例: Discord Bot Cog（[`src/app/presentation/bot/cogs/teams_cog.py`](../../src/app/presentation/bot/cogs/teams_cog.py)）、FastAPI Router（[`src/app/presentation/api/routers/teams.py`](../../src/app/presentation/api/routers/teams.py)）。

### Step 6: データベースマイグレーションの検証（スキーマ変更時）

既存のテーブル構造で対応できる場合はマイグレーションを作成しません。新規テーブルや新規列が必要な場合のみ、使い捨てDB環境で検証します。テスト実行時（`conftest.py`）の `create_all()` だけではマイグレーションスクリプト自体の正しさは検証できないため、マイグレーションガイドの手順を実施します。

手順の概要:
1. 一時DBを作成して現在のheadまで適用し、`alembic revision --autogenerate` でリビジョンファイルを生成します。
2. 生成された `alembic/versions/*.py` を開発者が目視で確認し、意図しない変更や未検出の制約を手動で修正します（ファイル内の `down_revision` の値を確認）。
3. 別の一時DBで直前の `down_revision` まで適用した上で代表的旧データを投入し、新headへのアップグレード、ダウングレード（`-1`）、再アップグレードでのデータ保持と `alembic check` を検証します。
4. 別の空DBから直接新headまで適用し、クリーンインストールの整合性を確認します。

詳細な実行手順および注意点は **[データベースマイグレーションガイド（マイグレーション作成と検証の基本フロー）](../infrastructure/DATABASE_MIGRATIONS.md#マイグレーション作成と検証の基本フロー)** を参照してください。

### Step 7: 登録完全性と全体品質の検証

新しく追加した機能が正しく登録され、既存機能を壊していないかを検証します。

1. **登録完全性テストの実行**:
   [`tests/infrastructure/test_registration_completeness.py`](../../tests/infrastructure/test_registration_completeness.py) を実行し、モデル読み込み、集約の永続化マッピング、Version設定、Handler登録の漏れがないか確認します。
2. **データベーステストの実行**:
   テスト環境の接続先制御（SQLite一時ファイル／PostgreSQL独立スキーマ）の詳細は [`tests/README.md`](../../tests/README.md) を参照してください。
   ```bash
   TEST_DATABASE_URL=sqlite+aiosqlite:// uv run --frozen --all-extras pytest
   ```
3. **静的解析・フォーマットチェックの実行**:
   ```bash
   uv run --frozen ruff format --check .
   uv run --frozen ruff check .
   uv run --frozen --all-extras pyright
   ```

## 公式ドキュメント・参考資料

- SQLAlchemy 2.0:
  - [Version Counter による楽観ロック](https://docs.sqlalchemy.org/en/20/orm/versioning.html)
  - [Session State Management (merging)](https://docs.sqlalchemy.org/en/20/orm/session_state_management.html#merging)
  - [Cascades の設定と挙動](https://docs.sqlalchemy.org/en/20/orm/cascades.html)
  - [AsyncSession での暗黙I/O防止](https://docs.sqlalchemy.org/en/20/orm/extensions/asyncio.html#preventing-implicit-io-when-using-asyncsession)
  - [Session Flushing とエラー処理](https://docs.sqlalchemy.org/en/20/orm/session_basics.html#flushing)
  - [ORM-enabled UPDATE/DELETE の注意点](https://docs.sqlalchemy.org/en/20/orm/queryguide/dml.html#important-notes-and-caveats-for-orm-enabled-update-and-delete)
- SQLModel: [SQLModel 公式ドキュメント](https://sqlmodel.tiangolo.com/)
- Alembic: [Alembic 公式ドキュメント](https://alembic.sqlalchemy.org/) / [自動生成の対象と限界](https://alembic.sqlalchemy.org/en/latest/autogenerate.html) / [check コマンド](https://alembic.sqlalchemy.org/en/latest/api/commands.html#alembic.command.check)
