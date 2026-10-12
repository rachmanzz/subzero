# Dynamic Multi-Tenant Data Transformation & Schema Normalization

This architectural guide explains the core problem Subzero solves for enterprise B2B SaaS platforms: **normalizing disparate tenant data formats into a unified canonical database schema using sandboxed WebAssembly execution**.

---

## 1. The Core Enterprise Problem: Schema Heterogeneity

In B2B SaaS platforms serving diverse enterprise clients (Banks, FinTechs, Logistics, Healthcare):
* **Your Core System Database** requires a strict, canonical data schema:
  $$\text{Canonical Target Schema: } [A, B, C, D]$$
* **Every Client Has Their Own Custom Format**:
  * **Bank Alpha** transmits columns: `[A, C, D, E, A, B]` (custom ordering, duplicate identifiers, internal metadata).
  * **FinTech Beta** transmits columns: `[A, D, B, C, A]` (inverted timestamps, different currency scaling).
  * **Enterprise Gamma** sends nested JSON or XML payloads via webhook.

```text
       Tenant Input Formats                     Subzero Wasm Sandboxes             Canonical Database
                                                                                  
[ Bank Alpha:    A C D E A B ] ───► [ Wasm Module: alpha-normalizer ] ──┐
                                                                        │
[ FinTech Beta:  A D B C A   ] ───► [ Wasm Module: beta-normalizer  ] ──┼──► CANONICAL SCHEMA:
                                                                        │      [ A, B, C, D ]
[ Enterprise Gamma: (JSON)   ] ───► [ Wasm Module: gamma-normalizer ] ──┘  (Clean, validated, ready
                                                                             for batch DB insertion)
```

---

## 2. Why Traditional Architectures Break Down

| Traditional Monolithic Approach | Subzero Sandboxed Engine |
| :--- | :--- |
| **Code Bloat & Re-deployment**: Every time a new client onboards, backend engineers must write hardcoded parsing logic and re-compile/re-deploy the main API server. | **Zero-Downtime Tenant Onboarding**: Upload the tenant's normalization script to the Subzero Dashboard; it compiles to Wasm and activates instantly without touching the main API. |
| **No Fault Isolation**: A bug, nil-pointer exception, or unexpected data structure in Bank Alpha's parser crashes the entire monolithic backend for all clients. | **Total Blast-Radius Containment**: Each tenant runs in an isolated WebAssembly sandbox. If Bank Alpha's script panics, only that sandbox fails; all other tenants remain 100% operational. |
| **Wasted Resources**: Custom parsers run in always-on worker pools consuming expensive RAM even if the bank only sends data once per day. | **Scale-to-Zero**: Workers spawn on demand (sub-11ms cold start), normalize the batch, and automatically terminate when idle (0 MB idle cost). |

---

## 3. Any Data Source, Any Data Format

Subzero is format-agnostic. While spreadsheet documents are common, Subzero normalizes:
* **Tabular streams**: CSV, TSV, XLSX.
* **Structured streams**: JSON lines, nested JSON payloads.
* **Financial & EDI formats**: Bank statements, MT940, ISO20022, custom pipe-delimited records.
* **Webhook payloads**: Raw HTTP event streams.

---

## 4. Normalization Workflow

1. **Client Ingestion**: Client data arrives (via direct storage upload or API payload reference).
2. **Task Dispatch**: Main API submits a `TaskMessage` specifying `ActionTarget: "tenant-bank-alpha/normalizer"`.
3. **Sandboxed Execution**: Subzero spawns or wakes the worker, loads `alpha-normalizer.wasm` inside the Wazero sandbox, and streams raw input.
4. **Canonical Emission**: The Wasm module sanitizes values, maps disparate keys, and emits clean canonical JSON/binary records matching `[A, B, C, D]`.
5. **Database Ingestion**: The clean stream is batch-inserted directly into your database.
