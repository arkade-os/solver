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
  "arrow-left-right":
    '<path d="M8 3 4 7l4 4"/><path d="M4 7h16"/><path d="m16 21 4-4-4-4"/><path d="M20 17H4"/>',
  info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
  eye:
    '<path d="M2.06 12.35a1 1 0 0 1 0-.7 10.75 10.75 0 0 1 19.88 0 1 1 0 0 1 0 .7 10.75 10.75 0 0 1-19.88 0"/><circle cx="12" cy="12" r="3"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  alert:
    '<circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/>',
  "wifi-off":
    '<path d="M12 20h.01"/><path d="M8.5 16.43a5 5 0 0 1 7 0"/><path d="M5 12.86a10 10 0 0 1 5.17-2.69"/><path d="M19 12.86a10 10 0 0 0-2.01-1.52"/><path d="M2 8.82a15 15 0 0 1 4.18-2.64"/><path d="M22 8.82a15 15 0 0 0-11.29-3.76"/><path d="m2 2 20 20"/>',
  send:
    '<path d="M14.536 21.686a.5.5 0 0 0 .937-.024l6.5-19a.496.496 0 0 0-.635-.635l-19 6.5a.5.5 0 0 0-.024.937l7.93 3.18a2 2 0 0 1 1.112 1.11z"/><path d="m21.854 2.147-10.94 10.939"/>',
  anchor:
    '<path d="M12 22V8"/><path d="M5 12H2a10 10 0 0 0 20 0h-3"/><circle cx="12" cy="5" r="3"/>',
  "chevron-down": '<path d="m6 9 6 6 6-6"/>',
  settings:
    '<path d="M20 7h-9"/><path d="M14 17H5"/><circle cx="17" cy="17" r="3"/><circle cx="7" cy="7" r="3"/>',
  key:
    '<path d="m15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4"/><path d="m21 2-9.6 9.6"/><circle cx="7.5" cy="15.5" r="5.5"/>',
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
  listMarkets: () => api._req("GET", "/v1/markets"),
  addMarket: (market) => api._req("POST", "/v1/market", { market }),
  updateMarket: (market) => api._req("PUT", "/v1/market", { market }),
  removeMarket: (base, quote) =>
    api._req(
      "DELETE",
      `/v1/market/${encodeURIComponent(base)}/${encodeURIComponent(quote)}`
    ),
  status: () => api._req("GET", "/v1/status"),
  balance: () => api._req("GET", "/v1/balance"),
  address: () => api._req("GET", "/v1/address"),
  listAssets: () => api._req("GET", "/v1/assets"),
  sendOffchain: (body) => api._req("POST", "/v1/wallet/send", body),
  collaborativeExit: (body) => api._req("POST", "/v1/wallet/exit", body),
  settle: (body) => api._req("POST", "/v1/wallet/settle", body),
  listTrades: (limit = 100, status = "") =>
    api._req("GET", `/v1/trades?${new URLSearchParams({ limit, status })}`),
  config: () => api._req("GET", "/v1/config"),
  dumpSeed: (password) => api._req("POST", "/v1/wallet/dump", { password }),
  card: (name) => api._req("GET", `/v1/card?name=${encodeURIComponent(name)}`),
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

// marketBase / marketQuote split a "base/quote" market id (used by trade rows,
// which carry the market id plus the concrete deposit/want assets).
function marketBase(name) {
  if (!name) return "";
  const i = name.indexOf("/");
  return i < 0 ? name : name.slice(0, i);
}
function marketQuote(name) {
  if (!name) return "";
  const i = name.indexOf("/");
  return i < 0 ? "" : name.slice(i + 1);
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("Copied to clipboard", "success");
  } catch (_) {
    toast("Copy failed", "error");
  }
}

// -------- connection banner --------

function setConnected(ok) {
  $("#conn-banner").hidden = ok;
}

// refreshStatus pings the daemon and toggles the "disconnected" banner.
async function refreshStatus() {
  try {
    await api.status();
    setConnected(true);
  } catch (_) {
    setConnected(false);
  }
}

// -------- navigation --------

function setSection(name) {
  $$(".nav-item").forEach((b) => {
    const active = b.dataset.section === name;
    b.toggleAttribute("aria-current", active);
    if (active) b.setAttribute("aria-current", "page");
  });
  $$(".view").forEach((v) => (v.hidden = true));
  const target = $(`#view-${name}`);
  if (target) target.hidden = false;

  if (name === "wallet") loadWallet();
  if (name === "markets") loadMarkets();
  if (name === "history") loadTrades();
  if (name === "settings") loadConfig();
}

$("#nav").addEventListener("click", (e) => {
  const btn = e.target.closest(".nav-item");
  if (btn) setSection(btn.dataset.section);
});

// -------- markets --------

let marketsCache = [];

// assetMeta maps a known asset id to its metadata (ticker/icon/decimals) so
// markets can show real tickers and icons instead of a raw hex id.
let assetMeta = new Map();

// metaFor returns metadata for a market side; BTC and unknown assets fall back
// to a minimal record.
function metaFor(id) {
  if (id === "BTC") return { asset_id: "BTC", ticker: "BTC", decimals: 8 };
  return assetMeta.get(id) || { asset_id: id };
}

// assetDisplay resolves an id (or "BTC") to its ticker/name when known, else a
// short truncated id — used wherever we'd otherwise show a raw hex id.
function assetDisplay(id) {
  if (id === "BTC") return "BTC";
  const m = metaFor(id);
  return m.ticker || m.name || assetLabel(id);
}

