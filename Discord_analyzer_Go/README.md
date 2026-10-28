# Discord Thread Analyzer (Go)

Python版 Discord Thread Analyzer と同じ `/analyze` コマンド、および接続確認用の `/ping` コマンドを提供するCLI Botです。Discordスレッドの直近メッセージを収集し、GeminiまたはOpenAI APIで「要約」「認識ズレ指摘」「簡易マネジメント」のいずれかを行い、結果をスレッドに返信します。

## 動作環境

- Go 1.16以降 (依存モジュールの `go.mod` に基づく。セキュリティ更新済みの新しいGoを推奨)
- Alpine Linux x86 (32bit / `GOARCH=386`)。Intel Atom N475を想定
- Discord Botを常駐起動できるネットワーク接続
- RAM 2GBを想定し、同時処理4件・待機キュー32件に制限

CGOを無効にしてビルドするため、生成バイナリはglibcに依存せず、Alpineのmusl環境で実行できます。実行時にはAPI通信のTLS証明書を検証するため、Alpineに `ca-certificates` をインストールしてください。

このBotはデータベースを使用しません。メッセージ履歴や解析結果をディスクへ保存しないため、DB接続設定も不要です。

## ビルドとセットアップ

1. Goを用意します。Alpine上で直接ビルドする場合:

   ```sh
   apk add --no-cache go ca-certificates
   ```

2. 設定ファイルを作成し、各トークンとAPIキーを設定します。

   ```sh
   cp .env.example .env
   chmod 600 .env
   ```

   - `DISCORD_BOT_TOKEN`: Discord Developer PortalのBot token
   - `LLM_PROVIDER`: `gemini` (既定) または `openai`
   - `GEMINI_API_KEY` または `OPENAI_API_KEY`: 選択したプロバイダーのAPIキー
   - `DISCORD_GUILD_ID`: 開発サーバーへコマンドを即時同期する場合のみ設定
   - `DEFAULT_HISTORY_LIMIT`: `limit` 未指定時の件数 (1〜1000、既定300)

   `.env` を使わず、同名の環境変数を設定しても起動できます。環境変数は `.env` の値より優先されます。

3. Discord Developer Portalで `MESSAGE CONTENT INTENT` を有効にし、Botに `bot` と `applications.commands` のスコープ、および `View Channel` / `Read Message History` / `Send Messages` / `Send Messages in Threads` 権限を付与してサーバーへ招待します。

4. ビルド・起動します。

   ```sh
   # Alpine Linux x86 (32bit) 向け。別のOS/CPUで実行しても同じバイナリを生成します。
   CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -trimpath -ldflags="-s -w" -o discord-analyzer .
   ./discord-analyzer
   ```

## 使い方

接続確認は任意のサーバーチャンネルで実行できます。

```text
/ping
```

BotはInteractionへの即時応答として `Pong! Botは正常に応答しています。` を返します。

解析対象のスレッドで実行します。

```text
/analyze mode:<要約 (summarize) | 認識ズレ指摘 (gap_analysis) | 簡易マネジメント (task_management)> limit:<1〜1000、任意>
```

`limit` の既定値は `DEFAULT_HISTORY_LIMIT`、最大値は1000です。Bot自身の発言、およびテキスト・添付ファイルのない発言は解析対象外です。結果は前置きなしの構造化テキストで返信し、1900文字を超える場合は複数メッセージに分けて送信します。添付ファイルの内容は取得せず、ファイル名のみログに含めます。

`DISCORD_GUILD_ID` を設定すると `/ping` と `/analyze` をそのサーバーへ同期します。未設定の場合は両コマンドともグローバル同期となり、Discord側に反映されるまで時間がかかる場合があります。起動ログに `scope=global` と出る場合はグローバル同期です。テスト時は `.env` にサーバーIDを指定すると反映を速められます。

## 運用上の動作

- `SIGINT` / `SIGTERM` を受け取ると新しいイベントの受け付けを止め、受け付け済みタスクを処理してから終了します。各処理には4分の上限があり、HTTP要求にはタイムアウトを設定しています。
- Discord Gatewayの初回接続、Discord API、Gemini/OpenAI APIで一時的な通信障害・HTTP 408/429/5xxが起きた場合は、最大5回、1秒から始まる指数バックオフで再試行します。Discord Gatewayの接続確立後の切断・再接続はdiscordgoの再接続機能に委ねます。
- Goroutine数は4つの処理ワーカー、1つのコマンド同期ワーカー、およびDiscordライブラリ内部に限定し、解析要求を無制限に生成しません。キュー満杯の場合は受付時に通知します。
- ワーカー内のpanicはスタックトレース付きで記録し、そのタスクを隔離してワーカーを継続させます。既にDiscordへ返信した可能性のあるタスクを丸ごと再実行すると二重投稿や二重課金になるため、panicした同一タスクは自動再実行しません。一時的なAPI障害は上記のAPI単位リトライで復旧します。
- Bot本体のpanicはスタックトレースを記録し、リソースを閉じた後、指数バックオフでプロセス内からBotを再起動します。設定エラーなどpanic以外の致命的な起動エラーはログに出して終了します。
- HTTPレスポンス本文と設定ファイルは必ず閉じます。DB接続はありません。

## 開発・テスト

```sh
go test ./...
```
