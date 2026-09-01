package main

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

type statusView struct {
	State        string
	StateLabel   string
	StateHint    string
	IntervalMin  int
	MaxTokens    int
	MaxSessions  int
	IdleEvictMin int
	SessionCount int
	EnabledCount int
	HasSessions  bool
	Sessions     []sessionRow
	LastPing     string
	LastPingAgo  string
	NextPing     string
	NextPingAgo  string
	PingError    string
	Now          string
}

type sessionRow struct {
	ID          string
	Label       string
	Model       string
	Enabled     bool
	ToggleHref  string
	ForgetHref  string
	SavedAt     string
	SavedAgo    string
	LastPing    string
	LastPingAgo string
	NextPing    string
	NextPingAgo string
	PingError   string
	CacheTTL    string
	BodySize    string
	Messages    string
	RowClass    string
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
		MaxSessions:  st.MaxSessions,
		IdleEvictMin: st.IdleEvictMin,
		SessionCount: len(st.Sessions),
		EnabledCount: st.EnabledCount,
		HasSessions:  len(st.Sessions) > 0,
		LastPing:     formatTime(st.LastPingAt),
		LastPingAgo:  formatAgo(st.Now, st.LastPingAt),
		NextPing:     formatTime(st.NextPingAt),
		NextPingAgo:  formatUntil(st.Now, st.NextPingAt),
		PingError:    st.LastPingError,
		Now:          st.Now.UTC().Format("15:04:05 UTC"),
	}
	interval := time.Duration(st.IntervalMin) * time.Minute
	view.Sessions = make([]sessionRow, 0, len(st.Sessions))
	for _, item := range st.Sessions {
		next := sessionNextPingAt(item.LastSeen, item.LastPingAt, st.Now, interval)
		row := sessionRow{
			ID:          item.ID,
			Label:       item.Label,
			Model:       dash(item.Model),
			Enabled:     item.Enabled,
			ToggleHref:  toggleHref(item.ID, !item.Enabled),
			ForgetHref:  "?forget=" + item.ID,
			SavedAt:     formatTime(item.LastSeen),
			SavedAgo:    formatAgo(st.Now, item.LastSeen),
			LastPing:    formatTime(item.LastPingAt),
			LastPingAgo: formatAgo(st.Now, item.LastPingAt),
			PingError:   item.LastPingError,
			CacheTTL:    dash(item.Info.CacheTTL),
			BodySize:    formatBytes(len(item.Body)),
			Messages:    dashInt(item.Info.MessageCount),
			RowClass:    "off",
		}
		if item.Enabled {
			row.NextPing = formatTime(next)
			row.NextPingAgo = formatUntil(st.Now, next)
		} else {
			row.NextPing = "—"
		}
		switch {
		case item.LastPingError != "":
			row.RowClass = "error"
		case item.Enabled:
			row.RowClass = "on"
		}
		view.Sessions = append(view.Sessions, row)
	}
	switch {
	case st.LastPingError != "" && st.EnabledCount > 0:
		view.State = "error"
		view.StateLabel = "保活失败"
		view.StateHint = "上一轮重放有会话出错。可先取消勾选出问题的对话，或看每条下面的错误。"
	case len(st.Sessions) == 0:
		view.State = "empty"
		view.StateLabel = "等待对话"
		view.StateHint = "还没有记下 Claude 对话。用 Claude Code 走 CPA 正常聊一轮后，这里会出现会话；新对话默认勾选保活。"
	case st.EnabledCount == 0:
		view.State = "empty"
		view.StateLabel = "已全部暂停"
		view.StateHint = "会话还在，但没有勾选保活。点左侧方块即可重新打开；取消勾选的对话超过空闲时间会被丢掉。"
	case st.LastPingAt.IsZero():
		view.State = "armed"
		view.StateLabel = "已记录，等待保活"
		view.StateHint = fmt.Sprintf("当前 %d 路勾选保活。每路从最后一次对话请求起算，每隔 %d 分钟重放一次，并把 max_tokens 卡成 %d。新消息会把该路倒计时重置。", st.EnabledCount, st.IntervalMin, st.MaxTokens)
	default:
		view.State = "ok"
		view.StateLabel = "保活运行中"
		view.StateHint = fmt.Sprintf("已勾选 %d 路。下次保活按各会话最后一次请求对齐，间隔 %d 分钟，输出上限 max_tokens=%d。", st.EnabledCount, st.IntervalMin, st.MaxTokens)
	}
	return view
}