// amountParts splits a raw bound into its formatted number and unit, so a
// min–max range can render the unit once. Scales by decimals, drops trailing
// zeros, and uses the ticker as the unit when the asset is known.
function amountParts(raw, meta) {
  if (meta.asset_id === "BTC") {
    return { num: Number(raw).toLocaleString("en-US"), unit: "sats" };
  }
  const d = Number(meta.decimals || 0);
  const num =
    d > 0
      ? (Number(raw) / 10 ** d).toLocaleString("en-US", {
          minimumFractionDigits: 0,
          maximumFractionDigits: d,
        })
      : Number(raw).toLocaleString("en-US");
  return { num, unit: meta.ticker || "units" };
}

async function loadMarkets() {
  const grid = $("#markets-grid");
  const empty = $("#markets-empty");
  try {
    const [data, assetsResp] = await Promise.all([
      api.listMarkets(),
      api.listAssets().catch(() => ({ assets: [] })),
    ]);
    assetMeta = new Map((assetsResp.assets || []).map((a) => [a.asset_id, a]));
    marketsCache = data.markets || [];
    renderMarkets(marketsCache);
  } catch (err) {
    toast(err.message, "error");
    grid.innerHTML = "";
    grid.hidden = true;
    empty.hidden = false;
    $("#markets-count").hidden = true;
  }
}

// feedHost shows just the host of a price-feed URL, falling back to the raw
// string when it doesn't parse.
function feedHost(url) {
  try {
    return new URL(url).host || url;
  } catch (_) {
    return url;
  }
}

// autoPricePath mirrors pricefeed.DefaultPricePath in pkg/swap/pricefeed.
function autoPricePath(feed) {
  const raw = String(feed || "").trim();
  if (!raw) return null;
  if (/binance/i.test(raw)) return "/price";
  try {
    const q = new URL(raw).searchParams;
    const ids = q.get("ids");
    const currencies = q.get("vs_currencies");
    if (!ids || !currencies) return null;
    return `/${ids.split(",")[0]}/${currencies.split(",")[0]}`;
  } catch (_) {
    return null;
  }
}

// bandGeom maps a slippage in basis points to a symmetric band centered on the
// feed marker. 500 bps (5%) or wider spans the whole track; a floor keeps tiny
// tolerances visible. Returns percentages of the track width.
function bandGeom(bps) {
  const pct = (Number(bps) || DEFAULT_SLIPPAGE_BPS) / 100;
  const half = Math.max(Math.min(pct / 5, 1) * 50, 3);
  return { left: 50 - half, width: half * 2 };
}

// marketId is the canonical "base/quote" identifier used for delete + dedup.
function marketId(m) {
  return `${m.base_asset}/${m.quote_asset}`;
}

// dirStat renders one direction block on a market card. A direction is enabled
// when its want-side max is non-zero; otherwise it reads "Disabled".
function dirStat(label, enabled, minRaw, maxRaw, meta) {
  if (!enabled) {
    return `<div class="mstat"><span class="mstat-k">${escapeHTML(
      label
    )}</span><span class="mstat-v off">Disabled</span></div>`;
  }
  const min = amountParts(minRaw, meta);
  const max = amountParts(maxRaw, meta);
  const v = `${min.num} – ${max.num} ${max.unit}`;
  return `<div class="mstat"><span class="mstat-k">${escapeHTML(
    label
  )}</span><span class="mstat-v" title="${escapeAttr(v)}">${escapeHTML(
    v
  )}</span></div>`;
}

function renderMarkets(markets) {
  const grid = $("#markets-grid");
  const empty = $("#markets-empty");
  const count = $("#markets-count");
  if (!markets.length) {
    grid.innerHTML = "";
    grid.hidden = true;
    empty.hidden = false;
    count.hidden = true;
    return;
  }
  empty.hidden = true;
  grid.hidden = false;
  count.hidden = false;
  count.textContent = `${markets.length} ${
    markets.length === 1 ? "market" : "markets"
  }`;
  grid.innerHTML = "";
  for (const m of markets) {
    const base = m.base_asset;
    const quote = m.quote_asset;
    const baseMeta = metaFor(base);
    const quoteMeta = metaFor(quote);
    // Prefer a real ticker/name; fall back to a short truncated id.
    const baseLabel = baseMeta.ticker || baseMeta.name || assetLabel(base, 4, 4);
    const quoteLabel =
      quoteMeta.ticker || quoteMeta.name || assetLabel(quote, 4, 4);
    // Sell-base: deposit base, want quote (quote-denominated bounds).
    // Buy-base: deposit quote, want base (base-denominated bounds).
    const sellOn = Number(m.max_quote_amount) > 0;
    const buyOn = Number(m.max_base_amount) > 0;
    const band = bandGeom(m.slippage_bps);
    const card = document.createElement("article");
    card.className = "market";
    card.dataset.market = marketId(m);
    card.innerHTML = `
      <div class="market-head">
        <div class="market-flow">
          <div class="market-side">
            ${assetAvatar(baseMeta)}
            <span class="market-sym"><b title="${escapeAttr(base)}">${escapeHTML(
              baseLabel
            )}</b><small>Base</small></span>
          </div>
          ${icon("arrow-left-right", "market-flow-arrow")}
          <div class="market-side">
            ${assetAvatar(quoteMeta)}
            <span class="market-sym"><b title="${escapeAttr(quote)}">${escapeHTML(
              quoteLabel
            )}</b><small>Quote</small></span>
          </div>
        </div>
        <div class="market-actions">
          <button class="icon-btn" data-edit aria-label="Edit market">${icon(
            "edit"
          )}</button>
          <button class="icon-btn btn-danger" data-del aria-label="Delete market">${icon(
            "trash"
          )}</button>
        </div>
      </div>

      <div class="market-stats">
        ${dirStat(
          `${baseLabel} → ${quoteLabel}`,
          sellOn,
          m.min_quote_amount,
          m.max_quote_amount,
          quoteMeta
        )}
        ${dirStat(
          `${quoteLabel} → ${baseLabel}`,
          buyOn,
          m.min_base_amount,
          m.max_base_amount,
          baseMeta
        )}
      </div>

      <div class="market-band">
        <div class="band-head">
          <span>Fill tolerance</span>
          <span class="band-pct">±${escapeHTML(fmtSlippage(m.slippage_bps))}</span>
        </div>
        <div class="band-track">
          <div class="band-fill" style="left:${band.left}%;width:${band.width}%"></div>
          <div class="band-mark"></div>
        </div>
      </div>

      <div class="market-foot">
        <a class="feed-link" href="${escapeAttr(safeHref(m.price_feed))}"
          target="_blank" rel="noreferrer noopener" title="${escapeAttr(
            `${m.price_feed}\n${
              m.price_path
                ? `price at ${m.price_path}`
                : `price at ${autoPricePath(m.price_feed) || "?"} (auto)`
            }`
          )}">${icon("external")}<span>${escapeHTML(feedHost(m.price_feed))}</span></a>
        ${
          Number(m.fee_bps) > 0
            ? `<span class="badge on" title="Solver margin">Fee ${escapeHTML(
                fmtSlippage(m.fee_bps)
              )}</span>`
            : ""
        }
      </div>`;
    card.querySelector("[data-edit]").addEventListener("click", () => openEdit(m));
    card
      .querySelector("[data-del]")
      .addEventListener("click", () => deleteMarket(m));
    grid.appendChild(card);
  }
}

