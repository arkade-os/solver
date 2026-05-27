// solverd dashboard — vanilla ES module, talks to /v1/* REST endpoints.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// -------- icons (inline SVG, lucide-style) --------

const ICONS = {
  layers:
    '<path d="m12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z"/><path d="M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"/><path d="M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"/>',
  wallet:
    '<path d="M21 12V7H5a2 2 0 0 1 0-4h14v4"/><path d="M3 5v14a2 2 0 0 0 2 2h16v-5"/><path d="M18 12a2 2 0 0 0 0 4h4v-4Z"/>',
  history:
    '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l4 2"/>',
  key:
    '<path d="m15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4"/><path d="m21 2-9.6 9.6"/><circle cx="7.5" cy="15.5" r="5.5"/>',
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  refresh:
    '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  copy:
    '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
  x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  trash:
    '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/>',
  edit:
    '<path d="M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.375 2.625a1 1 0 0 1 3 3l-9.013 9.014a2 2 0 0 1-.853.505l-2.873.84a.5.5 0 0 1-.62-.62l.84-2.873a2 2 0 0 1 .506-.852z"/>',
  external:
    '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
  "arrow-right": '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>',
  info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
  eye:
    '<path d="M2.06 12.35a1 1 0 0 1 0-.7 10.75 10.75 0 0 1 19.88 0 1 1 0 0 1 0 .7 10.75 10.75 0 0 1-19.88 0"/><circle cx="12" cy="12" r="3"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  alert:
    '<circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/>',
  "wifi-off":
    '<path d="M12 20h.01"/><path d="M8.5 16.43a5 5 0 0 1 7 0"/><path d="M5 12.86a10 10 0 0 1 5.17-2.69"/><path d="M19 12.86a10 10 0 0 0-2.01-1.52"/><path d="M2 8.82a15 15 0 0 1 4.18-2.64"/><path d="M22 8.82a15 15 0 0 0-11.29-3.76"/><path d="m2 2 20 20"/>',
  "power-off":
    '<path d="M18.36 6.64a9 9 0 1 1-12.73 0"/><line x1="12" x2="12" y1="2" y2="12"/>',
};

