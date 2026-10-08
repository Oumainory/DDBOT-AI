# Phase 6: Pluggable Semantic Decision Layer

## Overview

Phase 6 introduces the pluggable Semantic Decision Layer—the architectural
foundation that realises DDBOT-AI's ultimate goal: a system where Rules,
Local Classifiers, Jev, LLM, and future models are all interchangeable
Decision Providers behind a unified contract.

## Architecture

```
Source
  ↓
Normalized Event
  ↓
Decision Engine
  ↓
Decision Providers (Rules → Local → Jev → LLM → ...)
  ↓
Decision Evidence[]
  ↓
Decision Strategy (FirstAvailable / Majority / Weighted)
  ↓
Semantic Decision
  ↓
Policy
  ↓
Route Decision
  ↓
Delivery
  ↓
Feedback / Review
```

## Core Concepts

### Decision Provider

A Decision Provider produces **Decision Evidence**—semantic signals about an
event. It describes *what* the event is, never *how* to handle it. The final
routing authority always belongs to the Policy layer.

```go
type DecisionProvider interface {
    Evaluate(ctx context.Context, event domain.NormalizedEvent) (DecisionEvidence, error)
    Type() ProviderType
    ID() string
    HealthCheck(ctx context.Context) error
}
```

### Provider Types

| Type | Description | Requires API |
|---|---|---|
| `none` | No semantic assessment. First-class product state. | No |
| `rules` | Deterministic rule-based classification. | No |
| `local` | Local classifier (ONNX, small model). | No |
| `jev` | Remote Decision Model for classification. | Yes |
| `llm` | OpenAI-compatible large language model. | Yes |

### Decision Evidence

Each provider produces `DecisionEvidence` containing:
- `SemanticResult`: the provider's classification (category, importance, tags, flags, confidence)
- `Uncertain`: whether the provider is confident in its assessment
- `InsufficientContext`: whether the provider had enough information
- `ProviderConfidence`: the provider's self-reported meta-confidence

### Decision Strategy

A Strategy combines evidence from multiple providers into a single
`SemanticDecision`. The Policy layer consumes only the final decision
and never knows which provider produced it.

```go
type DecisionStrategy interface {
    Evaluate(ctx context.Context, event domain.NormalizedEvent) (StrategyResult, error)
    Mode() StrategyMode
}
```

### Strategy Modes

| Mode | Description |
|---|---|
| `none` | No providers consulted. Event passes through. |
| `first_available` | Cascade: stops at first confident provider. |
| `majority` | All providers run; majority vote decides. Ties PASS. |
| `weighted` | All providers run; weighted scores decide. |

## None / Disabled Mode

"Not using any Decision Provider" is a **first-class product state**, not a
degraded mode. When no AI provider is configured, the system automatically
uses `NoneProvider` + `FirstAvailableStrategy` as the default pipeline.

Users can:
- Install DDBOT-AI
- Configure no AI
- Use the entire product normally

This is not a provider failure, configuration error, or missing API key.

## Integration

The Decision Strategy is integrated into the Enforce Runtime via the optional
`Config.DecisionStrategy` field. When set, `classify()` obtains semantic
assessments through the strategy pipeline instead of calling
`Provider.Classify` directly. When nil, the runtime falls back to the
Phase 4/5 direct Provider path for full backward compatibility.

## Built-in Rules

The `RulesProvider` ships with 7 built-in deterministic rules:

1. **Maintenance announcements** → CategoryMaintenance, ImportanceHigh
2. **Giveaway / promotion posts** → CategoryGiveaway, ImportanceLow
3. **Version update / new content** → CategoryUpdate, ImportanceMedium
4. **Service outage / incident** → CategoryIncident, ImportanceCritical
5. **Livestream announcement** → CategoryEvent, ImportanceMedium
6. **Collaboration / merchandise** → CategoryPromotion, ImportanceLow
7. **Repost detection** → CategoryRepost, ImportanceLow

## Design Principles

1. **Provider neutrality**: Policy never knows which provider produced a decision
2. **Fail-open**: Uncertain or escalated decisions always PASS
3. **Pluggable**: New provider types can be added without changing Policy or Delivery
4. **Zero-dependency default**: Rules and None providers require no external API
5. **At-most-once**: Same durable claim mechanism as the legacy Provider path

## Files

| File | Purpose |
|---|---|
| `internal/decision/provider.go` | DecisionProvider interface, DecisionEvidence, ProviderType |
| `internal/decision/strategy.go` | DecisionStrategy interface, StrategyMode, StrategyResult |
| `internal/decision/none.go` | NoneProvider implementation |
| `internal/decision/rules.go` | RulesProvider with 7 built-in rules |
| `internal/decision/llm.go` | LLMProvider adapter for existing OpenAI Provider |
| `internal/decision/first_available.go` | FirstAvailableStrategy (cascade) |
| `internal/decision/majority.go` | MajorityStrategy (voting) |
| `internal/decision/weighted.go` | WeightedStrategy (weighted voting) |
| `internal/decision/decision_test.go` | 30 unit tests |
| `internal/enforce/runtime.go` | Enforce Runtime integration |
| `admin/server.go` | Production None/Disabled auto-configuration |
