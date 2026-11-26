# 0002. Deterministic reconstruction core, LLM only for narrative

- Status: Accepted
- Date: 2026-11-26

## Context

Most current tools in this space put an LLM agent at the center: it decides what to query and concludes the root cause. That works often enough to be useful, but the results are hard to reproduce, hard to evaluate, and when the model is wrong it tends to be confidently wrong. It also makes the tool unusable without a model.

## Decision

The reconstruction (graph, onsets, candidate ranking, log templates) is computed by deterministic code. Same input, same output. Scores carry the factors that produced them.

An LLM may only turn an existing evidence bundle into prose. It does not query data and does not pick the root cause. Its output is validated: each sentence must cite IDs present in the bundle, otherwise it is removed.

## Consequences

- Results can be benchmarked and compared between versions.
- The tool works with no model at all, or with a small local one, since writing prose from structured input is a much easier task than investigating.
- Some incidents a free-form agent could explain will be missed by fixed heuristics. The benchmark is where that shows up.
- Citation validation needs a strict output format from the model and a parser that rejects anything else.