function icon(name, cls = "") {
  const body = ICONS[name] || "";
  return `<svg class="${`icon ${cls}`.trim()}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
}

// hydrateIcons swaps every <i data-icon="name"> placeholder for inline SVG,
// preserving any extra classes (e.g. "empty-art").
function hydrateIcons(root = document) {
  $$("i[data-icon]", root).forEach((el) => {
    const extra = el.className.trim();
    el.outerHTML = icon(el.dataset.icon, extra);
  });
}

// -------- api --------

const api = {
  async _req(method, path, body) {
    const res = await fetch(path, {
      method,
      headers: body ? { "content-type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
      const text = await res.text();
      let msg = text;
      try {
        const j = JSON.parse(text);
        if (j.error) msg = j.error;
        else if (j.message) msg = j.message;
      } catch (_) {}
      throw new Error(msg || `${method} ${path} → ${res.status}`);
    }
    if (res.status === 204) return null;
    const txt = await res.text();
    return txt ? JSON.parse(txt) : null;
  },
  listPairs: () => api._req("GET", "/v1/pairs"),
  addPair: (pair) => api._req("POST", "/v1/pair", { pair }),
  updatePair: (pair) => api._req("PUT", "/v1/pair", { pair }),
  removePair: (name) => api._req("DELETE", `/v1/pair/${encodeURIComponent(name)}`),
  plugins: () => api._req("GET", "/v1/plugins"),
  balance: () => api._req("GET", "/v1/balance"),
  address: () => api._req("GET", "/v1/address"),
  listTrades: (limit = 100) =>
    api._req("GET", `/v1/trades?limit=${encodeURIComponent(limit)}`),
  solverKeys: () => api._req("GET", "/v1/preimage/solver-pubkey"),
};

// -------- toast --------

function toast(message, kind = "info", actionLabel, onAction) {
  const root = $("#toasts");
  const el = document.createElement("div");
  el.className = "toast" + (kind !== "info" ? ` ${kind}` : "");
  const ic = kind === "error" ? "alert" : kind === "success" ? "check" : "info";
  el.innerHTML = icon(ic);
  const msg = document.createElement("span");
  msg.className = "toast-msg";
  msg.textContent = message;
  el.appendChild(msg);
  if (actionLabel && onAction) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.textContent = actionLabel;
    btn.addEventListener("click", () => {
      try {
        onAction();
      } finally {
        el.remove();
      }
    });
    el.appendChild(btn);
  }
  root.appendChild(el);
  setTimeout(() => el.remove(), 5000);
}

// -------- format --------

function fmtSats(n) {
  if (n == null) return "—";
  return Number(n).toLocaleString("en-US") + " sats";
}

function fmtBTC(n) {
  if (n == null) return "—";
  return (Number(n) / 1e8).toFixed(8) + " BTC";
}

function truncMid(s, head = 8, tail = 6) {
  if (!s) return "";
  if (s === "BTC" || s.length <= head + tail + 1) return s;
  return `${s.slice(0, head)}…${s.slice(-tail)}`;
}

// assetLabel renders an asset id (or "BTC") for display, truncated when long.
function assetLabel(s, head = 6, tail = 4) {
  if (!s) return "?";
  return truncMid(s, head, tail);
}

// pairWantAsset returns the want-side (quote) asset of a pair name.
function pairWantAsset(name) {
  if (!name) return "";
  const i = name.indexOf("/");
  return i < 0 ? "" : name.slice(i + 1);
}

// fmtPairAmount formats a min/max value using the pair's want-side asset:
// BTC → satoshi count, asset → raw count + truncated id.
function fmtPairAmount(raw, pairName) {
  if (raw == null) return "—";
  const want = pairWantAsset(pairName);
  if (want === "BTC") return `${Number(raw).toLocaleString("en-US")} sats`;
  return `${Number(raw).toLocaleString("en-US")} · ${assetLabel(want)}`;
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("Copied to clipboard", "success");
  } catch (_) {
    toast("Copy failed", "error");
  }
}

// -------- plugin state, gating & connection banner --------

// pluginsState mirrors GET /v1/plugins; null until first fetch.
let pluginsState = null;

function pluginEnabled(name) {
  // Treat "unknown" as enabled so we don't gate before the first poll.
  return !pluginsState || pluginsState[name]?.enabled !== false;
}

function setPluginChip(name, info) {
  const chip = $(`#plugin-${name}`);
  const stateEl = $(`#plugin-${name}-state`);
  if (!chip || !stateEl) return;
  if (!info || !info.enabled) {
    chip.dataset.state = "disabled";
    stateEl.textContent = "disabled";
    return;
  }
  chip.dataset.state = info.running ? "running" : "stopped";
  stateEl.textContent = info.running ? "running" : "stopped";
}

function applyGating() {
  $$(".nav-item").forEach((btn) => {
    const plug = btn.dataset.plugin;
    const disabled = pluginsState && pluginsState[plug]?.enabled === false;
    btn.dataset.disabled = disabled ? "true" : "false";
    let off = btn.querySelector(".nav-off");
    if (disabled && !off) {
      off = document.createElement("span");
      off.className = "nav-off";
      off.textContent = "off";
      btn.appendChild(off);
    } else if (!disabled && off) {
      off.remove();
    }
  });
}

function setConnected(ok) {
  $("#conn-banner").hidden = ok;
}

function setPluginsOffline() {
  ["banco", "preimage"].forEach((name) => {
    const chip = $(`#plugin-${name}`);
    const stateEl = $(`#plugin-${name}-state`);
    if (!chip || !stateEl) return;
    chip.dataset.state = "stopped";
    stateEl.textContent = "offline";
  });
}

async function refreshStatus() {
  try {
    const p = await api.plugins();
    pluginsState = p;
    setPluginChip("banco", p.banco);
    setPluginChip("preimage", p.preimage);
    applyGating();
    setConnected(true);
  } catch (_) {
    setPluginsOffline();
    setConnected(false);
  }
}

