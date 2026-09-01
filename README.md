# Claude cache keepalive plugin for CLIProxyAPI

CPA 插件：把最后一次 **Claude** 上游请求存下来，每隔 50 分钟原样重发，并把 `max_tokens` 卡成 1（可改成 0）。用来刷新 Anthropic prompt cache 的 1 小时 TTL。

只处理 Claude 上游。Codex / GPT / Gemini / Grok 会跳过。

## 做什么

1. `request.intercept_after` 记下 CPA 已经改完的那一包
2. 定时重放，`stream=false`，`max_tokens` 限制输出
3. 管理页看快照时间和上次 ping

## 编译

需要 Go **1.26+**（CLIProxyAPI v7 SDK 要求）和 CGO（Linux 装 `gcc`，macOS 有 Xcode CLI tools）。

```bash
make test
make build
```

产物在 `dist/<goos>/<goarch>/claude-cache-keepalive.so`（macOS 是 `.dylib`）。

## 装到 CPA

把动态库放到 CPA 的插件目录，文件名必须是 `claude-cache-keepalive`：

```text
~/.cli-proxy-api/plugins/linux/amd64/claude-cache-keepalive.so
~/.cli-proxy-api/plugins/darwin/arm64/claude-cache-keepalive.dylib
```

`config.yaml`（常见在 `~/.cli-proxy-api/config.yaml`）：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    claude-cache-keepalive:
      enabled: true
      interval_minutes: 50
      max_tokens: 1
```

`plugins.enabled` 必须是 `true`。改完重启 CPA，或走管理 API 热加载。

确认二进制带插件：管理接口响应头有 `X-CPA-SUPPORT-PLUGIN: 1`。

## 接到 Claude Code

`~/.claude/settings.json`：

```json
{
  "promptCacheTtl": "1h",
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "ANTHROPIC_AUTH_TOKEN": "<CPA 入口 key>",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}
```

网关地址用 `127.0.0.1`，不要用局域网 IP。Cursor 的 Gateway 表单对非 loopback 的 `http://` 会直接报 `must use https (or http on loopback)`。

CPA 默契端口是 `8317`。Claude Code 正常聊一轮后，插件才会有快照；空闲约 50 分钟后打第一次保活。

状态页在 CPA 管理界面的 **Cache Keepalive**，或：

```text
/v0/resource/plugins/claude-cache-keepalive/status
```

## 怎么确认有效

保活响应当里：

- `cache_read_input_tokens` 很大，`cache_creation_input_tokens` 接近 0 → 命中，TTL 已刷新
- `cache_creation` 突然变大 → 前缀变了，不要继续空 ping

上游 TTL 若仍是 5 分钟，50 分钟一次来不及。Claude Code 里设 `promptCacheTtl: "1h"`，并让 CPA 原样转发 `anthropic-beta`。

## 配置

| 字段 | 默认 | 含义 |
|------|------|------|
| `interval_minutes` | 50 | 心跳间隔 |
| `max_tokens` | 1 | 保活输出上限；上游若接受 `0` 更干净 |
