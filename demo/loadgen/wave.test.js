import assert from "node:assert/strict";
import { test } from "node:test";
import { config, NOISE_STEP_S, noise, peak, rate } from "./wave.js";

const cfg = config({});

test("defaults", () => {
  assert.deepEqual(cfg, { rps: 20, periodS: 3600, amplitude: 0.2, jitter: 0.1 });
});

test("config reads env", () => {
  const c = config({ LOADGEN_RPS: "5", LOADGEN_WAVE_MINUTES: "10", LOADGEN_WAVE_AMPLITUDE: "0", LOADGEN_JITTER: "" });
  assert.deepEqual(c, { rps: 5, periodS: 600, amplitude: 0, jitter: 0.1 });
});

test("config rejects bad values", () => {
  for (const env of [
    { LOADGEN_RPS: "fast" },
    { LOADGEN_RPS: "-1" },
    { LOADGEN_WAVE_MINUTES: "0" },
    { LOADGEN_WAVE_AMPLITUDE: "-0.1" },
    { LOADGEN_WAVE_AMPLITUDE: "0.6", LOADGEN_JITTER: "0.4" },
  ]) {
    assert.throws(() => config(env), Error, JSON.stringify(env));
  }
});

test("noise stays in [-1, 1], is continuous and repeatable", () => {
  let prev = noise(0);
  let min = 1;
  let max = -1;
  for (let t = 0; t < 100000; t += 0.5) {
    const n = noise(t);
    assert.ok(n >= -1 && n <= 1, `noise(${t}) = ${n}`);
    assert.ok(Math.abs(n - prev) < 0.1, `jump at ${t}: ${prev} -> ${n}`);
    assert.equal(noise(t), n);
    min = Math.min(min, n);
    max = Math.max(max, n);
    prev = n;
  }
  // It should actually move around, not sit near zero.
  assert.ok(min < -0.8 && max > 0.8, `range [${min}, ${max}]`);
});

test("noise differs between steps", () => {
  const values = new Set();
  for (let i = 0; i < 100; i++) values.add(noise(i * NOISE_STEP_S).toFixed(6));
  assert.ok(values.size > 95);
});

test("rate stays between the wave bounds and never passes peak", () => {
  const t0 = Date.UTC(2026, 10, 26) / 1000;
  const low = cfg.rps * (1 - cfg.amplitude) * (1 - cfg.jitter);
  for (let t = t0; t < t0 + 2 * cfg.periodS; t += 1) {
    const r = rate(cfg, t);
    assert.ok(r >= low && r <= peak(cfg), `rate(${t}) = ${r}`);
  }
});

test("rate averages to rps over whole periods", () => {
  const t0 = Date.UTC(2026, 10, 26) / 1000;
  let sum = 0;
  let n = 0;
  for (let t = t0; t < t0 + 24 * cfg.periodS; t += 1) {
    sum += rate(cfg, t);
    n++;
  }
  const mean = sum / n;
  assert.ok(Math.abs(mean - cfg.rps) / cfg.rps < 0.02, `mean ${mean}`);
});

test("the wave is visible in any 15 minute window", () => {
  // Even around a crest, where the wave is flattest, the base rate should
  // move by a few percent over a scenario-sized window.
  const c = { ...cfg, jitter: 0 };
  for (let start = 0; start < c.periodS; start += 60) {
    const rates = [];
    for (let t = start; t <= start + 900; t += 10) rates.push(rate(c, t));
    const spread = (Math.max(...rates) - Math.min(...rates)) / c.rps;
    assert.ok(spread > 0.05, `window at ${start}s: spread ${spread}`);
  }
});

test("zero rps means no traffic", () => {
  const c = config({ LOADGEN_RPS: "0" });
  assert.equal(rate(c, 1234), 0);
  assert.equal(peak(c), 0);
});
