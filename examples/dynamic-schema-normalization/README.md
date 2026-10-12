# Dynamic Schema Normalization Example

This real-world example demonstrates how Subzero solves the **Heterogeneous Tenant Ingestion Problem** in multi-tenant B2B SaaS architectures.

---

## 🎯 The Scenario

Your core platform database expects a canonical record structure:
```go
type CanonicalRecord struct {
    AccountID  string  // Column A
    HolderName string  // Column B
    Balance    float64 // Column C
    Currency   string  // Column D
}
```

### Tenant 1: Bank Alpha
* **Input Structure**: `[A, C, D, E, A, B]`
* **Columns**: `[AccountNo, CurrencyCode, BalanceFloat, InternalNote, AccountNo_Repeat, CustomerFullName]`
* Has duplicate account numbers, currency before balance, and internal notes that must be stripped.

### Tenant 2: FinTech Beta
* **Input Structure**: `[A, D, B, C, A]`
* **Columns**: `[account_id, currency, user_name, total_cents, account_id_repeat]`
* Lowercase keys, inverted column order, and amount is expressed in cents (`150050` cents = `$1500.50`).

---

## ⚡ How Subzero Normalizes This

Instead of modifying and redeploying your core SaaS API with messy `switch/case` logic:
1. **Bank Alpha Transformer**: Uploaded as `bank_alpha_transformer/main.go` ➔ compiled to `bank-alpha.wasm`.
2. **FinTech Beta Transformer**: Uploaded as `fintech_beta_transformer/main.go` ➔ compiled to `fintech-beta.wasm`.
3. When Bank Alpha sends data, Subzero spawns the Bank Alpha Wasm sandbox.
4. When FinTech Beta sends data, Subzero spawns the FinTech Beta Wasm sandbox.
5. **Both emit the exact same canonical JSON structure `[A, B, C, D]`**, ready for direct bulk database insertion!

---

## 🧪 Verification Test

Run the end-to-end normalization test:
```bash
go test -v ./examples/dynamic-schema-normalization/...
```
The test compiles both transformer modules, feeds raw disparate records into each, and asserts that both outputs produce identical `CanonicalRecord` structures.
