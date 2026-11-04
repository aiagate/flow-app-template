# アーキテクチャ設計ドキュメント

このドキュメントは、Flow App Template（Discord Bot / HTTP API / LINE / Worker を
エントリポイントとする Python アプリケーションテンプレート）のアーキテクチャ設計と
実装パターンを詳細に説明します。

---

## アーキテクチャ概要

このプロジェクトは **クリーンアーキテクチャ（Clean Architecture）** に基づいて設計されています。

現行コードで確定しているドメイン境界と用語は
[現行のドメイン境界と用語集](./domain/BOUNDARIES_AND_GLOSSARY.md) にまとめています。
このテンプレートでは、未確定のコアドメインやサービス分割を仮定しません。

### レイヤー構造

```
┌─────────────────────────────────────────────┐
│  Presentation Layer                         │  外部インターフェース
│  (Bot, API, LINE, Worker)                   │  - ユーザーからの入力受付
│  - src/app/presentation/bot/__main__.py      │  - 出力のフォーマット
│  - src/app/presentation/bot/cogs/*.py       │
├─────────────────────────────────────────────┤
│  Application Layer                          │  ユースケース
│  (Use Cases, Mediator)                      │  - ビジネスフロー制御
│  - src/app/application/                     │  - Mediatorの構成
│  - src/app/usecases/                        │  - DTOでの入出力
│  - flow-med (External Library)              │  - Result型でのエラーハンドリング
├─────────────────────────────────────────────┤
│  Domain Layer                               │  ビジネスルール
│  (Aggregates, Entities, Value Objects)      │  - 純粋なPythonオブジェクト
│  - src/app/domain/aggregates/               │  - フレームワーク非依存
│  - src/app/domain/repositories/             │  - 汎用Repository契約
├─────────────────────────────────────────────┤
│  Contracts / Ports                          │  アプリケーション境界
│  - src/app/contracts/ports/                 │  - UoW・読み取りQuery・イベント契約
├─────────────────────────────────────────────┤
│  Infrastructure Layer                       │  技術的詳細
│  (Database, ORM, External Services)         │  - データベースアクセス
│  - src/app/infrastructure/database.py       │  - 外部API呼び出し
│  - src/app/infrastructure/orm_models/       │  - ファイルシステムアクセス
│  - src/app/infrastructure/repositories/     │
│  - src/app/infrastructure/unit_of_work.py   │
│  - src/app/container.py (DI)                │
└─────────────────────────────────────────────┘
```

### 依存関係の方向

```
Presentation ──▶ Application ──▶ Domain ◀── Infrastructure
                                    ▲
                                    │
                            依存性の逆転原理
                          (Dependency Inversion)
```

**重要な原則**:

- 上位層は下位層に依存可能
- **下位層は上位層に依存してはならない**
- **ドメイン層は最も独立しており、他のどの層にも依存しない**
- `IUnitOfWork` と `IChatHistoryQuery` は `contracts/ports` に置き、Domainから参照しない
- チャット履歴QueryはUnit of Workに含めず、呼び出しごとに読み取りセッションを閉じる
- インフラ層はドメイン層のインターフェースに依存（依存性逆転）

---

## 各レイヤーの詳細

### 1. Domain Layer（ドメイン層）

**責務**: ビジネスルールとビジネスロジックの実装

**特徴**:

- 純粋なPythonコード（dataclass、関数）
- フレームワーク非依存
- データベース、Web、UIに関する知識を持たない
- 他のどのレイヤーにも依存しない

#### 構成要素

##### 1.1 Aggregates（集約）

[User](../src/app/domain/aggregates/user.py)、[TeamMembership](../src/app/domain/aggregates/team_membership.py)
などの集約は、生成メソッドとドメイン操作を通じて状態を管理します。
Value Objectが値を検証し、集約が状態遷移を検証します。

集約の `==` は具象型とIDによる同一性比較です。カプセル化と不変性の違い、
状態の比較方法、集約境界の決め方は[Domain層実装ガイド](./domain/DOMAIN_IMPLEMENTATION_GUIDE.md)
を参照してください。

##### 1.2 Repository Interfaces（リポジトリインターフェース）

[Repository契約](../src/app/domain/repositories/interfaces.py)はDomainに置き、
実装はInfrastructureが担います。`IRepository` は追加・更新・削除を、
`IRepositoryWithId` はさらにID検索を定義します。結果は `Result` で返します。
Versionを持つ集約は、更新・削除時に古い版からの操作を拒否します。

