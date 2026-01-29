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
````

### 2. Keep changes focused

Small, targeted pull requests are strongly preferred over large, sweeping ones.

If you’re planning something non-trivial, consider opening an issue first to discuss approach and scope.

---

### 3. Write tests where appropriate

If you’re changing behaviour, please include tests that demonstrate:

* The previous incorrect behaviour (if applicable)
* The expected correct behaviour

Tests are especially important for:

* AWS API semantics
* Terraform/OpenTofu interactions
* Edge cases and error handling

---

### 4. Follow existing style

* Match existing code style and structure
* Avoid introducing new patterns without justification
* Prefer clarity over cleverness

If something feels surprising, it probably doesn’t belong.

---

### 5. Open a Pull Request

In your PR description, please include:

* What the change does
* Why it’s needed
* Any relevant AWS or Terraform references
* Any known limitations or trade-offs

---

## Reporting Bugs

Bug reports are very welcome.

Please include:

* What you expected to happen
* What actually happened
* Steps to reproduce
* Relevant logs or error output
* Terraform/OpenTofu versions if applicable

Clear bug reports make fixing things much easier.

---

## Security Issues

If you believe you’ve found a security issue, **please do not open a public issue**.

Instead, report it privately via GitHub Security Advisories or the contact method listed in `SECURITY.md`.

---

## Final Note

OpenSnack is intentionally opinionated and intentionally scoped.

Not every idea will be a good fit — and that’s okay.
Thoughtful discussion is always welcome, even if a change isn’t accepted.

Thanks for helping make OpenSnack better.
