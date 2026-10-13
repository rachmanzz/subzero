# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.0.x   | :white_check_mark: |
| < 1.0   | :x:                |

## Reporting a Vulnerability

The Subzero team takes security issues seriously. If you discover a security vulnerability in Subzero, please report it privately:

1. **GitHub Security Advisory**: Report privately via [GitHub Security Advisories](https://github.com/rachmanzz/subzero/security/advisories/new).
2. **Details to include**:
   - Description of the vulnerability and affected component (Supervisor, Worker, Wasm runtime, Management API, Transport layer).
   - Minimal proof of concept (PoC) code or steps to reproduce.
   - Any potential attack vectors (e.g., guest sandbox escape, zip-slip, privilege escalation, unauthorized hostcall).
3. **Response timeline**:
   - Initial acknowledgement: within 48 hours.
   - Assessment and coordinated disclosure plan: within 7 business days.

Please do not disclose security issues publicly until a patch has been released and coordinated.
