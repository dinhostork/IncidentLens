// Baseline traffic for the demo shop. See README.md in this directory.
import http from "k6/http";
import { sleep } from "k6";
import { config, peak, rate } from "./wave.js";

const cfg = config(__ENV);
const GATEWAY = (__ENV.GATEWAY_URL || "http://gateway:8080").replace(/\/+$/, "");
const PRODUCTS = 48; // seeded by catalog, IDs 1 to 48

// What a visitor does, how often, and how many requests that is on average.
// The averages have to match the functions below: they turn LOADGEN_RPS,
// which counts requests, into sessions per second.
const SESSIONS = [
  { name: "browse", weight: 0.75, requests: 3, run: browse }, // list, 1 to 3 product pages
  { name: "cart", weight: 0.17, requests: 3.5, run: cart }, // new cart, 1 or 2 items, view cart
  { name: "checkout", weight: 0.08, requests: 4, run: checkout }, // new cart, 1 to 3 items, checkout
];
const REQUESTS_PER_SESSION = SESSIONS.reduce((sum, s) => sum + s.weight * s.requests, 0);

// k6 starts sessions at a fixed rate, the most the wave can ask for, and each
// one goes ahead with probability rate(now) / that maximum. Skipped sessions
// cost nothing, and the result is irregular gaps between arrivals rather than
// a metronome.
const SLOTS_PER_MINUTE = Math.max(1, Math.ceil((peak(cfg) / REQUESTS_PER_SESSION) * 60));

export const options = {
  scenarios: {
    shop: {
      executor: "constant-arrival-rate",
      rate: SLOTS_PER_MINUTE,
      timeUnit: "1m",
      duration: "8760h",
      // A session takes a second or two. Slow dependencies make sessions
      // pile up, which is what real visitors do too, so leave plenty of room.
      preAllocatedVUs: 20,
      maxVUs: 300,
      gracefulStop: "5s",
    },
  },
  // Nothing reads k6's own metrics. Keep their tag sets small so a run that
  // lasts days does not grow in memory.
  systemTags: ["status", "method", "name"],
  discardResponseBodies: true,
};

export function setup() {
  console.log(
    `loadgen: ${cfg.rps} rps on average to ${GATEWAY}, wave ±${pct(cfg.amplitude)} over ` +
      `${cfg.periodS / 60} min, jitter ±${pct(cfg.jitter)}`,
  );
}

export default function () {
  const wanted = rate(cfg, Date.now() / 1000) / REQUESTS_PER_SESSION;
  if (Math.random() * (SLOTS_PER_MINUTE / 60) >= wanted) return;
  pick().run();
}

function pick() {
  let r = Math.random();
  for (const s of SESSIONS) {
    r -= s.weight;
    if (r < 0) return s;
  }
  return SESSIONS[0];
}

function browse() {
  get("/api/products", "/api/products");
  const pages = 1 + randInt(3);
  for (let i = 0; i < pages; i++) {
    think();
    get(`/api/products/${productId()}`, "/api/products/{id}");
  }
}

function cart() {
  const id = newCart();
  if (!id) return;
  addItems(id, 1 + randInt(2));
  think();
  get(`/api/carts/${id}`, "/api/carts/{id}");
}

function checkout() {
  const id = newCart();
  if (!id) return;
  if (addItems(id, 1 + randInt(3)) === 0) return;
  think();
  post(`/api/carts/${id}/checkout`, "/api/carts/{id}/checkout");
}

// Returns the new cart's ID, or null if the cart could not be created. A
// visitor who cannot get a cart gives up.
function newCart() {
  const res = post("/api/carts", "/api/carts", null, "text");
  if (res.status !== 201) return null;
  try {
    return res.json("id") || null;
  } catch {
    return null;
  }
}

// Adds n items and returns how many were added.
function addItems(cartId, n) {
  let added = 0;
  for (let i = 0; i < n; i++) {
    think();
    const body = { product_id: productId(), quantity: 1 + randInt(3) };
    if (post(`/api/carts/${cartId}/items`, "/api/carts/{id}/items", body).status === 200) added++;
  }
  return added;
}

// Popular products get most of the views. About 1 in 50 lookups asks for an
// ID that does not exist, the way stale links do.
function productId() {
  if (Math.random() < 0.02) return PRODUCTS + 1 + randInt(20);
  return 1 + Math.floor(PRODUCTS * Math.random() ** 2);
}

function get(path, route) {
  return http.get(GATEWAY + path, { tags: { name: route }, timeout: "10s" });
}

function post(path, route, body = null, responseType = "none") {
  return http.post(GATEWAY + path, body === null ? null : JSON.stringify(body), {
    headers: body === null ? {} : { "Content-Type": "application/json" },
    tags: { name: route },
    timeout: "10s",
    responseType,
  });
}

function think() {
  sleep(0.1 + Math.random() * 0.4);
}

function pct(x) {
  return `${Math.round(x * 1000) / 10}%`;
}

function randInt(n) {
  return Math.floor(Math.random() * n);
}
