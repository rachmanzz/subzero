package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// CanonicalRecord defines the core SaaS database target schema: [A, B, C, D]
type CanonicalRecord struct {
	AccountID  string  `json:"account_id"`  // Column A
	HolderName string  `json:"holder_name"` // Column B
	Balance    float64 `json:"balance"`     // Column C
	Currency   string  `json:"currency"`    // Column D
}

// FinTech Beta sends raw data with structure: [A, D, B, C, A]
// Headers: [account_id, currency, user_name, total_cents, account_id_repeat]
func main() {
	sourceURI := os.Getenv("SUBZERO_SOURCE_URI")

	var r io.Reader = os.Stdin
	if strings.HasPrefix(sourceURI, "file://") {
		f, err := os.Open(strings.TrimPrefix(sourceURI, "file://"))
		if err == nil {
			defer f.Close()
			r = f
		}
	} else if sourceURI != "" {
		if f, err := os.Open(sourceURI); err == nil {
			defer f.Close()
			r = f
		}
	}

	csvReader := csv.NewReader(r)
	csvReader.TrimLeadingSpace = true

	// Skip Header
	if _, err := csvReader.Read(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading header: %v\n", err)
		os.Exit(1)
	}

	var canonicalList []CanonicalRecord

	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(record) < 5 {
			continue
		}

		// FinTech Beta mapping:
		// record[0] -> Column A (account_id)
		// record[2] -> Column B (user_name)
		// record[3] -> Column C (total_cents in cents -> convert to dollars/float)
		// record[1] -> Column D (currency)
		// record[4] is discarded!

		cents, _ := strconv.ParseInt(record[3], 10, 64)
		balanceDollars := float64(cents) / 100.0

		canonical := CanonicalRecord{
			AccountID:  strings.TrimSpace(record[0]),
			HolderName: strings.TrimSpace(record[2]),
			Balance:    balanceDollars,
			Currency:   strings.ToUpper(strings.TrimSpace(record[1])),
		}
		canonicalList = append(canonicalList, canonical)
	}

	// Output normalized canonical JSON array to Stdout
	out, _ := json.Marshal(canonicalList)
	fmt.Println(string(out))
}
