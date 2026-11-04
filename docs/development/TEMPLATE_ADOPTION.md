# 独立アプリへの導入と選択的な更新

この手順は、テンプレートのGit履歴を保持して独立アプリを開始し、必要な上流修正だけをレビューして取り込む方法です。自動初期化・自動同期・一括上書き・DBの自動移行は行いません。

以下は、テンプレートとアプリのdefault branchがともに `main` の例です。異なる場合は、各リポジトリのbranch名に読み替えてください。

## 新しいアプリを開始する

GitとPython 3.13以上を用意し、Clone直後、独自の変更を加える前に開始commitを確認します。

```bash
git clone https://github.com/aiagate/flow-app-template.git my-app
cd my-app
git remote rename origin template-upstream
git rev-parse HEAD
```

`.template-origin.toml` を新しく作り、採用元と開始commitを手動で記録します。次の `ACTUAL_START_COMMIT` は、直前の `git rev-parse HEAD` が表示した完全なcommit SHAへ置き換えます。例示用の文字列をそのままcommitしないでください。

```toml
repository = "https://github.com/aiagate/flow-app-template.git"
base_commit = "ACTUAL_START_COMMIT"
```

- `repository`：Cloneした取得元を識別するURL。この例と異なる取得元を使った場合は、実際の取得元へ変更します。認証情報・token・秘密のquery/fragmentを記録せず、Gitのcredential helperやSSH認証を利用してください。
- `base_commit`：独自変更前の開始commit。現在のHEADや上流の最新版から、後日推測して作り直しません。

このファイルは人が内容を確認して作成する記録です。既存の記録を上書きせず、由来が不明な既存アプリへ推測で後付けしません。新しい生成ツールやCLI、Git履歴の削除、パッケージ名の変更は不要です。

記録をcommitし、独立したGitHubリポジトリへ公開します。GitHub CLIを使う例です。Gitの名前・メールアドレスとGitHub認証は、利用者自身の設定を使用します。

```bash
git add .template-origin.toml
git commit -m "chore: record application template origin"
gh repo create my-app --private --source . --remote origin
git push --set-upstream origin main
```

GitHubのWeb UIで空のリポジトリを作り、`origin` を登録してpushする方法でも構いません。`template-upstream` は元のテンプレート、`origin` は自分のアプリとして使い分けます。リポジトリ名・README等は、その後アプリに合わせて編集します。Pythonの `app` パッケージ名を変更する必要はありません。

アプリの依存・環境設定・起動は、[READMEのセットアップ](../../README.md#セットアップ)に従います。

## アプリをCloneし直した場合

Gitのremote設定は、他の開発者のCloneには引き継がれません。`.template-origin.toml` の `repository` を確認し、同じ取得元を `template-upstream` として登録します。記録ファイルを評価・実行する必要はありません。

```bash
# このテンプレートの標準URLを使って導入した場合
git remote add template-upstream https://github.com/aiagate/flow-app-template.git
git fetch template-upstream
```

別の取得元を記録している場合は、そのURLを使用してください。相対ローカルパスは配置変更によって同じ取得元を指さなくなるため、移動・再Clone後に実際の場所を確認します。記録を現在のHEADで作り直したり、開始commitを進めたりはしません。

## 必要な修正を取り込む

まずアプリ側のmainを最新にし、未commitの変更が残っていないことを確認します。作業中の変更がある場合は、ここで止めてcommit等で整理してください。

```bash
git switch main
git pull --ff-only origin main
git status --short
git fetch template-upstream
```

`.template-origin.toml` の `base_commit` から上流までの変更を確認します。次の例はBash用です。

```bash
BASE_COMMIT=$(python -c 'import pathlib, tomllib; print(tomllib.loads(pathlib.Path(".template-origin.toml").read_text(encoding="utf-8"))["base_commit"])')
git log --oneline "${BASE_COMMIT}..template-upstream/main"
```

開始commitは部分的な修正を採用しても動かしません。この一覧には、既に個別採用した修正も含まれ得ます。アプリ側のGit履歴・PRと、出典commitを照合して選別してください。

`COMMIT_SHA` を確認済みの修正commitに置き換え、差分を全文確認してから更新用branchへ取り込みます。

```bash
git show --stat COMMIT_SHA
git show COMMIT_SHA
git switch -c update/template-fix
git cherry-pick -x COMMIT_SHA
```

- 実装と、その修正を保証するテストを一緒に確認します。
- 業務モデル・Handler登録・DI接続・ORM定義を、テンプレートの都合で上書きしません。
- `alembic/versions/` の変更やschema変更を含む修正は、アプリのrevisionとデータに合わせた別の移行判断が必要です。既存migrationの置換・再生成はしません。
- 保存契約の変更はRepository・UoW・Version・ORM設定の対応も確認します。
- 依存変更を採る場合は、アプリの追加依存を残し、uvでlockを整合させます。上流のlockfile全体を差し替えません。

そのまま適用できない修正は、アプリに合う形で移植し、元のcommitをcommitメッセージまたはPRに記録します。`cherry-pick -x` でも、競合を解消して変更した場合等は出典を改めて確認してください。

競合した場合、indexや作業ツリーには途中の変更が残ります。終了コードだけでなく `git status` と差分を確認し、未解決を完了扱いしません。取り込み自体を中止する場合は、進行中の操作に対して `git cherry-pick --abort` を使います。

取り込み後は、アプリ側のRuff・Pyright・テストと必要な起動／DB検証を行い、PRで採否を判断します。上流全体の一括merge、自動マージ、本番DBへの自動適用はこの手順に含めません。「一部の修正を採用した」と「テンプレート最新版へ全体同期した」は区別します。

## 複数アプリで開発用PostgreSQLを使う

Composeのserviceとvolumeはproject単位です。アプリごとに異なるproject名とホスト側ポートを指定します。既定のホスト側ポートは5432です。

`.env.example` から作る `.env.local` で、PostgreSQLを使う場合の接続先を合わせます。次は開発用の例です。

```dotenv
POSTGRES_HOST_PORT=5433
DATABASE_URL=postgresql+asyncpg://bot:bot@localhost:5433/bot
```

```bash
docker compose --env-file .env.local -p my-app up -d
```

別アプリでは、例えばproject名を `another-app`、ホスト側ポートと接続先URLのポートを5434にします。Composeは `.env.local` を既定では読み込まないため、`--env-file` を明示します。アプリの環境読込み優先順位とは別の仕組みです。

既存DBを使う場合は同じCompose project名を保ってください。別のproject名は別volumeを使うため、データを削除していなくても新しい空DBに見える場合があります。更新作業を理由に既存volumeを削除しないでください。DBの安全な操作は[マイグレーションガイド](../infrastructure/DATABASE_MIGRATIONS.md)を参照します。