`IUnitOfWork` と `IChatHistoryQuery` は `contracts/ports` に置くApplicationポートで、
Domainからは参照しません。前者はトランザクションとRepositoryの寿命を、
後者は読み取り専用の履歴取得を表します。

##### 1.3 Result Type（結果型）

`Result`、`Ok`、`Err`、`AwaitableResult` は `flow_res` が提供します。
アプリケーション固有のエラー型は
[src/app/usecases/result.py](../src/app/usecases/result.py) に定義しています。

各Handlerの戻り値は `Result[出力DTO, UseCaseResultError]` です。
`UseCaseResultError` は `RepositoryError | UseCaseError` の型エイリアスであり、
Handlerはリポジトリのエラーをそのまま返し、入力検証やドメイン上のエラーだけを
`UseCaseError` に変換します。

`Result` のチェーンは、Handlerを呼び出すプレゼンテーション側で利用できます。
実際の呼び出し方は
[src/app/presentation/bot/cogs/teams_cog.py](../src/app/presentation/bot/cogs/teams_cog.py)
を参照してください。

---

### 2. Application Layer（アプリケーション層）

**責務**: ユースケースの実装、ビジネスフローの制御

**特徴**:

- ドメインオブジェクトを操作してビジネスフローを実現
- DTOで入出力を定義
- トランザクション境界の管理（Unit of Work）

#### 構成要素

##### 2.1 Use Cases（ユースケース）

各ユースケースのリクエスト、結果DTO、Handlerは、ユースケースごとのモジュールに
まとめます。たとえば、[GetUserの実装](../src/app/usecases/users/get_user.py) と
[CreateUserの実装](../src/app/usecases/users/create_user.py) を参照してください。

Handlerの契約は次のとおりです。

- `Request` は文字列などの外部入力を受け取ります。
- Handlerは `Result[ResultDTO, UseCaseResultError]` を返します。
- 入力検証やドメイン上の失敗は `UseCaseError` に変換します。
- RepositoryやUnit of Workの失敗は、`RepositoryError` のまま返します。
- Create/Updateの結果は識別子を返し、必要な詳細情報はQueryで取得します。

この契約により、ドメイン固有の処理はUse Case層に閉じ、プレゼンテーション層は
結果の表示だけを担当できます。

##### 2.2 Mediator Pattern（メディエーターパターン）

内部のディスパッチには `flow-med` を使用しますが、外部インターフェースが直接
利用するのは
[ApplicationMediator](../src/app/application/mediator.py) です。
`ApplicationMediator.send_async` はインスタンスメソッドで、Handlerの結果を返す前に
`classify_error` を一度だけ適用します。

Handlerの登録は
[`_HANDLER_TYPES`](../src/app/application/mediator.py) に明示し、
`create_application_mediator` が `HandlerRegistry` へ登録してからMediatorを構築します。
新しいHandlerを追加した場合は、この一覧に追加してください。

```python
mediator: ApplicationMediator
result = await mediator.send_async(GetUserQuery(user_id="01H...Z"))
```

Presentation層はBot、API、Workerのいずれも同じ `ApplicationMediator` をDIで受け取り、
そのインスタンスメソッドだけを呼び出します。

##### 2.3 DTOs（Data Transfer Objects）

Result DTOは共有DTOモジュールに集約せず、各ユースケースのモジュールで定義します。
たとえば `GetUserResult` と `CreateUserResult` は
[usersのユースケース](../src/app/usecases/users/) にあります。
DTOはドメイン集約をそのまま公開せず、プレゼンテーションに必要なプリミティブ値を
返します。

---

### 3. Infrastructure Layer（インフラストラクチャ層）

**責務**: 技術的な詳細の実装（DB、外部API等）

**特徴**:

- ドメイン層のインターフェースを実装
- ORM、データベース接続、外部サービスとの通信
- ドメイン集約とORMモデルの変換

#### 構成要素

##### 3.1 ORM Models

`src/app/infrastructure/orm_models/user_orm.py`:

```python
from datetime import datetime
from sqlalchemy import Column, DateTime, func
from sqlmodel import Field, SQLModel

class UserORM(SQLModel, table=True):
    """User table ORM model."""
    __tablename__ = "users"

    id: str | None = Field(default=None, primary_key=True, max_length=26)
    name: str = Field(max_length=255, index=True)
    email: str = Field(max_length=255, unique=True, index=True)
    created_at: datetime = Field(
        sa_column=Column(DateTime(timezone=True), server_default=func.now())
    )
    updated_at: datetime = Field(
        sa_column=Column(DateTime(timezone=True), server_default=func.now())
    )
```

