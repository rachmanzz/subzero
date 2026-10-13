package normalization_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/pkg/management"
)

type CanonicalRecord struct {
	AccountID  string  `json:"account_id"`
	HolderName string  `json:"holder_name"`
	Balance    float64 `json:"balance"`
	Currency   string  `json:"currency"`
}

func TestDynamicSchemaNormalizationE2E(t *testing.T) {
	ctx := context.Background()
	compiler := management.NewGoCompiler("")

	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// --- 1. Test Bank Alpha: Raw Format [A, C, D, E, A, B] ---
	alphaSource, err := os.ReadFile(filepath.Join("bank_alpha_transformer", "main.go"))
	if err != nil {
		t.Fatal(err)
	}

	alphaWasm, err := compiler.Compile(ctx, alphaSource, management.CompileOptions{
		TenantID:   "bank-alpha",
		ModuleName: "alpha-norm",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Bank Alpha compilation failed: %v", err)
	}

	// Raw input from Bank Alpha: [AccountNo, CurrencyCode, BalanceFloat, InternalNote, AccountNo_Repeat, CustomerFullName]
	alphaRawCSV := `AccountNo,CurrencyCode,BalanceFloat,InternalNote,AccountNo_Repeat,CustomerFullName
ACC-001,USD,1500.50,CONFIDENTIAL_NOTE,ACC-001,John Doe
`
	alphaRes, err := engine.Execute(ctx, alphaWasm.WasmBytes, wasm.ExecOptions{
		Stdin:   strings.NewReader(alphaRawCSV),
		Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("Bank Alpha execution failed: %v (stderr: %s)", err, alphaRes.Stderr)
	}

	var alphaRecords []CanonicalRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(alphaRes.Stdout)), &alphaRecords); err != nil {
		t.Fatalf("Failed to decode Bank Alpha canonical output: %v (raw: %s)", err, alphaRes.Stdout)
	}

	if len(alphaRecords) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(alphaRecords))
	}

	recordAlpha := alphaRecords[0]
	if recordAlpha.AccountID != "ACC-001" || recordAlpha.HolderName != "John Doe" || recordAlpha.Balance != 1500.50 || recordAlpha.Currency != "USD" {
		t.Fatalf("Unexpected canonical record from Bank Alpha: %+v", recordAlpha)
	}

	// --- 2. Test FinTech Beta: Raw Format [A, D, B, C_in_cents, A] ---
	betaSource, err := os.ReadFile(filepath.Join("fintech_beta_transformer", "main.go"))
	if err != nil {
		t.Fatal(err)
	}

	betaWasm, err := compiler.Compile(ctx, betaSource, management.CompileOptions{
		TenantID:   "fintech-beta",
		ModuleName: "beta-norm",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("FinTech Beta compilation failed: %v", err)
	}

	// Raw input from FinTech Beta: [account_id, currency, user_name, total_cents, account_id_repeat]
	// Notice: 150050 cents = 1500.50 dollars!
	betaRawCSV := `account_id,currency,user_name,total_cents,account_id_repeat
ACC-001,usd,John Doe,150050,ACC-001
`
	betaRes, err := engine.Execute(ctx, betaWasm.WasmBytes, wasm.ExecOptions{
		Stdin:   strings.NewReader(betaRawCSV),
		Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("FinTech Beta execution failed: %v (stderr: %s)", err, betaRes.Stderr)
	}

	var betaRecords []CanonicalRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(betaRes.Stdout)), &betaRecords); err != nil {
		t.Fatalf("Failed to decode FinTech Beta canonical output: %v (raw: %s)", err, betaRes.Stdout)
	}

	if len(betaRecords) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(betaRecords))
	}

	recordBeta := betaRecords[0]

	// --- 3. ASSERT: Both distinct formats produced the exact IDENTICAL Canonical Record [A, B, C, D]! ---
	if recordBeta != recordAlpha {
		t.Fatalf("Normalization mismatch! Alpha: %+v, Beta: %+v", recordAlpha, recordBeta)
	}

	t.Logf("✓ Dynamic Schema Normalization Verified! Both tenants produced canonical: %+v", recordAlpha)
}
