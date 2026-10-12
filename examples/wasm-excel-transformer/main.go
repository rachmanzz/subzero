package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// This program demonstrates a tenant-specific data transformer compiled to WebAssembly (wasip1).
// It reads CSV/tabular data, validates schema requirements, sanitizes fields,
// and outputs transformation metrics.
func main() {
	taskID := os.Getenv("SUBZERO_TASK_ID")
	tenantID := os.Getenv("SUBZERO_TENANT_ID")
	sourceURI := os.Getenv("SUBZERO_SOURCE_URI")

	fmt.Fprintf(os.Stderr, "[Tenant Transformer] Starting Wasm execution for Task: %s, Tenant: %s\n", taskID, tenantID)

	var reader io.Reader

	// If sourceURI points to a local file, read directly from mounted filesystem
	if strings.HasPrefix(sourceURI, "file://") {
		filePath := strings.TrimPrefix(sourceURI, "file://")
		if f, err := os.Open(filePath); err == nil {
			defer f.Close()
			reader = f
		}
	} else if sourceURI != "" && !strings.Contains(sourceURI, "://") {
		if f, err := os.Open(sourceURI); err == nil {
			defer f.Close()
			reader = f
		}
	}

	// For S3/MinIO (s3://) or direct pipes, data is streamed directly into Stdin by Subzero runtime
	if reader == nil {
		reader = os.Stdin
	}

	csvReader := csv.NewReader(reader)
	csvReader.TrimLeadingSpace = true

	// Read Header Row
	header, err := csvReader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Error] Failed to read CSV header: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[Tenant Transformer] Columns detected: %v\n", header)

	validRows := 0
	invalidRows := 0
	totalAmount := 0.0

	// Stream through rows sequentially (constant memory consumption)
	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			invalidRows++
			continue
		}

		// Example validation: Row must have at least 3 columns (ID, Name, Amount)
		if len(record) < 3 {
			invalidRows++
			continue
		}

		// Validate Amount is a valid number
		amount, err := strconv.ParseFloat(record[2], 64)
		if err != nil || amount < 0 {
			invalidRows++
			continue
		}

		validRows++
		totalAmount += amount
	}

	// Output summary to stdout
	fmt.Printf("VALID_ROWS=%d\n", validRows)
	fmt.Printf("INVALID_ROWS=%d\n", invalidRows)
	fmt.Printf("TOTAL_AMOUNT=%.2f\n", totalAmount)

	fmt.Fprintf(os.Stderr, "[Tenant Transformer] Process completed. Valid rows: %d, Invalid rows: %d, Sum: %.2f\n",
		validRows, invalidRows, totalAmount)
}