// DEFAULT_SLIPPAGE_BPS mirrors the server's DefaultSlippageBps (0/unset).
const DEFAULT_SLIPPAGE_BPS = 10;

// fmtSlippage renders basis points as a percentage; 0/unset means the server
// default.
function fmtSlippage(bps) {
  const n = Number(bps) || DEFAULT_SLIPPAGE_BPS;
  return `${n / 100}%`;
}

// -------- market dialog --------

const dialog = $("#market-dialog");
const form = $("#market-form");

// sideValue resolves a base/quote side to "BTC" or a normalized hex asset id.
function sideValue(side) {
  const kind = form.elements[`${side}_kind`].value;
  if (kind === "BTC") return "BTC";
  return String(form.elements[`${side}_asset`].value || "")
    .trim()
    .toLowerCase()
    .replace(/^0x/, "");
}

// dirEnabled reports whether a direction's toggle ("sell" | "buy") is on.
function dirEnabled(dir) {
  return form.elements[`${dir}_enabled`].checked;
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
  const baseDisp = base ? assetDisplay(base) : "an asset";
  const quoteDisp = quote ? assetDisplay(quote) : "an asset";

  // per-direction descriptions + want-side unit suffixes.
  $("#sell-desc").textContent = `Deposit ${baseDisp}, receive ${quoteDisp}.`;
  $("#buy-desc").textContent = `Deposit ${quoteDisp}, receive ${baseDisp}.`;
  const quoteUnit = quote === "BTC" ? "sats" : "units";
  const baseUnit = base === "BTC" ? "sats" : "units";
  $$("[data-quote-unit]").forEach((el) => (el.textContent = quoteUnit));
  $$("[data-base-unit]").forEach((el) => (el.textContent = baseUnit));
  $("#sell-hint").textContent = `Bounds apply to the ${quoteDisp} amount makers want${
    quote === "BTC" ? " (BTC dust ≥ 330 sats)" : ""
  }.`;
  $("#buy-hint").textContent = `Bounds apply to the ${baseDisp} amount makers want${
    base === "BTC" ? " (BTC dust ≥ 330 sats)" : ""
  }.`;

  // enable/disable each direction's inputs from its toggle.
  ["sell", "buy"].forEach((dir) => {
    const on = dirEnabled(dir);
    $(`#${dir}-section`).classList.toggle("off", !on);
    $$(`[data-dir-fields="${dir}"] input`).forEach((i) => (i.disabled = !on));
  });

  // feed hint reflects the configured slippage.
  $("#feed-hint").textContent = `The solver polls this URL for the quote-per-base price and fulfills offers within ${fmtSlippage(
    form.elements.slippage_bps.value
  )}.`;

  const auto = autoPricePath(form.elements.price_feed.value);
  const pathField = form.elements.price_path;
  pathField.required = !auto;
  pathField.placeholder = auto || "/bitcoin/usd";
  $("#path-hint").textContent = auto
    ? `Not needed — the solver reads ${auto} from this feed. Set a pointer to override.`
    : "JSON pointer to the price in the feed response.";
  $("#path-field").classList.toggle("optional", Boolean(auto));

  // live preview.
  $("#preview-market-str").textContent = `${
    base ? assetDisplay(base) : "?"
  } / ${quote ? assetDisplay(quote) : "?"}`;
  const flows = [];
  if (dirEnabled("sell")) {
    flows.push(
      `deposit <strong>${escapeHTML(baseDisp)}</strong> for <strong>${escapeHTML(
        quoteDisp
      )}</strong>`
    );
  }
  if (dirEnabled("buy")) {
    flows.push(
      `deposit <strong>${escapeHTML(quoteDisp)}</strong> for <strong>${escapeHTML(
        baseDisp
      )}</strong>`
    );
  }
  $("#preview-text").innerHTML = flows.length
    ? `The solver fulfills offers that ${flows.join(" or ")}.`
    : "Enable at least one direction to preview this market.";

  // fill-tolerance band mirrors the market card.
  $("#preview-band-pct").textContent = `±${fmtSlippage(
    form.elements.slippage_bps.value
  )}`;
  const bg = bandGeom(form.elements.slippage_bps.value);
  const bf = $("#preview-band-fill");
  bf.style.left = `${bg.left}%`;
  bf.style.width = `${bg.width}%`;
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

const DIR_FIELDS = [
  "min_quote_amount",
  "max_quote_amount",
  "min_base_amount",
  "max_base_amount",
];

function clearFormErrors() {
  ["assets", "sell", "buy"].forEach(
    (k) => ($(`#${k}-error`).textContent = "")
  );
  ["base_asset", "quote_asset", ...DIR_FIELDS].forEach((n) =>
    form.elements[n].classList.remove("invalid")
  );
}

// populateAssetDatalist fills the shared <datalist> with the wallet's known
// assets so operators can pick a market side by ticker instead of pasting hex.
function populateAssetDatalist() {
  const dl = $("#known-assets");
  if (!dl) return;
  dl.innerHTML = "";
  for (const [id, meta] of assetMeta) {
    const opt = document.createElement("option");
    opt.value = id;
    opt.label = meta.ticker || meta.name || id;
    dl.appendChild(opt);
  }
}

function openAdd() {
  form.reset();
  populateAssetDatalist();
  form.dataset.mode = "add";
  setAssetsLocked(false);
  // defaults: base BTC, quote asset, both directions on.
  form.elements.base_kind.value = "BTC";
  form.elements.quote_kind.value = "asset";
  form.elements.sell_enabled.checked = true;
  form.elements.buy_enabled.checked = true;
  clearFormErrors();
  $("#dialog-title").textContent = "Add market";
  $("#market-submit").textContent = "Add market";
  updateForm();
  dialog.showModal();
}

function openEdit(m) {
  form.reset();
  populateAssetDatalist();
  form.dataset.mode = "edit";
  const base = m.base_asset;
  const quote = m.quote_asset;
  form.elements.base_kind.value = base === "BTC" ? "BTC" : "asset";
  form.elements.quote_kind.value = quote === "BTC" ? "BTC" : "asset";
  form.elements.base_asset.value = base === "BTC" ? "" : base;
  form.elements.quote_asset.value = quote === "BTC" ? "" : quote;

  const sellOn = Number(m.max_quote_amount) > 0;
  const buyOn = Number(m.max_base_amount) > 0;
  form.elements.sell_enabled.checked = sellOn;
  form.elements.buy_enabled.checked = buyOn;
  form.elements.min_quote_amount.value = sellOn ? m.min_quote_amount : "";
  form.elements.max_quote_amount.value = sellOn ? m.max_quote_amount : "";
  form.elements.min_base_amount.value = buyOn ? m.min_base_amount : "";
  form.elements.max_base_amount.value = buyOn ? m.max_base_amount : "";

  form.elements.price_feed.value = m.price_feed;
  form.elements.price_path.value = m.price_path || "";
  form.elements.slippage_bps.value = m.slippage_bps || "";
  form.elements.fee_bps.value = m.fee_bps || "";
  setAssetsLocked(true); // identity can't change on edit
  clearFormErrors();
  $("#dialog-title").textContent = "Edit market";
  $("#market-submit").textContent = "Save changes";
  updateForm();
  dialog.showModal();
}

// validateDirection checks one enabled direction's min/max, writing to its
// error slot. Returns true when valid (or disabled).
function validateDirection(dir, minEl, maxEl, errId) {
  if (!dirEnabled(dir)) return true;
  const min = Number(form.elements[minEl].value);
  const max = Number(form.elements[maxEl].value);
  if (!(min > 0) || !(max > 0)) {
    $(errId).textContent = "Min and max must be greater than 0.";
    return false;
  }
  if (min > max) {
    $(errId).textContent = "Min must be less than or equal to max.";
    form.elements[minEl].classList.add("invalid");
    return false;
  }
  return true;
}

function validateForm(base, quote) {
  const isHex = (s) => /^[0-9a-f]+$/.test(s) && s.length % 2 === 0;
  if (!form.dataset.locked) {
    if (base !== "BTC" && !isHex(base)) {
      $("#assets-error").textContent = "Base asset must be a valid hex id.";
      form.elements.base_asset.classList.add("invalid");
      return false;
    }
    if (quote !== "BTC" && !isHex(quote)) {
      $("#assets-error").textContent = "Quote asset must be a valid hex id.";
      form.elements.quote_asset.classList.add("invalid");
      return false;
    }
    if (base === quote) {
      $("#assets-error").textContent = "Base and quote assets must differ.";
      return false;
    }
  }
  if (!dirEnabled("sell") && !dirEnabled("buy")) {
    $("#sell-error").textContent = "Enable at least one direction.";
    return false;
  }
  const okSell = validateDirection(
    "sell",
    "min_quote_amount",
    "max_quote_amount",
    "#sell-error"
  );
  const okBuy = validateDirection(
    "buy",
    "min_base_amount",
    "max_base_amount",
    "#buy-error"
  );
  return okSell && okBuy;
}

form.addEventListener("input", updateForm);
form.addEventListener("change", updateForm);

$("#btn-add-market").addEventListener("click", openAdd);
$("#btn-add-first-market").addEventListener("click", openAdd);
$$("#market-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => dialog.close())
);

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  clearFormErrors();
  const base = sideValue("base");
  const quote = sideValue("quote");
  if (!validateForm(base, quote)) return;

  const sellOn = dirEnabled("sell");
  const buyOn = dirEnabled("buy");
  const num = (n) => Number(form.elements[n].value) || 0;
  const market = {
    base_asset: base,
    quote_asset: quote,
    min_quote_amount: sellOn ? num("min_quote_amount") : 0,
    max_quote_amount: sellOn ? num("max_quote_amount") : 0,
    min_base_amount: buyOn ? num("min_base_amount") : 0,
    max_base_amount: buyOn ? num("max_base_amount") : 0,
    price_feed: String(form.elements.price_feed.value).trim(),
    price_path: String(form.elements.price_path.value).trim(),
    slippage_bps: Number(form.elements.slippage_bps.value) || 0,
    fee_bps: Number(form.elements.fee_bps.value) || 0,
  };
  const mode = form.dataset.mode;
  const submit = $("#market-submit");
  submit.disabled = true;
  try {
    if (mode === "edit") {
      await api.updateMarket(market);
      toast("Market updated", "success");
    } else {
      await api.addMarket(market);
      toast("Market added", "success");
    }
    dialog.close();
    await loadMarkets();
  } catch (err) {
    toast(err.message, "error");
  } finally {
    submit.disabled = false;
  }
});