// renderDisabled hides a view's normal content and shows a "plugin disabled"
// notice instead of letting its loader hit endpoints that 404.
function renderDisabled(viewName, pluginName) {
  $$(`#view-${viewName} > .card`).forEach((c) => (c.hidden = true));
  $$(`#view-${viewName} .view-head button`).forEach((b) => (b.hidden = true));
  let el = $(`#${viewName}-disabled`);
  if (!el) {
    el = document.createElement("div");
    el.className = "card";
    el.id = `${viewName}-disabled`;
    el.innerHTML = `<div class="empty">${icon(
      "power-off",
      "empty-art"
    )}<p>The <strong>${escapeHTML(
      pluginName
    )}</strong> plugin is disabled.</p><p class="muted">Enable it in the solverd configuration to use this section.</p></div>`;
    $(`#view-${viewName}`).appendChild(el);
  }
  el.hidden = false;
}

function clearDisabled(viewName) {
  const el = $(`#${viewName}-disabled`);
  if (el) el.hidden = true;
  $$(`#view-${viewName} > .card`).forEach((c) => {
    if (c.id !== `${viewName}-disabled`) c.hidden = false;
  });
  $$(`#view-${viewName} .view-head button`).forEach((b) => (b.hidden = false));
}

// -------- navigation --------

const VIEW_PLUGIN = {
  pairs: "banco",
  balance: "banco",
  history: "banco",
  solverkeys: "preimage",
};

function setSection(name) {
  $$(".nav-item").forEach((b) => {
    const active = b.dataset.section === name;
    b.toggleAttribute("aria-current", active);
    if (active) b.setAttribute("aria-current", "page");
  });
  $$(".view").forEach((v) => (v.hidden = true));
  const target = $(`#view-${name}`);
  if (target) target.hidden = false;

  const plug = VIEW_PLUGIN[name];
  if (plug && !pluginEnabled(plug)) {
    renderDisabled(name, plug);
    return;
  }
  clearDisabled(name);
  if (name === "balance") loadBalance();
  if (name === "pairs") loadPairs();
  if (name === "history") loadTrades();
  if (name === "solverkeys") loadKeys();
}

$("#nav").addEventListener("click", (e) => {
  const btn = e.target.closest(".nav-item");
  if (btn) setSection(btn.dataset.section);
});

// firstEnabledSection picks the landing tab based on which plugins are on.
function firstEnabledSection() {
  if (pluginEnabled("banco")) return "pairs";
  if (pluginEnabled("preimage")) return "solverkeys";
  return "pairs";
}

// -------- pairs --------

let pairsCache = [];

async function loadPairs() {
  const body = $("#pairs-body");
  const empty = $("#pairs-empty");
  try {
    const data = await api.listPairs();
    pairsCache = data.pairs || [];
    renderPairs(pairsCache);
  } catch (err) {
    toast(err.message, "error");
    body.innerHTML = "";
    empty.hidden = false;
  }
}

function renderPairs(pairs) {
  const body = $("#pairs-body");
  const empty = $("#pairs-empty");
  if (!pairs.length) {
    body.innerHTML = "";
    empty.hidden = false;
    return;
  }
  empty.hidden = true;
  body.innerHTML = "";
  for (const p of pairs) {
    const base = pairBase(p.pair);
    const quote = pairWantAsset(p.pair);
    const tr = document.createElement("tr");
    tr.dataset.pair = p.pair;
    tr.innerHTML = `
      <td>
        <span class="mono" title="${escapeAttr(p.pair)}">${escapeHTML(
          assetLabel(base)
        )} ${icon("arrow-right")} ${escapeHTML(assetLabel(quote))}</span>
      </td>
      <td class="num">${escapeHTML(fmtPairAmount(p.min_amount, p.pair))}</td>
      <td class="num">${escapeHTML(fmtPairAmount(p.max_amount, p.pair))}</td>
      <td><a href="${escapeAttr(safeHref(p.price_feed))}" target="_blank" rel="noreferrer noopener" class="mono trunc" title="${escapeAttr(
        p.price_feed
      )}">${escapeHTML(p.price_feed)}</a></td>
      <td>${
        p.invert_price
          ? '<span class="badge on">Yes</span>'
          : '<span class="badge">No</span>'
      }</td>
      <td class="actions-col">
        <div class="row-actions">
          <button class="icon-btn" data-edit aria-label="Edit pair">${icon(
            "edit"
          )}</button>
          <button class="icon-btn btn-danger" data-del aria-label="Delete pair">${icon(
            "trash"
          )}</button>
        </div>
      </td>`;
    tr.querySelector("[data-edit]").addEventListener("click", () => openEdit(p));
    tr.querySelector("[data-del]").addEventListener("click", () => deletePair(p));
    body.appendChild(tr);
  }
}

