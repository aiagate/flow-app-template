# Simple Feature: チーム招待コード

すべての業務を集約・Value Object・GenericRepository で実装しない選択肢を示す、
実行可能な最小例です。`src/app` の既存コードには依存しますが、変更は加えません。

## この例が示すこと

- 支援領域と仮定したチーム招待コードの、比較的小さな業務規則
- 1リクエスト内で完結する **トランザクションスクリプト**
- 業務専用の狭い Port と SQLAlchemy Core 実装
- 既存 `IUnitOfWork` の同一 Session を専用 Port に共有する構成
- 入力検証、DB commit、制約違反による rollback の実試験
- 例専用テーブルをアプリ本体の `SQLModel.metadata` / Alembic に混ぜない分離

### 位置づけの目安

| サブドメイン | 事業上の位置付け | 複雑度 | 実装パス |
| :--- | :--- | :--- | :--- |
| コア | 差別化を担う | 要件による | 複雑な規則があればドメインモデル。単純な処理は軽く始める |
| 支援 | 事業に必要 | 要件による | **この例**ではトランザクションスクリプト + 専用 Port |
| 汎用 | 差別化の中心ではない | 要件による | 既存製品の採用を先に比較。複雑・重要な機能もある |

複雑度が低いことと、重要でないことは別です。招待コードは業務上の影響が
大きいため、検証・制約・失敗時の rollback は省略せず、実装の重さだけを
下げています。

## 実行方法

リポジトリルートで実行します。`examples` はインストール対象の `app` パッケージ
には含めていませんが、リポジトリルートからの通常のパッケージ import が可能です。

```bash
uv run --frozen python -m examples.simple_feature
```

期待する出力:

```text
issued code=DEMO2026 remaining=2
redeemed code=DEMO2026 by=demo-user remaining=1
```

テスト:

```bash
uv run --frozen --all-extras pytest tests/examples --no-cov
```

例は通常のCIの型検査対象にも含まれます。例のコードだけを検査する場合:

```bash
uv run --frozen pyright examples tests/examples
uv run --frozen ruff check examples tests/examples
uv run --frozen ruff format --check examples tests/examples
```

## 構成

```text
examples/simple_feature/
  schema.py             例専用 MetaData と2テーブル
  records.py            ポート境界を渡るプリミティブな dataclass
  ports.py              専用 Port（add / consume / record_redemption）
  repository.py         SQLAlchemy Core 実装。Session は UoW から受け取る
  rules.py              入力規則の共有定数
  issue_invite_code.py  発行スクリプト
  redeem_invite_code.py 利用スクリプト
  composition.py        UoW と専用 Port の組み立て、例専用 schema 作成
  __main__.py           実行可能デモ
tests/examples/         実 DB（SQLite 一時ファイル）を使うテスト
```

## 選んだ理由

- 招待コードの規則（形式、有効期限、残回数、同一利用者の重複利用禁止）は、
  入力検証と条件付き更新・DB制約で表せます。発行と利用にまたがる規則もありますが、
  現段階では集約をロードせず、狭いPortと単一トランザクションで保証します。
- そのため `TeamInviteCode` 集約を導入するより、2 つの書き込み（残回数の
  条件付き更新と利用ログの追加）を 1 つの Unit of Work で実行するほうが
  変更範囲と理解コストを小さくできます。
- 残回数の消費は「読んでから書く」のではなく、条件付き `UPDATE` で原子的に
  行います。専用 Port にする価値があるのはこの種のクエリです。
- 専用 Port は `AsyncSession` を受け取るだけにし、自前の Session 生成や
  `commit()` を行いません。`uow.commit()` はスクリプト側の 1 か所だけです。
- 利用ログの `(code, redeemed_by)` 一意制約により、同じ利用者による二重利用を
  DB 制約で拒否します。失敗時は Port が同じ Session を rollback するため、
  先行する使用回数の加算も取り消されます。
  これは既存Repositoryと同じ失敗契約です。DB失敗後はUoWが失敗状態になり、
  同じスコープのcommitを拒否します。エラーを無視して続けず、新しいUoWで再試行します。
  業務上のNOT_FOUND等ではrollbackを即時に呼ばず、未commitの変更をUoW終了時に破棄します。

