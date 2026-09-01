# CPA Claude 缓存保活

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件：记住走 CPA 的 Claude 对话，每隔 50 分钟把已勾选会话原样重发，并把 `max_tokens` 卡成 1，用来刷新 Anthropic prompt cache 的 1 小时 TTL。

只处理 Claude 上游。Codex / GPT / Gemini / Grok 会跳过。没有 `max_tokens` 的请求（常见是 `/v1/messages/count_tokens`）也不会进保活名单。

当前版本 **0.8.1**。

## 做什么

- 最多记住 8 路。有 `X-Claude-Code-Session-Id` 时按这个 ID 认对话，`/compact` 后仍是同一路。没有这个头时只用「模型 + 第一条用户消息 + system」，compact 可能显示成新的一行，不会猜着并进别的对话
- 新对话默认勾选保活；从最后一次对话请求起算，每 50 分钟重放一次
- Claude Code 的 Task / Agent 子代理不进保活名单（带 `X-Claude-Code-Agent-Id`，system 里有 `cc_is_subagent=true`）
- 状态页可勾选、取消、改名、忘记某一路，也可改 5 小时额度的停线

取消勾选后仍会跟着新消息更新快照。已勾选的不会因为空闲被踢掉。未勾选的超过 `idle_evict_minutes`（默认 180）会被丢掉；超过上限时优先丢掉未勾选、最久没说话的对话。

重启 CPA 会清空内存里的会话名单和 5 小时用量窗口。状态页改过的停线会写到 `claude-cache-keepalive.settings.json`，重启后仍在。

## 编译

需要 Go **1.26+**（CLIProxyAPI v7 SDK）和 CGO（Linux 装 `gcc`，macOS 有 Xcode CLI tools）。

```bash
make test
make build
```

产物在 `dist/<goos>/<goarch>/claude-cache-keepalive.so`（macOS 是 `.dylib`）。

## 装到 CPA

文件名必须是 `claude-cache-keepalive`：

```text
~/.cli-proxy-api/plugins/linux/amd64/claude-cache-keepalive.so
~/.cli-proxy-api/plugins/darwin/arm64/claude-cache-keepalive.dylib
```

`config.yaml`：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    claude-cache-keepalive:
      enabled: true
      interval_minutes: 50
      max_tokens: 1
      max_sessions: 8
      idle_evict_minutes: 180
      window_minutes: 300
      stop_percent: 95
      five_hour_budget: 0
      guard_chat: true
```

`plugins.enabled` 必须是 `true`。改完重启 CPA，或走管理 API 热加载。管理接口响应头有 `X-CPA-SUPPORT-PLUGIN: 1` 说明二进制带插件。

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

网关地址用 `127.0.0.1`。Cursor Gateway 对非 loopback 的 `http://` 会报 `must use https (or http on loopback)`。CPA 默认端口 `8317`。

正常聊一轮后，状态页才会出现会话；空闲约 50 分钟后打第一次保活。

状态页：

```text
/v0/resource/plugins/claude-cache-keepalive/status
```

## 5 小时额度

插件不额外打上游查额度，只读已经回来的响应头 `Anthropic-Ratelimit-Unified-5h-Utilization`。

- CPA 对话默认用到 **95%** 就在本地拦成 429，避免一发长对话打穿 100%
- 状态页可改「对话停在 x%」（60–99），写到工作目录下的 `claude-cache-keepalive.settings.json`
- 标定过「多少用量对应 1%」后，若这一发预估会越过停线，也会先拦
- 保活继续用停线后面那一截（1 token + cache read）
- 用到 100% / `rejected` 时保活也停

状态页下方的 **额度记录** 最多 5 条，用来确认有没有真正读到用量头。你看到的 429 几乎都是 CPA 本地的；正在飞行的那一次拦不住。

`five_hour_budget` 只在没有用量头时当后备。

## 配置

| 字段 | 默认 | 含义 |
|------|------|------|
| `interval_minutes` | 50 | 心跳间隔 |
| `max_tokens` | 1 | 保活输出上限 |
| `max_sessions` | 8 | 最多记住多少路对话（上限 32） |
| `idle_evict_minutes` | 180 | 丢掉多久没新请求的未勾选对话；`0` 表示不按空闲淘汰 |
| `window_minutes` | 300 | 用量窗口，对应 Claude 约 5 小时滚动限额 |
| `stop_percent` | 95 | 5 小时用量到达该百分比时停 CPA 对话。状态页可改 |
| `reserve_percent` | 5 | 停线后面留下的百分比。设了 `stop_percent` 时会被覆盖 |
| `five_hour_budget` | 0 | 没有上游 5h 用量头时的加权后备；`0` 表示只信用量头 |
| `guard_chat` | true | 到达停线后拦截 CPA 上的新 Claude 对话 |

## 怎么确认有效

保活响应当里：

- `cache_read_input_tokens` 很大，`cache_creation_input_tokens` 接近 0 → 命中，TTL 已刷新
- `cache_creation` 突然变大 → 前缀变了，不要继续空 ping

上游 TTL 若仍是 5 分钟，50 分钟一次来不及。Claude Code 里设 `promptCacheTtl: "1h"`，并让 CPA 原样转发 `anthropic-beta`。
