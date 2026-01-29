# OpenSnack

OpenSnack is a local AWS API emulator focused on Terraform/OpenTofu workflows. It exposes AWS-like HTTP endpoints and stores resource metadata in Postgres, with S3 object bodies stored on disk.

## Why this exists

OpenSnack exists because running LocalStack inside Docker or Kubernetes is often painful: it depends on the Docker socket for key features and tends to break or require privileged access. OpenSnack is a lightweight, database-backed emulator that avoids the Docker socket entirely, making it easier to run in containers and clusters.

## Quick start (local)

1) Start Postgres (Docker):

	```bash
	docker compose up -d postgres
	```

2) Export environment variables (see .env.example):

	```bash
	export OPENSNACK_PG_DSN=postgres://opensnack:snack@localhost:5438/opensnack?sslmode=disable
	export OPENSNACK_OBJECT_ROOT=/tmp/opensnack/objects
	export LOG_FORMAT=json
	export LOG_LEVEL=debug
	```

3) Run the server:

	```bash
	go run ./cmd/opensnack
	```

The server listens on http://127.0.0.1:4566.

## Docker Compose

Bring up Postgres + OpenSnack together:

```bash
docker compose up -d
```

## Tests

Run Go tests:

```bash
go test ./...
```

Load tests live in [k6/README.md](k6/README.md).

## Infra clients

- OpenTofu examples in [opentofu/](opentofu/)

## Supported APIs (current)

The following services and operations are implemented and exercised by the k6 harness:

- **S3**: CreateBucket, HeadBucket, GetBucketLocation, PutBucketVersioning, GetBucketVersioning, PutBucketAcl, GetBucketAcl, PutBucketPolicy, GetBucketPolicy, PutObject, HeadObject, GetObject, DeleteObject, DeleteBucket
- **DynamoDB**: CreateTable, DescribeTable, ListTables, UpdateTable, DescribeTimeToLive, UpdateTimeToLive, ListTagsOfResource, TagResource, DescribeContinuousBackups, PutItem, GetItem, Query, Scan, DeleteItem, DeleteTable
- **SQS**: CreateQueue, ListQueues, GetQueueUrl, GetQueueAttributes, SetQueueAttributes, SendMessage, ReceiveMessage, PurgeQueue, DeleteQueue
- **SNS**: CreateTopic, ListTopics, GetTopicAttributes, SetTopicAttributes, ListTagsForResource, TagResource, Publish, Subscribe, ListSubscriptionsByTopic, Unsubscribe, DeleteTopic
- **IAM**: CreateUser, GetUser, ListUsers, CreatePolicy, GetPolicy, ListPolicies, AttachUserPolicy, ListAttachedUserPolicies, DetachUserPolicy, DeletePolicy, DeleteUser
- **STS**: GetCallerIdentity
- **EC2**: RunInstances, DescribeInstances, TerminateInstances, CreateVolume, DescribeVolumes, DeleteVolume
- **ElastiCache**: CreateCacheCluster, DescribeCacheClusters, DeleteCacheCluster, ListTagsForResource
- **KMS**: CreateKey, DescribeKey, ListKeys, GetKeyPolicy, GetKeyRotationStatus, ListResourceTags, ScheduleKeyDeletion
- **Lambda**: CreateFunction, GetFunction, DeleteFunction, ListFunctions, GetFunctionConfiguration
- **CloudWatch Logs**: CreateLogGroup, DescribeLogGroups, DeleteLogGroup, CreateLogStream, DescribeLogStreams
- **Route53**: CreateHostedZone, GetHostedZone, ListHostedZones, DeleteHostedZone, ChangeResourceRecordSets
- **SSM**: PutParameter, GetParameter, GetParameters, DescribeParameters, DeleteParameter, ListTagsForResource
- **Secrets Manager**: CreateSecret, DescribeSecret, GetSecretValue, PutSecretValue, ListSecrets, DeleteSecret

See [k6/README.md](k6/README.md) for the full test matrix.


## License

OpenSnack is licensed under the Mozilla Public License 2.0 (MPL-2.0).

You may use, modify, and distribute this software commercially.
Modifications to OpenSnack source files must remain open.
