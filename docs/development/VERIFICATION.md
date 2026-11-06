# 業務ルールを検証へつなぐ手順

このドキュメントは、業務上の約束を「不変条件 → 保証する責務 → 検証方法」の順に
追いかけるための参照資料です。すべての機能に形式モデルやPBTを要求するものでは
ありません。例ベースのテストで十分な処理も多くあります。ここでは、どの性質が
どの手法で担保され、どこから先は担保されないのかを区別できる状態を目的にします。

- 実装方式（Transaction Script / ドメインモデル / Query）の選び方は
  [業務に応じた実装の選び方](./IMPLEMENTATION_CHOICES.md) を参照してください。
- 機能追加の全体手順は [機能追加ガイド](./ADDING_FEATURE.md) を参照してください。
- 業務上の規則そのものの正本は
  [境界と用語集](../domain/BOUNDARIES_AND_GLOSSARY.md) です。この文書はその
  規則を「どう検査するか」だけを扱います。

## 1. まず約束を4つの欄に分ける

新しい機能や業務ルールを追加するとき、次の4つを短く書き出します。記入は
箇条書き数行で構いません。すべての欄を埋める必要はなく、空欄は「未検証」を
意味します。

| 欄 | 例（Membership） |
| --- | --- |
| 業務上の約束 | 同じ team/user の現在の加入期間は同時に一つ |
| いつ成立するか | 加入・承認・退会・再加入のどの操作の後でも |
| 保証する責務 | 集約は自分の期間しか見ない。DBの部分一意インデックスが担保する |
| 検証方法 | 実DBへの直接INSERT（境界の検査）+ 操作列の性質テスト |

「いつ成立するか」が操作の前後関係を含むなら、単発の入力検証ではなく状態遷移の
問題です。「保証する責務」が複数の行やテーブルにまたがるなら、集約単体のテスト
ではなくDB制約かトランザクション境界の問題になります。この切り分けが、次に
どの手法を選ぶかを決めます。

## 2. 手法の選び方

| 手法 | 選ぶ契機 | 向かないケース | 検査が保証する範囲 |
| --- | --- | --- | --- |
| 例ベースのテスト | 入力と期待結果が少数の代表例で決まる。エラー分岐の存在確認 | 入力の組合せや操作順が多く、代表例を選ぶ根拠が弱い | 書いた例だけ。例の外は未検査 |
| Property-based test (PBT) | 入力の組合せ、境界値、操作順、履歴の保持を広く見たい | 仕様が固まっておらず、期待値自体を決められない | 生成された例の範囲。分布とseedに依存し、網羅ではない |
| 実行可能な仕様モデル + 境界内全探索 | 状態遷移と不変条件を、実装から独立に確かめたい | 状態空間が境界を決められないほど広い。業務ルール自体が未確定 | 宣言した境界内の全状態。境界の外と実装対応は別問題 |
| Mutation testing（手動の変異確認） | 検査が「壊れた実装」を実際に検出できるか確かめたい | 常用の回帰。実行時間と管理コストが高い | 変異させた箇所を検査が捉えること。仕様の正しさは別 |

PBTと全探索はどちらも「仕様モデル」を必要とします。仕様モデルは実装をimportせず、
業務上の約束だけから書きます。実装を呼ぶテストと仕様モデルを同じコードにすると、
実装の誤りを仕様側にも写してしまい、照合の意味がなくなります。

