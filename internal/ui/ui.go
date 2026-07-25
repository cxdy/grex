// Package ui serves the grex web UI: fleet overview, agent detail, and server status.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dennisme/grex/internal/api"
	"github.com/dennisme/grex/internal/fleet"
)

//go:embed templates/*.html static/*
var assets embed.FS

const (
	defaultLimit = 100
	maxLimit     = 1000
)

// Config holds UI presentation settings.
type Config struct {
	// PollInterval is how often htmx partials refresh. Default 5s.
	PollInterval time.Duration
}

// Handler serves HTML pages and static assets for the UI listener.
type Handler struct {
	registry *fleet.Registry
	cfg      Config
	tmpl     *template.Template
	static   http.Handler
	started  time.Time
}

// New builds a UI Handler. startedAt is shown on the status page.
func New(registry *fleet.Registry, cfg Config, startedAt time.Time) (*Handler, error) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	funcMap := template.FuncMap{
		"shortUID":    shortUID,
		"relTime":     relTime,
		"statusLabel":  statusLabel,
		"statusClass":  statusClass,
		"statusTitle":  statusTitle,
		"yamlDisplay":  yamlDisplay,
		"viaLabel":     viaLabel,
		"queryWith":   queryWith,
		"pollQuery":   pollQuery,
		"attrPairs":   attrPairs,
		"attrChips":   attrChips,
		"hasFilter":   hasFilter,
		"formatTime":  formatTime,
		"dict":        dict,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"max": func(a, b int) int {
			if a > b {
				return a
			}
			return b
		},
		"pageCount":  pageCount,
		"pageNum":    pageNum,
		"pageOffset": pageOffset,
		"pageList":   pageList,
		"sortHref":   sortHref,
		"sortClass":  sortClass,
		"sortAria":   sortAria,
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("static assets: %w", err)
	}
	return &Handler{
		registry: registry,
		cfg:      cfg,
		tmpl:     tmpl,
		static:   http.FileServer(http.FS(staticFS)),
		started:  startedAt,
	}, nil
}

// Mount registers UI routes on mux. Call after API routes so /api/* is not
// captured by the page handlers.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.StripPrefix("/static/", h.static))
	mux.HandleFunc("GET /{$}", h.fleetPage)
	mux.HandleFunc("GET /partials/agents", h.fleetPartial)
	mux.HandleFunc("GET /agents/{id}", h.agentPage)
	mux.HandleFunc("GET /partials/agents/{id}", h.agentPartial)
	mux.HandleFunc("GET /status", h.statusPage)
	mux.HandleFunc("GET /partials/status", h.statusPartial)
}

type pageData struct {
	Title        string
	Nav          string
	PollInterval string
	Query        url.Values
	// Fleet
	Agents []fleet.AgentView
	Total  int
	Limit  int
	Offset int
	// Sort (fleet table)
	Sort  string // column key: status, name, role, version, via, transport, last_seen, instance
	Order string // asc | desc
	// Filters (echo)
	Healthy    string
	Connected  string
	ViaGateway string
	Matches    []string // Prometheus-style attribute matchers
	// Detail
	Agent *fleet.AgentView
	// Status
	Status *statusView
}

type statusView struct {
	Version       string
	Commit        string
	StartedAt     time.Time
	Uptime        string
	Total         int
	Connected     int
	Disconnected  int
	Healthy       int
	Unhealthy     int
	HealthUnknown int
	Awaiting      int
}

func (h *Handler) fleetPage(w http.ResponseWriter, r *http.Request) {
	data, err := h.fleetData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data.Title = "Fleet"
	data.Nav = "fleet"
	h.render(w, "fleet.html", data)
}

func (h *Handler) fleetPartial(w http.ResponseWriter, r *http.Request) {
	data, err := h.fleetData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.render(w, "partials/agents.html", data)
}

func (h *Handler) fleetData(r *http.Request) (pageData, error) {
	q := r.URL.Query()
	limit, offset, err := parsePagination(q)
	if err != nil {
		return pageData{}, err
	}
	filters, err := api.ParseFilters(q)
	if err != nil {
		return pageData{}, err
	}

	matched := api.MatchingAgents(h.registry.List(), filters)
	sortKey, order := parseSort(q)
	sortAgents(matched, sortKey, order)

	total := len(matched)
	start := min(offset, total)
	end := min(start+limit, total)
	page := matched[start:end]
	views := make([]fleet.AgentView, len(page))
	for i, a := range page {
		views[i] = fleet.SummaryView(a)
	}

	return pageData{
		PollInterval: formatPoll(h.cfg.PollInterval),
		Query:        q,
		Agents:       views,
		Total:        total,
		Limit:        limit,
		Offset:       offset,
		Sort:         sortKey,
		Order:        order,
		Healthy:      q.Get("healthy"),
		Connected:    q.Get("connected"),
		ViaGateway:   q.Get("via_gateway"),
		Matches:      collectMatches(q),
	}, nil
}

