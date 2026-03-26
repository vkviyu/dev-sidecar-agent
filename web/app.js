// ======================================================
// Dev-Sidecar Agent Dashboard - Frontend Logic
// ======================================================

(function () {
  "use strict";

  // ---- State ----
  let currentTab = "traffic";
  let refreshTimer = null;
  const REFRESH_INTERVAL = 3000; // ms
  let editingRuleId = null; // null=新增模式, 有值=编辑模式
  let cachedRules = []; // 缓存规则列表供编辑时查找

  // ---- DOM Ready ----
  document.addEventListener("DOMContentLoaded", init);

  function init() {
    // Tab switching
    document.querySelectorAll(".tab").forEach((btn) => {
      btn.addEventListener("click", () => switchTab(btn.dataset.tab));
    });

    // Auto refresh toggle
    document
      .getElementById("autoRefresh")
      .addEventListener("change", function () {
        if (this.checked) {
          startAutoRefresh();
        } else {
          stopAutoRefresh();
        }
      });

    // Global trace ID filter
    let filterTimeout;
    document
      .getElementById("globalTraceId")
      .addEventListener("input", function () {
        clearTimeout(filterTimeout);
        filterTimeout = setTimeout(() => refreshCurrentTab(), 300);
      });

    // Initial load
    refreshCurrentTab();
    startAutoRefresh();

    // Map Remote: bind form buttons
    document
      .getElementById("btnAddRule")
      .addEventListener("click", showAddRuleForm);
    document.getElementById("btnSaveRule").addEventListener("click", saveRule);
    document
      .getElementById("btnCancelRule")
      .addEventListener("click", hideAddRuleForm);

    // Detail panel resize handles
    initResizeHandles();

    // Pane tab switching (Reqable-style split detail)
    initPaneTabs();

    // Horizontal split divider drag
    initSplitDividers();

    // Right-click context menu
    initContextMenu();
  }

  // ---- Panel Resize (Split Pane) ----
  function initResizeHandles() {
    document.querySelectorAll(".resize-handle").forEach((handle) => {
      handle.addEventListener("mousedown", (e) => {
        e.preventDefault();
        const panelId = handle.dataset.panel;
        const panel = document.getElementById(panelId);
        if (!panel) return;

        const startY = e.clientY;
        const startPanelHeight = panel.offsetHeight;
        handle.classList.add("dragging");
        document.body.style.cursor = "ns-resize";
        document.body.style.userSelect = "none";

        function onMouseMove(e) {
          const delta = e.clientY - startY; // 正 = 向下拖 = panel 变小
          const newPanelHeight = Math.max(80, startPanelHeight - delta);
          panel.style.height = newPanelHeight + "px";
        }

        function onMouseUp() {
          handle.classList.remove("dragging");
          document.body.style.cursor = "";
          document.body.style.userSelect = "";
          document.removeEventListener("mousemove", onMouseMove);
          document.removeEventListener("mouseup", onMouseUp);
        }

        document.addEventListener("mousemove", onMouseMove);
        document.addEventListener("mouseup", onMouseUp);
      });
    });
  }

  // 显示 detail panel 时同时显示 resize handle
  function showPanel(panelId) {
    const panel = document.getElementById(panelId);
    const handle = document.getElementById(panelId + "Resize");
    if (panel) panel.classList.remove("hidden");
    if (handle) handle.classList.remove("hidden");
  }

  function hidePanel(panelId) {
    const panel = document.getElementById(panelId);
    const handle = document.getElementById(panelId + "Resize");
    if (panel) {
      panel.classList.add("hidden");
      panel.style.height = ""; // 重置拖拽设置的高度
    }
    if (handle) handle.classList.add("hidden");
  }

  // ---- Pane Tab Switching (Reqable-style split detail) ----
  function initPaneTabs() {
    document.querySelectorAll(".pane-tab").forEach((tab) => {
      tab.addEventListener("click", () => {
        const pane = tab.closest(".detail-pane");
        pane
          .querySelectorAll(".pane-tab")
          .forEach((t) => t.classList.remove("active"));
        tab.classList.add("active");
        const targetId = tab.dataset.paneTarget;
        pane
          .querySelectorAll(".pane-content")
          .forEach((c) => c.classList.remove("active"));
        document.getElementById(targetId).classList.add("active");

        // Show/hide body view toggle
        const toggle = pane.querySelector(".body-view-toggle");
        if (toggle) {
          const isBody = targetId.toLowerCase().includes("body");
          toggle.classList.toggle("hidden", !isBody);
        }
      });
    });

    // Body view toggle click handlers
    document.querySelectorAll(".body-view-toggle .view-btn").forEach((btn) => {
      btn.addEventListener("click", () => {
        const toggleContainer = btn.closest(".body-view-toggle");
        toggleContainer
          .querySelectorAll(".view-btn")
          .forEach((b) => b.classList.remove("active"));
        btn.classList.add("active");

        // Find the associated Body pre element
        const pane = btn.closest(".detail-pane");
        const bodyPre = pane.querySelector(".pane-content:last-child");
        if (bodyPre && currentDetailBodies[bodyPre.id] !== undefined) {
          bodyPre.textContent = formatBodySmart(
            currentDetailBodies[bodyPre.id],
            btn.dataset.mode,
          );
        }
      });
    });
  }

  // ---- Horizontal Split Divider Drag ----
  function initSplitDividers() {
    document.querySelectorAll(".detail-split-divider").forEach((divider) => {
      divider.addEventListener("mousedown", (e) => {
        e.preventDefault();
        const container = divider.parentElement;
        const leftPane = divider.previousElementSibling;
        const rightPane = divider.nextElementSibling;
        if (!leftPane || !rightPane) return;

        const startX = e.clientX;
        const containerWidth = container.getBoundingClientRect().width;
        const startLeftWidth = leftPane.getBoundingClientRect().width;

        divider.classList.add("dragging");
        document.body.style.cursor = "ew-resize";
        document.body.style.userSelect = "none";

        function onMouseMove(e) {
          const delta = e.clientX - startX;
          const newLeftWidth = startLeftWidth + delta;
          const minW = 120;
          const maxW = containerWidth - 120 - 4; // 4px divider
          const clamped = Math.max(minW, Math.min(maxW, newLeftWidth));
          const leftPct = (clamped / containerWidth) * 100;
          leftPane.style.flex = "none";
          leftPane.style.width = leftPct + "%";
          rightPane.style.flex = "1";
        }

        function onMouseUp() {
          divider.classList.remove("dragging");
          document.body.style.cursor = "";
          document.body.style.userSelect = "";
          document.removeEventListener("mousemove", onMouseMove);
          document.removeEventListener("mouseup", onMouseUp);
        }

        document.addEventListener("mousemove", onMouseMove);
        document.addEventListener("mouseup", onMouseUp);
      });
    });
  }

  // ---- Tab Switching ----
  function switchTab(tab) {
    currentTab = tab;
    document
      .querySelectorAll(".tab")
      .forEach((t) => t.classList.toggle("active", t.dataset.tab === tab));
    document
      .querySelectorAll(".tab-panel")
      .forEach((p) => p.classList.toggle("active", p.id === "tab-" + tab));
    refreshCurrentTab();
  }

  // ---- Auto Refresh ----
  function startAutoRefresh() {
    stopAutoRefresh();
    refreshTimer = setInterval(refreshCurrentTab, REFRESH_INTERVAL);
  }

  function stopAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer);
      refreshTimer = null;
    }
  }

  function refreshCurrentTab() {
    switch (currentTab) {
      case "traffic":
        fetchTraffic();
        break;
      case "logs":
        fetchLogs();
        break;
      case "mapr":
        fetchMapRemoteRules();
        fetchMapRemoteHits();
        break;
      case "marked":
        fetchMarked();
        break;
    }
  }

  // ---- API Helpers ----
  function getTraceIdFilter() {
    return (document.getElementById("globalTraceId").value || "").trim();
  }

  function apiUrl(path, params) {
    const traceId = getTraceIdFilter();
    if (traceId) {
      params = params || {};
      params.trace_id = traceId;
    }
    const qs = params ? "?" + new URLSearchParams(params).toString() : "";
    return path + qs;
  }

  async function fetchJSON(url) {
    try {
      const resp = await fetch(url);
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      return await resp.json();
    } catch (err) {
      console.error("Fetch error:", url, err);
      return null;
    }
  }

  // ---- Traffic Tab ----
  async function fetchTraffic() {
    const data = await fetchJSON(apiUrl("/api/traffic", { limit: 200 }));
    if (!data) return;

    const tbody = document.querySelector("#trafficTable tbody");
    const countEl = document.getElementById("trafficCount");
    const records = Array.isArray(data) ? data : [];
    countEl.textContent = records.length + " records";

    if (records.length === 0) {
      tbody.innerHTML =
        '<tr><td colspan="6"><div class="empty-state"><div class="empty-state-icon">&#9783;</div>No traffic captured yet</div></td></tr>';
      return;
    }

    // Render most recent first
    const reversed = [...records].reverse();
    tbody.innerHTML = reversed
      .map((rec, i) => {
        const statusClass = getStatusClass(rec.resp_status);
        const durationClass = getDurationClass(rec.duration_ms);
        const time = formatTime(rec.timestamp);
        const shortUrl = truncate(rec.url, 80);
        const shortTrace = rec.trace_id || "-";

        return `<tr class="clickable" data-index="${records.length - 1 - i}" data-trace-id="${rec.trace_id || ""}">
        <td>${time}</td>
        <td><span class="method-badge method-${rec.method}">${rec.method}</span></td>
        <td class="url-cell" title="${escapeHtml(rec.url)}">${escapeHtml(shortUrl)}</td>
        <td><span class="status-badge ${statusClass}">${rec.resp_status || "-"}</span></td>
        <td><span class="duration ${durationClass}">${rec.duration_ms != null ? rec.duration_ms + "ms" : "-"}</span></td>
        <td><span class="trace-id" title="${rec.trace_id || ""}">${shortTrace}</span></td>
      </tr>`;
      })
      .join("");

    // Row click handler
    tbody.querySelectorAll("tr.clickable").forEach((row) => {
      row.addEventListener("click", () => {
        const idx = parseInt(row.dataset.index);
        showTrafficDetail(records[idx]);
      });
    });
  }

  function showTrafficDetail(rec) {
    document.getElementById("trafficReqHeaders").textContent = formatHeaders(
      rec.req_headers,
    );
    const reqCt = getContentType(rec.req_headers);
    renderBodyWithToggle(
      "trafficReqBody",
      "trafficReqBodyToggle",
      rec.req_body,
      reqCt,
    );

    document.getElementById("trafficRespHeaders").textContent = formatHeaders(
      rec.resp_headers,
    );
    const respCt = getContentType(rec.resp_headers);
    renderBodyWithToggle(
      "trafficRespBody",
      "trafficRespBodyToggle",
      rec.resp_body,
      respCt,
    );

    showPanel("trafficDetail");
    stopAutoRefresh();
  }

  // ---- Logs Tab ----
  async function fetchLogs() {
    const data = await fetchJSON(apiUrl("/api/logs", { limit: 500 }));
    if (!data) return;

    const tbody = document.querySelector("#logsTable tbody");
    const countEl = document.getElementById("logsCount");
    const entries = Array.isArray(data) ? data : [];
    countEl.textContent = entries.length + " entries";

    if (entries.length === 0) {
      tbody.innerHTML =
        '<tr><td colspan="4"><div class="empty-state"><div class="empty-state-icon">&#9783;</div>No logs captured yet</div></td></tr>';
      return;
    }

    const reversed = [...entries].reverse();
    tbody.innerHTML = reversed
      .map((entry) => {
        const time = formatTime(entry.timestamp);
        const shortTrace = entry.trace_id || "-";

        return `<tr>
        <td>${time}</td>
        <td><span class="level-${entry.level}">${entry.level}</span></td>
        <td><span class="trace-id" title="${entry.trace_id || ""}">${shortTrace}</span></td>
        <td style="white-space:normal; max-width:600px; word-break:break-all;">${escapeHtml(entry.message)}</td>
      </tr>`;
      })
      .join("");
  }

  // ---- Map Remote Tab ----
  async function fetchMapRemoteRules() {
    const data = await fetchJSON("/api/map-remote");
    if (!data) return;

    const tbody = document.querySelector("#maprTable tbody");
    const countEl = document.getElementById("maprCount");
    const rules = Array.isArray(data) ? data : [];
    cachedRules = rules;
    countEl.textContent = rules.length + " rules";

    if (rules.length === 0) {
      tbody.innerHTML =
        '<tr><td colspan="5"><div class="empty-state"><div class="empty-state-icon">&#9783;</div>No Map Remote rules configured</div></td></tr>';
      return;
    }

    tbody.innerHTML = rules
      .map((rule) => {
        const fromStr = formatLocation(rule.from);
        const toStr = formatLocation(rule.to);
        const statusCls = rule.enable ? "rule-enabled" : "rule-disabled";
        const statusText = rule.enable ? "ON" : "OFF";
        const toggleText = rule.enable ? "Disable" : "Enable";

        return `<tr>
        <td><span class="rule-status ${statusCls}">${statusText}</span></td>
        <td title="${escapeHtml(rule.id)}">${escapeHtml(rule.name || rule.id)}</td>
        <td class="url-cell" title="${escapeHtml(fromStr)}">${escapeHtml(fromStr)}</td>
        <td class="url-cell" title="${escapeHtml(toStr)}">${escapeHtml(toStr)}</td>
        <td class="rule-actions">
          <button class="btn-toggle" onclick="toggleRule('${escapeHtml(rule.id)}', ${!rule.enable})">${toggleText}</button>
          <button class="btn-edit" onclick="editRule('${escapeHtml(rule.id)}')">Edit</button>
          <button class="btn-delete" onclick="deleteRule('${escapeHtml(rule.id)}')">Delete</button>
        </td>
      </tr>`;
      })
      .join("");
  }

  function formatLocation(loc) {
    if (!loc) return "*";
    const proto = loc.protocol || "*";
    const host = loc.host || "*";
    const port = loc.port || "*";
    const path = loc.path || "*";
    const query = loc.query || "";
    if (query && query !== "*") {
      return `${proto}://${host}:${port}${path}?${query}`;
    }
    return `${proto}://${host}:${port}${path}`;
  }

  async function fetchMapRemoteHits() {
    const data = await fetchJSON("/api/map-remote/hits?limit=200");
    if (!data) return;

    const tbody = document.querySelector("#hitsTable tbody");
    const countEl = document.getElementById("hitsCount");
    const hits = Array.isArray(data) ? data : [];
    countEl.textContent = hits.length + " hits";

    if (hits.length === 0) {
      tbody.innerHTML =
        '<tr><td colspan="6"><div class="empty-state"><div class="empty-state-icon">&#9783;</div>No Map Remote hits yet</div></td></tr>';
      return;
    }

    const reversed = [...hits].reverse();
    tbody.innerHTML = reversed
      .map((hit) => {
        const time = formatTime(hit.timestamp);
        const shortTrace = hit.trace_id || "-";
        const shortOrig = truncate(hit.orig_url, 60);
        const shortRewrite = truncate(hit.rewrite_url, 60);

        return `<tr class="${hit.trace_id ? "clickable" : ""}" data-trace-id="${hit.trace_id || ""}" data-hit-id="${hit.id || ""}">
        <td>${time}</td>
        <td title="${escapeHtml(hit.rule_id)}">${escapeHtml(hit.rule_name || hit.rule_id)}</td>
        <td><span class="method-badge method-${hit.method}">${hit.method}</span></td>
        <td class="url-cell" title="${escapeHtml(hit.orig_url)}">${escapeHtml(shortOrig)}</td>
        <td class="url-cell" title="${escapeHtml(hit.rewrite_url)}">${escapeHtml(shortRewrite)}</td>
        <td><span class="trace-id" title="${hit.trace_id || ""}">${shortTrace}</span></td>
      </tr>`;
      })
      .join("");

    // Row click for hit detail
    tbody.querySelectorAll("tr.clickable").forEach((row) => {
      row.addEventListener("click", () => {
        const traceId = row.dataset.traceId;
        if (traceId) showHitDetail(traceId);
      });
    });
  }

  async function showHitDetail(traceId) {
    const data = await fetchJSON(
      `/api/traffic?trace_id=${encodeURIComponent(traceId)}`,
    );

    if (!data || !Array.isArray(data) || data.length === 0) {
      document.getElementById("hitReqHeaders").textContent =
        "(No traffic record found for this trace ID - may have been evicted from ring buffer)";
      document.getElementById("hitReqBody").textContent = "";
      document.getElementById("hitRespHeaders").textContent = "";
      document.getElementById("hitRespBody").textContent = "";
      showPanel("hitDetail");
      stopAutoRefresh();
      return;
    }

    const rec = data[0];
    document.getElementById("hitReqHeaders").textContent = formatHeaders(
      rec.req_headers,
    );
    const reqCt = getContentType(rec.req_headers);
    renderBodyWithToggle("hitReqBody", "hitReqBodyToggle", rec.req_body, reqCt);

    document.getElementById("hitRespHeaders").textContent = formatHeaders(
      rec.resp_headers,
    );
    const respCt = getContentType(rec.resp_headers);
    renderBodyWithToggle(
      "hitRespBody",
      "hitRespBodyToggle",
      rec.resp_body,
      respCt,
    );

    showPanel("hitDetail");
    stopAutoRefresh();
  }

  function showAddRuleForm() {
    document.getElementById("addRuleForm").classList.remove("hidden");
  }

  function hideAddRuleForm() {
    document.getElementById("addRuleForm").classList.add("hidden");
    editingRuleId = null;
    document.getElementById("ruleFormTitle").textContent =
      "New Map Remote Rule";
    // Reset form
    document.getElementById("ruleName").value = "";
    document.getElementById("fromProtocol").value = "*";
    document.getElementById("fromHost").value = "";
    document.getElementById("fromPort").value = "*";
    document.getElementById("fromPath").value = ".*";
    document.getElementById("fromQuery").value = "*";
    document.getElementById("toProtocol").value = "http";
    document.getElementById("toHost").value = "";
    document.getElementById("toPort").value = "*";
    document.getElementById("toPath").value = "*";
    document.getElementById("toQuery").value = "*";
  }

  async function saveRule() {
    const name = document.getElementById("ruleName").value.trim();
    if (!name) {
      alert("Rule name is required");
      return;
    }

    const rule = {
      enable: true,
      name: name,
      from: {
        protocol: document.getElementById("fromProtocol").value.trim() || "*",
        host: document.getElementById("fromHost").value.trim() || "*",
        port: document.getElementById("fromPort").value.trim() || "*",
        path: document.getElementById("fromPath").value.trim() || ".*",
        query: document.getElementById("fromQuery").value.trim() || "",
      },
      to: {
        protocol: document.getElementById("toProtocol").value.trim() || "http",
        host: document.getElementById("toHost").value.trim() || "localhost",
        port: document.getElementById("toPort").value.trim() || "*",
        path: document.getElementById("toPath").value.trim() || "*",
        query: document.getElementById("toQuery").value.trim() || "",
      },
    };

    try {
      let resp;
      if (editingRuleId) {
        // 编辑模式：PUT + Body
        // 保留原规则的 enable 状态
        const existing = cachedRules.find((r) => r.id === editingRuleId);
        if (existing) rule.enable = existing.enable;
        rule.id = editingRuleId;
        resp = await fetch(
          "/api/map-remote?id=" + encodeURIComponent(editingRuleId),
          {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(rule),
          },
        );
      } else {
        // 新增模式：POST
        resp = await fetch("/api/map-remote", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(rule),
        });
      }
      if (!resp.ok) {
        const err = await resp.json();
        alert("Failed to save rule: " + (err.error || resp.statusText));
        return;
      }
      hideAddRuleForm();
      fetchMapRemoteRules();
    } catch (err) {
      alert("Network error: " + err.message);
    }
  }

  async function deleteRule(id) {
    if (!confirm("Delete rule: " + id + "?")) return;
    try {
      const resp = await fetch("/api/map-remote?id=" + encodeURIComponent(id), {
        method: "DELETE",
      });
      if (!resp.ok) {
        alert("Failed to delete rule");
        return;
      }
      fetchMapRemoteRules();
    } catch (err) {
      alert("Network error: " + err.message);
    }
  }

  async function toggleRule(id, enable) {
    try {
      const resp = await fetch(
        "/api/map-remote?id=" + encodeURIComponent(id) + "&enable=" + enable,
        { method: "PUT" },
      );
      if (!resp.ok) {
        alert("Failed to toggle rule");
        return;
      }
      fetchMapRemoteRules();
    } catch (err) {
      alert("Network error: " + err.message);
    }
  }

  // ---- Marked Tab ----
  async function fetchMarked() {
    const data = await fetchJSON("/api/marked");
    if (!data) return;

    const tbody = document.querySelector("#markedTable tbody");
    const countEl = document.getElementById("markedCount");
    const records = Array.isArray(data) ? data : [];
    countEl.textContent = records.length + " marked";

    if (records.length === 0) {
      tbody.innerHTML =
        '<tr><td colspan="5"><div class="empty-state"><div class="empty-state-icon">&#9783;</div>No marked records yet. Right-click any record to mark it.</div></td></tr>';
      return;
    }

    const reversed = [...records].reverse();
    tbody.innerHTML = reversed
      .map((rec) => {
        const time = formatTime(rec.marked_at);
        const sourceBadge = `<span class="source-badge source-${rec.source}">${rec.source}</span>`;
        const summary = getMarkedSummary(rec);
        const note = rec.note ? escapeHtml(rec.note) : "-";

        return `<tr class="clickable" data-mark-id="${rec.id}">
        <td>${time}</td>
        <td>${sourceBadge}</td>
        <td class="url-cell" title="${escapeHtml(summary)}">${escapeHtml(truncate(summary, 80))}</td>
        <td class="url-cell">${note}</td>
        <td><button class="btn-delete" onclick="event.stopPropagation(); deleteMarked('${escapeHtml(rec.id)}')">Delete</button></td>
      </tr>`;
      })
      .join("");

    // Row click to show detail
    tbody.querySelectorAll("tr.clickable").forEach((row) => {
      row.addEventListener("click", () => {
        const markId = row.dataset.markId;
        const rec = records.find((r) => r.id === markId);
        if (rec) showMarkedDetail(rec);
      });
    });
  }

  function getMarkedSummary(rec) {
    const d = rec.data;
    if (!d) return rec.source_id;
    switch (rec.source) {
      case "traffic":
        return (d.method || "") + " " + (d.url || "");
      case "hit":
        return (
          (d.method || "") +
          " " +
          (d.orig_url || "") +
          " -> " +
          (d.rewrite_url || "")
        );
      default:
        return rec.source_id;
    }
  }

  function showMarkedDetail(rec) {
    const content = document.getElementById("markedDetailContent");
    try {
      content.textContent = JSON.stringify(rec.data, null, 2);
    } catch {
      content.textContent = String(rec.data);
    }
    showPanel("markedDetail");
  }

  // ---- Right-click Context Menu ----
  let ctxMenuSource = null;
  let ctxMenuSourceId = null;

  function initContextMenu() {
    const menu = document.getElementById("contextMenu");

    // Hide menu on click elsewhere
    document.addEventListener("click", () => {
      menu.classList.add("hidden");
    });

    // Mark without note
    document.getElementById("ctxMark").addEventListener("click", () => {
      menu.classList.add("hidden");
      if (ctxMenuSource && ctxMenuSourceId) {
        markRecord(ctxMenuSource, ctxMenuSourceId, "");
      }
    });

    // Mark with note
    document.getElementById("ctxMarkNote").addEventListener("click", () => {
      menu.classList.add("hidden");
      if (ctxMenuSource && ctxMenuSourceId) {
        const note = prompt("Enter a note for this mark:");
        if (note !== null) {
          markRecord(ctxMenuSource, ctxMenuSourceId, note);
        }
      }
    });

    // Register contextmenu on all tables via event delegation
    document.addEventListener("contextmenu", (e) => {
      const row = e.target.closest("tr.clickable");
      if (!row) return;

      // Determine source and sourceId from the row's context
      const tabPanel = row.closest(".tab-panel");
      if (!tabPanel) return;

      const tabId = tabPanel.id; // "tab-traffic", "tab-mapr"
      let source = null;
      let sourceId = null;

      if (tabId === "tab-traffic") {
        source = "traffic";
        sourceId = row.dataset.traceId;
      } else if (tabId === "tab-mapr") {
        const hitsTable = row.closest("#hitsTable");
        if (hitsTable) {
          source = "hit";
          sourceId = row.dataset.hitId;
        }
      }

      if (!source || !sourceId) return;

      e.preventDefault();
      ctxMenuSource = source;
      ctxMenuSourceId = sourceId;

      // Position menu at cursor
      menu.style.left = e.pageX + "px";
      menu.style.top = e.pageY + "px";
      menu.classList.remove("hidden");
    });
  }

  async function markRecord(source, sourceId, note) {
    try {
      const resp = await fetch("/api/marked", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          source: source,
          source_id: sourceId,
          note: note,
        }),
      });
      if (!resp.ok) {
        const err = await resp.json();
        alert("Mark failed: " + (err.error || resp.statusText));
        return;
      }
      // Update marked count in tab
      const countEl = document.getElementById("markedCount");
      if (countEl) {
        const current = parseInt(countEl.textContent) || 0;
        countEl.textContent = current + 1 + " marked";
      }
    } catch (err) {
      alert("Network error: " + err.message);
    }
  }

  // ---- Utility Functions ----
  function formatTime(ts) {
    if (!ts) return "-";
    const d = new Date(ts);
    const pad = (n) => String(n).padStart(2, "0");
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, "0")}`;
  }

  function getStatusClass(status) {
    if (!status) return "";
    if (status >= 200 && status < 300) return "status-2xx";
    if (status >= 300 && status < 400) return "status-3xx";
    if (status >= 400 && status < 500) return "status-4xx";
    if (status >= 500) return "status-5xx";
    return "";
  }

  function getDurationClass(ms) {
    if (ms == null) return "";
    if (ms < 200) return "duration-fast";
    if (ms < 1000) return "duration-normal";
    return "duration-slow";
  }

  function truncate(str, max) {
    if (!str) return "";
    return str.length > max ? str.substring(0, max) + "..." : str;
  }

  function escapeHtml(str) {
    if (!str) return "";
    return str
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function formatJSON(obj) {
    if (!obj) return "(empty)";
    try {
      if (typeof obj === "string") obj = JSON.parse(obj);
      return JSON.stringify(obj, null, 2);
    } catch {
      return String(obj);
    }
  }

  function formatHeaders(headers) {
    if (!headers) return "(empty)";
    if (typeof headers === "string") {
      try {
        headers = JSON.parse(headers);
      } catch {
        return headers;
      }
    }
    if (typeof headers !== "object") return String(headers);
    return Object.entries(headers)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => `${k}: ${v}`)
      .join("\n");
  }

  function formatBody(body) {
    if (!body) return "(empty)";
    try {
      const parsed = JSON.parse(body);
      return JSON.stringify(parsed, null, 2);
    } catch {
      return body;
    }
  }

  // ---- Smart Body Formatting ----

  function getContentType(headers) {
    if (!headers) return "";
    if (typeof headers === "string") {
      try {
        headers = JSON.parse(headers);
      } catch {
        return "";
      }
    }
    // headers is map, keys may vary in case
    for (const [k, v] of Object.entries(headers)) {
      if (k.toLowerCase() === "content-type") return v.toLowerCase();
    }
    return "";
  }

  function detectBodyMode(contentType) {
    if (!contentType) return "text";
    if (contentType.includes("json")) return "json";
    if (contentType.includes("xml")) return "text";
    if (contentType.includes("html")) return "text";
    if (contentType.includes("text/")) return "text";
    if (contentType.includes("javascript")) return "text";
    if (contentType.includes("css")) return "text";
    if (contentType.includes("x-www-form-urlencoded")) return "form";
    if (contentType.includes("multipart/form-data")) return "text";
    // binary / octet-stream / image / etc
    return "hex";
  }

  function formatBodySmart(body, mode) {
    if (!body) return "(empty)";
    if (mode === "auto" || mode === "json") {
      // Try JSON first
      try {
        const parsed = JSON.parse(body);
        return JSON.stringify(parsed, null, 2);
      } catch {
        if (mode === "json") return body;
      }
      // Try form-urlencoded
      if (body.includes("=") && !body.includes(" ")) {
        try {
          return body
            .split("&")
            .map((pair) => {
              const [k, ...rest] = pair.split("=");
              return (
                decodeURIComponent(k) +
                " = " +
                decodeURIComponent(rest.join("="))
              );
            })
            .join("\n");
        } catch {
          // fall through
        }
      }
      return body;
    }
    if (mode === "form") {
      try {
        return body
          .split("&")
          .map((pair) => {
            const [k, ...rest] = pair.split("=");
            return (
              decodeURIComponent(k) + " = " + decodeURIComponent(rest.join("="))
            );
          })
          .join("\n");
      } catch {
        return body;
      }
    }
    if (mode === "hex") {
      return toHexDump(body);
    }
    // mode === "text"
    return body;
  }

  function toHexDump(str) {
    const lines = [];
    for (let i = 0; i < str.length; i += 16) {
      const slice = str.substring(i, i + 16);
      const hex = [];
      const ascii = [];
      for (let j = 0; j < 16; j++) {
        if (j < slice.length) {
          const code = slice.charCodeAt(j) & 0xff;
          hex.push(code.toString(16).padStart(2, "0"));
          ascii.push(code >= 32 && code < 127 ? slice[j] : ".");
        } else {
          hex.push("  ");
          ascii.push(" ");
        }
      }
      const offset = i.toString(16).padStart(8, "0");
      lines.push(
        offset +
          "  " +
          hex.slice(0, 8).join(" ") +
          "  " +
          hex.slice(8).join(" ") +
          "  |" +
          ascii.join("") +
          "|",
      );
    }
    return lines.join("\n");
  }

  // Store raw body data for view toggle re-rendering
  let currentDetailBodies = {};

  function renderBodyWithToggle(preId, toggleId, body, contentType) {
    const autoMode = detectBodyMode(contentType);
    const actualMode = autoMode === "hex" ? "hex" : "auto";
    const pre = document.getElementById(preId);
    const toggle = document.getElementById(toggleId);

    // Store raw body for later re-format
    currentDetailBodies[preId] = body;

    pre.textContent = formatBodySmart(body, actualMode);

    // Reset toggle buttons to Auto active
    if (toggle) {
      toggle.querySelectorAll(".view-btn").forEach((b) => {
        b.classList.toggle(
          "active",
          b.dataset.mode === (actualMode === "hex" ? "hex" : "auto"),
        );
      });
    }
  }

  // ---- Expose to HTML ----
  window.closeDetail = function (id) {
    hidePanel(id);
  };
  window.deleteRule = deleteRule;
  window.toggleRule = toggleRule;
  window.editRule = editRule;
  window.deleteMarked = async function (id) {
    try {
      const resp = await fetch("/api/marked?id=" + encodeURIComponent(id), {
        method: "DELETE",
      });
      if (resp.ok) fetchMarked();
    } catch (err) {
      alert("Failed to delete: " + err.message);
    }
  };

  function editRule(id) {
    const rule = cachedRules.find((r) => r.id === id);
    if (!rule) return;

    editingRuleId = id;
    document.getElementById("ruleFormTitle").textContent =
      "Edit Map Remote Rule";

    // 填充表单
    document.getElementById("ruleName").value = rule.name || "";
    document.getElementById("fromProtocol").value =
      (rule.from && rule.from.protocol) || "*";
    document.getElementById("fromHost").value =
      (rule.from && rule.from.host) || "";
    document.getElementById("fromPort").value =
      (rule.from && rule.from.port) || "*";
    document.getElementById("fromPath").value =
      (rule.from && rule.from.path) || ".*";
    document.getElementById("fromQuery").value =
      (rule.from && rule.from.query) || "*";
    document.getElementById("toProtocol").value =
      (rule.to && rule.to.protocol) || "http";
    document.getElementById("toHost").value = (rule.to && rule.to.host) || "";
    document.getElementById("toPort").value = (rule.to && rule.to.port) || "*";
    document.getElementById("toPath").value = (rule.to && rule.to.path) || "*";
    document.getElementById("toQuery").value =
      (rule.to && rule.to.query) || "*";

    document.getElementById("addRuleForm").classList.remove("hidden");
  }
})();
