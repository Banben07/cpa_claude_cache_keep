package main

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

type statusView struct {
	State         string
	StateLabel    string
	StateHint     string
	IntervalMin   int
	MaxTokens     int
	HasSnapshot   bool
	Model         string
	ToFormat      string
	BodySize      string
	SavedAt       string
	SavedAgo      string
	LastPing      string
	LastPingAgo   string
	NextPing      string
	NextPingAgo   string
	MessageCount  string
	CacheTTL      string
	CacheBlocks   string
	Stream        string
	HasMaxTokens  bool
	CountTokensWarn bool
	PingError     string
	Now           string
}

func renderStatusHTML() string {
	st := currentStatus()
	view := buildStatusView(st)
	var buf bytes.Buffer
	if err := statusTemplate.Execute(&buf, view); err != nil {
		return `<!doctype html><meta charset="utf-8"><title>缓存保活</title><p>页面渲染失败</p>`
	}
	return buf.String()
}

func buildStatusView(st statusPage) statusView {
	view := statusView{
		IntervalMin:  st.IntervalMin,
		MaxTokens:    st.MaxTokens,
		HasSnapshot:  st.HasSnapshot,
		Model:        dash(st.Model),
		ToFormat:     dash(st.ToFormat),
		BodySize:     formatBytes(st.BodyBytes),
		SavedAt:      formatTime(st.SavedAt),
		SavedAgo:     formatAgo(st.Now, st.SavedAt),
		LastPing:     formatTime(st.LastPingAt),
		LastPingAgo:  formatAgo(st.Now, st.LastPingAt),
		NextPing:     formatTime(st.NextPingAt),
		NextPingAgo:  formatUntil(st.Now, st.NextPingAt),
		MessageCount: dashInt(st.MessageCount),
		CacheTTL:     dash(st.CacheTTL),
		CacheBlocks:  dashInt(st.CacheControlCount),
		Stream:       boolLabel(st.HasSnapshot, st.Stream),
		HasMaxTokens: st.HasMaxTokens,
		PingError:    st.LastPingError,
		Now:          st.Now.UTC().Format("15:04:05 UTC"),
	}
	if st.HasSnapshot && !st.HasMaxTokens {
		view.CountTokensWarn = true
	}
	switch {
	case st.LastPingError != "":
		view.State = "error"
		view.StateLabel = "保活失败"
		view.StateHint = "上次重放出错。看下面的错误，修好前不要指望缓存 TTL 被刷新。"
	case !st.HasSnapshot:
		view.State = "empty"
		view.StateLabel = "等待快照"
		view.StateHint = "还没有记下 Claude 请求。用 Claude Code 走 CPA 正常聊一轮后，这里会出现快照。"
	case st.LastPingAt.IsZero():
		view.State = "armed"
		view.StateLabel = "已记录，等待保活"
		view.StateHint = fmt.Sprintf("每隔 %d 分钟重放最后一次请求，并把 max_tokens 卡成 %d。第一次保活按插件启动后的定时器计算。", st.IntervalMin, st.MaxTokens)
	default:
		view.State = "ok"
		view.StateLabel = "保活运行中"
		view.StateHint = fmt.Sprintf("上次重放成功。之后每隔 %d 分钟再打一次，输出上限 max_tokens=%d。", st.IntervalMin, st.MaxTokens)
	}
	return view
}

func dash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

func dashInt(v int) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", v)
}

func boolLabel(has bool, v bool) string {
	if !has {
		return "—"
	}
	if v {
		return "是"
	}
	return "否"
}

