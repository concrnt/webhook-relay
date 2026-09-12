# webhook-relay

任意のサービスからのwebhook POSTを受け取り、テンプレートで整形して concrnt (v2) のタイムラインへメッセージとして投稿するブリッジです。

[concurrent-webhook](https://github.com/rassi0429/concurrent-webhook) (v1向け・Node.js実装) の後継です。

## 使い方

```sh
cp config.example.yaml config.yaml
# config.yaml を編集
go build .
./webhook-relay -config config.yaml
```

設定した `path` に対して JSON ボディで POST すると、テンプレートを展開した本文が `timelines` に指定したタイムラインへ投稿されます。

```sh
curl -X POST localhost:8080/github \
  -H 'content-type: application/json' \
  -d '{"title": "hello", "text": "world"}'
```

## 設定

`config.example.yaml` を参照してください。

| キー | 説明 |
|---|---|
| `listen` | リッスンアドレス (デフォルト `:8080`) |
| `concrnt.domain` | 接続先ドメイン。非TLS環境では `http://localhost:8000` のようにスキーム付きで指定 |
| `concrnt.ccid` | 省略可。指定するとprivateKeyから導出したCCIDとの一致を起動時に検証 |
| `concrnt.privateKey` | 投稿主体の秘密鍵 (64桁hex)。環境変数 `PRIVATE_KEY` でも上書き可 |
| `webhooks[].path` | webhookを受け付けるパス |
| `webhooks[].timelines` | 投稿先タイムラインの cckv URI の配列 |
| `webhooks[].schema` | メッセージスキーマ。省略時は `https://schema.concrnt.world/m/markdown.json` |
| `webhooks[].profileOverride` | 表示名・アバター等の上書き (省略可) |
| `webhooks[].template` | 本文テンプレート (Go [text/template](https://pkg.go.dev/text/template) 形式) |

テンプレートには以下の変数が渡されます:

- `.body` — リクエストボディ (JSONパース済み)。例: `{{ .body.title }}`
- `.headers` — リクエストヘッダ (キーは小文字)。例: `{{ index .headers "x-github-event" }}`

JSONの `null` は `""` に置き換えてから渡されるので、`{{ .body.pull_request.body }}` のような空欄フィールドは何も出力しません(`{{ if }}` では偽のままです)。

レンダリング結果が空、または4096文字を超える場合は投稿せずに 200 を返します(送信元のリトライ抑止のため)。投稿(commit)に失敗した場合は 500 を返します。

## concurrent-webhook (v1) からの移行

- 設定が `CONFIG` 環境変数(JSON)から YAML ファイルに変わりました
- テンプレートが EJS (`<%= body.title %>`) から Go text/template (`{{ .body.title }}`) に変わりました
- `postStreams` は `timelines` になり、v2 の cckv URI 形式 (`cckv://<ccid>/concrnt.world/profiles/main/home-timeline` など) で指定します

## Docker

```sh
docker run -v $(pwd)/config.yaml:/app/config.yaml -p 8080:8080 ghcr.io/concrnt/webhook-relay
```