function pairBase(name) {
  if (!name) return "";
  const i = name.indexOf("/");
  return i < 0 ? name : name.slice(0, i);
}

// -------- pair dialog --------

const dialog = $("#pair-dialog");
const form = $("#pair-form");

// sideValue resolves a base/quote side to "BTC" or a normalized hex asset id.
function sideValue(side) {
  const kind = form.elements[`${side}_kind`].value;
  if (kind === "BTC") return "BTC";
  return String(form.elements[`${side}_asset`].value || "")
    .trim()
    .toLowerCase()
    .replace(/^0x/, "");
}

// syncAssetInputs shows the hex input only when its side is set to "asset",
// and toggles `required` so a hidden field never blocks submit.
function syncAssetInputs() {
  ["base", "quote"].forEach((side) => {
    const isAsset = form.elements[`${side}_kind`].value === "asset";
    const input = form.elements[`${side}_asset`];
    input.hidden = !isAsset;
    input.required = isAsset && !form.dataset.locked;
  });
}

function updateForm() {
  syncAssetInputs();
  const base = sideValue("base");
  const quote = sideValue("quote");
  const baseDisp = base === "BTC" ? "BTC" : base ? assetLabel(base) : "an asset";
  const quoteDisp =
    quote === "BTC" ? "BTC" : quote ? assetLabel(quote) : "an asset";

  // unit suffix + amount hint follow the want (quote) asset.
  const unit = quote === "BTC" ? "sats" : "units";
  $$("[data-unit-suffix]").forEach((el) => (el.textContent = unit));
  $("#amount-hint").textContent =
    quote === "BTC"
      ? "Bounds are in satoshis on the want side (BTC dust ≥ 330 sats)."
      : "Bounds are in the want asset's smallest (raw) unit.";

  // invert hint references the actual sides.
  $("#invert-hint").textContent = `Offers are priced as ${baseDisp} per ${quoteDisp}. Enable if your feed returns ${quoteDisp} per ${baseDisp} instead.`;

  // live preview.
  const baseStr = base || "?";
  const quoteStr = quote || "?";
  $("#preview-pair-str").textContent = `${assetLabel(baseStr)} / ${assetLabel(
    quoteStr
  )}`;
  const min = form.elements.min_amount.value;
  const max = form.elements.max_amount.value;
  const minStr = min ? Number(min).toLocaleString("en-US") : "min";
  const maxStr = max ? Number(max).toLocaleString("en-US") : "max";
  $("#preview-text").innerHTML = `The solver fulfills offers depositing <strong>${escapeHTML(
    baseDisp
  )}</strong> for <strong>${escapeHTML(
    quoteDisp
  )}</strong> when the maker wants between <strong>${escapeHTML(
    minStr
  )}</strong> and <strong>${escapeHTML(maxStr)}</strong> <strong>${escapeHTML(
    unit
  )}</strong>.`;
}

function setAssetsLocked(locked) {
  form.dataset.locked = locked ? "1" : "";
  ["base_kind", "quote_kind"].forEach((n) => {
    form.querySelectorAll(`[name="${n}"]`).forEach((r) => (r.disabled = locked));
  });
  form.elements.base_asset.disabled = locked;
  form.elements.quote_asset.disabled = locked;
  $("#assets-section").style.opacity = locked ? "0.7" : "";
}

function clearFormErrors() {
  $("#assets-error").textContent = "";
  $("#amount-error").textContent = "";
  ["base_asset", "quote_asset", "min_amount", "max_amount"].forEach((n) =>
    form.elements[n].classList.remove("invalid")
  );
}

function openAdd() {
  form.reset();
  form.dataset.mode = "add";
  setAssetsLocked(false);
  // defaults: deposit BTC, want asset.
  form.elements.base_kind.value = "BTC";
  form.elements.quote_kind.value = "asset";
  clearFormErrors();
  $("#dialog-title").textContent = "Add trading pair";
  $("#pair-submit").textContent = "Add pair";
  updateForm();
  dialog.showModal();
}