async function deleteMarket(m) {
  if (!confirm(`Delete market ${marketId(m)}?`)) return;
  const prev = marketsCache.slice();
  marketsCache = marketsCache.filter((x) => marketId(x) !== marketId(m));
  renderMarkets(marketsCache);
  try {
    await api.removeMarket(m.base_asset, m.quote_asset);
    toast("Market deleted", "success", "Undo", async () => {
      try {
        await api.addMarket(m);
        await loadMarkets();
      } catch (err) {
        toast(err.message, "error");
      }
    });
  } catch (err) {
    marketsCache = prev;
    renderMarkets(marketsCache);
    toast(err.message, "error");
  }
}

// -------- wallet --------

// wallet holds the latest snapshot used by the send dialog (BTC balance + the
// list of held assets with their metadata).
const wallet = { offchainSats: 0, assets: [] };

async function loadWallet() {
  try {
    const [b, a, assetsResp] = await Promise.all([
      api.balance(),
      api.address(),
      api.listAssets(),
    ]);
    wallet.offchainSats = Number(b.offchain_settled || 0);
    wallet.assets = assetsResp.assets || [];

    $("#offchain-address").textContent = a.offchain_address || "—";
    $("#boarding-address").textContent = a.boarding_address || "—";

    // BTC is rendered as the first row, just another asset. Its extra layers
    // (onchain confirmed/locked, offchain pending) live in an expandable detail
    // since the asset API exposes no equivalent dimensions for other assets.
    const btcRow = {
      asset_id: "BTC",
      ticker: "BTC",
      decimals: 8,
      balance: Number(b.offchain_settled || 0),
      detail: {
        onchainConfirmed: Number(b.onchain_confirmed || 0),
        onchainLocked: Number(b.onchain_unconfirmed || 0),
        offchainPending: Number(b.offchain_pending || 0),
      },
    };
    renderBalances([btcRow, ...wallet.assets]);
  } catch (err) {
    toast(err.message, "error");
  }
}

