# OpenSnack k6 Tests

Terraform-like k6 load tests for the OpenSnack AWS service emulator.

## Prerequisites

- [k6](https://k6.io/docs/getting-started/installation/) installed
- OpenSnack server running (default: `http://127.0.0.1.nip.io:4566`)

## Quick Start

```bash
# Start OpenSnack (in project root)
./start.sh

# Run a single service test
k6 run k6/tests/s3.test.js

# Run all services test
k6 run k6/tests/all-services.test.js

# Run load test
k6 run k6/tests/load.test.js
```

## Test Structure

```
k6/
├── config.js              # Configuration and base helpers
├── lib/
│   ├── aws.js             # AWS protocol helpers (Query, JSON, REST)
│   └── checks.js          # Common assertion helpers
├── tests/
│   ├── s3.test.js         # S3 bucket & object lifecycle tests
│   ├── dynamodb.test.js   # DynamoDB table & item tests
│   ├── sqs.test.js        # SQS queue & message tests
│   ├── sns.test.js        # SNS topic & subscription tests
│   ├── iam.test.js        # IAM user & policy tests
│   ├── all-services.test.js  # Combined multi-service test
│   └── load.test.js       # Load testing scenario
└── README.md
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OPENSNACK_ENDPOINT` | `http://127.0.0.1.nip.io:4566` | OpenSnack server URL |
| `K6_NAMESPACE` | `k6-{timestamp}` | Namespace for test isolation |
| `K6_CLEANUP` | `false` | Set to `true` to delete resources after tests |

## Namespace Isolation

Tests use the `User-Agent` header with a `custom-{namespace}` suffix for namespace isolation.
This approach is compatible with Terraform, which cannot set custom headers.

Example User-Agent: `k6/opensnack-test custom-k6-1702742400000`

OpenSnack extracts the namespace from User-Agent strings ending with `custom-{name}`.

## Resource Cleanup

By default, tests **do not delete** created resources so you can inspect them in the database.

```bash
# Run without cleanup (default) - resources retained
k6 run k6/tests/s3.test.js

# Run with cleanup - resources deleted after test
k6 run -e K6_CLEANUP=true k6/tests/s3.test.js
```

**Note:** The `load.test.js` always cleans up resources regardless of this setting to avoid DB bloat.

## Test Scenarios

### Individual Service Tests

Each service test follows the Terraform lifecycle pattern:
1. **Create** - Similar to `terraform apply` creating resources
2. **Read** - Similar to `terraform plan` checking state
3. **Update** - Similar to `terraform apply` with changes
4. **Delete** - Similar to `terraform destroy`
5. **Verify** - Confirm resource deletion

```bash
# S3 tests (buckets, objects, versioning, ACLs, policies)
k6 run k6/tests/s3.test.js

# DynamoDB tests (tables, items, TTL, tags)
k6 run k6/tests/dynamodb.test.js

# SQS tests (queues, messages, attributes)
k6 run k6/tests/sqs.test.js

# SNS tests (topics, subscriptions, publish)
k6 run k6/tests/sns.test.js

# IAM tests (users, policies, attachments)
k6 run k6/tests/iam.test.js
```

### All Services Test

Exercises all services in a single run:

```bash
k6 run k6/tests/all-services.test.js
```

### Load Testing

Simulates multiple Terraform operations running concurrently:

```bash
# Default load test (5 VUs steady, 20 VUs spike)
k6 run k6/tests/load.test.js

# Custom load
k6 run --vus 10 --duration 5m k6/tests/load.test.js

# With HTML report
k6 run --out json=results.json k6/tests/load.test.js
```

## Custom Configuration

Override the endpoint for different environments:

```bash
# Local development
k6 run -e OPENSNACK_ENDPOINT=http://localhost:4566 k6/tests/s3.test.js

# Custom namespace (for parallel test runs)
k6 run -e K6_NAMESPACE=my-test-run k6/tests/all-services.test.js
```

## Metrics

The load test tracks custom metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `terraform_apply_total` | Counter | Total successful resource creations |
| `terraform_destroy_total` | Counter | Total successful resource deletions |
| `resource_create_success` | Rate | Success rate for create operations |
| `resource_delete_success` | Rate | Success rate for delete operations |
| `api_latency_ms` | Trend | API call latency distribution |

## Thresholds

Default performance thresholds:

- HTTP request failure rate < 10%
- 95th percentile response time < 2s
- Resource create success rate > 90%
- Resource delete success rate > 90%

## Examples

### Run with verbose output

```bash
k6 run --http-debug=full k6/tests/s3.test.js
```

### Run with custom thresholds

```bash
k6 run --threshold 'http_req_duration{p95<500}' k6/tests/dynamodb.test.js
```

### Export results to JSON

```bash
k6 run --out json=results.json k6/tests/all-services.test.js
```

### Run tests in CI

```bash
# Exit with non-zero if thresholds fail
k6 run --no-color k6/tests/load.test.js
```

## Test Coverage

| Service | Operations Tested |
|---------|-------------------|
| S3 | CreateBucket, HeadBucket, GetBucketLocation, PutBucketVersioning, GetBucketVersioning, PutBucketAcl, GetBucketAcl, PutBucketPolicy, GetBucketPolicy, PutObject, HeadObject, GetObject, DeleteObject, DeleteBucket |
| DynamoDB | CreateTable, DescribeTable, ListTables, UpdateTable, DescribeTimeToLive, UpdateTimeToLive, ListTagsOfResource, TagResource, DescribeContinuousBackups, PutItem, GetItem, Query, Scan, DeleteItem, DeleteTable |
| SQS | CreateQueue, ListQueues, GetQueueUrl, GetQueueAttributes, SetQueueAttributes, SendMessage, ReceiveMessage, PurgeQueue, DeleteQueue |
| SNS | CreateTopic, ListTopics, GetTopicAttributes, SetTopicAttributes, ListTagsForResource, TagResource, Publish, Subscribe, ListSubscriptionsByTopic, Unsubscribe, DeleteTopic |
| IAM | CreateUser, GetUser, ListUsers, CreatePolicy, GetPolicy, ListPolicies, AttachUserPolicy, ListAttachedUserPolicies, DetachUserPolicy, DeletePolicy, DeleteUser |
| STS | GetCallerIdentity |
| EC2 | RunInstances, DescribeInstances, TerminateInstances, CreateVolume, DescribeVolumes, DeleteVolume |
| ElastiCache | CreateCacheCluster, DescribeCacheClusters, DeleteCacheCluster, ListTagsForResource |
| KMS | CreateKey, DescribeKey, ListKeys, GetKeyPolicy, GetKeyRotationStatus, ListResourceTags, ScheduleKeyDeletion |
| Lambda | CreateFunction, GetFunction, DeleteFunction, ListFunctions, GetFunctionConfiguration |
| CloudWatch Logs | CreateLogGroup, DescribeLogGroups, DeleteLogGroup, CreateLogStream, DescribeLogStreams |
| Route53 | CreateHostedZone, GetHostedZone, ListHostedZones, DeleteHostedZone, ChangeResourceRecordSets |
| SSM | PutParameter, GetParameter, GetParameters, DescribeParameters, DeleteParameter, ListTagsForResource |
| SecretsManager | CreateSecret, DescribeSecret, GetSecretValue, PutSecretValue, ListSecrets, DeleteSecret |