function openEdit(pair) {
  form.reset();
  form.dataset.mode = "edit";
  const base = pairBase(pair.pair);
  const quote = pairWantAsset(pair.pair);
  form.elements.base_kind.value = base === "BTC" ? "BTC" : "asset";
  form.elements.quote_kind.value = quote === "BTC" ? "BTC" : "asset";
  form.elements.base_asset.value = base === "BTC" ? "" : base;
  form.elements.quote_asset.value = quote === "BTC" ? "" : quote;
  form.elements.min_amount.value = pair.min_amount;
  form.elements.max_amount.value = pair.max_amount;
  form.elements.price_feed.value = pair.price_feed;
  form.elements.invert_price.checked = !!pair.invert_price;
  setAssetsLocked(true); // identity can't change on edit
  clearFormErrors();
  $("#dialog-title").textContent = "Edit trading pair";
  $("#pair-submit").textContent = "Save changes";
  updateForm();
  dialog.showModal();
}

function validateForm(base, quote, min, max) {
  let ok = true;
  const isHex = (s) => /^[0-9a-f]+$/.test(s) && s.length % 2 === 0;
  if (!form.dataset.locked) {
    if (base !== "BTC" && !isHex(base)) {
      $("#assets-error").textContent = "Deposit asset must be a valid hex id.";
      form.elements.base_asset.classList.add("invalid");
      ok = false;
    } else if (quote !== "BTC" && !isHex(quote)) {
      $("#assets-error").textContent = "Want asset must be a valid hex id.";
      form.elements.quote_asset.classList.add("invalid");
      ok = false;
    } else if (base === quote) {
      $("#assets-error").textContent = "Deposit and want assets must differ.";
      ok = false;
    }
  }
  if (ok) {
    if (!(min > 0) || !(max > 0)) {
      $("#amount-error").textContent = "Min and max must be greater than 0.";
      ok = false;
    } else if (min > max) {
      $("#amount-error").textContent = "Min must be less than or equal to max.";
      form.elements.min_amount.classList.add("invalid");
      ok = false;
    }
  }
  return ok;
}

form.addEventListener("input", updateForm);
form.addEventListener("change", updateForm);

$("#btn-add-pair").addEventListener("click", openAdd);
$("#btn-add-first-pair").addEventListener("click", openAdd);
$$("#pair-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => dialog.close())
);

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  clearFormErrors();
  const base = sideValue("base");
  const quote = sideValue("quote");
  const min = Number(form.elements.min_amount.value);
  const max = Number(form.elements.max_amount.value);
  if (!validateForm(base, quote, min, max)) return;

  const pair = {
    pair: `${base}/${quote}`,
    min_amount: min,
    max_amount: max,
    price_feed: String(form.elements.price_feed.value).trim(),
    invert_price: form.elements.invert_price.checked,
  };
  const mode = form.dataset.mode;
  const submit = $("#pair-submit");
  submit.disabled = true;
  try {
    if (mode === "edit") {
      await api.updatePair(pair);
      toast("Pair updated", "success");
    } else {
      await api.addPair(pair);
      toast("Pair added", "success");
    }
    dialog.close();
    await loadPairs();
  } catch (err) {
    toast(err.message, "error");
  } finally {
    submit.disabled = false;
  }
});

async function deletePair(pair) {
  if (!confirm(`Delete pair ${pair.pair}?`)) return;
  const prev = pairsCache.slice();
  pairsCache = pairsCache.filter((p) => p.pair !== pair.pair);
  renderPairs(pairsCache);
  try {
    await api.removePair(pair.pair);
    toast("Pair deleted", "success", "Undo", async () => {
      try {
        await api.addPair(pair);
        await loadPairs();
      } catch (err) {
        toast(err.message, "error");
      }
    });
  } catch (err) {
    pairsCache = prev;
    renderPairs(pairsCache);
    toast(err.message, "error");
  }
}

// -------- balance --------

