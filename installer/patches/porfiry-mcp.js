/* === MCP Gateway Patch v1.0.0 === */
/* Injected into Claude Code webview index.js by apply-mcp-gateway.sh/.ps1 */
/* Placeholders substituted at apply time: __GATEWAY_URL__, __GATEWAY_AUTH_TOKEN__, __PATCH_VERSION__ */
;(function () {
  var GATEWAY_URL = "__GATEWAY_URL__";
  var TOKEN = "__GATEWAY_AUTH_TOKEN__";
  var PATCH_VERSION = "__PATCH_VERSION__";

  var CONFIG = {
    DEBOUNCE_WINDOW_MS:              10000,
    DEBOUNCE_FORCE_FIRE_COUNT:       10,
    ACTIVE_TOOL_POSTPONE_CAP_MS:     10000,
    HEARTBEAT_INTERVAL_MS:           60000,
    HEARTBEAT_JITTER_MAX_MS:         5000,
    POLL_INTERVAL_MS:                2000,
    POLL_JITTER_MAX_MS:              500,
    INITIAL_SKEW_MAX_MS:             30000,
    INITIAL_SKEW_STORAGE_TTL_MS:     600000,
    AWAITING_DISCOVERY_QUEUE_MAX:    16,
    LATENCY_WARN_MS:                 30000,
    CONSECUTIVE_ERRORS_FAIL_THRESHOLD: 3,
    MODE_M_RESET_ON_SUCCESS:         true,
    MODE_D_MIN_RETRY_COUNT:          5,
    MODE_D_MIN_CONSECUTIVE_HEARTBEATS: 3,
    MAX_FIBER_DEPTH:                 80,
  };

  var CONFIG_OVERRIDE_RANGES = {
    LATENCY_WARN_MS:                    [5000, 300000],
    DEBOUNCE_WINDOW_MS:                 [2000, 60000],
    CONSECUTIVE_ERRORS_FAIL_THRESHOLD:  [2, 20],
  };

  // --- Session ID (per-window, not persisted) ---
  function getOrCreateSessionId() {
    try {
      return crypto.randomUUID();
    } catch (e) {
      return Math.random().toString(36).slice(2) + Date.now().toString(36);
    }
  }
  var SESSION_ID = getOrCreateSessionId();

  // --- Initial load skew (cross-window thundering-herd mitigation, P4-04) ---
  function loadInitialSkew() {
    try {
      var stored = localStorage.getItem("porfiry-mcp-initial-skew");
      var tsStored = localStorage.getItem("porfiry-mcp-initial-skew-ts");
      if (stored !== null && tsStored !== null) {
        if (Date.now() - Number(tsStored) < CONFIG.INITIAL_SKEW_STORAGE_TTL_MS) {
          return Number(stored);
        }
      }
      var skew = Math.floor(Math.random() * CONFIG.INITIAL_SKEW_MAX_MS);
      localStorage.setItem("porfiry-mcp-initial-skew", String(skew));
      localStorage.setItem("porfiry-mcp-initial-skew-ts", String(Date.now()));
      return skew;
    } catch (e) {
      return Math.floor(Math.random() * CONFIG.INITIAL_SKEW_MAX_MS);
    }
  }
  var INITIAL_SKEW_MS = loadInitialSkew();

  // --- Error scrubbing (SP4-M2) ---
  var PATH_SCRUB_RE = /[A-Za-z]:\\[^"'\s]+|\\\\[^"'\s\\]+\\[^"'\s]+|\/(?:home|Users|tmp|var|etc|opt|usr|app|workspace|System|Library|srv|proc|dev|mnt|root)[\/\s"'].*?(?=["'\s]|$)/g;

  function scrubError(e) {
    if (!e) return "";
    var msg = String(e);
    // Keep only the first line (strip stack frames starting with "  at ")
    var lines = msg.split("\n");
    var firstLine = lines[0] || "";
    // Remove filesystem paths from first line
    firstLine = firstLine.replace(PATH_SCRUB_RE, "<path>");
    // Truncate to 256 chars
    return firstLine.slice(0, 256);
  }

  // --- Config override validation (SP4-L2) ---
  function validateConfigOverride(override) {
    if (!override || typeof override !== "object") return;
    var keys = Object.keys(CONFIG_OVERRIDE_RANGES);
    for (var i = 0; i < keys.length; i++) {
      var k = keys[i];
      if (!(k in override)) continue;
      var val = override[k];
      var range = CONFIG_OVERRIDE_RANGES[k];
      if (typeof val !== "number" || val < range[0] || val > range[1]) {
        // Log rejection but keep compiled-in default
        try { console.warn("porfiry-mcp: config_override rejected — " + k + "=" + val + " out of range [" + range[0] + "," + range[1] + "]"); } catch(e2) {}
        continue;
      }
      CONFIG[k] = val;
    }
  }

  // --- State machine ---
  // mcpSessionState ∈ {unknown, discovering, ready, lost}
  var mcpSessionState = "unknown";
  var mcpSession = null;
  var mcp_method_fiber_depth = 0;
  var fiberWalkRetryCount = 0;
  var consecutiveErrors = 0;
  var lastReconnectLatencyMs = 0;
  var lastReconnectOk = false;
  var lastReconnectError = "";

  // awaitingDiscovery FIFO for actions queued while state ∈ {lost, discovering}
  var awaitingDiscovery = [];

  function transitionState(newState) {
    if (mcpSessionState === newState) return;
    mcpSessionState = newState;
    if (newState === "lost") {
      // Release old session reference on lost (P4-02 closure leak prevention)
      mcpSession = null;
    }
  }

  // --- Fiber walk (Alt-E) ---
  function normalizeAction(action) {
    if (!action || typeof action !== "object") return null;
    var type = action.type;
    if (type !== "reconnect" && type !== "probe-reconnect") return null;
    return {
      id: action.id || "",
      type: type,
      serverName: action.serverName || "mcp-gateway",
      nonce: action.nonce || "",
    };
  }

  function findMcpSession() {
    try {
      var root = document.querySelector('[class*="inputContainer_"]') || document.querySelector("#root");
      if (!root) return null;
      var fk = Object.keys(root).find(function (k) {
        return k.indexOf("__reactFiber$") === 0 || k.indexOf("__reactInternalInstance$") === 0;
      });
      if (!fk) return null;
      var fiber = root[fk];
      for (var depth = 0; depth < CONFIG.MAX_FIBER_DEPTH && fiber; depth++, fiber = fiber.return) {
        var p = fiber.memoizedProps;
        if (!p) continue;
        // (a) p.session?.reconnectMcpServer
        if (p.session && typeof p.session.reconnectMcpServer === "function") {
          mcp_method_fiber_depth = depth;
          return p.session;
        }
        // (b) p.actions?.reconnectMcpServer
        if (p.actions && typeof p.actions.reconnectMcpServer === "function") {
          mcp_method_fiber_depth = depth;
          return p.actions;
        }
        // (c) any own prop whose value.reconnectMcpServer === function
        var propKeys = Object.keys(p);
        for (var ki = 0; ki < propKeys.length; ki++) {
          var v = p[propKeys[ki]];
          if (v && typeof v === "object" && typeof v.reconnectMcpServer === "function") {
            mcp_method_fiber_depth = depth;
            return v;
          }
        }
      }
    } catch (e) {/* silent */}
    return null;
  }

  function runFiberWalk() {
    transitionState("discovering");
    var found = findMcpSession();
    if (found) {
      mcpSession = found;
      fiberWalkRetryCount = 0;
      transitionState("ready");
      drainAwaitingDiscovery();
    } else {
      fiberWalkRetryCount++;
    }
  }

  // --- MutationObserver (mirror porfiry-taskbar.js:93-97) ---
  var rootRef = document.querySelector("#root");
  function invalidateSession() {
    transitionState("lost");
    mcp_method_fiber_depth = 0;
  }

  new MutationObserver(function () {
    var cur = document.querySelector("#root");
    if (cur !== rootRef) {
      rootRef = cur;
      invalidateSession();
    }
    if (mcpSessionState !== "ready") {
      runFiberWalk();
    }
  }).observe(document.body, { childList: true, subtree: true });

  setTimeout(function () {
    runFiberWalk();
    if (mcpSessionState !== "ready") {
      setTimeout(function () { runFiberWalk(); }, 8000);
    }
  }, 2000);

  // --- Debounce state per serverName ---
  // Each entry: { timer, latestAction, count, firstArmed }
  var debounceMap = {};

  // --- Singleflight map per serverName ---
  // Each entry: Promise (in-flight)
  var inflightMap = {};

  // --- Pending-action polling cursor ---
  var lastActionId = "";

  // --- Active-tool-call suppression ---
  // Indirected through a mutable ref so tests can inject a deterministic probe
  // (main-session review finding CR-16.4-03). Default reads DOM.
  function defaultIsToolCallActive() {
    try {
      var spinner = document.querySelector('[class*="spinnerRow_"]');
      return !!(spinner && spinner.offsetParent !== null);
    } catch (e) { return false; }
  }
  var isToolCallActive = defaultIsToolCallActive;

  // --- VSCode API cached once at load (CR-16.4-02: acquireVsCodeApi throws on 2nd call) ---
  var _vscApi = null;
  try { _vscApi = (typeof acquireVsCodeApi === "function") ? acquireVsCodeApi() : null; } catch (e) {}

  // --- Auth header helper ---
  function authHeaders() {
    return { "Authorization": "Bearer " + TOKEN, "Content-Type": "application/json" };
  }

  // --- POST /probe-result for probe-reconnect outcome (CR-16.4-01 — dashboard probe nonce correlation) ---
  function postProbeResult(nonce, ok, errorMsg) {
    if (!nonce) return;
    fetch(GATEWAY_URL + "/api/v1/claude-code/probe-result", {
      method: "POST",
      headers: authHeaders(),
      body: JSON.stringify({
        nonce: nonce,
        ok: ok,
        error: ok ? "" : scrubError(errorMsg),
      }),
    }).catch(function () {});
  }

  // --- Complete an action: ack, and for probe-reconnect also POST /probe-result ---
  function completeAction(action, ok, errorMsg, latencyMs) {
    ackAction(action.id, ok, errorMsg, action.type, latencyMs);
    if (action.type === "probe-reconnect" && action.nonce) {
      postProbeResult(action.nonce, ok, errorMsg);
    }
  }

  // --- Ack a pending action ---
  function ackAction(id, ok, errorMsg, actionType, latencyMs) {
    fetch(GATEWAY_URL + "/api/v1/claude-code/pending-actions/" + encodeURIComponent(id) + "/ack", {
      method: "POST",
      headers: authHeaders(),
      body: JSON.stringify({
        ok: ok,
        error_message: ok ? "" : scrubError(errorMsg),
        action_type: actionType,
        latency_ms: latencyMs,
      }),
    }).catch(function () {});
  }

  // --- Execute a reconnect action (with active-tool suppression + singleflight) ---
  function executeReconnect(action, postponeAccumMs) {
    postponeAccumMs = postponeAccumMs || 0;
    if (isToolCallActive() && postponeAccumMs < CONFIG.ACTIVE_TOOL_POSTPONE_CAP_MS) {
      setTimeout(function () {
        executeReconnect(action, postponeAccumMs + 1000);
      }, 1000);
      return;
    }
    var serverName = action.serverName;
    // Singleflight: if in-flight for this serverName, attach to it
    if (inflightMap[serverName]) {
      var t0attach = Date.now();
      inflightMap[serverName].then(
        function () { completeAction(action, true, "", Date.now() - t0attach); },
        function (err) { completeAction(action, false, err && err.message ? err.message : String(err), Date.now() - t0attach); }
      );
      return;
    }
    var t0 = Date.now();
    var p = Promise.resolve().then(function () {
      return mcpSession.reconnectMcpServer(serverName);
    });
    inflightMap[serverName] = p;
    p.then(
      function () {
        var latency = Date.now() - t0;
        lastReconnectLatencyMs = latency;
        lastReconnectOk = true;
        lastReconnectError = "";
        if (CONFIG.MODE_M_RESET_ON_SUCCESS) consecutiveErrors = 0;
        delete inflightMap[serverName];
        completeAction(action, true, "", latency);
      },
      function (err) {
        var latency = Date.now() - t0;
        lastReconnectLatencyMs = latency;
        lastReconnectOk = false;
        lastReconnectError = err && err.message ? err.message : String(err);
        consecutiveErrors++;
        delete inflightMap[serverName];
        completeAction(action, false, lastReconnectError, latency);
      }
    );
  }

  // --- Drain awaitingDiscovery after state reaches ready ---
  function drainAwaitingDiscovery() {
    while (awaitingDiscovery.length > 0 && mcpSessionState === "ready") {
      var queued = awaitingDiscovery.shift();
      executeReconnect(queued);
    }
  }

  // --- Enqueue action into awaitingDiscovery FIFO ---
  function enqueueAwaiting(action) {
    if (awaitingDiscovery.length >= CONFIG.AWAITING_DISCOVERY_QUEUE_MAX) {
      // Drop oldest, complete it as overflow (acks + probe-result for probe-reconnect)
      var dropped = awaitingDiscovery.shift();
      completeAction(dropped, false, "awaiting-discovery-overflow", 0);
    }
    awaitingDiscovery.push(action);
  }

  // --- Debounce + dispatch logic ---
  function scheduleReconnect(action) {
    var serverName = action.serverName;
    var entry = debounceMap[serverName];
    if (!entry) {
      // First action in this window — arm timer
      var timer = setTimeout(function () { fireDebounce(serverName); }, CONFIG.DEBOUNCE_WINDOW_MS);
      entry = { timer: timer, latestAction: action, count: 1, firstArmed: Date.now() };
      debounceMap[serverName] = entry;
    } else {
      // Coalesce: keep latest action
      entry.latestAction = action;
      entry.count++;
      // SP4-L1: force-fire on starvation cap
      if (entry.count >= CONFIG.DEBOUNCE_FORCE_FIRE_COUNT) {
        clearTimeout(entry.timer);
        delete debounceMap[serverName];
        dispatchReconnect(action);
      }
    }
  }

  function fireDebounce(serverName) {
    var entry = debounceMap[serverName];
    if (!entry) return;
    var latest = entry.latestAction;
    delete debounceMap[serverName];
    dispatchReconnect(latest);
  }

  function dispatchReconnect(action) {
    // SP4-M1: state-check at fire time
    if (mcpSessionState === "lost" || mcpSessionState === "discovering") {
      enqueueAwaiting(action);
      return;
    }
    if (!mcpSession || typeof mcpSession.reconnectMcpServer !== "function") {
      // Session lost since debounce armed
      transitionState("lost");
      enqueueAwaiting(action);
      return;
    }
    executeReconnect(action);
  }

  // --- Poll pending actions ---
  function pollActions() {
    var url = GATEWAY_URL + "/api/v1/claude-code/pending-actions";
    if (lastActionId) url += "?after=" + encodeURIComponent(lastActionId);
    fetch(url, { headers: authHeaders() })
      .then(function (r) { return r.json(); })
      .then(function (actions) {
        if (!Array.isArray(actions) || actions.length === 0) return;
        for (var i = 0; i < actions.length; i++) {
          var raw = actions[i];
          if (raw.id) lastActionId = raw.id;
          var action = normalizeAction(raw);
          if (!action) continue;
          scheduleReconnect(action);
        }
      })
      .catch(function () {});
    schedulePoll();
  }

  function schedulePoll() {
    var jitter = Math.random() * CONFIG.POLL_JITTER_MAX_MS;
    setTimeout(pollActions, CONFIG.POLL_INTERVAL_MS + jitter);
  }

  // --- Heartbeat ---
  function sendHeartbeat() {
    var ccVer = "";
    var vsVer = "";
    // Reuse cached _vscApi (CR-16.4-02: acquireVsCodeApi throws on 2nd call)
    if (_vscApi) {
      try {
        ccVer = _vscApi.extensionVersion || "";
        vsVer = _vscApi.vscodeVersion || "";
      } catch (e) {}
    }

    var payload = {
      session_id: SESSION_ID,
      patch_version: PATCH_VERSION,
      cc_version: ccVer,
      vscode_version: vsVer,
      fiber_ok: mcpSessionState === "ready",
      mcp_method_ok: !!(mcpSession && typeof mcpSession.reconnectMcpServer === "function"),
      mcp_method_fiber_depth: mcp_method_fiber_depth,
      last_reconnect_latency_ms: lastReconnectLatencyMs,
      last_reconnect_ok: lastReconnectOk,
      last_reconnect_error: scrubError(lastReconnectError),
      pending_actions_inflight: awaitingDiscovery.length + Object.keys(inflightMap).length,
      fiber_walk_retry_count: fiberWalkRetryCount,
      mcp_session_state: mcpSessionState,
      ts: Date.now(),
    };

    fetch(GATEWAY_URL + "/api/v1/claude-code/patch-heartbeat", {
      method: "POST",
      headers: authHeaders(),
      body: JSON.stringify(payload),
    })
      .then(function (r) { return r.json(); })
      .then(function (resp) {
        if (resp && resp.config_override) {
          validateConfigOverride(resp.config_override);
        }
      })
      .catch(function () {});

    scheduleHeartbeat();
  }

  function scheduleHeartbeat() {
    var jitter = Math.random() * CONFIG.HEARTBEAT_JITTER_MAX_MS;
    setTimeout(sendHeartbeat, CONFIG.HEARTBEAT_INTERVAL_MS + jitter);
  }

  // --- Bootstrap ---
  // First heartbeat fires after initial skew + jitter
  setTimeout(sendHeartbeat, INITIAL_SKEW_MS + Math.random() * CONFIG.HEARTBEAT_JITTER_MAX_MS);
  // First poll fires after initial skew
  setTimeout(pollActions, INITIAL_SKEW_MS + Math.random() * CONFIG.POLL_JITTER_MAX_MS);

})();
