// bancod dashboard — vanilla ES module, talks to /v1/* REST endpoints.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

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
  status: () => api._req("GET", "/v1/status"),
  plugins: () => api._req("GET", "/v1/plugins"),
  balance: () => api._req("GET", "/v1/balance"),
  address: () => api._req("GET", "/v1/address"),
  listTrades: (limit = 100) =>
    api._req("GET", `/v1/trades?limit=${encodeURIComponent(limit)}`),
  listClaims: () => api._req("GET", "/v1/preimage/claims"),
  registerClaim: (body) => api._req("POST", "/v1/preimage/claim", body),
  deleteClaim: (addr) =>
    api._req("DELETE", `/v1/preimage/claim/${encodeURIComponent(addr)}`),
};

// -------- toast --------

function toast(message, kind = "info", actionLabel, onAction) {
  const root = $("#toasts");
  const el = document.createElement("div");
  el.className = "toast" + (kind !== "info" ? ` ${kind}` : "");
  const msg = document.createElement("span");
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

function displayPair(name) {
  if (!name) return "";
  const [base, quote] = name.split("/");
  return `${truncMid(base)} / ${truncMid(quote)}`;
}

// pairWantAsset returns the want-side asset of a pair name.
// In a base/quote pair the deposit is base and the want is quote.
function pairWantAsset(name) {
  if (!name) return "";
  const i = name.indexOf("/");
  return i < 0 ? "" : name.slice(i + 1);
}

// fmtPairAmount formats a min/max value using the pair's want-side asset:
// BTC → satoshi count + BTC label, asset → raw count + truncated id.
function fmtPairAmount(raw, pairName) {
  if (raw == null) return "—";
  const want = pairWantAsset(pairName);
  if (want === "BTC") return `${Number(raw).toLocaleString("en-US")} sats`;
  return `${Number(raw).toLocaleString("en-US")} · ${truncMid(want, 6, 4)}`;
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("Copied", "success");
  } catch (_) {
    toast("Copy failed", "error");
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
  if (name === "balance") loadBalance();
  if (name === "pairs") loadPairs();
  if (name === "history") loadTrades();
  if (name === "preimage") loadClaims();
}

$("#nav").addEventListener("click", (e) => {
  const btn = e.target.closest(".nav-item");
  if (btn) setSection(btn.dataset.section);
});

// -------- status polling --------

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
    setPluginChip("banco", p.banco);
    setPluginChip("preimage", p.preimage);
  } catch (err) {
    setPluginsOffline();
  }
}

setInterval(refreshStatus, 5000);

// -------- pairs --------

let pairsCache = [];

async function loadPairs() {
  try {
    const data = await api.listPairs();
    pairsCache = data.pairs || [];
    renderPairs(pairsCache);
  } catch (err) {
    toast(err.message, "error");
    renderPairs([]);
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
    const tr = document.createElement("tr");
    tr.dataset.pair = p.pair;
    tr.innerHTML = `
      <td><span class="mono" title="${escapeAttr(p.pair)}">${escapeHTML(displayPair(p.pair))}</span></td>
      <td>${escapeHTML(fmtPairAmount(p.min_amount, p.pair))}</td>
      <td>${escapeHTML(fmtPairAmount(p.max_amount, p.pair))}</td>
      <td><a href="${escapeAttr(safeHref(p.price_feed))}" target="_blank" rel="noreferrer noopener" class="mono trunc">${escapeHTML(p.price_feed)}</a></td>
      <td>${p.invert_price ? '<span class="badge on">Yes</span>' : '<span class="badge">No</span>'}</td>
      <td class="actions-col">
        <div class="row-actions">
          <button class="btn btn-ghost" data-edit>Edit</button>
          <button class="btn btn-ghost btn-danger" data-del>Delete</button>
        </div>
      </td>`;
    tr.querySelector("[data-edit]").addEventListener("click", () => openEdit(p));
    tr.querySelector("[data-del]").addEventListener("click", () => deletePair(p));
    body.appendChild(tr);
  }
}

// -------- dialog --------

const dialog = $("#pair-dialog");
const form = $("#pair-form");

function openAdd() {
  form.reset();
  form.dataset.mode = "add";
  $("#dialog-title").textContent = "Add pair";
  form.elements.pair.readOnly = false;
  $("#pair-submit").textContent = "Add pair";
  dialog.showModal();
  form.elements.pair.focus();
}

function openEdit(pair) {
  form.reset();
  form.dataset.mode = "edit";
  form.elements.pair.value = pair.pair;
  form.elements.pair.readOnly = true;
  form.elements.min_amount.value = pair.min_amount;
  form.elements.max_amount.value = pair.max_amount;
  form.elements.price_feed.value = pair.price_feed;
  form.elements.invert_price.checked = !!pair.invert_price;
  $("#dialog-title").textContent = "Edit pair";
  $("#pair-submit").textContent = "Save changes";
  dialog.showModal();
}