**ポイント**:

- **ドメイン集約とは完全に分離**
- データベーステーブルの表現
- IDはULIDのため `str` 型、タイムスタンプは `datetime` 型

##### 3.2 Generic Repository

`src/app/infrastructure/repositories/generic_repository.py`:

```python
class GenericRepository[T, K](IRepositoryWithId[T, K]):
    """汎用リポジトリ実装"""

    def __init__(
        self,
        session: AsyncSession,
        entity_type: type[T],
    ) -> None:
        self._session = session
        self._entity_type = entity_type
        self._orm_type = ORMMappingRegistry.get_orm_type(entity_type)

    async def get_by_id(self, id: K) -> Result[T, RepositoryError]:
        # Value Object をプリミティブ型に変換して検索
        primitive_id = id.to_primitive() if isinstance(id, IValueObject) else id

        statement = select(self._orm_type).where(self._orm_type.id == primitive_id)
        result = await self._session.execute(statement)
        orm_instance = result.scalar_one_or_none()

        if orm_instance is None:
            return Err(RepositoryError(type=RepositoryErrorType.NOT_FOUND, ...))

        # 登録済みマッパーで ORM → Domain を変換
        return Ok(ORMMappingRegistry.from_orm(orm_instance))
```

**ポイント**:

- 型安全な汎用実装（Generics使用）
- ORM ↔ Domain の変換を `ORMMappingRegistry` に委譲
- ORM標準の保存処理（`merge(load=True)` / `flush`）を使用し、SQL発行順序はSQLAlchemyに委譲
- 楽観ロック（Version管理）: 初期値0、更新ごとに+1（子要素のみ変更や同値更新を含む）。事前読み込みで古いVersionを即時検知し、競合時は `VERSION_CONFLICT` を返却
- 集約全体のスナップショットを受け取り、所有する子要素を整合性をもって同期（空コレクションは全削除）
- Result型でエラーハンドリング

##### 3.3 ORM Mapping Registry

ドメイン集約とORMモデル間の変換は、Infrastructureの明示的なマッパーによって行い、
`ORMMappingRegistry` は型ごとのマッパーを登録・検索します。

`src/app/infrastructure/orm_mapping.py`:

```python
register_orm_mapping(
    DomainClass, ORMClass, to_orm=domain_to_orm, from_orm=domain_from_orm
)
# from_orm(orm_instance) で登録済みの明示マッパーを使ってORMからドメインへ変換
# to_orm(domain_instance) でドメインからORMへ変換
```

User、Team、TeamMembership、ChatMessageは、それぞれのドメイン語彙と復元APIを
明示的に指定します。これにより、property名やprivate fieldの追加が暗黙にDB列へ
影響することを防ぎ、versionと監査日時も復元時に保持します。
登録には双方向の変換関数が必須です。未登録の型の変換はエラーとなり、
フィールド名や型注釈からの自動変換は行いません。

**利点**:

- **型安全**: 集約ごとの復元APIとValue Object変換を明示
- **保守性向上**: 永続化列の変更を対応するマッパーに閉じ込める
- **依存性逆転**: ドメイン層がインフラ層に依存しない
- **DRY原則**: 変換ロジックの重複を排除
- **一元管理**: 全てのマッピングを `orm_registry.py` で集中管理

##### 3.4 Unit of Work Pattern

`src/app/infrastructure/unit_of_work.py`:

```python
class SQLAlchemyUnitOfWork(IUnitOfWork):
    """トランザクション境界とリポジトリ寿命を管理"""

    def __init__(
        self,
        session_factory: async_sessionmaker[AsyncSession],
        repository_factories: RepositoryFactories | None = None,
    ) -> None:
        self._session_factory = session_factory
        self._repository_factories = dict(repository_factories or {})
        # ...

    def GetRepository[T, K](...) -> IRepository[T, K]:
        # 汎用リポジトリの取得（セッションスコープ内でキャッシュ）
        # ...

    def GetCustomRepository[R](self, port_type: type[R]) -> R:
        # 登録済み専用リポジトリの取得（同一セッションを渡して生成・キャッシュ）
        # ...

    async def __aenter__(self) -> "SQLAlchemyUnitOfWork":
        # ...

    async def __aexit__(self, exc_type, exc_val, exc_tb) -> None:
        # スコープ終了時にロールバックし、セッションを閉じる
        # ...
```

**ポイント**:

- **トランザクション境界の明確化**: リクエストやユースケース単位で同一Session・トランザクションを共有
- **汎用と専用リポジトリの統合管理**: `GetRepository` に加え、`GetCustomRepository` で型安全に専用Repositoryを取得。専用RepositoryはUoWの現在Sessionを共有し、独自にSession作成やcommitを行わない
- **ロールバックと失敗状態の追跡**: 正常な `uow.rollback()` は未確定の書き込みを破棄して継続可能（その後新たな操作を行いコミット可能）ですが、既に失敗済みの場合は失敗状態を保持します。flush失敗（`StaleDataError`, `IntegrityError` 等）やRepository内部からの直接ロールバックはSessionの `after_soft_rollback` イベントでUoWに失敗状態を記録し、同一スコープ内での後続commit呼び出しを拒否します（新UoWスコープで再試行）。通常の事前Version不一致はトランザクションを壊しません。
- **リポジトリのキャッシュ**: 同一スコープ内で同一型のリポジトリインスタンスを再利用
- **明示的なコミット**: 成功時のコミットは各ユースケースが明示する（コンテキストマネージャー終了時は未コミットの変更を破棄）

チャット履歴の読み取りは `IChatHistoryQuery` をDIで取得し、SQLAlchemy実装が
session factoryから呼び出し単位のセッションを作成・終了します。Unit of Workの
repositoryキャッシュやトランザクション境界とは独立しています。

##### 3.5 Dependency Injection Container

`src/app/container.py`:

```python
from injector import Binder, Module, singleton
from app.infrastructure.orm_registry import init_orm_mappings

class AppModule(Module):
    """DIコンテナの設定"""

    def configure(self, binder: Binder) -> None:
        # アプリケーション起動時に一度だけORMマッピングを初期化
        init_orm_mappings()

        # セッションファクトリをシングルトンでバインド
        binder.bind(async_sessionmaker[AsyncSession], to=get_session_factory(), ...)

        # UnitOfWork をリクエストごとに生成
        binder.bind(IUnitOfWork, to=SQLAlchemyUnitOfWork)
```

**ポイント**:

- `injector` ライブラリを使用
- `init_orm_mappings()` をコンテナ設定時に呼び出し、マッピングを保証
- テスト時のモック注入が容易

---

### 4. Presentation Layer（プレゼンテーション層）

**責務**: ユーザーインターフェース、入出力の制御

**特徴**:

- Discord Bot のコマンド実装
- 入力の受付とバリデーション
- 出力のフォーマット

#### 構成要素

##### 4.1 Discord Bot Entry Point

[`src/app/presentation/bot/__main__.py`](../src/app/presentation/bot/__main__.py):

```python
class MyBot(commands.Bot):
    async def setup_hook(self) -> None:
        await self._init_database()
        await self.load_cogs()

    async def _init_database(self) -> None:
        injector = Injector([container.configure])
        self.mediator = injector.get(ApplicationMediator)

    async def load_cogs(self) -> None:
        await self.add_cog(TeamsCog(self, self.mediator))
        await self.add_cog(UsersCog(self, self.mediator))
```

DIコンテナは `ApplicationMediator` を生成し、Handler一覧の登録とエラー分類を
アプリケーション起動時に構成します。各Cogには同じMediatorインスタンスを渡します。

##### 4.2 Discord Cogs

[`src/app/presentation/bot/cogs/users_cog.py`](../src/app/presentation/bot/cogs/users_cog.py):

```python
query = GetUserQuery(user_id=user_id)
message = await (
    self.mediator.send_async(query)
    .map(lambda value: f"User Information:\nID: {value.id}")
    .unwrap()
)
await ctx.send(content=message)
```

**ポイント**:

- `ApplicationMediator` 経由でユースケースを呼び出し
- `Result` 型で成功と失敗を扱う
- エラー表示には `UseCaseError.display_message` を使う
- Discord用のメッセージフォーマット
- IDは文字列として受け取る

---

## データフロー

### Query（読み取り）のフロー

```
1. User: !users get 01H...
   ↓
2. UsersCog: GetUserQuery(user_id="01H...")
   ↓
3. ApplicationMediator -> GetUserHandler
   ↓ UserId.from_primitive("01H...")
4. UoW -> GenericRepository.get_by_id(UserId(...))
   ↓ SELECT ... WHERE id = "01H..."
5. Database -> UserORM
   ↓ ORMMappingRegistry.from_orm()
6. User (Domain) -> GetUserResult
   ↓ Ok(GetUserResult)
7. UsersCog: formats message
   ↓
8. User: receives message
```