// assetTicker returns the best short label for an asset: its ticker, else its
// name, else a truncated id.
function assetTicker(a) {
  return a.ticker || a.name || assetLabel(a.asset_id);
}

// btcAvatar is the official Bitcoin logo (orange roundel + ₿), used for the BTC
// row instead of a generated monogram.
const btcAvatar = `<span class="asset-avatar btc"><svg viewBox="0 0 64 64" aria-hidden="true"><circle cx="32" cy="32" r="32" fill="#F7931A"/><path fill="#fff" d="M46.1 27.4c.6-4.2-2.6-6.5-7-8l1.4-5.7-3.5-.9-1.4 5.6c-.9-.2-1.9-.4-2.8-.6l1.4-5.7-3.5-.9-1.4 5.7c-.7-.2-1.5-.3-2.2-.5l-4.8-1.2-.9 3.7s2.6.6 2.5.6c1.4.4 1.7 1.3 1.6 2.1l-1.6 6.5c.1 0 .2.1.4.1-.1 0-.3-.1-.4-.1l-2.3 9c-.2.4-.6 1.1-1.6.8 0 .1-2.5-.6-2.5-.6l-1.7 4 4.5 1.1c.8.2 1.7.4 2.5.6l-1.4 5.8 3.5.9 1.4-5.7c1 .3 1.9.5 2.8.7l-1.4 5.6 3.5.9 1.4-5.8c6 1.1 10.5.7 12.4-4.7 1.5-4.4-.1-6.9-3.2-8.5 2.3-.5 4-2 4.5-5.1zm-8 11.2c-1.1 4.4-8.5 2-10.8 1.4l1.9-7.6c2.4.6 10 1.7 8.9 6.2zm1.1-11.3c-1 4-7.1 2-9.1 1.5l1.7-6.9c2 .5 8.4 1.4 7.4 5.4z"/></svg></span>`;

