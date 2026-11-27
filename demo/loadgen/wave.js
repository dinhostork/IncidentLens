// Request rate model for the load generator. No k6 imports here, so the
// functions can be tested with plain Node (wave.test.js).
//
// The rate is a function of wall-clock time:
//
//   rate(t) = rps * (1 + amplitude * sin(2*pi*t / period)) * (1 + jitter * noise(t))
//
// Using wall-clock time rather than time since start means every VU computes
// the same rate without coordinating, and restarting k6 does not reset the
// wave.

// Length of one noise step in seconds. The noise moves smoothly from one
// random value to the next over this long.
export const NOISE_STEP_S = 20;

// Reads the load settings from an env object (k6's __ENV or process.env).
export function config(env) {
  const cfg = {
    rps: number(env, "LOADGEN_RPS", 20),
    periodS: number(env, "LOADGEN_WAVE_MINUTES", 60) * 60,
    amplitude: number(env, "LOADGEN_WAVE_AMPLITUDE", 0.2),
    jitter: number(env, "LOADGEN_JITTER", 0.1),
  };
  if (cfg.rps < 0) throw new Error("LOADGEN_RPS must be 0 or more");
  if (cfg.periodS <= 0) throw new Error("LOADGEN_WAVE_MINUTES must be more than 0");
  if (cfg.amplitude < 0 || cfg.jitter < 0 || cfg.amplitude + cfg.jitter >= 1) {
    throw new Error("LOADGEN_WAVE_AMPLITUDE and LOADGEN_JITTER must be 0 or more and add up to less than 1");
  }
  return cfg;
}

function number(env, key, fallback) {
  const raw = env[key];
  if (raw === undefined || raw === "") return fallback;
  const n = Number(raw);
  if (!Number.isFinite(n)) throw new Error(`${key} must be a number, got "${raw}"`);
  return n;
}

// Target request rate at unix time t (seconds).
export function rate(cfg, t) {
  const wave = 1 + cfg.amplitude * Math.sin((2 * Math.PI * t) / cfg.periodS);
  return cfg.rps * wave * (1 + cfg.jitter * noise(t));
}

// Highest value rate() can return.
export function peak(cfg) {
  return cfg.rps * (1 + cfg.amplitude) * (1 + cfg.jitter);
}

// Smooth noise in [-1, 1]: a random value per NOISE_STEP_S, with cosine
// interpolation in between. The random values come from hashing the step
// number, so every caller gets the same noise for the same t.
export function noise(t) {
  const x = t / NOISE_STEP_S;
  const i = Math.floor(x);
  const f = (1 - Math.cos(Math.PI * (x - i))) / 2;
  return lattice(i) * (1 - f) + lattice(i + 1) * f;
}

// Uniform value in [-1, 1] for an integer step.
function lattice(i) {
  let h = Math.imul(i | 0, 0x9e3779b1) ^ 0x85ebca6b;
  h = Math.imul(h ^ (h >>> 16), 0x7feb352d);
  h = Math.imul(h ^ (h >>> 15), 0x846ca68b);
  h ^= h >>> 16;
  return ((h >>> 0) / 0xffffffff) * 2 - 1;
}