$("#btn-add-pair").addEventListener("click", openAdd);
$("#btn-add-first-pair").addEventListener("click", openAdd);
$$("#pair-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => dialog.close())
);

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const fd = new FormData(form);
  const pair = {
    pair: String(fd.get("pair")).trim(),
    min_amount: Number(fd.get("min_amount")),
    max_amount: Number(fd.get("max_amount")),
    price_feed: String(fd.get("price_feed")).trim(),
    invert_price: fd.get("invert_price") === "on",
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
    toast(
      "Pair deleted",
      "success",
      "Undo",
      async () => {
        try {
          await api.addPair(pair);
          await loadPairs();
        } catch (err) {
          toast(err.message, "error");
        }
      }
    );
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
        <td title="${escapeAttr(new Date(t.created_at * 1000).toISOString())}">${escapeHTML(fmtTime(t.created_at))}</td>
        <td><span class="mono" title="${escapeAttr(t.pair)}">${escapeHTML(displayPair(t.pair))}</span></td>
        <td>${escapeHTML(fmtAmount(t.deposit_amount, t.deposit_asset))}</td>
        <td>${escapeHTML(fmtAmount(t.want_amount, t.want_asset))}</td>
        <td><code class="mono trunc" title="${escapeAttr(t.offer_txid)}">${escapeHTML(truncMid(t.offer_txid, 6, 6))}</code></td>
        <td><code class="mono trunc" title="${escapeAttr(t.fulfill_txid)}">${escapeHTML(truncMid(t.fulfill_txid, 6, 6))}</code></td>`;
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
    return `${(Number(raw) / 1e8).toFixed(8)} BTC (${Number(raw).toLocaleString("en-US")} sats)`;
  }
  // Asset decimals aren't exposed by the API; show raw count + truncated asset id.
  return `${Number(raw).toLocaleString("en-US")} · ${truncMid(asset, 6, 4)}`;
}

$("#btn-refresh-trades").addEventListener("click", loadTrades);

// -------- preimage claims --------

let claimsCache = [];

async function loadClaims() {
  const body = $("#claims-body");
  const empty = $("#claims-empty");
  try {
    const data = await api.listClaims();
    claimsCache = data.claims || [];
    if (!claimsCache.length) {
      body.innerHTML = "";
      empty.hidden = false;
      return;
    }
    empty.hidden = true;
    body.innerHTML = "";
    for (const c of claimsCache) {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td><code class="mono trunc" title="${escapeAttr(c.claim_address)}">${escapeHTML(c.claim_address)}</code></td>
        <td class="actions-col">
          <div class="row-actions">
            <button class="btn btn-ghost" data-copy-claim>Copy</button>
            <button class="btn btn-ghost btn-danger" data-del-claim>Delete</button>
          </div>
        </td>`;
      tr.querySelector("[data-copy-claim]").addEventListener("click", () => copy(c.claim_address));
      tr.querySelector("[data-del-claim]").addEventListener("click", () => deleteClaim(c.claim_address));
      body.appendChild(tr);
    }
  } catch (err) {
    toast(err.message, "error");
    body.innerHTML = "";
    empty.hidden = false;
  }
}

function hexToBase64(hex) {
  let s = String(hex || "").trim().toLowerCase();
  if (s.startsWith("0x")) s = s.slice(2);
  if (s.length === 0) throw new Error("hex string is empty");
  if (s.length % 2 !== 0) throw new Error("hex string must have even length");
  if (!/^[0-9a-f]+$/.test(s)) throw new Error("invalid hex characters");
  let bin = "";
  for (let i = 0; i < s.length; i += 2) {
    bin += String.fromCharCode(parseInt(s.slice(i, i + 2), 16));
  }
  return btoa(bin);
}

const claimDialog = $("#claim-dialog");
const claimForm = $("#claim-form");

function openAddClaim() {
  claimForm.reset();
  claimDialog.showModal();
  claimForm.elements.preimage.focus();
}

$("#btn-add-claim").addEventListener("click", openAddClaim);
$("#btn-add-first-claim").addEventListener("click", openAddClaim);
$$("#claim-dialog [data-close]").forEach((b) =>
  b.addEventListener("click", () => claimDialog.close())
);

claimForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const fd = new FormData(claimForm);
  const submit = $("#claim-submit");
  submit.disabled = true;
  try {
    const taptreeLines = String(fd.get("taptree") || "")
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean);
    if (!taptreeLines.length) throw new Error("taptree must contain at least one closure");
    // Validate each closure is hex.
    taptreeLines.forEach((l) => {
      if (!/^(0x)?[0-9a-fA-F]+$/.test(l)) {
        throw new Error(`taptree line is not hex: ${l.slice(0, 16)}…`);
      }
    });
    const body = {
      preimage: hexToBase64(fd.get("preimage")),
      arkade_script: hexToBase64(fd.get("arkade_script")),
      taptree: taptreeLines.map((l) => l.replace(/^0x/i, "")),
    };
    const resp = await api.registerClaim(body);
    toast(`Claim registered: ${truncMid(resp.claim_address, 10, 10)}`, "success");
    claimDialog.close();
    await loadClaims();
  } catch (err) {
    toast(err.message, "error");
  } finally {
    submit.disabled = false;
  }
});

async function deleteClaim(address) {
  if (!confirm(`Delete claim ${address}?`)) return;
  const prev = claimsCache.slice();
  claimsCache = claimsCache.filter((c) => c.claim_address !== address);
  try {
    await api.deleteClaim(address);
    toast("Claim deleted", "success");
    await loadClaims();
  } catch (err) {
    claimsCache = prev;
    toast(err.message, "error");
    await loadClaims();
  }
}

// delegate copy buttons
document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-copy]");
  if (!b) return;
  const el = $(b.getAttribute("data-copy"));
  if (el) copy(el.textContent.trim());
});

// -------- utils --------

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]
  );
}
function escapeAttr(s) {
  return escapeHTML(s);
}

// safeHref returns the URL only if it parses and uses an http(s) scheme.
// Anything else (javascript:, data:, malformed) returns "#" to neutralize it.
function safeHref(s) {
  try {
    const u = new URL(String(s ?? ""));
    if (u.protocol === "http:" || u.protocol === "https:") {
      return u.href;
    }
  } catch (_) {}
  return "#";
}

// -------- init --------

refreshStatus();
setSection("pairs");