func (h *Handler) agentPage(w http.ResponseWriter, r *http.Request) {
	data, ok := h.agentData(r)
	if !ok {
		h.renderNotFound(w)
		return
	}
	data.Title = data.Agent.DisplayName
	data.Nav = "fleet"
	h.render(w, "agent.html", data)
}

func (h *Handler) agentPartial(w http.ResponseWriter, r *http.Request) {
	data, ok := h.agentData(r)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.render(w, "partials/agent.html", data)
}

func (h *Handler) agentData(r *http.Request) (pageData, bool) {
	id := r.PathValue("id")
	agent, ok := h.registry.Get(id)
	if !ok {
		return pageData{}, false
	}
	view := fleet.DetailView(agent)
	return pageData{
		PollInterval: formatPoll(h.cfg.PollInterval),
		Agent:        &view,
	}, true
}

func (h *Handler) statusPage(w http.ResponseWriter, r *http.Request) {
	data := h.statusData()
	data.Title = "Server status"
	data.Nav = "status"
	h.render(w, "status.html", data)
}

func (h *Handler) statusPartial(w http.ResponseWriter, r *http.Request) {
	h.render(w, "partials/status.html", h.statusData())
}

func (h *Handler) statusData() pageData {
	agents := h.registry.List()
	version, commit := buildVersion()
	sv := &statusView{
		Version:   version,
		Commit:    commit,
		StartedAt: h.started.UTC(),
		Uptime:    formatUptime(time.Since(h.started)),
		Total:     len(agents),
	}

	for _, a := range agents {
		if a.Connected {
			sv.Connected++
		} else {
			sv.Disconnected++
		}
		if !a.DescriptionReported {
			sv.Awaiting++
		}
		switch {
		case !a.HealthReported:
			sv.HealthUnknown++
		case a.Healthy:
			sv.Healthy++
		default:
			sv.Unhealthy++
		}
	}
	return pageData{
		PollInterval: formatPoll(h.cfg.PollInterval),
		Status:       sv,
	}
}

func (h *Handler) renderNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_ = h.tmpl.ExecuteTemplate(w, "notfound.html", pageData{
		Title:        "Not found",
		PollInterval: formatPoll(h.cfg.PollInterval),
	})
}

