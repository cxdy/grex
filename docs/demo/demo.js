/**
 * Static grex fleet demo for GitHub Pages.
 * Generates seeded pseudo-random agents and renders markup that matches the
 * live UI class names (app.css). Not connected to a grex server.
 */
(function () {
  "use strict";

  const DEFAULT_SEED = 0x67726578; // 'grex'
  // Fleet size is drawn from this range per seed so reshuffle changes scale.
  const AGENT_COUNT_MIN = 100;
  const AGENT_COUNT_MAX = 300;
  const PAGE_SIZE = 100;

  const ENVIRONMENTS = ["prod", "staging", "dev", "canary"];
  const NAMESPACES = [
    "observability",
    "platform",
    "payments",
    "edge",
    "security",
    "data",
    "ml",
    "infra",
  ];
  const REGIONS = [
    "us-east-1",
    "us-east-2",
    "us-west-2",
    "eu-west-1",
    "eu-central-1",
    "ap-southeast-1",
    "ap-northeast-1",
    "sa-east-1",
  ];
  const VERSIONS = ["0.157.0", "0.156.0", "0.155.1", "0.154.0", "0.153.0", "0.152.1"];
  const HOST_PREFIXES = ["otel", "col", "gw", "edge", "batch", "node", "worker", "relay"];
  const HEALTH_ERRORS = [
    "exporter/otlp timed out sending batch",
    "receiver/prometheus scrape target down",
    "memory_limiter soft limit exceeded",
    "extension/healthcheck failed dependency probe",
    "processor/batch queue full",
  ];

  function agentCountForSeed(seed) {
    // Stable count in [MIN, MAX] for a given seed.
    const span = AGENT_COUNT_MAX - AGENT_COUNT_MIN + 1;
    return AGENT_COUNT_MIN + (seed >>> 0) % span;
  }

  // —— PRNG (mulberry32) ——
  function mulberry32(seed) {
    let a = seed >>> 0;
    return function () {
      a |= 0;
      a = (a + 0x6d2b79f5) | 0;
      let t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  function pick(rng, arr) {
    return arr[Math.floor(rng() * arr.length)];
  }

  function uuidFromRng(rng) {
    const hex = [];
    for (let i = 0; i < 32; i++) {
      hex.push(Math.floor(rng() * 16).toString(16));
    }
    return (
      hex.slice(0, 8).join("") +
      "-" +
      hex.slice(8, 12).join("") +
      "-4" +
      hex.slice(13, 16).join("") +
      "-" +
      ((8 + Math.floor(rng() * 4)).toString(16) + hex.slice(17, 20).join("")) +
      "-" +
      hex.slice(20, 32).join("")
    );
  }

  function shortUID(uid) {
    return uid.replace(/-/g, "").slice(0, 8);
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function formatTime(d) {
    if (!d || !(d instanceof Date) || isNaN(d.getTime())) return "—";
    return d.toISOString().replace("T", " ").replace(/\.\d{3}Z$/, " UTC");
  }

  function relTime(d, now) {
    if (!d || isNaN(d.getTime())) return "—";
    const sec = Math.max(0, Math.floor((now - d.getTime()) / 1000));
    if (sec < 5) return "just now";
    if (sec < 60) return sec + "s ago";
    const min = Math.floor(sec / 60);
    if (min < 60) return min + "m ago";
    const hr = Math.floor(min / 60);
    if (hr < 48) return hr + "h ago";
    return Math.floor(hr / 24) + "d ago";
  }

  function formatUptime(ms) {
    const sec = Math.floor(ms / 1000);
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    if (d > 0) return d + "d " + h + "h";
    if (h > 0) return h + "h " + m + "m";
    if (m > 0) return m + "m " + s + "s";
    return s + "s";
  }

  function statusClass(a) {
    if (!a.connected) return "badge-disconnected";
    if (!a.healthReported) return "badge-unknown";
    return a.healthy ? "badge-healthy" : "badge-unhealthy";
  }

  function statusLabel(a) {
    if (!a.connected) return "Disconnected";
    if (!a.healthReported) return "Unknown";
    return a.healthy ? "Healthy" : "Unhealthy";
  }

  function statusTitle(a) {
    const parts = [statusLabel(a)];
    if (a.healthError) parts.push(a.healthError);
    return parts.join(" — ");
  }

  function displayName(a) {
    return a.identifying["service.name"] || a.nonIdentifying["host.name"] || a.instanceUID;
  }

  function roleOf(a) {
    if (a.identifying["service.component"]) return a.identifying["service.component"];
    if (a.nonIdentifying["service.component"]) return a.nonIdentifying["service.component"];
    const name = (a.identifying["service.name"] || "").toLowerCase();
    if (name.includes("gateway")) return "Gateway";
    return "Collector";
  }

  function sampleConfig(name, env) {
    return (
      "receivers:\n" +
      "  otlp:\n" +
      "    protocols:\n" +
      "      grpc:\n" +
      "        endpoint: 0.0.0.0:4317\n" +
      "processors:\n" +
      "  batch: {}\n" +
      "  resource:\n" +
      "    attributes:\n" +
      "      - key: deployment.environment\n" +
      "        value: " +
      env +
      "\n" +
      "        action: upsert\n" +
      "exporters:\n" +
      "  otlp/gateway:\n" +
      "    endpoint: otel-gateway." +
      env +
      ".svc:4317\n" +
      "    tls:\n" +
      "      insecure: false\n" +
      "service:\n" +
      "  pipelines:\n" +
      "    traces:\n" +
      "      receivers: [otlp]\n" +
      "      processors: [resource, batch]\n" +
      "      exporters: [otlp/gateway]\n" +
      "  extensions: [opamp]\n" +
      "  # instance: " +
      name +
      "\n"
    );
  }

  function generateFleet(seed) {
    const rng = mulberry32(seed);
    const count = agentCountForSeed(seed);
    const now = Date.now();
    const startedAt = new Date(now - (2 * 86400 + Math.floor(rng() * 86400)) * 1000);
    const agents = [];

    // Target mix (approximate): ~8% gateways, ~12% disconnected, ~8% unhealthy,
    // ~70%+ via OpAMP gateway. Guaranteed minimums so filters always have hits.
    const minGateways = Math.max(4, Math.floor(count * 0.06));
    const minDisconnected = Math.max(8, Math.floor(count * 0.1));
    const minUnhealthy = Math.max(6, Math.floor(count * 0.07));

    for (let i = 0; i < count; i++) {
      const forceGateway = i < minGateways;
      const forceDisconnected = i >= minGateways && i < minGateways + minDisconnected;
      const forceUnhealthy =
        i >= minGateways + minDisconnected &&
        i < minGateways + minDisconnected + minUnhealthy;

      const isGateway = forceGateway || (!forceDisconnected && rng() < 0.05);
      const viaGateway = !isGateway && rng() < 0.78;
      const connected = forceDisconnected ? false : rng() > 0.08;
      const healthReported = connected ? rng() > 0.05 : rng() > 0.45;
      let healthy = true;
      if (!healthReported) {
        healthy = false;
      } else if (forceUnhealthy) {
        healthy = false;
      } else {
        healthy = rng() > 0.08;
      }

      const env = pick(rng, ENVIRONMENTS);
      const ns = pick(rng, NAMESPACES);
      const region = pick(rng, REGIONS);
      const version = pick(rng, VERSIONS);
      const az = region + String.fromCharCode(97 + Math.floor(rng() * 3)); // a/b/c
      const host =
        pick(rng, HOST_PREFIXES) +
        "-" +
        region.replace(/-/g, "") +
        "-" +
        String(1000 + i).slice(-4) +
        "-" +
        String(Math.floor(rng() * 900) + 100);
      const serviceName = isGateway
        ? "otelcol-gateway-" + env + (rng() > 0.6 ? "-" + region.split("-")[0] : "")
        : "otelcol-" + ns + (rng() > 0.45 ? "-contrib" : "");
      const uid = uuidFromRng(rng);
      const lastSeenOffset = connected
        ? Math.floor(rng() * 50) * 1000
        : (90 + Math.floor(rng() * 900)) * 1000;
      const lastSeen = new Date(now - lastSeenOffset);
      const firstSeen = new Date(
        lastSeen.getTime() - (7200 + Math.floor(rng() * 400000)) * 1000
      );
      const healthError =
        healthReported && !healthy ? pick(rng, HEALTH_ERRORS) : "";

      const identifying = {
        "service.name": serviceName,
        "service.version": version,
        "service.instance.id": uid,
      };
      if (isGateway) {
        identifying["service.component"] = "Gateway";
      } else if (rng() > 0.85) {
        identifying["service.component"] = "Collector";
      }

      const nonIdentifying = {
        "host.name": host,
        "os.type": rng() > 0.25 ? "linux" : rng() > 0.5 ? "darwin" : "windows",
        "deployment.environment": env,
        "service.namespace": ns,
        "cloud.region": region,
        "cloud.availability_zone": az,
        "k8s.cluster.name": ns + "-" + env + "-" + region.split("-")[0],
      };
      if (rng() > 0.4) {
        nonIdentifying["k8s.namespace.name"] = "otel-" + ns;
      }
      if (rng() > 0.55) {
        nonIdentifying["k8s.pod.name"] = serviceName + "-" + String(Math.floor(rng() * 1e6));
      }

      // Full config only on a sample of agents to keep the demo light at 300 rows.
      const effectiveConfig =
        isGateway || forceUnhealthy || rng() < 0.2
          ? { "collector.yaml": sampleConfig(serviceName, env) }
          : {
              "collector.yaml":
                "service:\n  extensions: [opamp]\n  # truncated in demo for scale\n  # full config available on a sample of agents\n",
            };

      agents.push({
        instanceUID: uid,
        sequenceNum: 100 + Math.floor(rng() * 50000),
        identifying,
        nonIdentifying,
        capabilities: 0x1f,
        capabilityFlags: {
          ReportsStatus: true,
          ReportsHealth: true,
          ReportsEffectiveConfig: true,
          ReportsHeartbeat: true,
          ReportsAvailableComponents: rng() > 0.4,
          AcceptsRemoteConfig: false,
          AcceptsPackages: false,
          AcceptsRestartCommand: false,
        },
        healthy,
        healthError,
        healthStatus: !healthReported ? "" : healthy ? "OK" : "degraded",
        healthStartTime: firstSeen,
        healthStatusTime: lastSeen,
        healthReported,
        descriptionReported: rng() > 0.02,
        effectiveConfig,
        packages: {},
        conn: {
          remoteAddr: viaGateway
            ? "10." +
              Math.floor(rng() * 40) +
              "." +
              Math.floor(rng() * 10) +
              "." +
              Math.floor(1 + rng() * 20) +
              ":4320"
            : "10." +
              Math.floor(rng() * 200) +
              "." +
              Math.floor(rng() * 200) +
              "." +
              Math.floor(1 + rng() * 254) +
              ":" +
              (40000 + Math.floor(rng() * 20000)),
          tlsSubject:
            "CN=" + (viaGateway ? "opamp-gateway-" + env : host) + ",O=grex-demo",
          viaGateway,
          transport: rng() > 0.08 ? "ws" : "http",
        },
        connected,
        firstSeen,
        lastSeen,
        missingAttributes: rng() > 0.94 ? ["service.namespace"] : [],
      });
    }

    agents.sort((a, b) => a.instanceUID.localeCompare(b.instanceUID));

    return { seed, startedAt, agents, generatedAt: new Date(now), count };
  }

  // —— Filters / sort ——
  function parseHash() {
    const raw = (location.hash || "#/").replace(/^#/, "") || "/";
    const qIndex = raw.indexOf("?");
    const path = qIndex >= 0 ? raw.slice(0, qIndex) : raw;
    const qs = qIndex >= 0 ? raw.slice(qIndex + 1) : "";
    const params = new URLSearchParams(qs);
    return { path: path.replace(/\/+$/, "") || "/", params };
  }

  function setHash(path, params) {
    const qs = params && [...params].length ? "?" + params.toString() : "";
    const next = "#" + path + qs;
    if (location.hash !== next) {
      location.hash = next;
    } else {
      render();
    }
  }

  // Prometheus-style matchers, matching internal/api/filter.go semantics.
  const MATCHER_RE = /^(.+?)\s*(!~|=~|!=|=)\s*(.*)$/;
  const RESERVED_PARAMS = {
    limit: true,
    offset: true,
    sort: true,
    order: true,
    match: true,
    attr_key: true,
    attr_value: true,
    healthy: true,
    connected: true,
    via_gateway: true,
    seed: true,
  };

  function unquoteMatcherValue(s) {
    s = String(s || "").trim();
    if (s.length >= 2) {
      const a = s[0];
      const b = s[s.length - 1];
      if ((a === '"' && b === '"') || (a === "'" && b === "'")) {
        return s.slice(1, -1);
      }
    }
    return s;
  }

  function compileMatcherRE(pattern) {
    if (pattern === "") return new RegExp("^$");
    const hasStart = pattern.startsWith("^") || pattern.startsWith("\\A");
    const hasEnd = pattern.endsWith("$") || pattern.endsWith("\\z");
    let body = pattern;
    if (hasStart && hasEnd) body = pattern;
    else if (!hasStart && !hasEnd) body = "^(?:" + pattern + ")$";
    else if (!hasStart) body = "^(?:" + pattern + ")";
    else body = "(?:" + pattern + ")$";
    try {
      return new RegExp(body);
    } catch (_) {
      return null;
    }
  }

  function parseMatcher(raw) {
    const s = String(raw || "").trim();
    if (!s) return null;
    const parts = s.match(MATCHER_RE);
    if (!parts) return null;
    const key = parts[1].trim();
    const op = parts[2];
    const value = unquoteMatcherValue(parts[3]);
    if (!key) return null;
    const m = { key, op, value, re: null };
    if (op === "=~" || op === "!~") {
      m.re = compileMatcherRE(value);
      if (!m.re) return null;
    }
    return m;
  }

  function attrValue(agent, key) {
    if (Object.prototype.hasOwnProperty.call(agent.identifying, key)) {
      return { ok: true, got: agent.identifying[key] };
    }
    if (Object.prototype.hasOwnProperty.call(agent.nonIdentifying, key)) {
      return { ok: true, got: agent.nonIdentifying[key] };
    }
    return { ok: false, got: "" };
  }

  function matchAttr(agent, m) {
    const { ok, got } = attrValue(agent, m.key);
    switch (m.op) {
      case "=":
        return ok && got === m.value;
      case "!=":
        // Prometheus: absent label still matches !=
        return !ok || got !== m.value;
      case "=~":
        return ok && m.re && m.re.test(got);
      case "!~":
        return !ok || !m.re || !m.re.test(got);
      default:
        return false;
    }
  }

  function collectMatchers(params) {
    const matchers = [];
    for (const raw of params.getAll("match")) {
      const m = parseMatcher(raw);
      if (m) matchers.push(m);
    }
    // Legacy freeform (real grex also accepts these)
    const attrKey = (params.get("attr_key") || "").trim();
    if (attrKey) {
      const m = parseMatcher(attrKey + "=" + (params.get("attr_value") || ""));
      if (m) matchers.push(m);
    }
    // Bare attribute query params (API back-compat): ?service.name=foo
    for (const [key, value] of params.entries()) {
      if (RESERVED_PARAMS[key]) continue;
      const val = String(value || "").trim();
      if (!val) continue;
      const m = parseMatcher(key + "=" + val);
      if (m) matchers.push(m);
    }
    return matchers;
  }

  function filterAgents(agents, params) {
    let out = agents.slice();
    const healthy = params.get("healthy");
    const connected = params.get("connected");
    const via = params.get("via_gateway");
    // Match fleet.Agent bool fields: Healthy is the raw flag (false if unreported).
    if (healthy === "true") out = out.filter((a) => a.healthy);
    if (healthy === "false") out = out.filter((a) => !a.healthy);
    if (connected === "true") out = out.filter((a) => a.connected);
    if (connected === "false") out = out.filter((a) => !a.connected);
    if (via === "true") out = out.filter((a) => a.conn.viaGateway);
    if (via === "false") out = out.filter((a) => !a.conn.viaGateway);

    const matchers = collectMatchers(params);
    if (matchers.length) {
      out = out.filter((a) => matchers.every((m) => matchAttr(a, m)));
    }
    return out;
  }

  function matchParamsList(params) {
    return params
      .getAll("match")
      .map((s) => s.trim())
      .filter(Boolean);
  }

  function formatMatchDisplay(raw) {
    const m = parseMatcher(raw);
    if (!m) return raw;
    return m.key + " " + m.op + " " + m.value;
  }

  function sortAgents(agents, sortKey, order) {
    const dir = order === "desc" ? -1 : 1;
    const key = sortKey || "instance";
    const list = agents.slice();
    list.sort((a, b) => {
      let cmp = 0;
      switch (key) {
        case "name":
          cmp = displayName(a).localeCompare(displayName(b));
          break;
        case "role":
          cmp = roleOf(a).localeCompare(roleOf(b));
          break;
        case "version":
          cmp = (a.identifying["service.version"] || "").localeCompare(b.identifying["service.version"] || "");
          break;
        case "via":
          cmp = Number(a.conn.viaGateway) - Number(b.conn.viaGateway);
          break;
        case "transport":
          cmp = (a.conn.transport || "").localeCompare(b.conn.transport || "");
          break;
        case "last_seen":
          cmp = a.lastSeen - b.lastSeen;
          break;
        case "status": {
          const rank = (x) => {
            if (x.connected && x.healthReported && x.healthy) return 0;
            if (x.connected && x.healthReported && !x.healthy) return 1;
            if (x.connected) return 2;
            return 3;
          };
          cmp = rank(a) - rank(b);
          break;
        }
        default:
          cmp = a.instanceUID.localeCompare(b.instanceUID);
      }
      if (cmp === 0) cmp = a.instanceUID.localeCompare(b.instanceUID);
      return cmp * dir;
    });
    return list;
  }

  function attrChips(a) {
    const keys = [
      "deployment.environment",
      "service.namespace",
      "cloud.region",
      "os.type",
    ];
    const chips = [];
    for (const k of keys) {
      const v = a.identifying[k] || a.nonIdentifying[k];
      if (v) chips.push([k, v]);
    }
    return chips.slice(0, 4);
  }

  // —— Render ——
  let state = null;

  function seedFromURL() {
    const { params } = parseHash();
    const s = params.get("seed");
    if (s && /^\d+$/.test(s)) return parseInt(s, 10) >>> 0;
    return DEFAULT_SEED;
  }

  function ensureState() {
    const seed = seedFromURL();
    if (!state || state.seed !== seed) {
      state = generateFleet(seed);
    }
    return state;
  }

  function navHighlight(path) {
    const fleet = document.getElementById("nav-fleet");
    const status = document.getElementById("nav-status");
    if (!fleet || !status) return;
    const onFleet = path === "/" || path.startsWith("/agents");
    const onStatus = path === "/status";
    fleet.classList.toggle("is-current", onFleet);
    status.classList.toggle("is-current", onStatus);
    if (onFleet) fleet.setAttribute("aria-current", "page");
    else fleet.removeAttribute("aria-current");
    if (onStatus) status.setAttribute("aria-current", "page");
    else status.removeAttribute("aria-current");
  }

  function sortHref(params, col, curSort, curOrder) {
    const p = new URLSearchParams(params);
    const nextOrder = curSort === col && curOrder === "asc" ? "desc" : "asc";
    p.set("sort", col);
    p.set("order", curSort === col ? nextOrder : "asc");
    p.delete("offset");
    const seed = p.get("seed");
    // keep seed
    if (seed) p.set("seed", seed);
    return "#/?" + p.toString();
  }

  function sortClass(col, curSort, curOrder) {
    if (curSort !== col) return "th-sort";
    return "th-sort is-active order-" + curOrder;
  }

  function pageList(total, limit) {
    const pages = Math.max(1, Math.ceil(total / limit));
    const list = [];
    for (let i = 1; i <= pages; i++) list.push(i);
    return list;
  }

  function pageHref(params, pageNum) {
    const p = new URLSearchParams(params);
    // Preserve repeated match= params (URLSearchParams copy keeps them).
    const offset = (pageNum - 1) * PAGE_SIZE;
    if (offset <= 0) p.delete("offset");
    else p.set("offset", String(offset));
    const qs = p.toString();
    return "#/" + (qs ? "?" + qs : "");
  }

  function renderPager(total, offset, params) {
    const pages = pageList(total, PAGE_SIZE);
    if (pages.length <= 1) return "";
    const cur = Math.floor(offset / PAGE_SIZE) + 1;
    const links = pages
      .map((n) => {
        if (n === cur) {
          return `<span class="page-link is-current" aria-current="page">${n}</span>`;
        }
        return `<a class="page-link" href="${pageHref(params, n)}">${n}</a>`;
      })
      .join("");
    return `<nav class="pager" aria-label="Pagination">${links}</nav>`;
  }

  function renderFleet(params) {
    const now = Date.now();
    const sortKey = params.get("sort") || "instance";
    const order = params.get("order") === "desc" ? "desc" : "asc";
    let matched = filterAgents(state.agents, params);
    matched = sortAgents(matched, sortKey, order);
    const total = matched.length;
    let offset = Math.max(0, parseInt(params.get("offset") || "0", 10) || 0);
    if (offset >= total && total > 0) {
      offset = Math.floor((total - 1) / PAGE_SIZE) * PAGE_SIZE;
    }
    const page = matched.slice(offset, offset + PAGE_SIZE);

    const healthy = params.get("healthy") || "";
    const connected = params.get("connected") || "";
    const via = params.get("via_gateway") || "";
    const matches = matchParamsList(params);
    const hasFilter = !!(healthy || connected || via || matches.length);

    // Same seed format as fleet.html: newline-separated matchers for data-matches.
    const matchDataAttr = matches.map((m) => escapeHtml(formatMatchDisplay(m))).join("&#10;");

    const rows = page
      .map((a) => {
        const chips = attrChips(a)
          .map(
            ([k, v]) =>
              `<button type="button" class="attr-chip" data-attr-key="${escapeHtml(k)}" data-attr-value="${escapeHtml(v)}" title="Filter by ${escapeHtml(k)} = ${escapeHtml(v)}"><span class="attr-chip-key">${escapeHtml(k)}</span><span class="attr-chip-val">${escapeHtml(v)}</span></button>`
          )
          .join("");
        return (
          `<tr>
            <td><span class="badge ${statusClass(a)}" title="${escapeHtml(statusTitle(a))}"><span class="badge-dot" aria-hidden="true"></span>${statusLabel(a)}</span></td>
            <td><a class="row-link" href="#/agents/${encodeURIComponent(a.instanceUID)}${seedQuery(params)}">${escapeHtml(displayName(a))}</a>${
            a.nonIdentifying["host.name"]
              ? `<span class="cell-secondary">${escapeHtml(a.nonIdentifying["host.name"])}</span>`
              : ""
          }</td>
            <td>${escapeHtml(roleOf(a))}</td>
            <td class="mono">${escapeHtml(a.identifying["service.version"] || "—")}</td>
            <td class="attrs-cell">${chips ? `<div class="attr-chips">${chips}</div>` : `<span class="mono">—</span>`}</td>
            <td><span class="via-pill">${a.conn.viaGateway ? "gateway" : "direct"}</span></td>
            <td class="mono">${escapeHtml(a.conn.transport || "—")}</td>
            <td title="${escapeHtml(formatTime(a.lastSeen))}">${relTime(a.lastSeen, now)}</td>
            <td class="mono" title="${escapeHtml(a.instanceUID)}">${shortUID(a.instanceUID)}</td>
          </tr>`
        );
      })
      .join("");

    const cols = [
      ["status", "Status"],
      ["name", "Name"],
      ["role", "Role"],
      ["version", "Version"],
      [null, "Attributes"],
      ["via", "Via"],
      ["transport", "Transport"],
      ["last_seen", "Last seen"],
      ["instance", "Instance"],
    ];
    const head = cols
      .map(([key, label]) => {
        if (!key) return `<th scope="col">${label}</th>`;
        return `<th scope="col"><a class="${sortClass(key, sortKey, order)}" href="${sortHref(params, key, sortKey, order)}">${label}</a></th>`;
      })
      .join("");

    const table =
      total === 0
        ? `<div class="empty"><strong>${hasFilter ? "No agents match" : "No agents connected"}</strong>${
            hasFilter ? " Try clearing filters." : ""
          }</div>`
        : `<div class="table-wrap"><table class="fleet"><thead><tr>${head}</tr></thead><tbody>${rows}</tbody></table></div>`;

    const showing =
      total === 0
        ? "0 agents"
        : `Showing ${offset + 1}–${Math.min(offset + PAGE_SIZE, total)} of ${total}`;
    const pager = renderPager(total, offset, params);

    return `
      <h1 class="page-title">Fleet</h1>
      <p class="demo-scale-hint">${state.agents.length} synthetic agents in this seed · page size ${PAGE_SIZE}</p>
      <form class="filters" id="filter-form" role="search">
        <div class="filters-toolbar">
          <div class="filters-status">
            <div class="field">
              <label for="healthy">Health</label>
              <select id="healthy" name="healthy">
                <option value="" ${healthy === "" ? "selected" : ""}>Any</option>
                <option value="true" ${healthy === "true" ? "selected" : ""}>Healthy</option>
                <option value="false" ${healthy === "false" ? "selected" : ""}>Unhealthy</option>
              </select>
            </div>
            <div class="field">
              <label for="connected">Connection</label>
              <select id="connected" name="connected">
                <option value="" ${connected === "" ? "selected" : ""}>Any</option>
                <option value="true" ${connected === "true" ? "selected" : ""}>Connected</option>
                <option value="false" ${connected === "false" ? "selected" : ""}>Disconnected</option>
              </select>
            </div>
            <div class="field">
              <label for="via_gateway">Via gateway</label>
              <select id="via_gateway" name="via_gateway">
                <option value="" ${via === "" ? "selected" : ""}>Any</option>
                <option value="true" ${via === "true" ? "selected" : ""}>Yes</option>
                <option value="false" ${via === "false" ? "selected" : ""}>No (direct)</option>
              </select>
            </div>
          </div>
          <div class="filter-actions">
            <button type="submit" class="btn">Apply</button>
            ${hasFilter ? `<a class="btn btn-ghost" href="#/${seedQuery(params, true)}">Clear</a>` : ""}
          </div>
        </div>
        <div class="filters-labels">
          <div class="filters-labels-head">
            <span class="filters-section-label">Label filters</span>
            <p class="field-hint">
              Type a key, choose <code>=</code> <code>!=</code> <code>=~</code> <code>!~</code>, then a value · click a chip to edit
            </p>
          </div>
          <div class="matcher-field" data-matcher data-matches="${matchDataAttr}">
            <label class="visually-hidden" for="matcher-input">Attribute label filter</label>
            <div class="matcher-chips" aria-live="polite"></div>
            <div class="matcher-wrap">
              <input
                id="matcher-input"
                class="matcher-input"
                type="text"
                autocomplete="off"
                spellcheck="false"
                placeholder="service.name = otelcol-contrib"
                aria-autocomplete="list"
                aria-controls="matcher-suggest"
                aria-expanded="false"
              >
              <ul id="matcher-suggest" class="matcher-suggest" role="listbox" hidden></ul>
            </div>
          </div>
        </div>
      </form>
      <div id="fleet-table">${table}
        <div class="table-meta"><span>${showing}</span>${pager}</div>
      </div>`;
  }

  function seedQuery(params, onlySeed) {
    const seed = params.get("seed") || String(state.seed);
    if (onlySeed) return seed && seed !== String(DEFAULT_SEED) ? "?seed=" + seed : "";
    const p = new URLSearchParams();
    if (seed && seed !== String(DEFAULT_SEED)) p.set("seed", seed);
    const s = p.toString();
    return s ? "?" + s : "";
  }

  function kvRows(obj) {
    const keys = Object.keys(obj || {}).sort();
    if (!keys.length) return `<p class="empty-inline">None reported yet.</p>`;
    return (
      `<div class="kv-list">` +
      keys
        .map(
          (k) =>
            `<div class="kv-row"><span class="kv-key mono">${escapeHtml(k)}</span><span class="kv-val">${escapeHtml(obj[k])}</span></div>`
        )
        .join("") +
      `</div>`
    );
  }

  function renderAgent(uid, params) {
    const a = state.agents.find((x) => x.instanceUID === uid);
    if (!a) {
      const back = "#/" + (seedQuery(params, true) || "");
      return `
        <div class="detail-toolbar"><a class="back-link" href="${back}">← Fleet</a></div>
        <div class="empty"><strong>Agent not found</strong> This instance is not in the demo dataset. <a href="${back}">Back to fleet</a></div>`;
    }
    const now = Date.now();
    const flags = a.capabilityFlags;
    const configs = Object.entries(a.effectiveConfig || {})
      .map(
        ([name, body]) =>
          `<p class="config-name mono">${escapeHtml(name || "(default)")}</p><pre class="demo-config">${escapeHtml(body)}</pre>`
      )
      .join("");

    const backQs = seedQuery(params, true);

    return `
      <div class="detail-toolbar">
        <a class="back-link" href="#/${backQs}">← Fleet</a>
      </div>
      <h1 class="page-title">${escapeHtml(displayName(a))}</h1>
      <p class="mono detail-uid">${escapeHtml(a.instanceUID)}</p>
      <div class="stat-row">
        <div class="stat"><div class="label">Status</div><div class="value value-sm"><span class="badge ${statusClass(a)}" title="${escapeHtml(statusTitle(a))}"><span class="badge-dot"></span>${statusLabel(a)}</span></div></div>
        <div class="stat"><div class="label">Role</div><div class="value value-md">${escapeHtml(roleOf(a))}</div></div>
        <div class="stat"><div class="label">Version</div><div class="value value-md">${escapeHtml(a.identifying["service.version"] || "—")}</div></div>
        <div class="stat"><div class="label">Last seen</div><div class="value value-md" title="${escapeHtml(formatTime(a.lastSeen))}">${relTime(a.lastSeen, now)}</div></div>
      </div>
      <div class="grid">
        <section class="card">
          <h2>Connection</h2>
          <div class="kv-list">
            <div class="kv-row"><span class="kv-key">Connected</span><span class="kv-val">${a.connected ? "yes" : "no"}</span></div>
            <div class="kv-row"><span class="kv-key">Via</span><span class="kv-val">${a.conn.viaGateway ? "gateway" : "direct"}</span></div>
            <div class="kv-row"><span class="kv-key">Transport</span><span class="kv-val mono">${escapeHtml(a.conn.transport || "—")}</span></div>
            <div class="kv-row"><span class="kv-key">Remote</span><span class="kv-val mono">${escapeHtml(a.conn.remoteAddr || "—")}</span></div>
            <div class="kv-row"><span class="kv-key">TLS subject</span><span class="kv-val mono">${escapeHtml(a.conn.tlsSubject || "—")}</span></div>
            <div class="kv-row"><span class="kv-key">First seen</span><span class="kv-val">${escapeHtml(formatTime(a.firstSeen))}</span></div>
            <div class="kv-row"><span class="kv-key">Sequence</span><span class="kv-val mono">${a.sequenceNum}</span></div>
          </div>
        </section>
        <section class="card">
          <h2>Health</h2>
          <div class="kv-list">
            <div class="kv-row"><span class="kv-key">Reported</span><span class="kv-val">${a.healthReported ? "yes" : "no"}</span></div>
            <div class="kv-row"><span class="kv-key">Healthy</span><span class="kv-val">${a.healthReported ? (a.healthy ? "yes" : "no") : "—"}</span></div>
            <div class="kv-row"><span class="kv-key">Status</span><span class="kv-val">${escapeHtml(a.healthStatus || "—")}</span></div>
            <div class="kv-row"><span class="kv-key">Error</span><span class="kv-val">${escapeHtml(a.healthError || "—")}</span></div>
            <div class="kv-row"><span class="kv-key">Start time</span><span class="kv-val">${escapeHtml(formatTime(a.healthStartTime))}</span></div>
            <div class="kv-row"><span class="kv-key">Status time</span><span class="kv-val">${escapeHtml(formatTime(a.healthStatusTime))}</span></div>
          </div>
        </section>
        <section class="card">
          <h2>Identifying attributes</h2>
          ${kvRows(a.identifying)}
        </section>
        <section class="card">
          <h2>Non-identifying attributes</h2>
          ${kvRows(a.nonIdentifying)}
        </section>
        <section class="card card-wide">
          <h2>Capabilities</h2>
          <div class="kv-list">
            <div class="kv-row"><span class="kv-key">Reports status</span><span class="kv-val">${flags.ReportsStatus}</span></div>
            <div class="kv-row"><span class="kv-key">Reports health</span><span class="kv-val">${flags.ReportsHealth}</span></div>
            <div class="kv-row"><span class="kv-key">Reports effective config</span><span class="kv-val">${flags.ReportsEffectiveConfig}</span></div>
            <div class="kv-row"><span class="kv-key">Reports heartbeat</span><span class="kv-val">${flags.ReportsHeartbeat}</span></div>
            <div class="kv-row"><span class="kv-key">Reports available components</span><span class="kv-val">${flags.ReportsAvailableComponents}</span></div>
            <div class="kv-row"><span class="kv-key">Accepts remote config</span><span class="kv-val">${flags.AcceptsRemoteConfig}</span></div>
            <div class="kv-row"><span class="kv-key">Accepts packages</span><span class="kv-val">${flags.AcceptsPackages}</span></div>
            <div class="kv-row"><span class="kv-key">Accepts restart</span><span class="kv-val">${flags.AcceptsRestartCommand}</span></div>
            <div class="kv-row"><span class="kv-key">Raw bitmask</span><span class="kv-val mono">${a.capabilities}</span></div>
          </div>
        </section>
        <section class="card card-wide">
          <h2>Effective configuration</h2>
          ${configs || `<p class="empty-inline">None reported.</p>`}
        </section>
      </div>`;
  }

  function renderStatus(params) {
    const agents = state.agents;
    let connected = 0,
      disconnected = 0,
      healthy = 0,
      unhealthy = 0,
      unknown = 0,
      awaiting = 0;
    for (const a of agents) {
      if (a.connected) connected++;
      else disconnected++;
      if (!a.descriptionReported) awaiting++;
      if (!a.healthReported) unknown++;
      else if (a.healthy) healthy++;
      else unhealthy++;
    }
    const uptime = formatUptime(Date.now() - state.startedAt.getTime());
    return `
      <h1 class="page-title">Server status</h1>
      <div class="stat-row">
        <div class="stat"><div class="label">Agents</div><div class="value">${agents.length}</div></div>
        <div class="stat"><div class="label">Connected</div><div class="value ok">${connected}</div></div>
        <div class="stat"><div class="label">Disconnected</div><div class="value">${disconnected}</div></div>
        <div class="stat"><div class="label">Healthy</div><div class="value ok">${healthy}</div></div>
        <div class="stat"><div class="label">Unhealthy</div><div class="value ${unhealthy > 0 ? "bad" : ""}">${unhealthy}</div></div>
        <div class="stat"><div class="label">Health unknown</div><div class="value">${unknown}</div></div>
        <div class="stat"><div class="label">Awaiting full state</div><div class="value">${awaiting}</div></div>
      </div>
      <div class="grid">
        <section class="card">
          <h2>grex (demo)</h2>
          <dl class="kv">
            <dt>Version</dt><dd class="mono">demo</dd>
            <dt>Commit</dt><dd class="mono">static</dd>
            <dt>Started</dt><dd>${escapeHtml(formatTime(state.startedAt))}</dd>
            <dt>Uptime</dt><dd>${escapeHtml(uptime)}</dd>
          </dl>
          <p class="demo-meta">seed=${state.seed} · ${agents.length} synthetic agents (${AGENT_COUNT_MIN}–${AGENT_COUNT_MAX} per seed) · not a live process</p>
        </section>
      </div>`;
  }

  function render() {
    ensureState();
    installDemoAttributeAPI();
    const { path, params } = parseHash();
    navHighlight(path);
    const app = document.getElementById("app");
    if (!app) return;

    let html;
    if (path === "/status") {
      html = renderStatus(params);
      document.title = "Server status · grex demo";
    } else if (path.startsWith("/agents/")) {
      const uid = decodeURIComponent(path.slice("/agents/".length));
      html = renderAgent(uid, params);
      document.title = "Agent · grex demo";
    } else {
      html = renderFleet(params);
      document.title = "Fleet · grex demo";
    }
    app.innerHTML = html;
    bindFleetHandlers(params);
    // Re-bind Prometheus matcher autocomplete after SPA re-render.
    document.dispatchEvent(new CustomEvent("grex:demo-ready"));
    if (typeof window.grexInitMatchers === "function") {
      window.grexInitMatchers();
    }
  }

  function bindFleetHandlers(params) {
    const form = document.getElementById("filter-form");
    if (form) {
      form.addEventListener("submit", (e) => {
        e.preventDefault();
        // matcher.js may still be committing the input → chip on this event;
        // read fields after a tick so hidden match= inputs are present.
        const read = () => {
          const fd = new FormData(form);
          const p = new URLSearchParams();
          const matches = [];
          for (const [k, v] of fd.entries()) {
            const val = String(v).trim();
            if (!val) continue;
            if (k === "match") {
              matches.push(val);
              continue;
            }
            p.set(k, val);
          }
          // De-dupe matchers while preserving order
          const seen = new Set();
          for (const m of matches) {
            if (seen.has(m)) continue;
            seen.add(m);
            p.append("match", m);
          }
          const seed =
            params.get("seed") || (state.seed !== DEFAULT_SEED ? String(state.seed) : "");
          if (seed) p.set("seed", seed);
          p.delete("offset");
          setHash("/", p);
        };
        setTimeout(read, 0);
      });
    }
    // Table attr-chips are handled by matcher.js (grex:add-match → chip bar).
    // User clicks Apply to filter — same as the live UI.
  }

  function installDemoAttributeAPI() {
    window.grexDemoListKeys = function (prefix) {
      if (!state || !state.agents) return [];
      const keys = new Set();
      for (const a of state.agents) {
        Object.keys(a.identifying || {}).forEach((k) => keys.add(k));
        Object.keys(a.nonIdentifying || {}).forEach((k) => keys.add(k));
      }
      let list = Array.from(keys).sort();
      const p = (prefix || "").toLowerCase();
      if (p) list = list.filter((k) => k.toLowerCase().includes(p));
      return list.slice(0, 50);
    };
    window.grexDemoListValues = function (key, prefix) {
      if (!state || !state.agents || !key) return [];
      const vals = new Set();
      for (const a of state.agents) {
        if (Object.prototype.hasOwnProperty.call(a.identifying, key)) {
          vals.add(a.identifying[key]);
        } else if (Object.prototype.hasOwnProperty.call(a.nonIdentifying, key)) {
          vals.add(a.nonIdentifying[key]);
        }
      }
      let list = Array.from(vals).sort();
      const p = (prefix || "").toLowerCase();
      if (p) list = list.filter((v) => String(v).toLowerCase().includes(p));
      return list.slice(0, 50);
    };
  }

  function reshuffle() {
    const seed = (Math.floor(Math.random() * 0xffffffff) >>> 0) || 1;
    state = null;
    const p = new URLSearchParams();
    p.set("seed", String(seed));
    setHash("/", p);
  }

  function fixDocsLink() {
    const a = document.getElementById("docs-link");
    if (!a) return;
    // When served under /grex/demo/, ../ is the docs home.
    a.href = new URL("../", location.href).href;
  }

  document.addEventListener("DOMContentLoaded", () => {
    fixDocsLink();
    document.getElementById("reshuffle")?.addEventListener("click", reshuffle);
    window.addEventListener("hashchange", render);
    render();
  });
})();
