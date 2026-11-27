# Load generator

A [k6](https://grafana.com/docs/k6/) script that keeps steady traffic on the demo shop through the gateway. It runs as the `loadgen` service in the compose stack, so `make up` starts it once the gateway is healthy.

## What it sends

Each visit is one session:

| Session | Share of sessions | Requests | Share of requests |
|---|---|---|---|
| browse | 75% | product list, then 1 to 3 product pages | about 71% |
| cart | 17% | new cart, 1 or 2 items, view the cart, leave | about 19% |
| checkout | 8% | new cart, 1 to 3 items, check out | about 10% |

At the default rate that is about 0.5 checkouts per second. There is a 0.1 to 0.5s pause between the requests of a session. Popular products get most of the views, and about 2% of product lookups ask for an ID that does not exist, so a normal baseline already has a few 404s.

A session stops when it cannot create a cart, and a checkout session does not check out if no item could be added. During a fault in orders, load on the services behind it therefore drops a little, the same as when real users give up.

## Rate

The average is `LOADGEN_RPS` requests per second (default 20), shaped by a slow wave and some noise:

```
rate(t) = rps * (1 + amplitude * sin(2*pi*t / period)) * (1 + jitter * noise(t))
```

- The wave stands in for a daily traffic cycle, compressed to an hour by default, so that any 15 minute scenario window sees the baseline drift by at least 5% (up to about 28%, depending on where in the cycle it falls).
- `noise(t)` is smooth random noise in [-1, 1] that changes over about 20s.
- `t` is wall-clock time, so the wave does not restart when k6 does.

A perfectly flat baseline would make onset detection look better than it is, so the variation is there on purpose.

k6 starts sessions at a fixed rate, the highest the formula can reach. Each session then runs with probability `rate(now) / highest`, otherwise it ends at once. This gives irregular gaps between arrivals instead of evenly spaced ones, and lets the rate follow the formula without a long list of k6 stages.

Arrivals do not wait for earlier sessions to finish (k6's open model), so when the shop slows down the load stays the same and sessions pile up, like real visitors would. k6 runs at most 300 sessions at once. Beyond that it skips arrivals and logs a warning about insufficient VUs.

## Settings

Set these in the environment of `make up` (or `docker compose up`):

| Variable | Default | What |
|---|---|---|
| `LOADGEN_RPS` | `20` | Average requests per second. `0` stops the traffic but keeps the container running. |
| `LOADGEN_WAVE_MINUTES` | `60` | Length of one wave cycle |
| `LOADGEN_WAVE_AMPLITUDE` | `0.2` | Wave height as a fraction of the average |
| `LOADGEN_JITTER` | `0.1` | Noise height as a fraction of the wave |

`LOADGEN_WAVE_AMPLITUDE` plus `LOADGEN_JITTER` must stay below 1.

```sh
LOADGEN_RPS=50 make up                                       # more traffic
docker compose -f deploy/compose/compose.yaml stop loadgen   # no traffic
```

k6's own metrics are not exported, and the end-of-run summary and anonymous usage report are off. The only telemetry the load produces is what the demo services report. k6 does not send `traceparent`, so every trace starts at the gateway.

## Files

| File | What |
|---|---|
| `script.js` | The k6 script: sessions and the arrival rate |
| `wave.js` | The rate formula, with no k6 imports so it runs under Node too |
| `wave.test.js` | Tests for the formula, `make test-loadgen` (Node's test runner) |
| `package.json` | Only marks the directory as ES modules for Node. No dependencies. |
