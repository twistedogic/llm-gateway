# llm-gateway — Design Specification

**Version:** 0.1.0-draft
**Status:** Design
**Language:** Go

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [API Key Management](#3-api-key-management)
4. [Tools & Skills Injection](#4-tools--skills-injection)
5. [Context Compaction](#5-context-compaction)
6. [Hooks — Temporal-Extensible](#6-hooks--temporal-extensible)
7. [Rate Limiting](#7-rate-limiting)
8. [Provider Routing & Fallback](#8-provider-routing--fallback)
9. [Telemetry (OpenTelemetry)](#9-telemetry-opentelemetry)
10. [Project Structure](#10-project-structure)
11. [Resolved Decisions](#11-resolved-decisions)

---

## 1. Overview

**llm-gateway** is a Go-based LLM proxy/gateway that sits between LLM clients and upstream providers. It provides a single, unified API surface (OpenAI-compatible) while offering operational capabilities that production deployments demand.

### 1.1 Goals

- Single endpoint for all LLM traffic (OpenAI, Anthropic, Azure OpenAI, AWS Bedrock, Ollama)
- API key management via file-based key store with hot-reload
- Dual injection mechanisms: **skills** (behavioral guidance via prompt) and **tools** (function calling)
- Async context compaction via Temporal workflows
- Extensible pre/post hooks (HTTP + Temporal dispatch)
- Per-key rate limiting (RPM, TPM, daily)
- Full OpenTelemetry observability (`gen_ai.*` + `llm.gateway.*` semantic conventions)
- Open routing via OpenRouter or OpenAI-compatible fallback

### 1.2 Non-Goals

- Authentication beyond API key validation (handled by upstream)
- Billing/cost allocation (out of scope for v1; can be added via hooks)
- Model fine-tuning or training
- Vector storage or RAG

---

## 2. Architecture

```
┌──────────────────────────────────────────────────────────────────────────┐
│                        LLM Clients (any OpenAI-compatible SDK)          │
└─────────────────────────────┬──────────────────────────────────────────┘
                               │ HTTP POST /v1/chat/completions
                               ▼
┌──────────────────────────────────────────────────────────────────────────┐
│                         llm-gateway  (Go)                                 │
│                                                                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │   Auth   │→ │  Rate    │→ │  Pre-    │→ │  Skills  │→ │  Tools   │  │
│  │  + Key   │  │  Limit   │  │  Hooks   │  │ Injection│  │ Injection│  │
│  │Validation│  │(per-key) │  │(parallel)│  │(MetaClaw)│  │(fn call) │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
│       │              │              │             │             │       │
│       ▼              ▼              ▼             ▼             ▼       │
│  Key Store     Sliding Window  HTTP +        Skill         Provider    │
│  (keys.json)    (RPM+TPM)     Temporal      Store         Schema       │
│  hot-reload                              (skills.json)   Conversion     │
│                                              + TF-IDF                    │
│                                             retrieval                     │
│                                                  │                        │
│                                                  ▼                        │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │              Provider Router + Retry + Fallback                   │   │
│  │  OpenAI → Anthropic → Azure → Bedrock → Ollama → OpenRouter     │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                               │                                          │
│                               ▼                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │           Tool Execution Loop (parallel, cached)                 │   │
│  │  LLM → tool_call → execute → stream result → LLM → ...           │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                               │                                          │
│                               ▼                                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                               │
│  │ Context  │  │  Post-   │  │ Telemetry│                               │
│  │Compaction│  │  Hooks   │  │ Emit     │                               │
│  │(async)   │  │(async)   │  │OTEL      │                               │
│  └──────────┘  └──────────┘  └──────────┘                               │
└──────────────────────────────────────────────────────────────────────────┘
           │                        │
           ▼                        ▼
┌──────────────────────┐  ┌──────────────────────────────────┐
│  Upstream LLM         │  │  Temporal Cluster                 │
│  Providers           │  │  · conversation-summarize workflow │
│  (OpenAI / Anthropic │  │  · audit-hook workflow            │
│   Azure / Bedrock /  │  │  · (hooks dispatch)              │
│   Ollama)            │  │  · durable execution + retry      │
└──────────────────────┘  └──────────────────────────────────┘
```

### 2.1 Request Flow

```
HTTP Request
    │
    ▼
[1] Auth + Key Validation         → reject bad keys, extract key metadata
    │
    ▼
[2] Rate Limiter (per-key)        → 429 if over limit
    │
    ▼
[3] Pre-Hooks (parallel)         → HTTP + Temporal dispatch, fail_open
    │
    ▼
[4] Skills Retrieval             → MetaClaw: match msg + topic hints → skill blocks
    │
    ▼
[5] System Prompt Injection      → prepend skills to system prompt
    │
    ▼
[6] Tools Injection              → convert schemas to provider format, inject into req body
    │
    ▼
[7] Route to Provider             → OpenAI / Anthropic / Azure / Bedrock / Ollama
    │
    ▼
[8] Stream / Collect Response    → handle tool_call blocks (execute + stream back)
    │
    ▼
[9] Context Compaction Trigger   → async Temporal workflow if over threshold
    │
    ▼
[10] Post-Hooks (async)          → fire-and-forget, don't block response
    │
    ▼
Response to Client
```

---

## 3. API Key Management

### 3.1 Design Decisions

- **Storage:** File-based JSON store (`keys.json`), hot-reloaded via `fsnotify`
- **Lookup:** SHA-256 hash of raw key value; also supports direct key ID lookup
- **Rotation:** Old key remains valid for a 5-minute grace window during rotation
- **Security:** Raw key values are stored hashed in-memory for lookups; full hash comparison on match

### 3.2 Key Store Schema

```json
// keys.json  — chmod 600, owned by gateway process
{
  "keys": [
    {
      "id":       "key-team-a",
      "provider": "openai",
      "key":      "sk-proj-...",
      "tier":     "standard",
      "limits": {
        "rpm":   500,
        "tpm":   300000,
        "daily": 50000
      },
      "metadata": {
        "team":   "team-a",
        "owner":  "jordan@example.com"
      }
    },
    {
      "id":       "key-enterprise",
      "provider": "azure",
      "key":      "...",
      "endpoint": "https://mycompany.openai.azure.com",
      "api_version": "2024-06-01",
      "tier":     "enterprise",
      "limits": {
        "rpm":   10000,
        "tpm":   10000000
      }
    },
    {
      "id":       "key-user-01",
      "provider": "openai",
      "key":      "sk-...",
      "tier":     "personal",
      "limits": {
        "rpm":   60,
        "tpm":   30000
      }
    }
  ]
}
```

### 3.3 Key Store Implementation

```go
// internal/auth/keystore.go

type Key struct {
    ID         string
    Provider   string
    Key        string    // raw key (kept for forwarding to upstream)
    KeyHash    string    // SHA-256 of Key, used for fast lookups
    Tier       string
    Limits     RateLimits
    Metadata   map[string]string
    Endpoint   string    // for Azure
    APIVersion string    // for Azure
}

type KeyStore struct {
    mu    sync.RWMutex
    keys  map[string]*Key  // keyed by hash(rawKey) AND by key ID
    path  string
    watch *fsnotify.Watcher
}

// Load reads keys from file, computes hashes, populates in-memory map.
func (s *KeyStore) Load(ctx context.Context) error

// Get resolves by hash then by key ID (supports both Bearer sk-xxx and Bearer key-id).
func (s *KeyStore) Get(rawKey string) (*Key, error)

// Hot-reload: fsnotify watches keys.json; on change, re-parse and swap map atomically.
func (s *KeyStore) watchAndReload() error
```

### 3.4 Authorization Header

```http
# Raw key value (recommended)
Authorization: Bearer sk-proj-xxxx

# By key ID (for aliasing)
Authorization: Bearer key-team-a
```

---

## 4. Tools & Skills Injection

Two distinct injection mechanisms at different layers:

| Mechanism | Layer | Effect |
|---|---|---|
| **Skills injection** | System prompt | Guides LLM *behavior* (MetaClaw method) |
| **Tools injection** | Request body | Gives LLM *agency* via function calling |

They compose — skills shape *how* the LLM thinks; tools give it *what* it can do.

### 4.1 Skills Injection — MetaClaw Method

Skills are markdown blocks retrieved from a local JSON store based on message content, then **prepended to the system prompt**. This is prompt injection via retrieval — no tool schema normalization required, fully provider-agnostic.

#### 4.1.1 Skill Store Schema

```json
// skills.json  — same hot-reload pattern as keys.json
{
  "general_skills": [
    {
      "name": "clarify-ambiguous-requests",
      "description": "Use when the user's request is ambiguous, under-specified, or could be interpreted in multiple ways.",
      "content": "## Clarify Ambiguous Requests\n\nWhen the task is unclear, do not guess — ask one focused clarifying question before proceeding.\n\n**Process:**\n1. Identify the single most important ambiguity.\n2. Ask exactly one targeted question.\n3. Wait for the answer before proceeding."
    },
    {
      "name": "structured-step-by-step-reasoning",
      "description": "Use for any problem that involves multiple steps, tradeoffs, or non-trivial logic.",
      "content": "## Structured Step-by-Step Reasoning\n\nFor non-trivial problems, reason explicitly before giving the final answer.\n\n1. Restate the core question.\n2. Identify key sub-problems.\n3. Work through each sub-problem.\n4. Check intermediate results.\n5. Summarize the conclusion."
    }
  ],
  "task_specific_skills": {
    "coding": [
      {
        "name": "secure-code-review",
        "description": "Use when reviewing or writing code that handles user input, authentication, or file I/O.",
        "content": "## Secure Code Review Checklist\n\n**Input Validation:** Never trust user-supplied input; validate type, length, and format at boundaries.\n\n**Secrets:** No hardcoded passwords or API keys. Use environment variables.\n\n**Auth:** Verify authorization on every protected endpoint, not just at login."
      }
    ],
    "math": [
      {
        "name": "show-all-steps",
        "description": "Use when the user asks a math problem, derivation, or quantitative analysis question.",
        "content": "## Show All Steps\n\n1. State the knowns and what you're solving for.\n2. Write out every algebraic step explicitly.\n3. Include units throughout.\n4. Verify by substituting back."
      }
    ]
  }
}
```

#### 4.1.2 Retrieval Algorithm (TF-IDF-like keyword scoring)

```go
// internal/middleware/skills.go

func (s *SkillStore) Retrieve(msg string, topicHints []string) []Skill {
    scores := make(map[string]float64)

    // 1. Always include matching general_skills
    for _, skill := range s.data.GeneralSkills {
        score := keywordOverlap(msg, skill.Description)
        if score > 0.1 {
            scores[skill.Name] = score
        }
    }

    // 2. Topic-guided retrieval from task_specific_skills
    for _, topic := range topicHints {
        if skills, ok := s.data.TaskSkills[topic]; ok {
            for _, skill := range skills {
                scores[skill.Name] += keywordOverlap(msg, skill.Description) + 0.5
            }
        }
    }

    return topN(scores, 5)  // cap at 5 skills
}

// Jaccard similarity between message tokens and description tokens
func keywordOverlap(text, desc string) float64 {
    // tokenize, lowercase, remove stop words
    // return Jaccard(tokens(text), tokens(desc))
}
```

#### 4.1.3 Topic Hint Sources

```go
// Topic hints come from:
// 1. Explicit header: X-Gateway-Topics: "coding,math"
// 2. Route-based: /v1/chat/completions/coding → topic "coding"
// 3. Detection: light LLM call to classify the message (async, not on hot path)
```

#### 4.1.4 Injection into System Prompt

```go
func InjectSkills(systemPrompt string, skills []Skill) string {
    if len(skills) == 0 {
        return systemPrompt
    }
    var buf bytes.Buffer
    buf.WriteString(systemPrompt)
    buf.WriteString("\n\n## Additional Instructions\n\n")
    for i, skill := range skills {
        buf.WriteString(fmt.Sprintf("### [%d] %s\n%s\n\n", i+1, skill.Name, skill.Content))
    }
    return buf.String()
}
```

### 4.2 Tools Injection — Function Calling

Traditional tool-calling: the LLM produces `tool_call` blocks in its response, the gateway executes them and streams results back. The gateway iterates until the LLM produces a final answer (no more tool calls).

#### 4.2.1 Tool Registry

```go
// internal/tools/registry.go

type ToolHandler func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)

type ToolDef struct {
    Name        string          // unique, kebab-case: "get-current-time"
    Description string          // shown to LLM
    Schema      json.RawMessage // JSON Schema (OpenAI canonical format)
    Handler     ToolHandler
    Timeout     time.Duration
    CacheTTL    time.Duration   // 0 = no cache; >0 = cached for TTL
}

type Registry struct {
    mu    sync.RWMutex
    tools map[string]*ToolDef
    cache *ristretto.Cache[string, json.RawMessage]
}

func (r *Registry) Register(def ToolDef) error { ... }
func (r *Registry) Get(name string) (*ToolDef, bool) { ... }
func (r *Registry) List() []*ToolDef { ... }
```

#### 4.2.2 Provider-Specific Schema Conversion

OpenAI format is canonical in the registry; converted per upstream provider:

```go
// internal/tools/convert.go

// OpenAI canonical — stored in registry
func ToOpenAI(tools []*ToolDef) interface{} {
    out := make([]map[string]interface{}, len(tools))
    for i, t := range tools {
        out[i] = map[string]interface{}{
            "type": "function",
            "function": map[string]interface{}{
                "name":        t.Name,
                "description": t.Description,
                "parameters":  t.Schema,
            },
        }
    }
    return out
}

// Anthropic format
func ToAnthropic(tools []*ToolDef) []map[string]interface{} {
    out := make([]map[string]interface{}, len(tools))
    for i, t := range tools {
        out[i] = map[string]interface{}{
            "name":         t.Name,
            "description": t.Description,
            "input_schema": t.Schema,
        }
    }
    return out
}

// AWS Bedrock format
func ToAWSBedrock(tools []*ToolDef) *bedrock.ToolConfig {
    fns := make([]bedrock.Function, len(tools))
    for i, t := range tools {
        fns[i] = bedrock.Function{
            ToolSpec: &bedrock.ToolSpec{
                Function: &bedrock.FunctionDeclaration{
                    Name:        t.Name,
                    Description: t.Description,
                    Parameters:  t.Schema,
                },
            },
        }
    }
    return &bedrock.ToolConfig{
        ToolChoice: &bedrock.ToolChoice{Auto: &struct{}{}},
        FunctionDeclarations: fns,
    }
}

// Ollama: no native function calling — handled by Ollama compat layer (see 4.2.5)
func ToOllama(tools []*ToolDef) interface{} { return nil }
```

#### 4.2.3 Tool Execution Loop

```
LLM returns tool_call:
  {
    "tool_call": {
      "id": "call_abc123",
      "function": { "name": "get-current-time", "arguments": "{}" }
    }
  }
         │
         ▼
┌─────────────────────────────────────────────────────┐
│  Execute ALL tool_calls in parallel                 │
│                                                     │
│  For each tool_call:                               │
│    1. Look up handler in registry                   │
│    2. Check cache — if hit and TTL valid, return   │
│    3. Execute handler with timeout                  │
│    4. On error: return error JSON (don't crash LLM)│
│    5. Cache result if TTL > 0                       │
└─────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────┐
│  Stream tool results back to LLM as:               │
│                                                     │
│  OpenAI:  {"tool_call_id":"call_abc123",            │
│             "role":"tool",                          │
│             "content":"{\"utc_iso\":\"...\"}"}    │
│                                                     │
│  Anthropic: {"role":"user",                         │
│              "content":[{"type":"tool_result",      │
│                "tool_use_id":"toolu_abc",           │
│                "content":"{\"utc_iso\":\"...\"}"}]} │
└─────────────────────────────────────────────────────┘
         │
         ▼
  Continue LLM generation until complete (no more tool_calls)
```

```go
// internal/tools/executor.go

type Executor struct {
    registry *Registry
    timeout  time.Duration
}

type ToolResult struct {
    ID      string          `json:"id"`
    Name    string          `json:"name"`
    Output  json.RawMessage `json:"output"`
    Error   string          `json:"error,omitempty"`
    Elapsed time.Duration   `json:"elapsed_ms"`
}

func (e *Executor) ExecuteAll(ctx context.Context, calls []ToolCall) ([]ToolResult, error) {
    eg, ctx := errgroup.WithContext(ctx)
    results := make([]ToolResult, len(calls))

    for i, call := range calls {
        i, call := i, call
        eg.Go(func() error {
            result, _ := e.executeOne(ctx, call)
            results[i] = result
            return nil // tool errors don't fail the group
        })
    }

    eg.Wait()
    return results, nil
}

func (e *Executor) executeOne(ctx context.Context, call ToolCall) (ToolResult, error) {
    tool, ok := e.registry.Get(call.Function.Name)
    if !ok {
        return ToolResult{
            ID: call.ID, Name: call.Function.Name,
            Error: fmt.Sprintf("unknown tool: %s", call.Function.Name),
        }, nil
    }

    // Check cache
    if tool.CacheTTL > 0 {
        cacheKey := fmt.Sprintf("%s:%s", tool.Name, call.Function.Arguments)
        if cached, ok := e.registry.GetCached(cacheKey); ok {
            return ToolResult{ID: call.ID, Name: tool.Name, Output: cached, Elapsed: 0}, nil
        }
    }

    // Execute with timeout
    execCtx, cancel := context.WithTimeout(ctx, tool.Timeout)
    defer cancel()

    start := time.Now()
    output, err := tool.Handler(execCtx, call.Function.Arguments)
    elapsed := time.Since(start)

    if err != nil {
        errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
        return ToolResult{ID: call.ID, Name: tool.Name,
            Output: errJSON, Error: err.Error(), Elapsed: elapsed}, nil
    }

    if tool.CacheTTL > 0 && output != nil {
        e.registry.Cache.Set(
            fmt.Sprintf("%s:%s", tool.Name, call.Function.Arguments),
            output, int64(len(output)))
    }

    return ToolResult{ID: call.ID, Name: tool.Name, Output: output, Elapsed: elapsed}, nil
}
```

#### 4.2.4 Built-in Tools

```go
// internal/tools/builtins.go — registered at startup

// Tool: get-current-time
registry.Register(ToolDef{
    Name:        "get-current-time",
    Description: "Returns the current UTC date and time.",
    Schema:      mustParse(`{"type":"object","properties":{}}`),
    Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
        now := time.Now().UTC()
        return json.Marshal(map[string]string{
            "utc_iso":  now.Format(time.RFC3339),
            "unix":     strconv.FormatInt(now.Unix(), 10),
            "timezone": "UTC",
        })
    },
    Timeout:  50 * time.Millisecond,
    CacheTTL: 5 * time.Second,
})

// Tool: fetch-url
registry.Register(ToolDef{
    Name:        "fetch-url",
    Description: "Fetches the content of a URL. Use for retrieving current information, documentation, or web pages.",
    Schema: mustParse(`{
        "type":"object",
        "properties":{
            "url":      {"type":"string","description":"URL to fetch"},
            "max_chars":{"type":"integer","description":"Max characters to extract (default 5000)"}
        },
        "required":["url"]
    }`),
    Handler: fetchURLHandler,  // uses agent-browser skill under the hood
    Timeout: 10 * time.Second,
    CacheTTL: 5 * time.Minute,
})

// Tool: read-file
registry.Register(ToolDef{
    Name:        "read-file",
    Description: "Reads the contents of a local file.",
    Schema: mustParse(`{
        "type":"object",
        "properties":{
            "path":   {"type":"string","description":"Absolute file path"},
            "offset": {"type":"integer","description":"Byte offset to start reading"},
            "length": {"type":"integer","description":"Maximum bytes to read (default 65536)"}
        },
        "required":["path"]
    }`),
    Handler: readFileHandler,
    Timeout: 2 * time.Second,
})

// Tool: execute-command
registry.Register(ToolDef{
    Name:        "execute-command",
    Description: "Executes a shell command on the local system.",
    Schema: mustParse(`{
        "type":"object",
        "properties":{
            "command":    {"type":"string","description":"Shell command to execute"},
            "working_dir":{"type":"string","description":"Optional working directory"}
        },
        "required":["command"]
    }`),
    Handler: execHandler,  // allowlist enforcement inside handler
    Timeout: 30 * time.Second,
})
```

#### 4.2.5 Ollama Compatibility Layer

Ollama does not natively support function calling. For Ollama routes, tools are converted to a **skill** that instructs the LLM to emit structured JSON tool calls, which the gateway then parses and executes:

```go
// internal/tools/ollama.go

func OllamaToolSkill(tools []*ToolDef) *Skill {
    var buf bytes.Buffer
    buf.WriteString("## Available Actions\n\n")
    buf.WriteString("When you need to perform an action, respond with a JSON block:\n\n")
    buf.WriteString("```json\n{\"tool\": \"<tool_name>\", \"args\": {...}}\n```\n\n")
    buf.WriteString("Available tools:\n\n")
    for _, t := range tools {
        buf.WriteString(fmt.Sprintf(
            "- **%s**: %s\n  Args: `%s`\n\n",
            t.Name, t.Description, string(t.Schema)))
    }
    buf.WriteString("After responding with a tool call, wait for results before continuing.\n")
    buf.WriteString("If multiple independent actions are needed, call them in sequence.\n")
    return &Skill{Name: "ollama-tool-compat", Description: "Ollama-compatible JSON tool calling", Content: buf.String()}
}

// Gateway parses response for {"tool": "...", "args": {...}} pattern
// and executes the matched tool, then streams the result back.
```

### 4.3 Tool + Skill Interaction

When both mechanisms are active simultaneously:

```
System prompt after injection:

[Base system prompt from config]
  ↓
[Skill blocks — 1-5 matching skills]     ← "Clarify ambiguous requests"
                                           ← "Show all steps"
  ↓
[Tool definitions — provider-specific]    ← get-current-time, fetch-url, read-file
                                           ← (OpenAI tools[] or Anthropic tools[])
  ↓
[Optional Ollama compat skill]             ← JSON tool call format instructions
```

### 4.4 Configuration

```yaml
# configs/gateway.yaml
tools:
  enabled: true
  builtin:
    - get-current-time
    - fetch-url
    - read-file
    - execute-command
  custom:
    - path: ./configs/tools.json
  cache:
    enabled: true
    max_size_mb: 256
  execution:
    max_parallel: 5
    default_timeout: 30s
    fail_open: true

skills:
  enabled: true
  path: ./configs/skills.json
  max_skills: 5
  inject_method: append
  topic_header: X-Gateway-Topics
```

---

## 5. Context Compaction

### 5.1 Trigger

```
User sends message
    │
    ▼
Count tokens in current conversation history
    │
    ▼
if total_tokens > model_context_limit * threshold (default 0.85):
    │
    ├─→ [Serve request immediately]                           ← synchronous
    │
    └─→ [Queue async job: summarize older half]               ← Temporal workflow
              │
              ▼
         Older N messages replaced with:
         1 assistant message: "[Summary]: ..."
```

### 5.2 Temporal Workflow: `conversation-summarize`

```go
// temporal/workflows/summarize.go

type SummarizeInput struct {
    ConversationID string
    Messages       []llm.Message
    TargetLang     string
}

func ConversationSummarizeWorkflow(ctx workflow.Context, in SummarizeInput) (string, error) {
    var summary string
    err := workflow.ExecuteActivity(ctx, SummarizeActivity, in).Get(ctx, &summary)
    if err != nil {
        return "", err
    }

    var updated int
    err = workflow.ExecuteActivity(ctx, UpdateConversationActivity, UpdateInput{
        ConversationID: in.ConversationID,
        OriginalCount:  len(in.Messages),
        SummaryMessage: summary,
    }).Get(ctx, &updated)

    return summary, nil
}

// temporal/activities/summarize.go

func SummarizeActivity(ctx context.Context, in SummarizeInput) (string, error) {
    // Prompt: "Summarize the following conversation concisely, preserving key facts,
    //           decisions, user intent, and any unfinished tasks."
    prompt := buildSummaryPrompt(in.Messages)
    resp, err := llmClient.Chat(ctx, llm.Request{
        Model:    in.TargetLang,
        Messages: []llm.Message{{Role: "user", Content: prompt}},
    })
    return resp.Choices[0].Message.Content, err
}
```

### 5.3 Compaction Strategies

```yaml
# configs/gateway.yaml
context:
  compaction:
    enabled: true
    threshold: 0.85          # trigger at 85% of context window
    strategy: summarize      # v1: summarize only
    # Strategies:
    #   summarize: async Temporal workflow, replace older half with summary
    #   truncate:  drop oldest messages until under threshold (sync, no cost)
    #   hybrid:    truncate if > 150% threshold, summarize if > 85%
    summary_prompt: |
      Summarize the following conversation messages concisely, preserving:
      - All factual information, numbers, and names mentioned
      - Key decisions and conclusions reached
      - User goals and constraints
      - Any unfinished tasks or follow-up items
    async: true              # fire-and-forget; don't block the response
    model: "{{ original_model }}"
```

---

## 6. Hooks — Temporal-Extensible

### 6.1 Hook Interface

```go
// internal/hooks/hooks.go

type PreHook interface {
    Name() string
    Execute(ctx context.Context, req *llm.Request) (*llm.Request, error)
}

type PostHook interface {
    Name() string
    Execute(ctx context.Context, req *llm.Request, resp *llm.Response) error
}

// Built-in implementations
type HTTPHook     { URL string; Timeout time.Duration; FailOpen bool }
type TemporalHook { WorkflowName string; TaskQueue string }
type LoggingHook  {}
type MetricsHook  {}
```

### 6.2 Temporal Integration

```go
// internal/hooks/temporal.go

type TemporalHook struct {
    client    temporalsdk_client.Client
    workflow  string
    taskQueue string
}

func (h *TemporalHook) Execute(ctx context.Context, req *llm.Request, resp *llm.Response) error {
    // Non-blocking: fire workflow and return immediately
    go func() {
        _, err := h.client.ExecuteWorkflow(
            context.Background(),
            temporalsdk_workflow.StartWorkflowOptions{
                TaskQueue: h.taskQueue,
                ID: fmt.Sprintf("hook-%s-%d", h.Name(), time.Now().UnixNano()),
            },
            h.workflow,
            HookPayload{Req: req, Resp: resp, HookName: h.Name()},
        )
        if err != nil {
            otelRecordHookError(h.Name(), err)
        }
    }()
    return nil
}
```

### 6.3 Hook Configuration

```yaml
# configs/gateway.yaml
hooks:
  pre:
    - type: http
      name: validate-request
      url: https://internal.example.com/validate
      timeout: 500ms
      fail_open: true
    - type: temporal
      name: audit-log
      workflow: gateway.audit-hook
      task_queue: gateway-hooks
      fail_open: true

  post:
    - type: temporal
      name: conversation-summarize
      workflow: gateway.conversation-summarize
      task_queue: gateway-compaction
      fail_open: true
    - type: http
      name: webhook-notify
      url: https://notify.example.com/llm
      timeout: 2s
      fail_open: true
```

Pre-hooks run in parallel; post-hooks fire asynchronously (fire-and-forget).

---

## 7. Rate Limiting

### 7.1 Algorithm

**Sliding window counter** — circular buffer, atomic operations, no locks on hot path:

```
┌──────────────────────────────────────────────────────────┐
│  Window: 60 seconds, 1-second buckets                     │
│                                                          │
│  Bucket[now-59s] │ Bucket[now-58s] │ ... │ Bucket[now]   │
│                                                          │
│  Count = sum(Bucket[-59s..now])                          │
│  If count >= limit → 429 Too Many Requests              │
│  Retry-After = seconds until oldest bucket drains        │
└──────────────────────────────────────────────────────────┘
```

### 7.2 Three Limit Dimensions

```go
type RateLimits struct {
    RPM   int64  // requests per minute
    TPM   int64  // tokens per minute (estimated via tokenizer)
    Daily int64  // requests per day
}
```

### 7.3 Implementation

```go
// internal/ratelimit/sliding_window.go

type SlidingWindowLimiter struct {
    windowSecs int64
    limit      int64
    buckets    []atomic.Int64
    head       atomic.Int64
}

func (l *SlidingWindowLimiter) Allow(key string) (allowed bool, retryAfterSecs int64) {
    now := time.Now().Unix()
    idx := now % l.windowSecs

    // Slide window
    l.head.CompareAndSwap(idx-1, idx)

    // Sum all buckets
    var total int64
    for i := int64(0); i < l.windowSecs; i++ {
        total += l.buckets[i].Load()
    }

    if total >= l.limit {
        return false, l.windowSecs - (now % l.windowSecs)
    }

    l.buckets[idx].Add(1)
    return true, 0
}
```

### 7.4 Response Headers

```http
X-RateLimit-Limit: 500
X-RateLimit-Remaining: 487
X-RateLimit-Reset: 1742809200
X-RateLimit-Window: 60s
Retry-After: 23          # only on 429
```

---

## 8. Provider Routing & Fallback

### 8.1 Provider Interface

```go
// internal/provider/router.go

type ProviderClient interface {
    Chat(ctx context.Context, req *llm.Request, tools []*tools.ToolDef) (*llm.Response, error)
    Stream(ctx context.Context, req *llm.Request, tools []*tools.ToolDef) (<-chan llm.Choice, error)
    Name() string
}

type Router struct {
    providers map[string]ProviderClient  // provider name → client
}

func (r *Router) Route(ctx context.Context, key *auth.Key, req *llm.Request) (*llm.Response, error) {
    // 1. Use key's preferred provider
    // 2. If that provider fails, try fallback chain
    // 3. Record provider attempt in trace (llm.gateway.provider.attempted)
    chain := append([]string{key.Provider}, r.fallbackChain...)
    var lastErr error
    for _, prov := range chain {
        if client, ok := r.providers[prov]; ok {
            resp, err := client.Chat(ctx, req, nil)
            if err == nil {
                return resp, nil
            }
            lastErr = err
            // Log error, emit provider failure metric, continue to next
        }
    }
    return nil, fmt.Errorf("all providers failed; last error: %w", lastErr)
}
```

### 8.2 Supported Providers

| Provider | Status | Notes |
|---|---|---|
| OpenAI | ✅ | Standard OpenAI API |
| Anthropic | ✅ | Anthropic API with Claude models |
| Azure OpenAI | ✅ | Azure endpoints, API version |
| AWS Bedrock | ✅ | Claude via Bedrock, Converse API |
| Ollama | ✅ | Local models, OpenAI compat layer |
| OpenRouter | ✅ | Unified routing, model selection |

---

## 9. Telemetry (OpenTelemetry)

### 9.1 Signal Overview

| Signal | Purpose | Backend |
|---|---|---|
| **Traces** | Request flow, per-stage latency, errors | OTLP (gRPC/HTTP) |
| **Metrics** | Token counts, operation latency, quota | Prometheus + OTLP |
| **Logs** | Structured events, correlated with spans | OTLP |

All signals share a **root span** (trace ID propagated to every child).

### 9.2 Trace Span Structure

```
llm-gateway              (root span, SERVER kind, /v1/chat/completions)
├── auth.validate_key    (CLIENT kind)
│   attributes: user.key.id, user.key.tier, ...
├── ratelimit.check      (INTERNAL)
│   attributes: ratelimit.allowed, ...
├── hooks.pre            (INTERNAL, parallel children)
├── skills.retrieve      (INTERNAL)
│   attributes: skills.retrieved_count, skill.names[]
├── skills.inject         (INTERNAL)
├── tools.inject          (INTERNAL)
│   attributes: tools.count, tool.names[]
├── provider.chat        (CLIENT kind)
│   attributes: gen_ai.* + llm.gateway.*
│   events: provider.request_tokens, provider.response_tokens
├── tools.execute        (INTERNAL, parallel children)
│   attributes: tools.executed_count, tool.name, tool.elapsed_ms
├── context.compaction.trigger (INTERNAL)
├── hooks.post           (INTERNAL, async)
└── (error spans as needed)
```

### 9.3 Metric Instruments

```
# Counter: total requests
llm_gateway_requests_total{provider, model, key_id, tier, status_code}

# Counter: total tokens (input + output)
llm_gateway_tokens_total{provider, model, key_id, type="input|output"}

# Histogram: end-to-end request duration
llm_gateway_request_duration_seconds{provider, model, key_id, status_code}

# Histogram: per-provider latency
llm_gateway_provider_duration_seconds{provider, model, operation="chat|stream"}

# Histogram: per-tool execution time
llm_gateway_tool_duration_seconds{tool_name}

# Gauge: rate limit utilization per key
llm_gateway_ratelimit_usage{key_id, type="rpm|tpm|daily"}

# Counter: rate limit rejections
llm_gateway_ratelimit_rejected_total{key_id, type="rpm|tpm|daily"}

# Counter: compaction events
llm_gateway_compaction_total{strategy, status="triggered|summarized|skipped"}

# Counter: hook invocations
llm_gateway_hook_invocations_total{hook_name, type="pre|post", status="success|error"}

# Counter: skill retrievals
llm_gateway_skills_retrieved_total{skill_name, topic}

# Counter: tool executions
llm_gateway_tool_executions_total{tool_name, status="success|error|cached"}
```

### 9.4 gen_ai.* Attribute Conventions (OTEL v1.40.0)

```go
// internal/telemetry/spans.go

func setGenAIAttributes(span trace.Span, req *llm.Request, resp *llm.Response, key *auth.Key) {
    span.SetAttributes(
        // Request
        attribute.String("gen_ai.system", req.Model),       // e.g. "openai", "anthropic"
        attribute.String("gen_ai.request.model", req.Model),
        attribute.Int("gen_ai.request.max_tokens", req.MaxTokens),
        attribute.String("gen_ai.request.temperature", fmt.Sprintf("%.2f", req.Temperature)),

        // Response
        attribute.String("gen_ai.response.model", modelName(resp)), // resolved model
        attribute.Int("gen_ai.response.token_count", resp.Usage.TotalTokens),
        attribute.Int("gen_ai.usage.input_tokens", resp.Usage.PromptTokens),
        attribute.Int("gen_ai.usage.output_tokens", resp.Usage.CompletionTokens),

        // Gateway-specific
        attribute.String("llm.gateway.provider", key.Provider),
        attribute.String("llm.gateway.key_id", key.ID),
        attribute.String("llm.gateway.key_tier", key.Tier),
        attribute.Int("llm.gateway.skills_injected", len(retrievedSkills)),
        attribute.Int("llm.gateway.tools_injected", len(injectedTools)),
        attribute.String("llm.gateway.compaction_strategy", "..."),
    )
}
```

### 9.5 OTEL Initialization

```go
// internal/telemetry/telemetry.go

func InitTelemetry(ctx context.Context, cfg TelemetryConfig) (func(), error) {
    // 1. OTLP exporter (configurable endpoint)
    exporter, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
        otlptracegrpc.WithInsecure(),
    )

    // 2. TracerProvider
    tp := trace.NewTracerProvider(
        trace.WithBatcher(exporter),
        trace.WithSampler(trace.ParentBased(
            trace.TraceIDRatioBased(cfg.SamplingRate))),
    )

    // 3. MeterProvider (OTLP + Prometheus)
    mp := metric.NewMeterProvider(
        metric.WithReader(prometheus.NewReader()),  // Prometheus scrape endpoint
        metric.WithResource(resource),
    )

    otel.SetTracerProvider(tp)
    otel.SetMeterProvider(mp)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},
        propagation.Baggage{},
    ))

    return func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        tp.Shutdown(ctx)
        mp.Shutdown(ctx)
    }, nil
}
```

---

## 10. Project Structure

```
llm-gateway/
├── cmd/
│   └── gateway/
│       └── main.go               # Entry point, wires everything
│
├── internal/
│   ├── config/
│   │   └── config.go             # YAML config loader + env var overrides
│   │
│   ├── auth/
│   │   └── keystore.go           # File-based key store, hot-reload (fsnotify)
│   │
│   ├── middleware/
│   │   ├── auth.go               # Auth middleware (key validation)
│   │   ├── ratelimit.go          # Per-key sliding window rate limiting
│   │   ├── prehooks.go           # Pre-hook dispatcher (parallel)
│   │   ├── poshooks.go           # Post-hook dispatcher (async)
│   │   ├── skills.go             # MetaClaw skill retrieval + injection
│   │   ├── tools.go              # Tool schema injection per provider
│   │   └── compaction.go         # Context compaction trigger
│   │
│   ├── llm/
│   │   ├── request.go            # Unified LLM request struct
│   │   ├── response.go           # Unified LLM response struct
│   │   └── tokenizer.go         # Token counting (cl100k_base / tiktoken)
│   │
│   ├── provider/
│   │   ├── router.go             # Provider selection + fallback chain
│   │   ├── openai.go             # OpenAI-compatible client
│   │   ├── anthropic.go          # Anthropic API client
│   │   ├── azure.go              # Azure OpenAI client
│   │   ├── bedrock.go            # AWS Bedrock (Converse API) client
│   │   └── ollama.go              # Ollama client
│   │
│   ├── tools/
│   │   ├── registry.go           # Tool registration + cache
│   │   ├── executor.go           # Parallel tool execution loop
│   │   ├── convert.go            # Schema conversion per provider
│   │   ├── builtins.go           # Built-in tools (time, fetch, read, exec)
│   │   └── ollama.go             # Ollama compat layer → skill injection
│   │
│   ├── ratelimit/
│   │   └── sliding_window.go    # Sliding window counter (atomic, lock-free)
│   │
│   ├── hooks/
│   │   ├── hook.go               # Hook interface definitions
│   │   ├── http_hook.go          # HTTP hook implementation
│   │   └── temporal_hook.go     # Temporal workflow dispatch
│   │
│   ├── telemetry/
│   │   ├── telemetry.go         # OTEL init (TP + MP + propagator)
│   │   ├── metrics.go            # Metric instruments + emission
│   │   └── spans.go              # Span attribute helpers (gen_ai.*)
│   │
│   └── gateway/
│       ├── server.go             # HTTP server, route handlers, SSE
│       └── middleware.go         # Composes all middleware into one chain
│
├── temporal/
│   ├── workflows/
│   │   ├── summarize.go          # conversation-summarize workflow
│   │   └── audit.go              # audit-hook workflow
│   │
│   └── activities/
│       ├── summarize.go          # SummarizeActivity + UpdateConversationActivity
│       └── audit.go              # AuditActivity
│
├── pkg/
│   └── skillstore/
│       └── skillstore.go         # Skill retrieval (TF-IDF keyword matching)
│
├── configs/
│   ├── gateway.yaml               # Example gateway config
│   ├── keys.json.example          # Example key store
│   ├── skills.json.example       # Example skill store
│   └── tools.json.example        # Example external tool definitions
│
├── go.mod
├── go.sum
├── Dockerfile
├── docker-compose.yaml           # Gateway + Temporal + Postgres
├── Makefile
└── README.md
```

---

## 11. Resolved Decisions

| # | Question | Decision |
|---|---|---|
| 1 | API key storage | File-based `keys.json`, hot-reload via `fsnotify` |
| 2 | Skills injection | MetaClaw retrieval → system prompt injection (keyword TF-IDF) |
| 3 | Tools injection | Traditional function calling, provider schema conversion |
| 4 | Context compaction | Async summarization via Temporal workflow (v1) |
| 5 | Hooks durability | Temporal-extensible — HTTP + Temporal dispatch |
| 6 | Rate limit algorithm | Per-key sliding window (atomic, lock-free) |
| 7 | Rate limit dimensions | RPM + TPM + daily |
| 8 | Ollama tool support | Compat layer: tools → skill → parse JSON → execute |
| 9 | OTLP backend | Open — gateway emits OTLP, user picks backend |
| 10 | Deployment target | Standard Go binary + Docker, hot config reload |

---

*Last updated: 2026-03-23*