Mutation testingは常用しません。検査の空振り（常に成功する検査）を疑ったとき、
または重要なガードを追加したときに、手動で1〜2箇所だけ壊して検出を確認します。
手順は [6. 検査が壊れた実装を検出するか確認する](#6-検査が壊れた実装を検出するか確認する)
にあります。

## 3. 保証範囲を混ぜない

形式モデルやPBTを導入すると、次の4つが同じ「検証済み」にまとまりがちです。
報告や文書では分けて書きます。

- **モデルの正しさ**: モデルが業務上の約束を正しく写しているか。モデル自体の
  検査では確かめられない。業務ルールの正本と人間のレビューで担保する。
- **実装対応**: 実装がモデルの予測と一致するか。実装を呼ぶPBTが扱う。
- **原子性・並行性**: 1アクション内のDB読み書きのインターリーブや、同時実行時の
  保証。逐次モデルは表現しない。別途、同時実行テストや分離レベルで扱う。
- **探索境界・公平性**: 全探索は宣言した境界（チーム数・ユーザー数・手数）の
  内側だけを見る。境界の外は未検査。全アクションを有効な限り探索するので、
  公平性は仮定していない（特定のアクションだけが永遠に選ばれないとは言わない）。

「任意の操作列で成立する」と書けるのは、境界を明示せずに済む証明がある場合だけ
です。境界付きの全探索や生成例では、その境界を必ず併記します。

## 4. 実行例: TeamMembership

対象は `TeamMembership`（1回の加入から退会までの期間）です。業務上の規則は
[境界と用語集](../domain/BOUNDARIES_AND_GLOSSARY.md#team-membership) にあり、
この例はその規則を検査へ写したものです。コードにない規則（認可、退会後の再申請
審査など）はモデルにもテストにも入れていません。

### 規則・責務・検証の対応

| 業務上の約束（正本） | 保証する責務 | 検証コード |
| --- | --- | --- |
| 同じ team/user の `PENDING`/`ACTIVE` は同時に一つ | 集約は他の期間を見ない。DBの部分一意インデックス `uq_team_memberships_current_period` が担保し、重複は `ALREADY_EXISTS` に写像される | [`tests/pbt/test_membership_partial_index_pbt.py`](../../tests/pbt/test_membership_partial_index_pbt.py)（実DBへ直接INSERT）と `formal/explore.py` の `one_current_period` |
| `LEAVED` は終端で履歴を保持 | `leave()` は `LEAVED` からの再遷移を拒否し、行を削除しない | `tests/domain/aggregates/test_team_membership.py` の遷移表、`formal/explore.py` の `leaved_is_terminal` |
| 再加入は新しい期間と新しいIDを作る | `join()`/`request_join()` が毎回新しいULIDの期間を作る | [`tests/pbt/test_membership_stateful_pbt.py`](../../tests/pbt/test_membership_stateful_pbt.py) の再加入テスト（退会を必ず発生させる）と既存の [`test_membership_periods.py`](../../tests/usecases/memberships/test_membership_periods.py) |
| 承認は `PENDING` から `ACTIVE` | 集約の `approve()` が `PENDING` 以外を拒否する | 遷移表、状態列のPBT、`formal/explore.py` の `approve` 有効条件 |

未確定の業務判断（コードからは決められないため、実装が許す側に合わせて記録して
いるだけ）:

- `PENDING` のまま `leave()` できる（実装は許可、文書に明記なし）。
- `change_role()` を `PENDING` に許可する（同上）。
- 退会後の再申請に審査を挟むか（`request_join()` は常に新しい `PENDING` を作る）。
- 誰が承認できるか（認可）はコードにも文書にもない。

### エラー分類はAPI契約であり業務規則ではない

PBTの参照モデルが期待する `NOT_FOUND` / `VALIDATION_ERROR` / `ALREADY_EXISTS` は、
業務上の約束ではなく、ユースケースの戻り値を入口へ写す契約です。HTTPでは
`VALIDATION_ERROR` → 400、`NOT_FOUND` → 404、`ALREADY_EXISTS`（`CONFLICT`）→ 409
に対応します（[API実装ガイド](../api/implementation_guide.md#4-エラーハンドリング)、
[`src/app/presentation/api/error_mapping.py`](../../src/app/presentation/api/error_mapping.py)）。

つまり、同じ業務上の拒否でも入口が変われば分類は変わります。「承認できない」は
業務規則、「その拒否が400で返る」はAPI契約です。業務ルールを変えるときは両方を
別々に更新します。

### モデルとテストの構成

| ファイル | 役割 | 実装への依存 |
| --- | --- | --- |
| [`formal/explore.py`](../../formal/explore.py) | 業務上の約束だけを書いた実行可能な仕様モデル。境界内の全状態を列挙して不変条件を確認する | なし |
| [`tests/pbt/reference_model.py`](../../tests/pbt/reference_model.py) | 同じ約束を操作の期待値として表した参照モデル。PBTの判定に使う | なし |
| [`tests/pbt/test_membership_stateful_pbt.py`](../../tests/pbt/test_membership_stateful_pbt.py) | 生成した操作列を実ハンドラで実行し、DBの全期間行をID単位で参照モデルと比較する | あり（ハンドラ・ORM・実SQLite） |
| [`tests/pbt/test_membership_partial_index_pbt.py`](../../tests/pbt/test_membership_partial_index_pbt.py) | 現在期間の一意性を、DBへ直接INSERTして確認する | あり（ORMメタデータと実SQLite） |

`formal/explore.py` と `reference_model.py` は同じ業務上の約束を2つの形で書いて
います。片方を変えたときにもう片方へ自動で反映する仕組みはありません。**どちらが
正しいかを機械的に照合する検査もありません。** 業務ルールを変えるときは、正本の
文書・両モデル・実装・テストを同じ変更の中で更新し、レビューで突き合わせます。
この重複は、モデルの独立性（実装の写しにしない）と、実装を呼ばない全探索を両立
させるためのコストとして残しています。どちらか一方に寄せる判断は、業務ルールの
変更頻度と、状態空間の広さが変わったときに行います。

### 検証範囲

- 全探索: チーム1・ユーザー2・手数8まで（`formal/explore.py` の既定値）。到達可能な
  **343状態・688遷移**を列挙し、違反0件。
- PBT: チーム1・ユーザー2・手数8までの状態列は最大60例、インデックスは最大50例を生成して実行。
  再加入は `join` と `request→approve` の2経路を決定論的に実行。生成された列は全列の網羅ではない。
- 並行実行、PostgreSQLのインデックス挙動（`TEST_DATABASE_URL` を設定した場合のみ）、
  認可・監査・通知は対象外。

## 5. 実行方法

追加のツールは不要です。Node/npm は使いません。

```sh
# 依存（初回のみ。Python 3.13 は uv が用意する）
uv sync --frozen --all-extras

# 業務ルールの検査（実ハンドラ + 実SQLite + 部分一意インデックス）
uv run --frozen --all-extras pytest tests/pbt --no-cov -q

# 仕様モデルの境界内全探索（違反があれば exit 1）
python3 formal/explore.py --max-depth 8 --teams 1 --users 2
```

この環境での実測値:

| コマンド | 所要時間 | 結果 |
| --- | --- | --- |
| `pytest tests/pbt --no-cov -q` | 約35〜40秒 | 3 passed |
| `python3 formal/explore.py`（既定境界） | 0.1秒未満 | 343状態 / 688遷移 / 違反0 |

`tests/pbt` は通常の `tests/` 配下にあるため、リポジトリ全体の `pytest` でも実行されます。
CIでは、PBTを実行するのはSQLite側のテストステップだけで、PostgreSQL側の
テストステップは `--ignore=tests/pbt` で除外します。PBTハーネスは自前の一時SQLiteを
作って動くため、PostgreSQL側での再実行は同じ検査の繰り返しになり、約35〜40秒の
追加コストに見合うカバレッジがありません。

`formal/explore.py` はデータベースに依存しないため、CIでは1回だけ実行します
（0.1秒未満）。ただし、全探索をCIに入れても**モデル側の更新漏れは検出できません**。
モデルは実装を見ていないので、業務ルールを変えてモデルを直さなくても、古い約束の
まま違反0で合格します（§7で実測）。CIで検出できるのは、モデル自体を編集した結果
として不変条件違反が出た場合や、モデルが実行できなくなった場合です。モデルと正本の
一致は自動検査の対象ではなく、レビューで突き合わせます。

Mutation testingは常用のCIには入れません。手動で行い、結果をレビュー記録に残します。

## 6. 検査が壊れた実装を検出するか確認する

重要なガードを追加・変更したときは、そのガードを一時的に壊し、検査が失敗する
ことを確認します。確認後は必ず復元し、同じ検査が成功することを記録します。

例: 承認ガード（`TeamMembership.approve()` の `PENDING` 以外を拒否する条件）を
外す場合

```sh
# 1. 変異を入れる（src/app/domain/aggregates/team_membership.py の approve()）
uv run --frozen --all-extras pytest tests/pbt --no-cov -q   # 失敗することを確認
# 2. 復元する
git diff --stat src/                                        # 差分が空であることを確認
uv run --frozen --all-extras pytest tests/pbt --no-cov -q   # 成功に戻ることを確認
```

モデル側の検査も同じように確認できます。

```sh
# 承認の有効条件を壊した仕様モデルで全探索する
python3 formal/explore.py --variant broken-approve-guard   # exit 1（違反を報告）
python3 formal/explore.py                                  # exit 0（違反0）
```

## 7. 演習: 業務ルールを1つ変えて、影響範囲を追う

ここでは仮想の要件を使います。**この要件を本番仕様として採用しないでください。**
目的は、業務ルールの変更がどの層へ波及するかを体験することです。

> 仮想要件: 「承認の再実行を冪等にする。既に `ACTIVE` の期間へ承認を再実行しても
> エラーにせず、状態を変えずに成功として扱う。」

### 追う順序と、変える場所

1. **業務上の約束（正本）**: [境界と用語集](../domain/BOUNDARIES_AND_GLOSSARY.md#team-membership)
   の「承認は `PENDING` から `ACTIVE`」に、`ACTIVE` への再承認は成功で状態不変、
   という1文を足す。`LEAVED` への承認は従来どおり拒否する、も明記する。
   文書だけを変えてもテストは何も失敗しない。文書は実行されないためで、これは
   「文書の更新漏れを検査が教えてくれない」ことの確認でもある。
2. **API契約**: 再承認が成功になるなら、入口の応答は400ではなく200と現在状態に
   なる。`VALIDATION_ERROR` → 400 の対応自体は変えない（分類は契約のまま）。
   変わるのは「どの操作がどの分類になるか」。
3. **仕様モデル**: `formal/explore.py` の `approve` の有効条件を「`PENDING` または
   `ACTIVE`」に広げ、`ACTIVE` への適用が状態を変えないことを表現する。ここを
   変え忘れると、モデルは古い約束のまま合格し続ける（モデルは実装を見ていない）。
4. **参照モデル**: `tests/pbt/reference_model.py` の `expect_approve()` を、
   `ACTIVE` なら `OK`（状態は `ACTIVE` のまま）に変える。
5. **実装**: `TeamMembership.approve()` のガードを、`PENDING` と `ACTIVE` を許し、
   `LEAVED` だけ拒否する形に変える。
6. **テスト**: [`test_approve_join_request.py`](../../tests/usecases/memberships/test_approve_join_request.py)
   のエラー期待値を再承認成功へ変え、必要なら再承認の回帰テストを1本足す。
7. **ドメインテスト（遷移表）**: [`test_team_membership.py`](../../tests/domain/aggregates/test_team_membership.py)
   の遷移表で `approve` と `activate` の `ACTIVE` の期待値を「拒否（`None`）」から
   「状態不変で成功（`ACTIVE`）」へ変え、`test_team_membership_cannot_approve_active_period`
   を「`ACTIVE` への承認は状態を変えない」検査へ変える。

### どこで何が失敗するか（順序の確認）

- 1だけ変えた場合: 失敗なし。文書は検査されない。
- 5だけ変えた場合: `pytest tests/pbt` が失敗する。参照モデルは再承認を
  `VALIDATION_ERROR` と予測するため、`[join(u1), approve(u1)]` のような列で
  「期待: validation_error / 実際: ok」の不一致が出る。
  さらに、5だけでは **PBT以外にも既存テストが4本失敗する**（実測）:
  `tests/domain/aggregates/test_team_membership.py` の遷移表2件（`approve` と
  `activate` の `ACTIVE`）、`test_team_membership_cannot_approve_active_period`、
  `tests/usecases/memberships/test_approve_join_request.py` の `not_pending`。
  実装とテストを同じ変更で更新しないと、PBT以外の既存の約束も裏切ったままになる。
- 4と5を変え、3を忘れた場合: PBTは通るが `formal/explore.py` は古い約束のまま
  合格する。実装とモデルの片側だけが更新された状態を、全探索は検出しない。
- 3・4・5を変え、6と7を忘れた場合: `pytest tests/usecases/memberships` と
  `tests/domain/aggregates/test_team_membership.py` が失敗する。

「5だけ変えた場合」の失敗範囲は、この例を追加したときの実測に基づく（PBT 1 failed /
ドメイン・ユースケース 4 failed, 161 passed）。7箇所すべてを変えた場合は 44 passed、
全探索は 343状態 / **828遷移** / 違反0だった。

**§6の変異との違い**: §6で実施済みなのは「承認ガードを**すべて**外す」変異で、
`LEAVED` からの承認も通してしまいます。この演習の冪等版は `ACTIVE` だけを許し
`LEAVED` は拒否するため、両者は `LEAVED` で等価ではありません（全ガード削除は
`leaved_is_terminal` 違反で exit 1、冪等版は違反0）。PBTの反例はどちらも
`['join(u1)', 'approve(u1)']` になり紛らわしいので、区別して読んでください。

### 実行と復元

```sh
uv run --frozen --all-extras pytest \
  tests/domain/aggregates/test_team_membership.py \
  tests/usecases/memberships tests/pbt --no-cov -q
python3 formal/explore.py
git diff --stat        # 変更したファイルを確認
git checkout -- .      # 演習の変更を破棄（未追跡ファイルは別途削除）
```

演習の答えは実装として残していません。採用する場合は、上の7箇所を同じ変更で
更新し、[6.](#6-検査が壊れた実装を検出するか確認する) の変異確認を行ってください。

## 8. この例で測っていないこと

- 使いやすさの改善量（この文書と例が開発速度をどれだけ上げたか）は未測定。
- 他のドメインへの一般性は未検証。Membershipは履歴と一意性を持つ小さな例であり、
  読み取り専用Queryや外部API連携には別の検証設計が要る。
- PostgreSQL上の部分一意インデックス、同時実行、境界外の状態空間は未検証。
