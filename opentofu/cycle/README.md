# Resource cycle cases

Each directory under `cases/` is one check that a resource type works the way Terraform expects:

1. `apply` succeeds.
2. A second `plan` shows no changes.
3. `apply -var variant=b` updates in place (no replacement), and the plan after it is clean.
4. Every resource is removed from state and imported back with `import` blocks, and that plan changes nothing.
5. `destroy` succeeds, and the inspection API shows the namespace is empty.

Run them against a dev-mode opensnack that treats `localtest.me` as an AWS domain. `*.localtest.me` resolves to 127.0.0.1, so the provider talks to real-style host names such as `sqs.us-east-1.localtest.me` and `bucket.s3.us-east-1.localtest.me`:

```bash
OPENSNACK_HOST_SUFFIXES=localtest.me go run ./cmd/opensnack &
go run ./cmd/tfcycle opentofu/cycle/cases/*
```

CI doesn't run these: the full set takes the best part of an hour. Run it before merging a change to a service, or just the cases you touched:

```bash
go run ./cmd/tfcycle opentofu/cycle/cases/sqs_* opentofu/cycle/cases/sns_*
```

You need OpenTofu 1.6 or later, or Terraform 1.5 or later (for `import` blocks). Pass `-tofu` to choose the binary and `-provider` to pin the AWS provider version.

Writing a case:

- Don't declare a provider. The harness adds `aws` (us-east-1) and `aws.west` (eu-west-1), plus `variable "variant"`.
- Make at least one updatable argument depend on `var.variant` ("a" first, then "b").
- Resources that don't import by their `id` attribute need a line in `imports.txt`, e.g. `aws_iam_role_policy_attachment.a {role}/{policy_arn}`.
- `no_import.txt` lists addresses that AWS itself can't import.
