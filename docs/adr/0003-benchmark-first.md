# 0003. Build the scenario benchmark before the engine

- Status: Accepted
- Date: 2026-11-26

## Context

Root cause heuristics are easy to write and hard to judge. Without a set of incidents with known causes, every change to the engine is judged by looking at one or two examples, which mostly tells you what you wanted to see.

## Decision

Phase 0 delivers the demo system, fault injection and scenario files with ground truth before any engine code exists. Phase 2 does not count as done until `incidentlens bench` reports accuracy over all scenarios, and results are published including the failures.

## Consequences

- Slower start: the first weeks produce infrastructure, not features.
- Every engine change can be checked against the same scenarios.
- Scenarios are synthetic and the demo system is small. Good results here do not prove the engine works on real systems; the README should say so plainly.
- Scenarios need variety (faults with and without change events, faults at the edge and deep in the graph, noisy baselines) or the engine will overfit to them.
