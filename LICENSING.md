# Licensing Policy

This project is **dual licensed**. This document explains which license applies to which part of the codebase, and what the current licensing status is.

## Status Summary

| Item | Status |
|------|--------|
| Default license | **AGPL-3.0** - the only license currently in effect |
| Commercial license | **Not established yet** - no commercial licensing mechanism exists at present |
| Intended commercial option | **SSPL-1.0**, should a commercial offering become necessary |
| Commercial contact channel | **Not defined yet** |

> **Important:** As of today, there is no commercial licensing mechanism for this project. Everything in this repository is licensed under AGPL-3.0 and nothing else. SSPL-1.0 is documented only as the intended future option, and grants no rights today.

## License Files

| File | License | Applies to |
|------|---------|-----------|
| [`LICENSE`](LICENSE) | GNU AGPL-3.0 | All code currently in this repository |
| [`LICENSE-SSPL-1.0.txt`](LICENSE-SSPL-1.0.txt) | SSPL-1.0 | Future commercial option, not yet applied |

## 1. AGPL-3.0 (Default)

The full license text is in [`LICENSE`](LICENSE).

The AGPL-3.0 was chosen because it satisfies the following project requirements:

1. **Users must comply.** Recipients of the software are bound by all AGPL-3.0 terms for as long as they use or distribute it, not merely by an attribution requirement.
2. **Derivatives must use the same license.** Strong copyleft. Every derivative work and every modification must be licensed under AGPL-3.0. A closed-source derivative is not allowed.
3. **The original author must be credited.** Copyright and licence notices must be preserved in every copy. Removing them is a licence violation.
4. **The copyright holder is not bound.** The AGPL places obligations on recipients, not on the copyright holder. The holder stays free to dual-license, sell commercial licenses, or publish the work under other terms.

### Network Use Copyleft

AGPL-3.0 section 13 extends the copyleft obligation to network use. Anyone who modifies this software and makes it available to users over a network must offer those users the corresponding source code of the modified version. This closes the "SaaS loophole" where a closed-service deployment would otherwise avoid the copyleft obligation.

## 2. SSPL-1.0 (Commercial Option)

The full license text is in [`LICENSE-SSPL-1.0.txt`](LICENSE-SSPL-1.0.txt).

> **This license does not apply to any code at present.** It is recorded here for reference and for future decisions.

SSPL-1.0 is included because it is a strong copyleft license built for server-side software, which matches this project. It would serve as the commercial option if the project ever needs to offer terms that AGPL-3.0 cannot satisfy, such as allowing a party to run a competing service without publishing their service source code.

### Properties of SSPL-1.0

- **Not open source.** The OSI rejected SSPL-1.0 because its service provision clause is considered discriminatory against commercial users. It is source available, nothing more.
- **Stricter than AGPL for services.** Where AGPL requires releasing the source of the licensed work itself, SSPL requires releasing the source of the **entire service stack** needed for a user to run their own instance, including management software, user interfaces, APIs, automation, monitoring, backup, storage, and hosting software.
- **Forbids competing offerings.** SSPL-1.0 grants no permission to offer the functionality of the software to third parties as a competing service.
- **Not GPL compatible.** Code under SSPL-1.0 cannot simply be combined with code under the GPL.

## 3. Dual Licensing Rules

Because the AGPL-3.0 and SSPL-1.0 are alternatives and not layers, the following rules apply:

1. **One license per piece of code.** A given file is either AGPL-3.0 or SSPL-1.0. The two never stack.
2. **Relicensing is forward only.** Code already published under AGPL-3.0 has been granted to recipients irrevocably. That published version can never be withdrawn or changed to SSPL-1.0. Only future versions and never-published code may be relicensed.
3. **Separate enterprise modules.** Any code intended for an SSPL-1.0 or proprietary release must live in its own module or branch and must never appear in an AGPL-3.0 release. Once code is published under AGPL-3.0, it is permanently AGPL-3.0.
4. **Record the decision.** Any relicensing decision is recorded as an Architecture Decision Record, with the reasoning, the affected versions, and the date.

### Intended Structure

```
subzero/
├── cmd/                 -> AGPL-3.0 (core binaries, always open)
├── internal/            -> AGPL-3.0 (runtime, supervisor, storage, worker)
├── pkg/                 -> AGPL-3.0 (management, permission, protocol, sdk, transport)
└── <enterprise-module>/ -> SSPL-1.0 or proprietary (never released under AGPL-3.0)
```

The strategy is that the core engine stays open under AGPL-3.0 to drive community adoption and security transparency, while any future commercial offering is delivered through support, enterprise SLAs, and separately licensed enterprise features that were never published under AGPL-3.0.

## 4. Contributor Terms

Contributions submitted to this repository are accepted under **AGPL-3.0**.

The copyright holder reserves the right to offer the same work, or any future version of it, under additional or different terms, including SSPL-1.0 or a proprietary commercial license.

By contributing, you confirm that:

- Your contribution is your own original work, or that you have the right to submit it under these terms.
- You grant the copyright holder the rights needed to dual-license the work as described above.

## 5. SPDX and Source Headers

Each file should carry an SPDX identifier so automated tooling can detect the license:

```
SPDX-License-Identifier: AGPL-3.0-only
```

or, for modules intended for the commercial option:

```
SPDX-License-Identifier: SSPL-1.0
```

`AGPL-3.0-only` rather than `AGPL-3.0-or-later` is used deliberately, so that a future version change is an explicit decision rather than an automatic consequence.

## 6. Future Changes

If a commercial licensing mechanism is established later, the following must happen together:

1. Add a `COMMERCIAL-LICENSE.md` describing terms, pricing, and coverage.
2. Publish an official contact channel for commercial enquiries.
3. Announce the change in the README and release notes.
4. Update this document to state the effective date and the versions covered.
5. Ensure that AGPL-3.0 obligations are never weakened retroactively for already published versions.

Until all of the above is done, **AGPL-3.0 is the only license in effect**.

## 7. References

- [GNU AGPL-3.0 full text](https://www.gnu.org/licenses/agpl-3.0.txt)
- [Server Side Public License](https://www.mongodb.com/legal/licensing/server-side-public-license)
- [SSPL-1.0 on SPDX](https://spdx.org/licenses/SSPL-1.0.html)
