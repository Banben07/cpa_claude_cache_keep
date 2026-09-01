# Claude cache keepalive plugin for CLIProxyAPI

CPA 插件：按对话记住 Claude 上游请求，每隔 50 分钟把**已勾选**的会话原样重发，并把 `max_tokens` 卡成 1。用来刷新 Anthropic prompt cache 的 1 小时 TTL。

只处理 Claude 上游。Codex / GPT / Gemini / Grok 会跳过。没有 `max_tokens` 的请求（常见是 `/v1/messages/count_tokens`）也不会进保活名单。

## 做什么

1. `request.intercept_after` 按「模型 + 第一条用户消息 + system 前缀」识别对话
2. 最多记住 8 路会话；新对话默认勾选保活。Claude Code 子代理不进保活名单。实测请求日志里 `X-Claude-Code-Parent-Agent-Id` / `x-app: cli-bg` / `task_budget` 都没有；子代理带 `X-Claude-Code-Agent-Id`，system 里有 `cc_is_subagent=true`，且和主对话共用同一个 `X-Claude-Code-Session-Id`
3. 每路从最后一次对话请求起算，每隔 50 分钟重放已勾选会话（`stream=false`，`max_tokens` 限制输出）。新消息会重置该路倒计时，不跟插件启动或上次保活时钟走。
4. 状态页可单独勾选 / 取消 / 忘记某一路

取消勾选后仍会跟着新消息更新快照，重新打开会用最新前缀。已勾选的不会因为空闲被踢掉（保活本来就是在你不说话时打请求）。未勾选的超过 `idle_evict_minutes`（默认 180）会被丢掉；超过上限时优先丢掉未勾选、最久没说话的对话。

重启 CPA 会清空内存里的会话名单和 5 小时用量窗口。

## 5 小时额度

Claude Code 的 session limit 是滚动 5 小时。插件**不会为了查额度多打上游**：只读已经回来的响应头 `Anthropic-Ratelimit-Unified-5h-Utilization`。

- **CPA 对话**用到 `100 - reserve_percent`（默认 **98%**）就在本地拦截，返回 429，请求到不了 Anthropic
- 已经标定过「多少用量对应 1%」时，若**这一发**预估会越过预留线，也会在本地拦住，避免 97% 时一发超长上下文打穿 100%、随后被上游连着拒
- **保活**继续用最后 2%（`max_tokens=1` + cache read，用不了 10%）
- 真的用到 100% / `rejected` 时，保活也停，避免空打

状态页的 **额度记录** 在会话保活列表下面，最多 5 条，列出每次 Claude 响应里解析到的 `5h-utilization` / `5h-status`（以及有的话 7 天窗口）。用来确认插件有没有真正拿到用量头；没有这几行或一直显示「未读到」，说明这次响应没带这组头。CPA 重启后记录清空。

所以你看到的 429 几乎都是 CPA 本地的；上游连着拒，只会发生在已经打穿窗口之后。正在飞行的那一次拦不住，最多超一发。

`five_hour_budget` 只在没有用量头时当后备。不要把保活自己限死，那会把窗口全留给对话。

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
      max_sessions: 8
      idle_evict_minutes: 180
      window_minutes: 300
      reserve_percent: 2
      five_hour_budget: 0
      guard_chat: true
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

CPA 默契端口是 `8317`。Claude Code 正常聊一轮后，插件才会出现会话；空闲约 50 分钟后打第一次保活。

状态页在 CPA 管理界面的 **缓存保活**，或：

```text
/v0/resource/plugins/claude-cache-keepalive/status
```

点左侧方块勾选或取消某一路。`忘记` 会从名单里删掉。

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
| `max_sessions` | 8 | 最多记住多少路对话（上限 32） |
| `idle_evict_minutes` | 180 | 丢掉多久没新请求的未勾选对话；`0` 表示不按空闲淘汰 |
| `window_minutes` | 300 | 用量窗口，对应 Claude 约 5 小时滚动限额 |
| `reserve_percent` | 2 | CPA 对话在 5 小时窗口还剩这么多时停住。保活输出已卡成 1 token，2% 足够 |
| `five_hour_budget` | 0 | 没有上游 5h 用量头时的加权后备；`0` 表示只信用量头 |
| `guard_chat` | true | 到达预留线后拦截 CPA 上的新 Claude 对话 |
