package main

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

type statusView struct {
	State          string
	StateLabel     string
	StateHint      string
	IntervalMin    int
	MaxTokens      int
	MaxSessions    int
	IdleEvictMin   int
	SessionCount   int
	EnabledCount   int
	HasSessions    bool
	Sessions       []sessionRow
	LastPing       string
	LastPingAgo    string
	NextPing       string
	NextPingAgo    string
	PingError      string
	Now            string
	Version        string
	BudgetWindow   string
	BudgetChat     string
	BudgetKeep     string
	BudgetCap      string
	BudgetRemain   string
	BudgetUsed     string
	BudgetBlock    string
	BudgetNote     string
	ReservePercent int
	ChatBlocked    bool
	KeepPaused     bool
	HasQuotaLog    bool
	QuotaLog       []quotaRow
	SubagentNote   string
}

type quotaRow struct {
	At       string
	Ago      string
	Kind     string
	Model    string
	HeaderOK bool
	Util5h   string
	Status5h string
	Reset5h  string
	Util7d   string
	Status7d string
	Unified  string
	Raw      string
	RowClass string
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
		IntervalMin:    st.IntervalMin,
		MaxTokens:      st.MaxTokens,
		MaxSessions:    st.MaxSessions,
		IdleEvictMin:   st.IdleEvictMin,
		SessionCount:   len(st.Sessions),
		EnabledCount:   st.EnabledCount,
		HasSessions:    len(st.Sessions) > 0,
		LastPing:       formatTime(st.LastPingAt),
		LastPingAgo:    formatAgo(st.Now, st.LastPingAt),
		NextPing:       formatTime(st.NextPingAt),
		NextPingAgo:    formatUntil(st.Now, st.NextPingAt),
		PingError:      st.LastPingError,
		Now:            st.Now.UTC().Format("15:04:05 UTC"),
		BudgetWindow:   fmt.Sprintf("%d 小时", (st.Budget.WindowMin+59)/60),
		BudgetChat:     formatUnits(st.Budget.ChatUnits),
		BudgetKeep:     formatUnits(st.Budget.KeepaliveUnits),
		BudgetCap:      formatUnits(st.Budget.KeepaliveCap),
		BudgetRemain:   formatUnits(st.Budget.KeepaliveRemain),
		BudgetUsed:     formatPercent(st.Budget.UsedPercent, st.Budget.UsedKnown),
		BudgetBlock:    fmt.Sprintf("%d%%", st.Budget.BlockAtPercent),
		ReservePercent: st.Budget.ReservePercent,
		ChatBlocked:    st.Budget.ChatBlocked,
		KeepPaused:     st.Budget.KeepalivePaused,
		Version:        st.Version,
		HasQuotaLog:    len(st.QuotaLog) > 0,
	}
	if st.SubagentSkipped > 0 {
		view.SubagentNote = fmt.Sprintf("已跳过 %d 次子代理请求，不进保活名单。最近：%s %s（%s）", st.SubagentSkipped, dash(st.LastSubagentKind), st.LastSubagentLabel, formatAgo(st.Now, st.LastSubagentAt))
	}
	view.QuotaLog = make([]quotaRow, 0, len(st.QuotaLog))
	for _, sample := range st.QuotaLog {
		view.QuotaLog = append(view.QuotaLog, buildQuotaRow(st.Now, sample))
	}
	switch {
	case st.Budget.ChatBlocked:
		if st.Budget.ReservePercent <= 0 {
			view.BudgetNote = "CPA 对话已停：5 小时额度用尽或这一发会打穿。"
		} else {
			view.BudgetNote = fmt.Sprintf("CPA 对话已在 %d%% 停住，最后 %d%% 留给保活。", st.Budget.BlockAtPercent, st.Budget.ReservePercent)
		}
	case st.Budget.KeepalivePaused:
		view.BudgetNote = st.Budget.PauseReason
	case st.Budget.UsedKnown:
		if st.Budget.ReservePercent <= 0 {
			view.BudgetNote = fmt.Sprintf("上游 5 小时已用 %.0f%%。对话用到 100%%（或这一发会打穿）才拦，不为保活另留百分比。", st.Budget.UsedPercent)
		} else {
			view.BudgetNote = fmt.Sprintf("上游 5 小时已用 %.0f%%。CPA 对话会在 %d%% 停住，把最后 %d%% 留给保活。", st.Budget.UsedPercent, st.Budget.BlockAtPercent, st.Budget.ReservePercent)
		}
	case st.Budget.Budget > 0:
		if st.Budget.ReservePercent <= 0 {
			view.BudgetNote = "还没读到上游 5h 用量头，先按加权预算拦会打穿窗口的对话。"
		} else {
			view.BudgetNote = fmt.Sprintf("还没读到上游 5h 用量头，先按加权预算拦对话，预留 %d%% 给保活。", st.Budget.ReservePercent)
		}
	default:
		if st.Budget.ReservePercent <= 0 {
			view.BudgetNote = "等下一轮 CPA 请求带上 5h 用量头后，对话用到 100%（或这一发会打穿）才拦。"
		} else {
			view.BudgetNote = fmt.Sprintf("等下一轮 CPA 请求带上 5h 用量头后，会在 %d%% 拦住对话，把最后 %d%% 留给保活。", st.Budget.BlockAtPercent, st.Budget.ReservePercent)
		}
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
	case st.Budget.ChatBlocked:
		view.State = "armed"
		view.StateLabel = "对话已限流"
		view.StateHint = "5 小时额度快用完。拦截发生在 CPA 本地，请求不会打到 Anthropic。最后一截留给缓存保活，窗口回落后会自动放开。"
	case st.Budget.KeepalivePaused && st.EnabledCount > 0:
		view.State = "armed"
		view.StateLabel = "保活暂停"
		view.StateHint = st.Budget.PauseReason
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

func buildQuotaRow(now time.Time, sample quotaSample) quotaRow {
	row := quotaRow{
		At:       formatTime(sample.At),
		Ago:      formatAgo(now, sample.At),
		Model:    dash(sample.Model),
		HeaderOK: sample.HeaderOK,
		Util5h:   "未读到",
		Status5h: "—",
		Reset5h:  dash(formatTime(sample.Reset5h)),
		Util7d:   "—",
		Status7d: dash(sample.Status7d),
		Unified:  dash(sample.UnifiedStatus),
		RowClass: "miss",
	}
	switch {
	case sample.Failed:
		row.Kind = "失败"
		row.RowClass = "fail"
	case sample.Keepalive:
		row.Kind = "保活"
	default:
		row.Kind = "对话"
	}
	if sample.HeaderOK {
		row.Util5h = fmt.Sprintf("%.1f%%", sample.Util5h*100)
		row.Status5h = dash(sample.Status5h)
		row.RowClass = "ok"
		if sample.Failed || sample.Status5h == "rejected" {
			row.RowClass = "fail"
		}
	}
	if sample.Header7dOK {
		row.Util7d = fmt.Sprintf("%.1f%%", sample.Util7d*100)
	}
	var raw []string
	if sample.RawUtil5h != "" {
		raw = append(raw, "5h-utilization="+sample.RawUtil5h)
	}
	if sample.RawStatus5h != "" {
		raw = append(raw, "5h-status="+sample.RawStatus5h)
	}
	if sample.RawReset5h != "" {
		raw = append(raw, "5h-reset="+sample.RawReset5h)
	}
	if sample.RawUtil7d != "" {
		raw = append(raw, "7d-utilization="+sample.RawUtil7d)
	}
	if sample.RawStatus7d != "" {
		raw = append(raw, "7d-status="+sample.RawStatus7d)
	}
	if sample.UnifiedStatus != "" {
		raw = append(raw, "unified="+sample.UnifiedStatus)
	}
	if len(raw) == 0 {
		row.Raw = "响应头里没有 Anthropic-Ratelimit-Unified-5h-*"
	} else {
		row.Raw = strings.Join(raw, " · ")
	}
	return row
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

func formatPercent(v float64, known bool) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", v)
}

func formatUnits(n int64) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1000000)
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
.rename { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin: 0 0 4px; }
.rename input {
  flex: 1; min-width: 10rem; max-width: 22rem;
  background: #11141a; color: var(--text);
  border: 1px solid var(--line); border-radius: 8px;
  padding: 6px 8px; font: inherit; font-weight: 650; font-size: 15px;
}
.rename input:focus { outline: none; border-color: color-mix(in srgb, var(--armed) 55%, var(--line)); }
.rename button {
  color: var(--armed); background: var(--armed-bg);
  border: 1px solid var(--line); border-radius: 8px;
  padding: 6px 10px; font: inherit; font-size: 12px; cursor: pointer;
}
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
.warn {
  margin-top: 14px;
  padding: 12px 14px;
  border-radius: 12px;
  color: var(--warn); background: rgba(245,193,74,.12);
}
.row-err { margin-top: 8px; color: var(--err); font-size: 12px; }
.hint {
  margin-top: 16px;
  color: var(--muted);
  font-size: 13px;
}
.quota {
  margin: 18px 0 8px;
  background: linear-gradient(180deg, var(--panel), var(--panel-2));
  border: 1px solid var(--line);
  border-radius: 16px;
  padding: 14px 14px 8px;
}
.quota h2 { margin: 0 0 4px; font-size: 15px; font-weight: 650; }
.quota .sub { margin: 0 0 12px; color: var(--muted); font-size: 13px; }
.quota-table { width: 100%; border-collapse: collapse; font-size: 13px; font-variant-numeric: tabular-nums; }
.quota-table th {
  text-align: left; color: var(--muted); font-weight: 500;
  font-size: 11px; text-transform: uppercase; letter-spacing: .06em;
  padding: 6px 8px; border-bottom: 1px solid var(--line);
}
.quota-table td { padding: 8px; border-bottom: 1px solid rgba(42,49,64,.65); vertical-align: top; }
.quota-table tr:last-child td { border-bottom: 0; }
.quota-table .raw { color: var(--muted); font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; word-break: break-all; }
.quota-table tr.ok td.util { color: var(--ok); font-weight: 650; }
.quota-table tr.miss td.util { color: var(--warn); }
.quota-table tr.fail td.util { color: var(--err); font-weight: 650; }
.scroll { overflow-x: auto; }
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
  <div class="stats">
    <div class="stat"><div class="k">5 小时已用</div><div class="v">{{.BudgetUsed}}<small>{{if eq .ReservePercent 0}}对话用到 100% 才停{{else}}CPA 对话停在 {{.BudgetBlock}}{{end}}</small></div></div>
    <div class="stat"><div class="k">CPA 对话 / 保活</div><div class="v">{{.BudgetChat}} / {{.BudgetKeep}}<small>{{.BudgetNote}}</small></div></div>
  </div>
  {{if .ChatBlocked}}
  {{if eq .ReservePercent 0}}
  <div class="warn">CPA 上的新对话已拦住：5 小时额度用尽，或这一发会打穿窗口。窗口回落后会自动恢复。</div>
  {{else}}
  <div class="warn">CPA 上的新对话请求已被拦住，把 5 小时额度留给保活。窗口回落后会自动恢复。</div>
  {{end}}
  {{end}}
  {{if and .KeepPaused (not .ChatBlocked)}}
  <div class="warn">{{.BudgetNote}}</div>
  {{end}}
  {{if not .HasSessions}}
  <div class="empty">把 Claude Code 指到 CPA 的 <code>127.0.0.1:8317</code>，并设置 <code>promptCacheTtl: "1h"</code>。正常完成一轮对话后刷新本页。插件会跳过 <code>count_tokens</code>。</div>
  {{else}}
  <div class="list">
    {{range .Sessions}}
    <article class="session {{.RowClass}}">
      <a class="cb {{if .Enabled}}on{{end}}" data-action href="{{.ToggleHref}}" title="{{if .Enabled}}取消保活{{else}}开启保活{{end}}"><span class="mark">{{if .Enabled}}✓{{else}}○{{end}}</span></a>
      <div>
        <form class="rename" data-rename action="" method="get">
          <input type="hidden" name="rename" value="{{.ID}}">
          <input name="name" value="{{.Label}}" maxlength="42" aria-label="会话名称" spellcheck="false">
          <button type="submit">改名</button>
        </form>
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
  <p class="hint">点左侧方块勾选或取消保活。会话名称可以自己改，留空再保存会回到首条消息。{{if eq .ReservePercent 0}}CPA 对话用到 100%（或这一发预估会打穿）才停，不为保活另留百分比。{{else}}CPA 对话会在 5 小时窗口用到 {{.BudgetBlock}} 时停住，把最后一截留给保活刷新 cache。{{end}} Claude Code 的 Task/Agent 子代理会单独成一路请求，插件认出来后不保活。</p>
  {{if .SubagentNote}}
  <p class="hint">{{.SubagentNote}}</p>
  {{end}}
  {{end}}
  {{if .PingError}}
  <div class="error">上一轮保活错误：{{.PingError}}</div>
  {{end}}
  {{if and .SubagentNote (not .HasSessions)}}
  <p class="hint">{{.SubagentNote}}</p>
  {{end}}
  <section class="quota">
    <h2>额度记录</h2>
    <p class="sub">每次 Claude 响应回来后，插件从 HTTP 头解析的结果。用来确认有没有真正读到 <code>Anthropic-Ratelimit-Unified-5h-*</code>。最新在上，最多 5 条；CPA 重启后清空。</p>
    {{if .HasQuotaLog}}
    <div class="scroll">
      <table class="quota-table">
        <thead>
          <tr><th>时间</th><th>来源</th><th>5 小时</th><th>状态</th><th>7 天</th><th>原始头</th></tr>
        </thead>
        <tbody>
          {{range .QuotaLog}}
          <tr class="{{.RowClass}}">
            <td>{{.At}}<div class="raw">{{.Ago}} · {{.Kind}} · {{.Model}}</div></td>
            <td>{{.Kind}}</td>
            <td class="util">{{.Util5h}}</td>
            <td>{{.Status5h}}</td>
            <td>{{.Util7d}}{{if ne .Status7d "—"}}<div class="raw">{{.Status7d}}</div>{{end}}</td>
            <td class="raw">{{.Raw}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
    </div>
    {{else}}
    <p class="sub">还没有用量回调。用 Claude Code 走 CPA 正常聊一轮后，这里应出现 5h-utilization / 5h-status。如果只有「未读到」，说明这次响应没带用量头。</p>
    {{end}}
  </section>
  <p class="foot">数据每 15 秒静默更新 · {{.Now}} · {{.Version}} · 不会显示 prompt 正文</p>
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
  document.addEventListener("submit", function (ev) {
    var form = ev.target.closest("form[data-rename]");
    if (!form) {
      return;
    }
    ev.preventDefault();
    var action = form.getAttribute("action") || location.pathname;
    var url = action.split("?")[0] + "?" + new URLSearchParams(new FormData(form)).toString();
    load(url).catch(function () {
      form.submit();
    });
  });
  timer = setInterval(function () {
    if (document.hidden) {
      return;
    }
    if (document.activeElement && document.activeElement.closest("form[data-rename]")) {
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
