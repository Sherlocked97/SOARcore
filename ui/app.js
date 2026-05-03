// app.js — the v1 status page. Vanilla JS on purpose: no build step,
// no framework, no bundler. Once a UI-scoped tech-stack ADR (Q-010)
// lands, this file is deleted and replaced.
//
// The UI talks only to the public API exposed by the core (ADR-006).
// Same-origin via the nginx proxy in front of this app, so there is
// no CORS dance and no cross-origin auth header to manage.

const POLL_INTERVAL_MS = 10_000;

const $ = (id) => document.getElementById(id);

async function fetchJSON(path) {
  const res = await fetch(path, {
    // The auth-stub principal header is injected by the nginx proxy.
    // Keeping it out of client JS means the v1 stub-auth posture
    // never leaks into the browser.
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}`);
  }
  return res.json();
}

async function refreshHealth() {
  const dot = $("health-dot");
  const text = $("health-text");
  const checked = $("health-checked");
  try {
    const body = await fetchJSON("/healthz");
    dot.className = "status-dot ok";
    text.textContent = body.status === "ok" ? "healthy" : `degraded (${body.status})`;
  } catch (err) {
    dot.className = "status-dot bad";
    text.textContent = `unreachable — ${err.message}`;
  }
  checked.textContent = new Date().toLocaleTimeString();
}

async function refreshIncidents() {
  const tbody = $("incidents-body");
  const empty = $("incidents-empty");
  try {
    const incidents = await fetchJSON("/v1/incidents?limit=20");
    renderStats(incidents);
    if (incidents.length === 0) {
      tbody.innerHTML = "";
      empty.hidden = false;
      return;
    }
    empty.hidden = true;
    tbody.innerHTML = incidents.map(renderRow).join("");
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="5" class="muted small">failed to load — ${escapeHTML(err.message)}</td></tr>`;
    empty.hidden = true;
  }
}

function renderStats(incidents) {
  const counts = { total: incidents.length, new: 0, triaged: 0, "in_progress": 0, closed: 0 };
  for (const i of incidents) {
    if (counts[i.status] !== undefined) counts[i.status] += 1;
  }
  $("count-total").textContent = counts.total;
  $("count-new").textContent = counts.new;
  $("count-triaged").textContent = counts.triaged;
  $("count-in-progress").textContent = counts["in_progress"];
  $("count-closed").textContent = counts.closed;
}

function renderRow(i) {
  const created = new Date(i.created_at).toLocaleString();
  const sev = (i.severity || "").toLowerCase();
  const source = i.source_connector_id || "—";
  return `
    <tr>
      <td class="mono">${escapeHTML(created)}</td>
      <td>${escapeHTML(i.title || "")}</td>
      <td class="severity-${escapeHTML(sev)}">${escapeHTML(i.severity || "")}</td>
      <td>${escapeHTML(i.status || "")}</td>
      <td class="mono">${escapeHTML(source)}</td>
    </tr>
  `;
}

// escapeHTML guards every string interpolated into innerHTML — incident
// titles, source IDs, and error text all come from the API and must be
// treated as untrusted.
function escapeHTML(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

async function tick() {
  await Promise.all([refreshHealth(), refreshIncidents()]);
}

tick();
setInterval(tick, POLL_INTERVAL_MS);