// assetAvatar renders the asset icon: the official BTC logo for BTC, the issuer's
// icon_url when present, else a deterministic colored monogram derived from the id.
function assetAvatar(a) {
  if (a.asset_id === "BTC") return btcAvatar;
  if (a.icon_url && /^https?:\/\//.test(a.icon_url)) {
    return `<img class="asset-avatar" src="${escapeAttr(
      a.icon_url
    )}" alt="" loading="lazy" />`;
  }
  let hash = 0;
  for (const ch of a.asset_id || "") hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
  const hue = hash % 360;
  const mono = (a.ticker || a.asset_id || "?").slice(0, 2).toUpperCase();
  return `<span class="asset-avatar gen" style="--h:${hue}">${escapeHTML(
    mono
  )}</span>`;
}

// fmtAssetAmount scales a raw smallest-unit balance by the asset's decimals and
// appends its ticker. With decimals=0 it renders the raw integer.
function fmtAssetAmount(raw, decimals, ticker) {
  const n = Number(raw || 0);
  const d = Number(decimals || 0);
  // Trailing zeros are dropped (min 0, max d fraction digits).
  const valStr =
    d > 0
      ? (n / 10 ** d).toLocaleString("en-US", {
          minimumFractionDigits: 0,
          maximumFractionDigits: d,
        })
      : n.toLocaleString("en-US");
  return ticker ? `${valStr} ${ticker}` : valStr;
}

