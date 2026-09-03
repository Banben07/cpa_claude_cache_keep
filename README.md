# CPA Claude cache keepalive / CPA Claude 缓存保活

[English](#english) · [中文](#中文)

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that remembers Claude chats going through CPA and, every 50 minutes, replays each checked session with `max_tokens=1` so Anthropic’s 1-hour prompt cache TTL stays warm.

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件：记住走 CPA 的 Claude 对话，每隔 50 分钟把已勾选会话原样重发，并把 `max_tokens` 卡成 1，用来刷新 Anthropic prompt cache 的 1 小时 TTL。

Current version **0.8.7**. / 当前版本 **0.8.7**。

---

## English

Claude upstream only. Codex / GPT / Gemini / Grok are skipped. Requests without `max_tokens` (usually `/v1/messages/count_tokens`) are not added to the keepalive list.

Keepalive replay is `POST /v1/messages` on CPA itself (the same gin path Claude Code uses), not `host.model.execute`. Cloak, `cache_control`, beta headers, and the Claude executor then match a real chat, so the prompt cache can hit.

A successful keepalive is a real Messages completion that **reads** the cached prefix. That cache read refreshes the TTL. `max_tokens=1` is the cheap way to force a completion without a long reply. `count_tokens` does not refresh the cache. Official `max_tokens: 0` prefill exists, but it is rejected when thinking is on, so this plugin keeps `max_tokens=1`.

### What it does

- Remembers up to 8 chats. With `X-Claude-Code-Session-Id`, `/compact` stays the same row. Without that header it keys on model + first user message + system; compact may show up as a new row and is not merged into another chat
- New chats default to keepalive on. From the last real chat request, each checked session is POSTed back to local `/v1/messages` every 50 minutes (`max_tokens=1`, `stream=false`). Thinking / effort are left as-is so the cache key does not change
- Status page: toggle, rename, forget, **Ping now** (replays that session immediately, even if the timer is still running), and the 5-hour stop percent
- Claude Code Task / Agent subagents are not kept alive (`X-Claude-Code-Agent-Id`, `cc_is_subagent=true` in system)
- After a 5-hour stop, **chat stays blocked** until utilization drops or the window `Reset` time passes. `/compact` and compact continuations still go through. If chat and keepalive are both paused and no Reset time is known, one probe chat is allowed so the window cannot deadlock

Unchecked sessions still update their snapshot on new messages. Checked sessions are not evicted for idle. Unchecked ones older than `idle_evict_minutes` (default 180) are dropped; over the cap, unchecked / least-recent chats go first.

Restarting CPA clears in-memory sessions and the 5-hour usage window. The stop percent from the status page is written to `claude-cache-keepalive.settings.json` and survives restart.

### Build

Needs Go **1.26+** (CLIProxyAPI v7 SDK) and CGO (Linux: `gcc`; macOS: Xcode CLI tools). Must be **`-buildmode=c-shared`**, not `-buildmode=plugin`.

```bash
make test
make build
```

Output: `dist/<goos>/<goarch>/claude-cache-keepalive.so` (`.dylib` on macOS).

On a host without a local Go 1.26 toolchain, Docker works:

```bash
docker run --rm -e CGO_ENABLED=1 -v "$PWD":/src -w /src golang:1.26 \
  bash -c 'go test ./... && go build -buildvcs=false -buildmode=c-shared -o dist/linux/amd64/claude-cache-keepalive.so .'
```

### Install

The file name must be `claude-cache-keepalive`:

```text
~/.cli-proxy-api/plugins/linux/amd64/claude-cache-keepalive.so
~/.cli-proxy-api/plugins/darwin/arm64/claude-cache-keepalive.dylib
```

`config.yaml`:

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
      messages_url: http://127.0.0.1:8317/v1/messages
```

`plugins.enabled` must be `true`. Restart CPA, or hot-reload via the management API. `X-CPA-SUPPORT-PLUGIN: 1` on the management response means the binary has plugin support.

The management UI can flip this plugin back to `enabled: false`. Check that before a restart.

### Claude Code

`~/.claude/settings.json`:

```json
{
  "promptCacheTtl": "1h",
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "ANTHROPIC_AUTH_TOKEN": "<CPA gateway key>",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}
```

Use `127.0.0.1` for the gateway. Cursor Gateway rejects non-loopback `http://` with `must use https (or http on loopback)`. CPA’s default port is `8317`.

A session appears on the status page after a real chat. The first keepalive is about 50 minutes after that, unless you click **Ping now**.

Status page:

```text
/v0/resource/plugins/claude-cache-keepalive/status
```

### 5-hour quota

The plugin never probes Anthropic for quota. It only reads response headers that already came back: `Anthropic-Ratelimit-Unified-5h-Utilization`, `Status`, and `Reset`.

- Default: CPA chat is locally 429’d at **95%** and **stays blocked** until util drops (including a keepalive header) or `Reset` is reached, so one long turn cannot punch through 100%
- Status page can change “stop chat at x%” (60–99). That value is stored in `claude-cache-keepalive.settings.json`. When a Reset time is known, it is shown on the 5-hour card
- After Reset, local utilization is cleared. The top card may show `—` until the next Anthropic response brings a new header. The quota table still lists the last samples
- If units-per-percent has been calibrated, a turn predicted to land past the stop line is also blocked
- Keepalive keeps using the slice after the stop line (1 token + cache read)
- At 100% / `rejected`, keepalive pauses too. If both are paused and Reset is unknown, one chat probe is allowed
- `/compact` (Claude Code summary prompt, compact continuation, `context_management` compact edits, or `Anthropic-Beta` containing `compact-2026`) is not quota-blocked. Talking about `/compact` is still a normal chat

The **Quota log** on the status page keeps 5 rows: cache read/write, 5h/7d headers, and raw values. Almost every 429 you see is local to CPA; an in-flight request cannot be stopped.

`five_hour_budget` is only a fallback when utilization headers are missing.

Cache misses are usually a **prefix change**, not a missing 1-token output: `currentDate`, tool schemas, skills/MCP reminders, effort/thinking, or mixed 5m/1h breakpoints will rewrite the whole prefix.

### Config

| Field | Default | Meaning |
|------|---------|---------|
| `interval_minutes` | 50 | Keepalive interval |
| `max_tokens` | 1 | Keepalive output cap |
| `max_sessions` | 8 | Max remembered chats (cap 32) |
| `idle_evict_minutes` | 180 | Drop unchecked chats this idle; `0` disables idle eviction |
| `window_minutes` | 300 | Usage window (~Claude 5-hour rolling limit) |
| `stop_percent` | 95 | Stop CPA chat at this 5h percent. Editable on the status page |
| `reserve_percent` | 5 | Slice left after the stop line. Overridden if `stop_percent` is set |
| `five_hour_budget` | 0 | Weighted fallback with no 5h headers; `0` trusts headers only |
| `guard_chat` | true | Intercept new Claude chats after the stop line |
| `messages_url` | `http://127.0.0.1:8317/v1/messages` | Keepalive replay URL. Change if CPA is not on 8317 |

### How to tell it works

On the keepalive response, or the status-page **Cache** column:

- Large `cache_read_input_tokens`, `cache_creation_input_tokens` near 0 → hit, TTL refreshed
- Sudden large `cache_creation` → prefix changed; do not keep empty-pinging that snapshot

If upstream TTL is still 5 minutes, a 50-minute ping is too late. Set `promptCacheTtl: "1h"` in Claude Code and let CPA forward `anthropic-beta` unchanged.

---

## 中文

只处理 Claude 上游。Codex / GPT / Gemini / Grok 会跳过。没有 `max_tokens` 的请求（常见是 `/v1/messages/count_tokens`）也不会进保活名单。

保活重放走 CPA 自己的 `POST /v1/messages`（gin 入口，和 Claude Code 同一条），不再用 `host.model.execute`。这样 cloak、`cache_control`、beta 头和 Claude executor 的处理跟对话一致，prompt cache 才能对上。

保活必须是一发真正的 Messages **补全**，用 **cache read** 刷新 TTL，不是 `count_tokens`。`max_tokens=1` 是最便宜的补全；官方还有 `max_tokens: 0` 只做 prefill，但和 thinking 开着不兼容，所以插件继续用 1。

### 做什么

- 最多记住 8 路。有 `X-Claude-Code-Session-Id` 时按这个 ID 认对话，`/compact` 后仍是同一路。没有这个头时只用「模型 + 第一条用户消息 + system」，compact 可能显示成新的一行，不会猜着并进别的对话
- 新对话默认勾选保活；从最后一次对话请求起算，每 50 分钟把上次请求 POST 回本机 `/v1/messages` 一次（`max_tokens=1`，`stream=false`）。thinking / effort 原样带着，避免把缓存键改掉
- 状态页可勾选、取消、改名、忘记某一路，也可点「立刻保活」马上重放这一路；还可改 5 小时额度的停线
- Claude Code 的 Task / Agent 子代理不进保活名单（带 `X-Claude-Code-Agent-Id`，system 里有 `cc_is_subagent=true`）
- 5 小时用量碰到停线后会一直拦对话，等到用量掉回停线或窗口 `Reset` 到期。`/compact` 和压缩续写仍放行。对话和保活都停、又还没读到刷新时间时，会放行一发探测，避免卡死

取消勾选后仍会跟着新消息更新快照。已勾选的不会因为空闲被踢掉。未勾选的超过 `idle_evict_minutes`（默认 180）会被丢掉；超过上限时优先丢掉未勾选、最久没说话的对话。

重启 CPA 会清空内存里的会话名单和 5 小时用量窗口。状态页改过的停线会写到 `claude-cache-keepalive.settings.json`，重启后仍在。

### 编译

需要 Go **1.26+**（CLIProxyAPI v7 SDK）和 CGO（Linux 装 `gcc`，macOS 有 Xcode CLI tools）。必须用 **`-buildmode=c-shared`**，不要用 `-buildmode=plugin`。

```bash
make test
make build
```

产物在 `dist/<goos>/<goarch>/claude-cache-keepalive.so`（macOS 是 `.dylib`）。

机器上没有 Go 1.26 时可以用 Docker：

```bash
docker run --rm -e CGO_ENABLED=1 -v "$PWD":/src -w /src golang:1.26 \
  bash -c 'go test ./... && go build -buildvcs=false -buildmode=c-shared -o dist/linux/amd64/claude-cache-keepalive.so .'
```

### 装到 CPA

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
      messages_url: http://127.0.0.1:8317/v1/messages
```

`plugins.enabled` 必须是 `true`。改完重启 CPA，或走管理 API 热加载。管理接口响应头有 `X-CPA-SUPPORT-PLUGIN: 1` 说明二进制带插件。

管理页有时会把 `claude-cache-keepalive.enabled` 拨回 `false`，重启前看一眼。

### 接到 Claude Code

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

正常聊一轮后，状态页才会出现会话；空闲约 50 分钟后打第一次保活，或点「立刻保活」。

状态页：

```text
/v0/resource/plugins/claude-cache-keepalive/status
```

### 5 小时额度

插件不额外打上游查额度，只读已经回来的响应头 `Anthropic-Ratelimit-Unified-5h-Utilization` / `Status` / `Reset`。

- CPA 对话默认用到 **95%** 就在本地拦成 429，并一直拦到用量掉回停线（包括保活带回来的头）或 `Reset` 到期，避免一发长对话打穿 100%
- 状态页可改「对话停在 x%」（60–99），写到工作目录下的 `claude-cache-keepalive.settings.json`；有刷新时间时会显示在 5 小时卡片上
- `Reset` 到期后会清掉本地用量。顶栏可能显示 `—`，等下一发 Anthropic 响应带新头；额度表仍保留最近几条记录
- 标定过「多少用量对应 1%」后，若这一发预估会越过停线，也会先拦
- 保活继续用停线后面那一截（1 token + cache read）
- 用到 100% / `rejected` 时保活也停。两边都停且没有 `Reset` 时，放行一发对话探测新额度头
- `/compact`（Claude Code 摘要提示、压缩续写、`context_management` 的 compact edit、或 `Anthropic-Beta` 里带 `compact-2026`）不受停线限制。只是聊「/compact 怎么用」仍按普通对话拦

状态页下方的 **额度记录** 最多 5 条，用来确认有没有真正读到用量头和 cache read/write。你看到的 429 几乎都是 CPA 本地的；正在飞行的那一次拦不住。

`five_hour_budget` 只在没有用量头时当后备。

缓存不命中多半是 **前缀变了**，不是少输出了 1 个 token：`currentDate`、工具描述、skills/MCP 提醒、effort/thinking、5 分钟和 1 小时断点混用，都会整段重写。

### 配置

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
| `messages_url` | `http://127.0.0.1:8317/v1/messages` | 保活重放地址。CPA 不在 8317 时改这里 |

### 怎么确认有效

保活响应当里，或状态页额度表「缓存」列：

- `cache_read_input_tokens` 很大，`cache_creation_input_tokens` 接近 0 → 命中，TTL 已刷新
- `cache_creation` 突然变大 → 前缀变了，不要继续空 ping

上游 TTL 若仍是 5 分钟，50 分钟一次来不及。Claude Code 里设 `promptCacheTtl: "1h"`，并让 CPA 原样转发 `anthropic-beta`。