## この道を選ぶ条件

- 業務が 1 ユースケース内の入力検証 + 少数の SQL で完結する
- 集約をまたぐ不変条件や状態遷移が存在しない、または既存集約の再利用で足りる
- 画面・API・バッチごとに業務ルールが大きく変わらない
- 監査や同時実行制御を DB 制約と単一トランザクションで説明できる

逆に、次のいずれかが現れたら集約 + Repository への昇格を検討します。

- 招待コードの失効・再発行・チーム状態との連動など、複数操作にまたがる規則
- 同じデータを複数のユースケースが異なる規則で更新する
- 変更履歴や楽観ロックを業務として扱う必要がある

## 制限と本番化に必要な作業

- **認可は未実装です。** actorや発行・利用権限を扱っていません。外部入口へ接続する際は、
  利用主体の検証、試行回数制限、コードの強度と公開エラー応答を要件に合わせて設計します。
  このデモをそのまま公開APIとして使う前提ではありません。
- **チームの存在確認はしていません。** この例は `team_id` を不透明な文字列として
  保存します。本番では既存 `Team` リポジトリを同じ UoW で確認するか、FK と
  `TeamId` による検証を追加してください。
- **テーブルはマイグレーション対象外です。** 例専用 `MetaData` をテストとデモで
  `create_all()` しているだけです。本番化する場合は Alembic リビジョンを作成し、
  `app.infrastructure.orm_models` の登録方針に合わせてください。
- **完全な冪等 API ではありません。** 同じ `redeemed_by` の再実行は、コードが
  有効で残数もある場合には利用ログの一意制約で `ALREADY_EXISTS` になります。
  残数ゼロまたは期限切れの場合は、その条件を先に検出し `CONFLICT` を返します。
  再送で同じ成功結果を返す要件がある場合は、リクエストID等による識別と
  保存済み結果の読み直しを設計してください。
- **自動生成コードの衝突は再試行しません。** `code` を省略した場合の衝突は
  `ALREADY_EXISTS` として返るため、必要なら新しいUoWによる回数制限付きの再試行を
  呼び出し側で設計します。生成前の存在確認だけでは並行実行時の衝突を防げません。
- **SQLite では日時が naive で返ります。** 保存時に UTC へ正規化し、読み取り側で
  UTC とみなすヘルパーを使っています。PostgreSQL では `timezone=True` のまま
  扱えます。
- **Mediator へは登録していません。** 例を本体の起動経路に混ぜないためです。
  実装する場合は `_HANDLER_TYPES` への登録と DI 構成が必要です。
  Handlerを直接呼ぶ場合はRepositoryErrorを返す経路があります。Presentationへ接続する際は
  既存のApplicationMediatorによる`classify_error`でCONFLICT等へ変換します。
- **同時実行の分類は保証しません。** 条件付き `UPDATE` 自体は原子的ですが、
  0 件だった後の「期限切れ / 残回数なし」の分類は同じトランザクション内の
  再読み取りに依存します。厳密なエラー分類が競合下で必要なら、状態を返す
  単一 SQL（`RETURNING` など）に寄せてください。

## 既存パターンとの関係

- 読み取り専用の `IChatHistoryQuery` と同様、専用 Port はユースケースが必要とする
  操作だけを公開します。ただしこちらは書き込みを伴うため、Session の所有は
  UoW に置きます。
- 既存 `TeamMembership` のような集約は、状態遷移と不変条件を守る必要がある
  業務で引き続き使用します。この例はそれらを置き換えるものではありません。
- 招待コードの利用を加入申請に接続する場合は、同じ UoW の中で
  `uow.GetRepository(TeamMembership)` を取得し、既存の
  `TeamMembership.request_join(...)` を `add` してから `commit` できます。
  ただし途中のDB失敗では先行する書き込みも全て破棄し、そのUoWを再利用しません。
  この例では既存業務の意味論を変えないため、その接続は行っていません。