// renderBalances renders one row per asset (BTC first, as a regular asset).
// Rows carrying a `detail` object get an expandable sub-row with their extra
// balance layers; today only BTC has one.
function renderBalances(rows) {
  const body = $("#balances-body");
  body.innerHTML = "";
  for (const a of rows) {
    const isBTC = a.asset_id === "BTC";
    const decimals = Number(a.decimals || 0);
    // realLabel is a genuine ticker/name; empty when the asset has no metadata.
    // We use it as the amount unit and only then show the id on its own line, so
    // a metadata-less asset isn't rendered with its id repeated three times.
    const realLabel = isBTC ? "BTC" : a.ticker || a.name || "";
    const label = realLabel || assetLabel(a.asset_id);
    const primary = fmtAssetAmount(a.balance, decimals, realLabel);
    // Secondary line shows the raw smallest-unit value when decimals hide it.
    const sub = isBTC
      ? fmtSats(a.balance)
      : decimals > 0
      ? Number(a.balance || 0).toLocaleString("en-US")
      : "";
    const expandable = !!a.detail;

    const tr = document.createElement("tr");
    tr.className = "balance-row";
    tr.innerHTML = `
      <td>
        <div class="asset-cell">
          ${
            expandable
              ? `<button class="expand-toggle" aria-label="Toggle details" aria-expanded="false">${icon(
                  "chevron-down"
                )}</button>`
              : `<span class="expand-spacer"></span>`
          }
          ${assetAvatar(a)}
          <div class="asset-meta">
            <span class="asset-ticker">${escapeHTML(label)}</span>
            ${
              realLabel && !isBTC
                ? `<code class="mono trunc asset-id" title="${escapeAttr(
                    a.asset_id
                  )}">${escapeHTML(assetLabel(a.asset_id))}</code>`
                : ""
            }
          </div>
        </div>
      </td>
      <td class="num">
        <div class="bal-primary">${escapeHTML(primary)}</div>
        ${sub ? `<div class="bal-sub">${escapeHTML(sub)}</div>` : ""}
      </td>
      <td class="actions-col">
        <button class="icon-btn" data-send-asset aria-label="Send ${escapeAttr(
          label
        )}">${icon("send")}</button>
      </td>`;
    tr.querySelector("[data-send-asset]").addEventListener("click", () =>
      openSend(a.asset_id)
    );
    body.appendChild(tr);

    if (expandable) {
      const detail = document.createElement("tr");
      detail.className = "balance-detail";
      detail.hidden = true;
      detail.innerHTML = `
        <td colspan="3">
          <div class="detail-grid">
            <div class="detail-item">
              <span class="detail-label">Onchain confirmed</span>
              <span class="detail-val tnum">${escapeHTML(
                fmtSats(a.detail.onchainConfirmed)
              )}</span>
            </div>
            <div class="detail-item">
              <span class="detail-label">Onchain locked</span>
              <span class="detail-val tnum">${escapeHTML(
                fmtSats(a.detail.onchainLocked)
              )}</span>
            </div>
            <div class="detail-item">
              <span class="detail-label">Offchain pending</span>
              <span class="detail-val tnum">${escapeHTML(
                fmtSats(a.detail.offchainPending)
              )}</span>
            </div>
          </div>
        </td>`;
      body.appendChild(detail);

      const toggle = tr.querySelector(".expand-toggle");
      toggle.addEventListener("click", () => {
        const open = detail.hidden;
        detail.hidden = !open;
        tr.classList.toggle("expanded", open);
        toggle.setAttribute("aria-expanded", String(open));
      });
    }
  }
}

$("#btn-refresh-balance").addEventListener("click", loadWallet);

// -------- send dialog --------

const sendDialog = $("#send-dialog");
const sendForm = $("#send-form");

// sendLayer returns the selected destination layer ("offchain" | "onchain").
function sendLayer() {
  return sendForm.elements.layer.value;
}

// selectedAsset returns the asset id chosen in the picker ("BTC" or hex id).
function selectedAsset() {
  if (sendLayer() === "onchain") return "BTC";
  return sendForm.elements.asset.value || "BTC";
}

function populateSendAssets(preset) {
  const sel = $("#send-asset");
  sel.innerHTML = "";
  const opts = [{ value: "BTC", label: "BTC" }];
  for (const a of wallet.assets) {
    opts.push({ value: a.asset_id, label: assetTicker(a) });
  }
  for (const o of opts) {
    const el = document.createElement("option");
    el.value = o.value;
    el.textContent = o.label;
    sel.appendChild(el);
  }
  sel.value = preset && opts.some((o) => o.value === preset) ? preset : "BTC";
}

function assetBalanceOf(id) {
  if (id === "BTC") return wallet.offchainSats;
  const a = wallet.assets.find((x) => x.asset_id === id);
  return a ? Number(a.balance) : 0;
}

function updateSendForm() {
  const onchain = sendLayer() === "onchain";
  const assetField = $("#send-asset").closest(".field");
  // Onchain exit is BTC-only.
  assetField.hidden = onchain;
  $("#send-asset").disabled = onchain;

  const asset = selectedAsset();
  const isBtc = asset === "BTC";
  $("#send-amount-suffix").textContent = isBtc ? "sats" : "units";
  $("#send-layer-hint").textContent = onchain
    ? "Collaborative exit: moves BTC to an onchain address via a batch round."
    : "Send to an Ark address. Settles instantly off-chain.";

  const avail = assetBalanceOf(asset);
  const unit = isBtc ? "sats" : "units";
  $("#send-available").textContent = `Available: ${Number(avail).toLocaleString(
    "en-US"
  )} ${unit}`;

  const addr = sendForm.elements.address;
  addr.placeholder = onchain ? "bcrt1… / bc1…" : "ark1… / tark1…";
}

function openSend(presetAsset) {
  sendForm.reset();
  $("#send-error").textContent = "";
  sendForm.elements.layer.value = "offchain";
  populateSendAssets(presetAsset);
  updateSendForm();
  sendDialog.showModal();
}

sendForm.addEventListener("change", updateSendForm);
sendForm.addEventListener("input", updateSendForm);
$("#btn-send").addEventListener("click", () => openSend());
$$("#send-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => sendDialog.close())
);

sendForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#send-error").textContent = "";
  const onchain = sendLayer() === "onchain";
  const asset = selectedAsset();
  const address = String(sendForm.elements.address.value).trim();
  const amount = Number(sendForm.elements.amount.value);
  const password = sendForm.elements.password.value;

  if (!address) {
    $("#send-error").textContent = "Destination address is required.";
    return;
  }
  if (!(amount > 0)) {
    $("#send-error").textContent = "Amount must be greater than 0.";
    return;
  }
  if (amount > assetBalanceOf(asset)) {
    $("#send-error").textContent = "Amount exceeds the available balance.";
    return;
  }

  const submit = $("#send-submit");
  submit.disabled = true;
  try {
    let res;
    if (onchain) {
      res = await api.collaborativeExit({ password, address, amount });
    } else {
      res = await api.sendOffchain({
        password,
        address,
        asset_id: asset,
        amount,
      });
    }
    sendDialog.close();
    toast(`Sent — ${truncMid(res.txid || "", 8, 8)}`, "success");
    await loadWallet();
  } catch (err) {
    $("#send-error").textContent = err.message;
  } finally {
    submit.disabled = false;
  }
});

// -------- settle dialog --------

const settleDialog = $("#settle-dialog");
const settleForm = $("#settle-form");

$("#btn-settle").addEventListener("click", () => {
  settleForm.reset();
  $("#settle-error").textContent = "";
  settleDialog.showModal();
});
$$("#settle-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => settleDialog.close())
);

settleForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#settle-error").textContent = "";
  const password = settleForm.elements.password.value;
  const submit = $("#settle-submit");
  submit.disabled = true;
  submit.textContent = "Settling…";
  try {
    const res = await api.settle({ password });
    settleDialog.close();
    toast(`Settled — ${truncMid(res.txid || "", 8, 8)}`, "success");
    await loadWallet();
  } catch (err) {
    $("#settle-error").textContent = err.message;
  } finally {
    submit.disabled = false;
    submit.textContent = "Settle now";
  }
});

// -------- history --------

// assetChip renders a compact avatar + ticker/label for one side of a trade,
// mirroring the market cards' flow.
function assetChip(id) {
  const m = metaFor(id);
  const label = m.ticker || m.name || assetLabel(id, 4, 4);
  return `<span class="tf-side">${assetAvatar(m)}<b title="${escapeAttr(
    id
  )}">${escapeHTML(label)}</b></span>`;
}

// txChip renders a labelled, click-to-copy transaction id (or a muted dash when
// the id is missing).
function txChip(label, txid) {
  if (!txid) return `<span class="tx-none">${escapeHTML(label)} —</span>`;
  return `<button type="button" class="tx-chip" data-copy-text="${escapeAttr(
    txid
  )}" title="Copy ${escapeHTML(label.toLowerCase())} txid — ${escapeAttr(
    txid
  )}"><span>${escapeHTML(label)}</span><code>${escapeHTML(
    truncMid(txid, 6, 6)
  )}</code></button>`;
}