### Command（書き込み）のフロー

```
1. User: !teams create "My Team"
   ↓
2. TeamsCog: CreateTeamCommand(name="My Team")
   ↓
3. ApplicationMediator -> CreateTeamHandler -> Team(id=TeamId.generate(), ...)
   ↓ UoW -> GenericRepository.add()
4. ORMMappingRegistry.to_orm() -> TeamORM
   ↓ INSERT ...
5. Database commits
   ↓ Ok(CreateTeamResult(id="01H..."))
6. TeamsCog: .and_then() is called
   ↓ GetTeamQuery(id="01H...")
7. (Queryフローと同様の処理)
   ↓ Ok(GetTeamResult)
8. TeamsCog: .map() formats message
   ↓
9. User: receives success message
```

Create操作は作成したエンティティのIDを返します。詳細情報が必要な場合は、
Presentation層が同じ `ApplicationMediator` を通じてGet操作を続けて呼び出します。
CreateとGetの結果をつなぐ実例は
[`TeamsCog.teams_create`](../src/app/presentation/bot/cogs/teams_cog.py) にあります。

---

## テスト戦略

- [集約の単体テスト](../tests/domain/aggregates)では、状態遷移・同一性・不変性を検証します。
- [集約の共通契約テスト](../tests/domain/test_aggregate_contracts.py)（定義: [aggregate_cases.py](../tests/domain/aggregate_cases.py)）では、同一性・可変性・hash契約を共通で検証します。
- [ユースケースのテスト](../tests/usecases)では、ドメイン操作と保存の流れを検証します。
- [マッピングテスト](../tests/infrastructure/test_domain_mappings.py)では、IDだけでなく復元後の各属性を確認します。
- [Repositoryテスト](../tests/infrastructure/test_repositories.py)では、古いVersionからの削除拒否と最新状態の保持を確認します。
- [Membership制約テスト](../tests/infrastructure/test_team_membership_constraints.py)では、同時加入時の一意性を確認します。
- [集約永続化・UoW統合テスト](../tests/infrastructure/test_aggregate_persistence.py)（モデル: [helpers/aggregate_persistence.py](../tests/helpers/aggregate_persistence.py)）では、親子集約保存、実race競合ロールバック、専用RepositoryとUoWの連携を検証します。
- [登録完全性テスト](../tests/infrastructure/test_registration_completeness.py)では、モデル・マッピング・Handler登録漏れを検証します。

非同期テストは `@pytest.mark.anyio` を使用します。DBを使うテストの環境は
[共通fixture](../tests/conftest.py)を参照してください。

---

## 依存関係管理

依存とバージョンの正本は [`pyproject.toml`](../pyproject.toml) と `uv.lock` です。

- コア: DB、マイグレーション、DI、Mediator、Result型、環境変数ローダー。
- 入口SDK: `api` / `discord` / `line` のoptional extras。Workerは追加SDK不要。
- 開発用: `dev` dependency group。全入口のテスト・型検査には`--all-extras`を指定します。

具体的なインストール・起動・CI経路は[入口ごとの依存管理](./development/OPTIONAL_ENTRYPOINTS.md)を参照してください。

---

## 拡張方法

### 新しい機能・集約の追加

新しい集約、ORMモデル、明示的マッピング、UseCase/Handler、Presentation層の接続、およびテストの一連の追加手順は、正本ガイドである **[機能追加ガイド（ADDING_FEATURE.md）](./development/ADDING_FEATURE.md)** を参照してください。

実在する `Team` 機能を主例に、編集先・登録先・検証コマンドの対応表とともに手順を一本化して案内しています。

### データベースマイグレーション

マイグレーションの作成・検証手順の詳細は、**[データベースマイグレーションガイド](./infrastructure/DATABASE_MIGRATIONS.md)** および **[機能追加ガイド（Step 6）](./development/ADDING_FEATURE.md#step-6-データベースマイグレーションの検証スキーマ変更時)** を参照してください。使い捨てデータベース環境での往復テストと `alembic check` による検証を推奨しています。

---

## 📚 参考資料

- [Clean Architecture (Robert C. Martin)](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html)
- [Domain-Driven Design](https://www.domainlanguage.com/ddd/)
- [CQRS Pattern](https://martinfowler.com/bliki/CQRS.html)
- [Repository Pattern](https://martinfowler.com/eaaCatalog/repository.html)
- [Unit of Work Pattern](https://martinfowler.com/eaaCatalog/unitOfWork.html)