func toggleHref(id string, enable bool) string {
	on := "0"
	if enable {
		on = "1"
	}
	return "?toggle=" + id + "&on=" + on
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
}
* { box-sizing: border-box; }
html, body {
  margin: 0;
  background: var(--bg);
  color-scheme: dark;
}
body {
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
.lead { margin: 0; color: var(--muted); max-width: 46rem; }
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
.stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 16px; }
@media (max-width: 760px) {
  .stats { grid-template-columns: 1fr 1fr; }
  main { padding: 20px 16px 36px; }
}
.stat {
  background: linear-gradient(180deg, var(--panel), var(--panel-2));
  border: 1px solid var(--line);
  border-radius: 14px;
  padding: 12px 14px;
}
.stat .k { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .06em; }
.stat .v { margin-top: 4px; font-variant-numeric: tabular-nums; font-weight: 600; }
.stat .v small { display: block; color: var(--muted); font-weight: 400; font-size: 12px; margin-top: 2px; }
.empty {
  margin-top: 6px;
  padding: 18px;
  border: 1px dashed var(--line);
  border-radius: 16px;
  color: var(--muted);
  background: rgba(255,255,255,.02);
}
.list { display: flex; flex-direction: column; gap: 10px; }
.session {
  display: grid;
  grid-template-columns: auto 1fr auto;
  gap: 12px;
  align-items: start;
  background: linear-gradient(180deg, var(--panel), var(--panel-2));
  border: 1px solid var(--line);
  border-radius: 16px;
  padding: 14px 14px 12px;
}
.session.on { box-shadow: inset 3px 0 0 var(--ok); }
.session.off { opacity: .78; }
.session.error { box-shadow: inset 3px 0 0 var(--err); }
.cb {
  width: 28px; height: 28px; margin-top: 2px;
  border-radius: 8px; border: 1px solid var(--line);
  background: #11141a; color: var(--muted);
  display: grid; place-items: center;
  text-decoration: none;
}
.cb.on { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 55%, var(--line)); background: var(--ok-bg); }
.cb .mark { font-size: 16px; line-height: 1; }
h3 { margin: 0 0 4px; font-size: 15px; font-weight: 650; letter-spacing: -.01em; word-break: break-word; }
.meta { color: var(--muted); font-size: 13px; }
.meta span { white-space: nowrap; }
.times { margin-top: 8px; display: flex; flex-wrap: wrap; gap: 10px 16px; font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; }
.forget {
  color: var(--muted); font-size: 12px; text-decoration: none;
  padding: 6px 8px; border-radius: 8px; border: 1px solid transparent;
}
.forget:hover { color: var(--err); border-color: var(--line); }
.error {
  margin-top: 14px;
  padding: 12px 14px;
  border-radius: 12px;
  color: var(--err); background: var(--err-bg); white-space: pre-wrap;
}
.row-err { margin-top: 8px; color: var(--err); font-size: 12px; }
.hint {
  margin-top: 16px;
  color: var(--muted);
  font-size: 13px;
}
.foot { margin-top: 18px; color: var(--muted); font-size: 12px; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; background: #11141a; padding: .12rem .35rem; border-radius: 6px; }
</style>
</head>
<body>
<main id="view">
  <div class="top">
    <div>
      <h1>Claude 缓存保活</h1>
      <p class="lead">{{.StateHint}}</p>
    </div>
    <div class="badge state-{{.State}}"><span class="dot"></span>{{.StateLabel}}</div>
  </div>
  <div class="stats">
    <div class="stat"><div class="k">会话</div><div class="v">{{.SessionCount}} / {{.MaxSessions}}<small>{{.EnabledCount}} 路保活中</small></div></div>
    <div class="stat"><div class="k">间隔</div><div class="v">{{.IntervalMin}} 分钟<small>输出上限 {{.MaxTokens}}</small></div></div>
    <div class="stat"><div class="k">上次保活</div><div class="v">{{.LastPing}}<small>{{.LastPingAgo}}</small></div></div>
    <div class="stat"><div class="k">最近一次到期</div><div class="v">{{.NextPing}}<small>{{.NextPingAgo}}</small></div></div>
  </div>
  {{if not .HasSessions}}
  <div class="empty">把 Claude Code 指到 CPA 的 <code>127.0.0.1:8317</code>，并设置 <code>promptCacheTtl: "1h"</code>。正常完成一轮对话后刷新本页。插件会跳过 <code>count_tokens</code>。</div>
  {{else}}
  <div class="list">
    {{range .Sessions}}
    <article class="session {{.RowClass}}">
      <a class="cb {{if .Enabled}}on{{end}}" data-action href="{{.ToggleHref}}" title="{{if .Enabled}}取消保活{{else}}开启保活{{end}}"><span class="mark">{{if .Enabled}}✓{{else}}○{{end}}</span></a>
      <div>
        <h3>{{.Label}}</h3>
        <div class="meta">{{.Model}} · TTL {{.CacheTTL}} · {{.Messages}} 条消息 · {{.BodySize}}</div>
        <div class="times">
          <span>最近请求 {{.SavedAt}} {{.SavedAgo}}</span>
          <span>下次保活 {{.NextPing}} {{.NextPingAgo}}</span>
          <span>上次保活 {{.LastPing}} {{.LastPingAgo}}</span>
        </div>
        {{if .PingError}}<div class="row-err">{{.PingError}}</div>{{end}}
      </div>
      <a class="forget" data-action href="{{.ForgetHref}}">忘记</a>
    </article>
    {{end}}
  </div>
  <p class="hint">点左侧方块勾选或取消保活。下次保活从该路最后一次对话请求起算，不跟全局保活时钟走。新消息会重置倒计时。取消后仍会跟着新消息更新快照。已勾选的不会因空闲被踢掉；未勾选的超过 {{.IdleEvictMin}} 分钟没新请求会被丢掉。</p>
  {{end}}
  {{if .PingError}}
  <div class="error">上一轮保活错误：{{.PingError}}</div>
  {{end}}
  <p class="foot">数据每 15 秒静默更新 · {{.Now}} · 不会显示 prompt 正文</p>
</main>
<script>
(function () {
  var timer = null;
  function apply(html, url) {
    var doc = new DOMParser().parseFromString(html, "text/html");
    var next = doc.getElementById("view");
    var cur = document.getElementById("view");
    if (!next || !cur) {
      return;
    }
    cur.replaceWith(next);
    if (url && url.indexOf("?") !== -1 && window.history && history.replaceState) {
      history.replaceState(null, "", url.split("?")[0]);
    }
  }
  function load(url) {
    return fetch(url, { cache: "no-store", headers: { Accept: "text/html" }, redirect: "follow" }).then(function (res) {
      if (!res.ok) {
        throw new Error("HTTP " + res.status);
      }
      return res.text().then(function (html) {
        apply(html, res.url || url);
      });
    });
  }
  document.addEventListener("click", function (ev) {
    var a = ev.target.closest("a[data-action]");
    if (!a) {
      return;
    }
    ev.preventDefault();
    load(a.href).catch(function () {
      location.assign(a.href);
    });
  });
  timer = setInterval(function () {
    if (document.hidden) {
      return;
    }
    load(location.pathname).catch(function () {});
  }, 15000);
  window.addEventListener("pagehide", function () {
    if (timer) {
      clearInterval(timer);
    }
  });
})();
</script>
</body>
</html>`
