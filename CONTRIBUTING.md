# Contributing to OpenSnack

Thanks for your interest in contributing to OpenSnack!  
Contributions are welcome, whether that’s bug reports, fixes, documentation, or new functionality.

OpenSnack is an **opinionated, correctness-focused AWS emulator**, primarily intended for local development, testing, and training workflows (e.g. Terraform/OpenTofu). Please keep that goal in mind when contributing.

---

## Code of Conduct

Be respectful, constructive, and professional.  
This project follows standard open source community norms. Harassment, hostility, or bad-faith participation will not be tolerated.

---

## What Makes a Good Contribution

OpenSnack values:

- **Correctness over completeness**
- **Predictable, deterministic behaviour**
- **Clear, readable code**
- **Well-documented decisions**
- **Minimal magic**

Good contributions include:
- Bug fixes
- Behaviour corrections to better match AWS semantics
- Improvements to Terraform/OpenTofu compatibility
- Tests that lock in correct behaviour
- Documentation improvements
- Small, well-scoped features

---

## What This Project Is *Not*

Please avoid contributions that:

- Attempt to fully re-implement AWS services
- Add large amounts of speculative or unused functionality
- Introduce hidden state, side effects, or non-determinism
- Optimise for performance at the cost of clarity or correctness
- Add “enterprise” features (auth, billing, multi-tenant SaaS concerns, etc.)

Those may be valid ideas — they’re just out of scope here.

---

## Licensing

OpenSnack is licensed under the **Mozilla Public License 2.0 (MPL-2.0)**.

By contributing, you agree that:
- Your contributions will be licensed under MPL-2.0
- Modifications to existing OpenSnack source files remain open under MPL-2.0
- You retain copyright to your contributions

This allows OpenSnack to remain open while still being usable in commercial and proprietary environments.

---

## How to Contribute

### 1. Fork and branch
Create a fork and work on a feature branch:

```bash
git checkout -b fix/s3-put-object-edge-case
```
