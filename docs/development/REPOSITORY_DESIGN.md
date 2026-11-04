# Repository設計の比較と選択

## 推奨

このテンプレートでは、既存`GenericRepository`を適合する集約の保存に残し、単純な業務は用途専用Port、表示・検索はQuery Portを選べるようにします。汎用実装の全面置換や、新しいRepositoryライブラリの導入は行いません。

汎用Repositoryが問題になるのは、共通化した契約よりも実行時の前提が多くなり、利用者が挙動を予測できなくなる場合です。共通化の有無より、取得単位・保存単位・競合検出・トランザクション所有者を説明できるかを評価します。

## 外部事例

以下は実装形態の違いを見るために選んだ事例です。採用率・普及率を測った調査ではありません。参照ファイルの範囲での比較であり、各プロジェクト全体の品質評価ではありません。

| 事例 | 確認できる構成 | このテンプレートへの示唆 |
| --- | --- | --- |
| [Cosmic Python・Repository章](https://www.cosmicpython.com/book/chapter_02_repository.html) / [UoW章](https://www.cosmicpython.com/book/chapter_06_uow.html) | 業務モデルを扱うRepositoryと、Session・commit・rollbackを所有するUoWを分離する教育用参照実装 | 業務の取得・保存を狭いPortにし、原子性をUoWに集める選択肢 |
| [Polar・RepositoryBase](https://github.com/polarsource/polar/blob/2fa71521565f/server/polar/kit/repository/base.py) | ORMモデルを扱う汎用基底、Protocolによるモデル属性契約、取得・pagination・create/update等を共有 | 汎用Repositoryも実在する。ただし本テンプレートのドメイン⇄ORM二重モデル変換とは条件が違う |
| [Advanced Alchemy・非同期Repository](https://github.com/litestar-org/advanced-alchemy/blob/d00c03ac05b2/advanced_alchemy/repository/_async.py) | Sessionを受け取る汎用Repositoryと多数の設定・例外変換。`auto_commit`の既定はFalse | 既製の汎用基盤も選択肢だが、設定・契約の学習と依存保守を引き受ける必要がある |
| [Full Stack FastAPI Template・crud.py](https://github.com/fastapi/full-stack-fastapi-template/blob/aabb90126964a164e02aff39e7c385bb0598236d/backend/app/crud.py) | Sessionを関数に渡し、SQLModelを直接操作。create/update関数自身がcommitする | 二重モデルとRepositoryを省ける実例。ただし複数操作を原子的に組み合わせたい場合はcommit境界を調整する必要がある |

Cosmic Pythonは書籍の設計例、Polarはアプリケーション、Advanced Alchemyは汎用ライブラリ、FastAPIの例はアプリテンプレートです。目的が違うため、いずれかの形式をそのまま標準解とはしません。

## 一次資料から分かること

[FowlerのRepository](https://martinfowler.com/eaaCatalog/repository.html)は、ドメインとData Mapperの間でコレクションに似たアクセスを提供します。複雑なモデル、多数のクラス、重いクエリがある場合のクエリ構築集約・重複削減も便益として説明されています。単なるCRUD共通化だけが目的ではありません。

[Transaction Script](https://martinfowler.com/eaaCatalog/transactionScript.html)は一つの要求を手続きとして扱い、DBまたは薄いラッパーを呼びます。[Domain Model](https://martinfowler.com/eaaCatalog/domainModel.html)はデータと振る舞いを持つモデルで複雑な業務を整理します。単純な機能にもモデル変換を必須にすると、分離の便益に対して維持対象が増える可能性があります。

[SQLAlchemy Session Basics](https://docs.sqlalchemy.org/en/20/orm/session_basics.html#when-do-i-construct-a-session-when-do-i-commit-it-and-when-do-i-close-it)は、Sessionの寿命とトランザクション制御を個々のデータ操作の外で管理する方針を説明します。Session自体もUnit of Workパターンを実装するため、独自UoWはDB機能の再実装より、アプリの原子性・寿命・失敗契約を表す境界として評価します。

ただし、この説明だけから『Repository内rollbackは常に不正』とは結論しません。例外をResultへ変換して処理を継続可能に見せる場合、DB失敗後のSessionと先行変更をどう扱うかを明示する必要があります。

## 現行GenericRepositoryの契約

参照する実装は`src/app/infrastructure/repositories/generic_repository.py`と`unit_of_work.py`です。

- ドメイン⇄ORMは`ORMMappingRegistry`に登録した明示的マッパーで変換します。変換コードと登録が必要です。
- IDは`id`属性を前提とし、監査日時・Version・追記専用の振る舞いは`IAuditable` / `IVersionable` / `IAppendOnly`への実行時判定で選びます。
- 更新は完全な集約スナップショットの保存です。子の空コレクションを『変更なし』とは扱いません。
- 事前Version比較に加え、ORMの`version_id_col`がflush時の競合を検出します。[SQLAlchemy Versioning](https://docs.sqlalchemy.org/en/20/orm/versioning.html)も参照してください。
- `merge(load=True)`と関連のロード設定に振る舞いを委ねます。[Merging](https://docs.sqlalchemy.org/en/20/orm/session_state_management.html#merging)が示すロードやcascadeの費用を確認します。性能は今回測定していません。
- 成功commitはUoWです。一方、Repository内のDB失敗ではrollbackし、UoWが`after_soft_rollback`で失敗状態を記録し、そのスコープでのcommitを拒否します。通常の事前Version不一致は同じ扱いではありません。一方、flush時の競合検出はrollbackを伴い、そのスコープでのcommitを拒否します。

この契約に適合する集約には、共通化の便益があります。契約に合わせるためだけに監査属性やVersionを追加する場合は、専用Portを検討します。

## 選択と代償

| 方法 | 適する場面 | 引き受ける費用 |
| --- | --- | --- |
| 現行GenericRepository | 共通のスナップショット保存・Version・監査契約に適合する集約 | 実行時契約、明示マッパー、登録、ORM設定を理解する必要 |
| 集約・用途専用Port | 特有の検索、条件付き更新、追記専用等を明示したい | 実装の重複を許容し、必要なSQL・競合制御を個別に保証 |
| Session直接利用 | ORMモデルとアプリのモデルを一致させ、DBへの依存を許容する構成 | 現行のApplication/Infrastructure境界を変更する。commit責任を統一する必要 |
| 読み取りQuery Port | 履歴・一覧・検索など、更新用の集約を必要としない処理 | 参照DTOとSQLの維持。現行`IChatHistoryQuery`は集約を返すため、必要に応じ投影へ絞る |

専用Portを既存UoWに参加させる場合は、Composition Rootで`SQLAlchemyUnitOfWork(..., repository_factories=...)`を構築し、同じSessionを各Portへ渡します。標準DIのfactoryは空なので、採用するアプリで明示的に配線します。この資料ではコードの追加・変更を行いません。

## 次に見直す候補

以下は調査に基づく提案であり、この変更で既存保存処理を改修したという意味ではありません。

1. **失敗とrollbackの境界**: 現行のResult契約と先行変更の破棄を保ちつつ、RepositoryとUoWの連携をさらに単純化できるか検討します。rollbackを一律削除すると既存契約を壊すため、別途回帰テストを伴う変更にします。
2. **エラー分類**: `IntegrityError`を一律`ALREADY_EXISTS`へ変換する現行方針は、FK・CHECK違反との区別を必要とするアプリでは見直します。
3. **追記専用の型**: 追記専用機能にupdate/deleteまで公開する必要があるか検討します。汎用型に例外条件を追加するより狭いPortで表現できる場合があります。
4. **読み取り投影**: `IChatHistoryQuery`が返す集約全体が本当に必要か、利用側DTOに絞れるかを確認します。Query層を設けただけで変換費用がなくなるわけではありません。

性能改善を主張する場合は、代表的な集約とDB環境でクエリと実行時間を測定します。事業上のコア・支援・汎用という位置付けと、規則・状態遷移・競合制御の複雑さは別々に評価し、分類だけで実装パターンを固定しません。
