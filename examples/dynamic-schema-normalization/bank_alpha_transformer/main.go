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

// Bank Alpha sends raw data with structure: [A, C, D, E, A, B]
// Headers: [AccountNo, CurrencyCode, BalanceFloat, InternalNote, AccountNo_Repeat, CustomerFullName]
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
		if err != nil || len(record) < 6 {
			continue
		}

		// Bank Alpha mapping:
		// record[0] -> Column A (AccountNo)
		// record[5] -> Column B (CustomerFullName)
		// record[2] -> Column C (BalanceFloat)
		// record[1] -> Column D (CurrencyCode)
		// record[3] and record[4] are discarded!

		bal, _ := strconv.ParseFloat(record[2], 64)

		canonical := CanonicalRecord{
			AccountID:  strings.TrimSpace(record[0]),
			HolderName: strings.TrimSpace(record[5]),
			Balance:    bal,
			Currency:   strings.ToUpper(strings.TrimSpace(record[1])),
		}
		canonicalList = append(canonicalList, canonical)
	}

	// Output normalized canonical JSON array to Stdout
	out, _ := json.Marshal(canonicalList)
	fmt.Println(string(out))
}