async function loadTrades() {
  const body = $("#trades-body");
  const empty = $("#trades-empty");
  const status = $("#trades-filter").value;
  try {
    const [data, assetsResp] = await Promise.all([
      api.listTrades(100, status),
      api.listAssets().catch(() => ({ assets: [] })),
    ]);
    assetMeta = new Map((assetsResp.assets || []).map((a) => [a.asset_id, a]));
    const trades = data.trades || [];
    if (!trades.length) {
      body.innerHTML = "";
      $("#trades-empty-title").textContent =
        status === "failed"
          ? "No failed attempts."
          : status === "succeeded"
          ? "No successful attempts."
          : "No attempts yet.";
      empty.hidden = false;
      return;
    }
    empty.hidden = true;
    body.innerHTML = "";
    for (const t of trades) {
      const dep = t.deposit_asset || marketBase(t.market);
      const want = t.want_asset || marketQuote(t.market);
      const tr = document.createElement("tr");
      if (t.error) tr.className = "trade-failed";
      tr.innerHTML = `
        <td class="trade-time" title="${escapeAttr(
          new Date(t.created_at * 1000).toISOString()
        )}">${escapeHTML(fmtTime(t.created_at))}</td>
        <td>
          <div class="trade-flow">
            ${assetChip(dep)}${icon("arrow-right", "tf-arrow")}${assetChip(want)}
          </div>
        </td>
        <td class="num trade-amt">${escapeHTML(
          fmtAmount(t.deposit_amount, dep)
        )}</td>
        <td class="num trade-amt">${escapeHTML(
          fmtAmount(t.want_amount, want)
        )}</td>
        <td>
          <div class="tx-chips">
            ${txChip("Offer", t.offer_txid)}${txChip("Fill", t.fulfill_txid)}
          </div>
        </td>
        <td>${
          t.error
            ? `<span class="badge err" title="${escapeAttr(
                t.error
              )}">${escapeHTML(t.error)}</span>`
            : `<span class="badge on">Filled</span>`
        }</td>`;
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
    return fmtAssetAmount(raw, 8, "BTC");
  }
  // Scale by decimals and show the ticker when the asset is known; otherwise
  // fall back to a raw count + truncated id.
  const meta = metaFor(asset);
  if (meta.ticker || meta.decimals) {
    return fmtAssetAmount(raw, meta.decimals, meta.ticker || assetLabel(asset));
  }
  return `${Number(raw).toLocaleString("en-US")} · ${assetLabel(asset)}`;
}

$("#btn-refresh-trades").addEventListener("click", loadTrades);
$("#trades-filter").addEventListener("change", loadTrades);

// delegate copy buttons: [data-copy] copies a target element's text,
// [data-copy-text] copies its own attribute value (for dynamic rows).
document.addEventListener("click", (e) => {
  const t = e.target.closest("[data-copy-text]");
  if (t) {
    copy(t.getAttribute("data-copy-text"));
    return;
  }
  const b = e.target.closest("[data-copy]");
  if (!b) return;
  const el = $(b.getAttribute("data-copy"));
  if (el) copy(el.textContent.trim());
});

// -------- settings --------

const CONFIG_FIELDS = [
  ["ark_url", "Ark URL"],
  ["emulator_url", "Emulator URL"],
  ["explorer_url", "Explorer URL"],
  ["datadir", "Data directory"],
  ["grpc_port", "gRPC port"],
  ["http_port", "HTTP port"],
  ["log_level", "Log level"],
];

async function loadConfig() {
  const list = $("#config-list");
  try {
    const cfg = await api.config();
    list.innerHTML = CONFIG_FIELDS.map(([k, label]) => {
      const val = cfg[k] === "" || cfg[k] == null ? "—" : String(cfg[k]);
      return `<div class="card-row">
        <div class="card-row-label">${escapeHTML(label)}</div>
        <div class="card-row-value"><code class="mono">${escapeHTML(
          val
        )}</code></div>
      </div>`;
    }).join("");
  } catch (err) {
    toast(err.message, "error");
    list.innerHTML = "";
  }
  if (!$("#card-output").hidden) await generateCard();
}

$("#btn-refresh-config").addEventListener("click", loadConfig);

// -------- registry card --------

let cardJSON = "";

async function generateCard() {
  const output = $("#card-output");
  const err = $("#card-error");
  try {
    const card = await api.card($("#card-name").value.trim());
    $("#card-name").value = card.name;
    cardJSON = JSON.stringify(card, null, 2);
    $("#card-json").textContent = cardJSON;
    output.hidden = false;
    err.textContent = "";
  } catch (e) {
    cardJSON = "";
    output.hidden = true;
    err.textContent = e.message;
  }
}

$("#btn-gen-card").addEventListener("click", generateCard);
$("#btn-copy-card").addEventListener("click", () => copy(cardJSON));

// -------- seed backup dialog --------

const seedDialog = $("#seed-dialog");
const seedForm = $("#seed-form");

function resetSeedDialog() {
  seedForm.reset();
  $("#seed-error").textContent = "";
  $("#seed-text").textContent = "";
  $("#seed-prompt").hidden = false;
  $("#seed-reveal").hidden = true;
  const submit = $("#seed-submit");
  submit.hidden = false;
  submit.disabled = false;
  submit.textContent = "Reveal seed";
}

$("#btn-reveal-seed").addEventListener("click", () => {
  resetSeedDialog();
  seedDialog.showModal();
});
$$("#seed-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => seedDialog.close())
);
seedDialog.addEventListener("close", () => {
  $("#seed-text").textContent = "";
});

seedForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#seed-error").textContent = "";
  const submit = $("#seed-submit");
  submit.disabled = true;
  try {
    const res = await api.dumpSeed(seedForm.elements.password.value);
    $("#seed-text").textContent = res.seed || "";
    $("#seed-prompt").hidden = true;
    $("#seed-reveal").hidden = false;
    submit.hidden = true;
  } catch (err) {
    $("#seed-error").textContent = err.message;
    submit.disabled = false;
  }
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
  setSection("markets");
})();
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refreshStatus();
});