async function loadBalance() {
  try {
    const [b, a] = await Promise.all([api.balance(), api.address()]);
    $("#offchain-sats").textContent = fmtSats(b.offchain_settled);
    $("#offchain-btc").textContent = fmtBTC(b.offchain_settled);
    $("#onchain-confirmed").textContent = fmtSats(b.onchain_confirmed);
    $("#onchain-confirmed-btc").textContent = fmtBTC(b.onchain_confirmed);
    $("#onchain-locked").textContent = fmtSats(b.onchain_unconfirmed);
    $("#onchain-locked-btc").textContent = fmtBTC(b.onchain_unconfirmed);
    $("#offchain-address").textContent = a.offchain_address || "—";
    $("#boarding-address").textContent = a.boarding_address || "—";
  } catch (err) {
    toast(err.message, "error");
  }
}

$("#btn-refresh-balance").addEventListener("click", loadBalance);

// -------- history --------

async function loadTrades() {
  const body = $("#trades-body");
  const empty = $("#trades-empty");
  try {
    const data = await api.listTrades(100);
    const trades = data.trades || [];
    if (!trades.length) {
      body.innerHTML = "";
      empty.hidden = false;
      return;
    }
    empty.hidden = true;
    body.innerHTML = "";
    for (const t of trades) {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td title="${escapeAttr(
          new Date(t.created_at * 1000).toISOString()
        )}">${escapeHTML(fmtTime(t.created_at))}</td>
        <td><span class="mono" title="${escapeAttr(t.pair)}">${escapeHTML(
          assetLabel(pairBase(t.pair))
        )} ${icon("arrow-right")} ${escapeHTML(
          assetLabel(pairWantAsset(t.pair))
        )}</span></td>
        <td class="num">${escapeHTML(
          fmtAmount(t.deposit_amount, t.deposit_asset)
        )}</td>
        <td class="num">${escapeHTML(
          fmtAmount(t.want_amount, t.want_asset)
        )}</td>
        <td><code class="mono trunc" title="${escapeAttr(
          t.offer_txid
        )}">${escapeHTML(truncMid(t.offer_txid, 6, 6))}</code></td>
        <td><code class="mono trunc" title="${escapeAttr(
          t.fulfill_txid
        )}">${escapeHTML(truncMid(t.fulfill_txid, 6, 6))}</code></td>`;
      body.appendChild(tr);
    }
  } catch (err) {
    toast(err.message, "error");
    body.innerHTML = "";
    empty.hidden = false;
  }
}

function fmtTime(unixSec) {
  if (!unixSec) return "—";
  const d = new Date(Number(unixSec) * 1000);
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return d.toLocaleString();
}

function fmtAmount(raw, asset) {
  if (raw == null) return "—";
  if (asset === "BTC" || !asset) {
    return `${(Number(raw) / 1e8).toFixed(8)} BTC`;
  }
  // Asset decimals aren't exposed by the API; show raw count + truncated id.
  return `${Number(raw).toLocaleString("en-US")} · ${assetLabel(asset)}`;
}

$("#btn-refresh-trades").addEventListener("click", loadTrades);

// -------- solver keys --------

async function loadKeys() {
  try {
    const k = await api.solverKeys();
    $("#solver-pubkey").textContent = k.solver_pub_key || "—";
    $("#emulator-pubkey").textContent = k.emulator_pub_key || "—";
  } catch (err) {
    toast(err.message, "error");
    $("#solver-pubkey").textContent = "—";
    $("#emulator-pubkey").textContent = "—";
  }
}

$("#btn-refresh-keys").addEventListener("click", loadKeys);

// delegate copy buttons (copies full textContent; CSS handles truncation)
document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-copy]");
  if (!b) return;
  const el = $(b.getAttribute("data-copy"));
  if (el) copy(el.textContent.trim());
});

// -------- utils --------

function escapeHTML(s) {
  return String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      })[c]
  );
}
function escapeAttr(s) {
  return escapeHTML(s);
}

// safeHref returns the URL only if it parses and uses an http(s) scheme.
function safeHref(s) {
  try {
    const u = new URL(String(s ?? ""));
    if (u.protocol === "http:" || u.protocol === "https:") return u.href;
  } catch (_) {}
  return "#";
}

// -------- init --------

hydrateIcons(document);
(async () => {
  await refreshStatus();
  setSection(firstEnabledSection());
})();
setInterval(refreshStatus, 5000);
