# Protocol & Wire Framing Specification

This document details the binary wire framing protocol, cryptographic signing, and entity formats used across all Subzero communication channels.

---

## 1. Wire Framing Architecture

Control plane communication in Subzero prioritizes minimal CPU overhead, low memory allocations, and tamper resistance. All data transmitted between Producers, Supervisors, and Workers is wrapped in a strict binary framing layout:

```text
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Payload Length (Uint32)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
+                                                               +
|                                                               |
+                  Integrity Signature (32 Bytes)               +
|                          (HMAC-SHA256)                        |
+                                                               +
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
|                        Payload Body (N Bytes)                 |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

### Segment Breakdown

| Segment | Offset | Length | Type | Description |
| :--- | :--- | :--- | :--- | :--- |
| **Payload Length** | `0` | `4 Bytes` | `Uint32` (Big-Endian) | The exact length in bytes of the following payload body. |
| **Integrity Signature** | `4` | `32 Bytes` | Raw Binary | Cryptographic HMAC-SHA256 hash computed over the payload body. |
| **Payload Body** | `36` | `Length Bytes` | Binary Slice | Serialized entity data (e.g. `TaskMessage` or `ResultMessage`). |

### Guardrails & Constraints
* **Header Size**: Exactly 36 bytes (`4 + 32`).
* **Maximum Payload Size**: Configurable upper bound (`DefaultMaxPayloadSize = 64MB`) to reject malicious or corrupt length headers before allocating memory buffers.

---

## 2. Cryptographic Security & HMAC-SHA256

Every frame requires validation against a shared symmetric key:

```go
func Sign(payload, secretKey []byte) []byte {
    mac := hmac.New(sha256.New, secretKey)
    mac.Write(payload)
    return mac.Sum(nil)
}
```

### Constant-Time Verification
Verification uses `hmac.Equal` to prevent side-channel timing attacks:
```go
func Verify(payload, signature, secretKey []byte) bool {
    if len(signature) != 32 {
        return false
    }
    expected := Sign(payload, secretKey)
    return hmac.Equal(signature, expected)
}
```
If a payload is tampered with or the wrong key is supplied, `ReadFrame` immediately terminates the connection with `ErrInvalidSignature`.

---

## 3. Message Entity Schemas

### 3.1 TaskMessage
Sent by the Producer to instruct a Worker to execute a transformation.

```json
{
  "task_id": "b1b70275-c9f2-472e-8cb5-15a97492c6ee",
  "tenant_id": "tenant-enterprise-1",
  "action_target": "custom-excel-parser",
  "source_uri": "s3://corp-bucket/tenant-1/october-data.xlsx",
  "custom_config": {
    "sheet_name": "Revenue",
    "header_row": "2"
  }
}
```

### 3.2 ResultMessage
Returned by the Worker upon task completion.

```json
{
  "task_id": "b1b70275-c9f2-472e-8cb5-15a97492c6ee",
  "is_success": true,
  "processed_rows": 154200,
  "error_message": "",
  "execution_time": "342.15ms"
}
```

### 3.3 UploadSlotResponse
Provided to client applications to perform direct, out-of-band document uploads.

```json
{
  "target_url": "https://storage.provider.com/upload-signed?signature=...",
  "http_method": "PUT",
  "required_headers": {
    "Content-Type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
  },
  "storage_reference": "tenant-1/october-data.xlsx",
  "expired_at": "2026-10-12T10:45:00Z"
}
```