func formatBytes(n int) string {
	if n <= 0 {
		return "—"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.2f MB", float64(n)/(1024*1024))
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func formatAgo(now, then time.Time) string {
	if then.IsZero() {
		return ""
	}
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	return humanDuration(d) + "前"
}

func formatUntil(now, then time.Time) string {
	if then.IsZero() {
		return ""
	}
	d := then.Sub(now)
	if d <= 0 {
		return "即将"
	}
	return humanDuration(d) + "后"
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		sec := int(d.Seconds())
		if sec < 1 {
			sec = 1
		}
		return fmt.Sprintf("%d 秒", sec)
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%d 小时", h)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", h, m)
}

var statusTemplate = template.Must(template.New("status").Parse(statusHTML))

const statusHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="15">
<title>缓存保活</title>
<style>
:root {
  --bg: #0f1115;
  --panel: #171a21;
  --panel-2: #1e232d;
  --line: #2a3140;
  --text: #e7eaf0;
  --muted: #8b93a7;
  --ok: #3dd68c;
  --ok-bg: rgba(61,214,140,.12);
  --armed: #7aa2ff;
  --armed-bg: rgba(122,162,255,.12);
  --empty: #8b93a7;
  --empty-bg: rgba(139,147,167,.12);
  --err: #ff6b6b;
  --err-bg: rgba(255,107,107,.12);
  --warn: #f5c14a;
  --warn-bg: rgba(245,193,74,.12);
}
* { box-sizing: border-box; }
body {
  margin: 0;
  color: var(--text);
  background:
    radial-gradient(1200px 500px at 10% -10%, rgba(122,162,255,.16), transparent 55%),
    radial-gradient(900px 400px at 100% 0%, rgba(61,214,140,.08), transparent 50%),
    var(--bg);
  font: 15px/1.55 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
}
main { max-width: 980px; margin: 0 auto; padding: 28px 20px 48px; }
.top { display: flex; gap: 16px; justify-content: space-between; align-items: flex-start; flex-wrap: wrap; margin-bottom: 22px; }
h1 { margin: 0 0 6px; font-size: 24px; letter-spacing: -.02em; }
.lead { margin: 0; color: var(--muted); max-width: 42rem; }
.badge {
  display: inline-flex; align-items: center; gap: 8px;
  padding: 8px 12px; border-radius: 999px; font-weight: 600; font-size: 13px;
  border: 1px solid transparent;
}
.badge .dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; box-shadow: 0 0 0 4px color-mix(in srgb, currentColor 18%, transparent); }
.state-ok { color: var(--ok); background: var(--ok-bg); }
.state-armed { color: var(--armed); background: var(--armed-bg); }
.state-empty { color: var(--empty); background: var(--empty-bg); }
.state-error { color: var(--err); background: var(--err-bg); }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 760px) { .grid { grid-template-columns: 1fr; } main { padding: 20px 16px 36px; } }
.card {
  background: linear-gradient(180deg, var(--panel), var(--panel-2));
  border: 1px solid var(--line);
  border-radius: 16px;
  padding: 18px 18px 8px;
  min-height: 220px;
}
.card h2 { margin: 0 0 14px; font-size: 13px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .08em; }
.row { display: flex; justify-content: space-between; gap: 12px; padding: 10px 0; border-top: 1px solid var(--line); }
.row:first-of-type { border-top: 0; }
.k { color: var(--muted); }
.v { font-variant-numeric: tabular-nums; text-align: right; word-break: break-all; }
.v small { display: block; color: var(--muted); font-size: 12px; margin-top: 2px; }
.empty {
  margin-top: 14px;
  padding: 18px;
  border: 1px dashed var(--line);
  border-radius: 16px;
  color: var(--muted);
  background: rgba(255,255,255,.02);
}
.warn, .error {
  margin-top: 14px;
  padding: 12px 14px;
  border-radius: 12px;
}
.warn { color: var(--warn); background: var(--warn-bg); }
.error { color: var(--err); background: var(--err-bg); white-space: pre-wrap; }
.foot { margin-top: 18px; color: var(--muted); font-size: 12px; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; background: #11141a; padding: .12rem .35rem; border-radius: 6px; }
</style>
</head>
<body>
<main>
  <div class="top">
    <div>
      <h1>Claude 缓存保活</h1>
      <p class="lead">{{.StateHint}}</p>
    </div>
    <div class="badge state-{{.State}}"><span class="dot"></span>{{.StateLabel}}</div>
  </div>
  {{if not .HasSnapshot}}
  <div class="empty">把 Claude Code 指到 CPA 的 <code>127.0.0.1:8317</code>，并设置 <code>promptCacheTtl: "1h"</code>。正常完成一轮对话后刷新本页。</div>
  {{else}}
  <div class="grid">
    <section class="card">
      <h2>最后一次请求</h2>
      <div class="row"><span class="k">模型</span><span class="v">{{.Model}}</span></div>
      <div class="row"><span class="k">上游格式</span><span class="v">{{.ToFormat}}</span></div>
      <div class="row"><span class="k">请求体</span><span class="v">{{.BodySize}}</span></div>
      <div class="row"><span class="k">消息数</span><span class="v">{{.MessageCount}}</span></div>
      <div class="row"><span class="k">缓存 TTL</span><span class="v">{{.CacheTTL}}<small>{{if ne .CacheBlocks "—"}}{{.CacheBlocks}} 个 cache_control{{end}}</small></span></div>
      <div class="row"><span class="k">记录时间</span><span class="v">{{.SavedAt}}<small>{{.SavedAgo}}</small></span></div>
    </section>
    <section class="card">
      <h2>保活</h2>
      <div class="row"><span class="k">间隔</span><span class="v">{{.IntervalMin}} 分钟</span></div>
      <div class="row"><span class="k">输出上限</span><span class="v">max_tokens={{.MaxTokens}}</span></div>
      <div class="row"><span class="k">上次保活</span><span class="v">{{.LastPing}}<small>{{.LastPingAgo}}</small></span></div>
      <div class="row"><span class="k">下次保活</span><span class="v">{{.NextPing}}<small>{{.NextPingAgo}}</small></span></div>
      <div class="row"><span class="k">原请求 stream</span><span class="v">{{.Stream}}</span></div>
    </section>
  </div>
  {{if .CountTokensWarn}}
  <div class="warn">这份快照没有 <code>max_tokens</code>，很可能是 <code>/v1/messages/count_tokens</code> 而不是对话请求。保活重放它通常刷新不了对话的 prompt cache。</div>
  {{end}}
  {{end}}
  {{if .PingError}}
  <div class="error">上次保活错误：{{.PingError}}</div>
  {{end}}
  <p class="foot">页面每 15 秒自动刷新 · {{.Now}} · 不会显示 prompt 正文</p>
</main>
</body>
</html>`