func (h *Handler) render(w http.ResponseWriter, name string, data pageData) {
	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func cloneQuery(q url.Values) url.Values {
	cp := make(url.Values, len(q))
	for k, vs := range q {
		cp[k] = append([]string(nil), vs...)
	}
	return cp
}

func parsePagination(q url.Values) (limit, offset int, err error) {
	limit = defaultLimit
	if v := q.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
		limit = min(n, maxLimit)
	}
	if v := q.Get("offset"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

// collectMatches returns display strings for active attribute matchers.
func collectMatches(q url.Values) []string {
	var out []string
	for _, m := range q["match"] {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	if key := strings.TrimSpace(q.Get("attr_key")); key != "" {
		out = append(out, key+"="+q.Get("attr_value"))
	}
	// Bare attribute query params (API-style ?service.name=foo)
	reserved := map[string]bool{
		"limit": true, "offset": true, "sort": true, "order": true,
		"healthy": true, "connected": true, "via_gateway": true,
		"match": true, "attr_key": true, "attr_value": true,
	}
	for k, vs := range q {
		if reserved[k] || len(vs) == 0 || strings.TrimSpace(vs[0]) == "" {
			continue
		}
		out = append(out, k+"="+vs[0])
	}
	return out
}

// Allowed fleet table sort columns (attributes is intentionally excluded).
var sortColumns = map[string]bool{
	"status": true, "name": true, "role": true, "version": true,
	"via": true, "transport": true, "last_seen": true, "instance": true,
}

func parseSort(q url.Values) (sortKey, order string) {
	sortKey = strings.TrimSpace(q.Get("sort"))
	if !sortColumns[sortKey] {
		sortKey = "instance"
	}
	order = strings.ToLower(strings.TrimSpace(q.Get("order")))
	if order != "asc" && order != "desc" {
		// Last seen defaults to newest-first when first selected via UI;
		// URL without order still uses asc for stable instance default.
		if sortKey == "last_seen" && q.Get("order") == "" && q.Get("sort") != "" {
			order = "desc"
		} else {
			order = "asc"
		}
	}
	// Default column instance: always asc unless order specified.
	if q.Get("sort") == "" {
		sortKey = "instance"
		order = "asc"
	}
	return sortKey, order
}

func sortAgents(agents []fleet.Agent, sortKey, order string) {
	desc := order == "desc"
	cmp := func(a, b fleet.Agent) int {
		var c int
		switch sortKey {
		case "status":
			c = strings.Compare(statusRank(a), statusRank(b))
		case "name":
			c = strings.Compare(
				strings.ToLower(fleet.DisplayNameOf(a)),
				strings.ToLower(fleet.DisplayNameOf(b)),
			)
		case "role":
			c = strings.Compare(
				strings.ToLower(fleet.RoleOf(a)),
				strings.ToLower(fleet.RoleOf(b)),
			)
		case "version":
			c = strings.Compare(fleet.Attr(a, "service.version"), fleet.Attr(b, "service.version"))
		case "via":
			// direct before gateway in asc
			ai, bi := 0, 0
			if a.Conn.ViaGateway {
				ai = 1
			}
			if b.Conn.ViaGateway {
				bi = 1
			}
			c = ai - bi
		case "transport":
			c = strings.Compare(a.Conn.Transport, b.Conn.Transport)
		case "last_seen":
			if a.LastSeen.Before(b.LastSeen) {
				c = -1
			} else if a.LastSeen.After(b.LastSeen) {
				c = 1
			}
		default: // instance
			c = strings.Compare(a.InstanceUID, b.InstanceUID)
		}
		if c == 0 && sortKey != "instance" {
			// Stable tie-break
			c = strings.Compare(a.InstanceUID, b.InstanceUID)
		}
		if desc {
			return -c
		}
		return c
	}
	slices.SortFunc(agents, cmp)
}

// statusRank orders connected healthy first, then unhealthy, unknown, then disconnected.
func statusRank(a fleet.Agent) string {
	if !a.Connected {
		return "3-disconnected"
	}
	if !a.HealthReported {
		return "2-unknown"
	}
	if a.Healthy {
		return "0-healthy"
	}
	return "1-unhealthy"
}

// sortHref builds a fleet URL that sorts by col, toggling order when already active.
// Resets offset to 0 so page 1 is shown after a sort change.
func sortHref(q url.Values, col, curSort, curOrder string) string {
	cp := cloneQuery(q)
	cp.Del("offset")
	nextOrder := "asc"
	if col == "last_seen" {
		nextOrder = "desc"
	}
	if curSort == col {
		if curOrder == "asc" {
			nextOrder = "desc"
		} else {
			nextOrder = "asc"
		}
	}
	cp.Set("sort", col)
	cp.Set("order", nextOrder)
	enc := cp.Encode()
	if enc == "" {
		return "/"
	}
	return "/?" + enc
}

func sortClass(col, curSort, curOrder string) string {
	if col != curSort {
		return "th-sort"
	}
	return "th-sort is-active order-" + curOrder
}

func sortAria(col, curSort, curOrder string) string {
	if col != curSort {
		return "none"
	}
	if curOrder == "desc" {
		return "descending"
	}
	return "ascending"
}

func formatPoll(d time.Duration) string {
	// htmx every N s — prefer whole seconds
	sec := int(d.Round(time.Second) / time.Second)
	if sec < 1 {
		sec = 5
	}
	return fmt.Sprintf("%ds", sec)
}

func shortUID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…"
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func statusLabel(a fleet.AgentView) string {
	if !a.Connected {
		return "Disconnected"
	}
	if !a.HealthReported {
		return "Unknown"
	}
	if a.Healthy {
		return "Healthy"
	}
	return "Unhealthy"
}

func statusClass(a fleet.AgentView) string {
	if !a.Connected {
		return "badge-disconnected"
	}
	if !a.HealthReported {
		return "badge-unknown"
	}
	if a.Healthy {
		return "badge-healthy"
	}
	return "badge-unhealthy"
}

// statusTitle explains the badge, especially when disconnected agents still
// carry a last-known health report (not the same as Unhealthy).
func statusTitle(a fleet.AgentView) string {
	if !a.Connected {
		if !a.HealthReported {
			return "No recent check-in; no health report received"
		}
		if a.Healthy {
			return "No recent check-in; last health report was healthy"
		}
		return "No recent check-in; last health report was unhealthy"
	}
	if !a.HealthReported {
		return "Connected; waiting for a health report"
	}
	if a.Healthy {
		return "Connected and healthy"
	}
	return "Connected but reporting unhealthy"
}

func viaLabel(via bool) string {
	if via {
		return "gateway"
	}
	return "direct"
}

// yamlDisplay normalizes effective-config YAML for display: expand tabs to
// two spaces and collapse oversized indent units (e.g. 4-space) to 2-space
// steps so collector dumps are readable without looking "tabbed out".
func yamlDisplay(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	// Expand tabs to 2 spaces first.
	for i, line := range lines {
		if strings.Contains(line, "\t") {
			var b strings.Builder
			col := 0
			for _, r := range line {
				if r == '\t' {
					// tab stops every 2 columns
					n := 2 - (col % 2)
					for j := 0; j < n; j++ {
						b.WriteByte(' ')
						col++
					}
				} else {
					b.WriteRune(r)
					col++
				}
			}
			lines[i] = b.String()
		}
	}
	// Detect indent unit as GCD of leading space counts on non-empty lines.
	unit := 0
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		n := len(line) - len(trimmed)
		if n == 0 {
			continue
		}
		if unit == 0 {
			unit = n
		} else {
			unit = gcd(unit, n)
		}
	}
	// Only re-scale when content uses a larger consistent unit (4, 8, …).
	if unit >= 4 && unit%2 == 0 {
		scale := unit / 2
		for i, line := range lines {
			trimmed := strings.TrimLeft(line, " ")
			if trimmed == "" {
				continue
			}
			n := len(line) - len(trimmed)
			if n == 0 {
				continue
			}
			lines[i] = strings.Repeat(" ", n/scale) + trimmed
		}
	}
	return strings.Join(lines, "\n")
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// queryWith returns the current query string with key overridden (or deleted if val empty).
func queryWith(q url.Values, key, val string) string {
	cp := cloneQuery(q)
	if val == "" {
		cp.Del(key)
	} else {
		cp.Set(key, val)
	}
	enc := cp.Encode()
	if enc == "" {
		return ""
	}
	return "?" + enc
}

// pollQuery builds the query string for htmx poll URLs (includes current filters).
func pollQuery(q url.Values) string {
	if q == nil {
		return ""
	}
	enc := q.Encode()
	if enc == "" {
		return ""
	}
	return "?" + enc
}

func attrPairs(m map[string]string) [][2]string {
	if len(m) == 0 {
		return nil
	}
	out := make([][2]string, 0, len(m))
	// stable order
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for _, k := range keys {
		out = append(out, [2]string{k, m[k]})
	}
	return out
}

func hasFilter(data pageData) bool {
	return data.Healthy != "" || data.Connected != "" || data.ViaGateway != "" || len(data.Matches) > 0
}

// pageCount returns how many pages total items fill at the given page size.
func pageCount(total, limit int) int {
	if total <= 0 || limit <= 0 {
		return 0
	}
	return (total + limit - 1) / limit
}

// pageNum is the 1-based page index for the current offset.
func pageNum(offset, limit int) int {
	if limit <= 0 {
		return 1
	}
	return offset/limit + 1
}

// pageOffset is the offset for the given 1-based page number.
func pageOffset(page, limit int) int {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		return 0
	}
	return (page - 1) * limit
}

// pageList returns 1-based page numbers from 1..pageCount(total, limit).
func pageList(total, limit int) []int {
	n := pageCount(total, limit)
	if n == 0 {
		return nil
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

// attrChips merges identifying and non-identifying attributes for compact
// display, skipping blank values so empty keys do not clutter the table.
func attrChips(a fleet.AgentView) [][2]string {
	n := len(a.Identifying) + len(a.NonIdentifying)
	if n == 0 {
		return nil
	}
	out := make([][2]string, 0, n)
	for _, p := range attrPairs(a.Identifying) {
		if strings.TrimSpace(p[1]) == "" {
			continue
		}
		out = append(out, p)
	}
	for _, p := range attrPairs(a.NonIdentifying) {
		if strings.TrimSpace(p[1]) == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict requires even args")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		m[k] = pairs[i+1]
	}
	return m, nil
}
