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

The server listens on http://127.0.0.1:4566 (`OPENSNACK_LISTEN_ADDR` changes it). The schema is created or upgraded on start.

## Tenants, credentials and regions

Every request runs as a tenant (a *namespace*), in an AWS account and a region:

- **Namespace**: the SigV4 access key ID, or the `namespace` the credential source assigns to that key.
- **Account**: the credential's `account_id`, else `OPENSNACK_ACCOUNT_ID` (default `000000000000`). It shows up in ARNs, queue URLs and `sts get-caller-identity`.
- **Region**: the region in the SigV4 credential scope, else the one in the host name, else `OPENSNACK_DEFAULT_REGION` (default `us-east-1`). Regional resources are stored per region, so the same name can exist in two. IAM, Route 53, S3 and STS are global.

| Variable | Meaning |
|---|---|
| `OPENSNACK_AUTH_MODE` | `dev` (default): accept anything. The namespace is the legacy `custom-<ns>` User-Agent token if present, else the access key ID. `strict`: verify every signature. Unsigned requests, unknown keys and bad signatures get the real AWS errors (`InvalidClientTokenId`, `SignatureDoesNotMatch`, ...). |
| `OPENSNACK_CREDENTIALS_FILE` | JSON list of `{"access_key_id", "secret_access_key", "account_id", "namespace"}` (the last two optional). |
| `OPENSNACK_CREDENTIALS_URL` | Lookup callback: `GET <url>?access_key_id=AKID`. It returns a credential as JSON with 200, or 404 for an unknown key. Answers are cached for 5 minutes (30 seconds for unknown keys). |
| `OPENSNACK_CREDENTIALS_TOKEN` | Sent as `Authorization: Bearer` to the callback. |

Strict mode needs a file, a URL, or both (the file is checked first). If a proxy sits in front of OpenSnack, it must pass the `Host` header through unchanged, because the signature covers it.

OpenSnack answers on the real AWS host names (`{service}.{region}.amazonaws.com`, the global `iam`/`route53`/`sts` hosts, `{bucket}.s3[.{region}].amazonaws.com`, `{account}.s3-control.{region}.amazonaws.com`) as well as on plain `localhost`, where the SigV4 signing service picks the service. `OPENSNACK_HOST_SUFFIXES=localtest.me` makes it treat another domain like `amazonaws.com`, which is useful locally.

## Limits

| Variable | Default | Meaning |
|---|---|---|
| `OPENSNACK_MAX_REQUEST_BYTES` | 80 MiB | Request body cap, except S3 uploads (`RequestEntityTooLargeException`). |
| `OPENSNACK_MAX_OBJECT_BYTES` | 100 MiB | S3 object upload cap (`EntityTooLarge`). |
| `OPENSNACK_MAX_RESOURCES` | off | Resources per namespace. At the cap, creates fail with `LimitExceeded`/`LimitExceededException`/`TooManyBuckets`; updates and deletes still work. |
| `OPENSNACK_MAX_S3_BYTES` | off | Total S3 object bytes per namespace (`QuotaExceeded`). |
| `OPENSNACK_RATE_LIMIT`, `OPENSNACK_RATE_BURST` | off, 2x rate | Requests per second per namespace. Over the rate, requests get the throttling codes SDKs retry (`SlowDown`, `ThrottlingException`, `Throttling`). |
| `OPENSNACK_PG_MAX_OPEN`, `OPENSNACK_PG_MAX_IDLE` | 25, 10 | Postgres connection pool, per process. |

## Inspection API

`/_opensnack/v1/` is OpenSnack's own API, for checkers and test harnesses. It sits outside AWS authentication and requires `Authorization: Bearer $OPENSNACK_INTERNAL_TOKEN`. Without a token it is served only in dev mode. Keep it off the public network.

| Request | Returns |
|---|---|
| `GET /_opensnack/v1/namespaces` | Every namespace with its resource count. |
| `GET /_opensnack/v1/namespaces/{ns}` | `{"total": n, "by_service": {...}}` |
| `DELETE /_opensnack/v1/namespaces/{ns}` | Deletes every resource and object in the namespace. |
| `GET /_opensnack/v1/namespaces/{ns}/resources?service=&type=&region=&id=` | Matching resources, with full attributes, `created_at` and `updated_at`. `region=` (empty) selects global resources. |
| `GET /_opensnack/v1/namespaces/{ns}/resources/{service}/{type}/{id}?region=` | One resource. |
| `GET /_opensnack/v1/namespaces/{ns}/s3/{bucket}/{key}` | An object's bytes. |

`created_at` never changes after a resource is created, and `updated_at` moves on every in-place update. A different `created_at` for the same name therefore means the resource was replaced.

`GET /_opensnack/health` is an unauthenticated liveness check.

## Resource cycle harness

`go run ./cmd/tfcycle opentofu/cycle/cases/*` checks that each resource applies, plans clean, updates in place, imports and destroys cleanly. CI doesn't run it, because the full set takes the best part of an hour; run it locally before merging a change to a service. See [opentofu/cycle/README.md](opentofu/cycle/README.md).

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

The following services and operations are implemented. Each Terraform resource built on them has a case in [opentofu/cycle/cases](opentofu/cycle/cases) that `cmd/tfcycle` runs through apply, a clean plan, an in-place update, import and destroy.

- **S3**: CreateBucket, HeadBucket, ListBuckets, GetBucketLocation, DeleteBucket; Put/GetBucketVersioning; Put/Get/DeleteBucketTagging; Put/Get/DeleteBucketEncryption; Put/Get/DeletePublicAccessBlock; Put/Get/DeleteBucketOwnershipControls; Put/Get/DeleteBucketCors; Put/Get/DeleteBucketWebsite; Put/Get/DeleteBucketPolicy, GetBucketPolicyStatus; Put/GetBucketAcl; Put/Get/DeleteBucketLifecycle; PutObject (conditional, with tagging), CopyObject, GetObject, HeadObject, DeleteObject, DeleteObjects, Put/Get/DeleteObjectTagging; ListObjects, ListObjectsV2, ListObjectVersions, with real version storage, delete markers and suspended (null) versions
- **DynamoDB**: CreateTable (GSIs, LSIs), DescribeTable, ListTables, UpdateTable (GSI create/update/delete, billing mode, streams, SSE), DeleteTable, DescribeTimeToLive, UpdateTimeToLive, DescribeContinuousBackups, UpdateContinuousBackups, TagResource, UntagResource, ListTagsOfResource, PutItem, GetItem, UpdateItem, DeleteItem (condition, update, projection and filter expressions), Query (tables and indexes), Scan, BatchWriteItem, BatchGetItem, TransactWriteItems, TransactGetItems
- **S3 Control**: ListTagsForResource, TagResource, UntagResource (bucket ARNs; the same tags as S3's ?tagging)
- **SQS** (JSON and Query protocols): CreateQueue, ListQueues, GetQueueUrl, GetQueueAttributes, SetQueueAttributes, DeleteQueue, TagQueue, UntagQueue, ListQueueTags, ListDeadLetterSourceQueues, SendMessage(Batch), ReceiveMessage (long polling), DeleteMessage(Batch), ChangeMessageVisibility(Batch), PurgeQueue. FIFO ordering and deduplication, redrive to a DLQ, retention. Messages are held in memory and don't survive a restart; queues do.
- **SNS**: CreateTopic, ListTopics, GetTopicAttributes, SetTopicAttributes, DeleteTopic, TagResource, UntagResource, ListTagsForResource, Subscribe, Unsubscribe, GetSubscriptionAttributes, SetSubscriptionAttributes, ListSubscriptions, ListSubscriptionsByTopic, Publish, PublishBatch. Publish delivers to SQS subscriptions (enveloped or raw) and honours filter policies on message attributes or body.
- **IAM**: users (Create/Get/Update/Delete/ListUsers), roles (Create/Get/Delete/ListRoles, UpdateRole, UpdateRoleDescription, UpdateAssumeRolePolicy), groups (Create/Get/Update/Delete/ListGroups, AddUserToGroup, RemoveUserFromGroup, ListGroupsForUser), managed policies (Create/Get/Delete/ListPolicies, ListEntitiesForPolicy, Create/Get/Delete/ListPolicyVersions, SetDefaultPolicyVersion), inline policies (Put/Get/Delete/List for User, Role and Group), Attach/Detach/ListAttached for User, Role and Group, instance profiles (Create/Get/Delete/ListInstanceProfiles, Add/RemoveRoleFromInstanceProfile, ListInstanceProfilesForRole), access keys (Create/List/Update/DeleteAccessKey, GetAccessKeyLastUsed), Tag/Untag/List*Tags for users, roles, policies and instance profiles, Put/Delete permissions boundaries. Deletes fail with DeleteConflict while anything is still attached, as in AWS.
- **STS**: GetCallerIdentity, AssumeRole, GetSessionToken. Issued keys (ASIA...) act in the caller's namespace and account and verify in strict mode; they are held in memory, so a restart forgets them.
- **EC2**: Run/Describe/Terminate/Stop/Start/RebootInstances, Modify/DescribeInstanceAttribute, instance credit, metadata and maintenance options, IAM instance profile associations; Create/Delete/Describe/Attach/Detach/ModifyVolume; Create/Import/Delete/DescribeKeyPairs; DescribeImages (a fixed catalogue of Amazon Linux and Ubuntu AMIs), DescribeInstanceTypes, DescribeInstanceTypeOfferings, DescribeAvailabilityZones, DescribeRegions, DescribeAccountAttributes; Create/Delete/DescribeTags with filters on every Describe call
- **ElastiCache**: CreateCacheCluster, DescribeCacheClusters, DeleteCacheCluster, ListTagsForResource
- **KMS**: CreateKey, DescribeKey, ListKeys, UpdateKeyDescription, EnableKey, DisableKey, ScheduleKeyDeletion, CancelKeyDeletion, Get/PutKeyPolicy, ListKeyPolicies, Enable/DisableKeyRotation, GetKeyRotationStatus, Tag/UntagResource, ListResourceTags, Create/Update/Delete/ListAliases, Encrypt, Decrypt, GenerateDataKey, GenerateDataKeyWithoutPlaintext
- **Lambda** (REST-JSON at the real paths): CreateFunction (ZipFile or S3), GetFunction (Code.Location is a signed opensnack download URL, valid 10 minutes), Get/UpdateFunctionConfiguration, UpdateFunctionCode, DeleteFunction, ListFunctions, PublishVersion, ListVersionsByFunction, Create/Get/Update/Delete/ListAliases, AddPermission, RemovePermission, GetPolicy, TagResource, UntagResource, ListTags, Put/Get/DeleteFunctionConcurrency, Create/Get/Update/Delete/ListFunctionUrlConfig(s), Create/Get/Update/Delete/ListEventSourceMappings (SQS sources are polled and invoke the function; failed batches are retried and redriven), Put/Update/Get/Delete/ListFunctionEventInvokeConfig(s), Get/Put/DeleteFunctionCodeSigningConfig, Get/PutFunctionRecursionConfig, GetRuntimeManagementConfig, GetAccountSettings, Invoke (RequestResponse, Event, DryRun) and InvokeAsync. Invoke needs an executor: `OPENSNACK_LAMBDA_EXECUTOR=local` runs python3 and node functions as plain local processes, **with no sandbox: development only, never with untrusted code**. Without it, Invoke returns a 501 ServiceException.
- **CloudWatch Logs**: CreateLogGroup, DescribeLogGroups, DeleteLogGroup, PutRetentionPolicy, DeleteRetentionPolicy, AssociateKmsKey, DisassociateKmsKey, PutLogGroupDeletionProtection, CreateLogStream, DescribeLogStreams, DeleteLogStream, PutLogEvents, GetLogEvents, FilterLogEvents (term, JSON and space-delimited patterns), TagResource, UntagResource, ListTagsForResource, TagLogGroup, UntagLogGroup, ListTagsLogGroup, PutMetricFilter, DescribeMetricFilters, DeleteMetricFilter, TestMetricFilter, PutSubscriptionFilter, DescribeSubscriptionFilters, DeleteSubscriptionFilter. Events are stored in a separate `log_events` table; metric filters feed CloudWatch metrics
- **CloudWatch** (RPCv2 CBOR, JSON and Query): PutMetricAlarm, PutCompositeAlarm, DescribeAlarms, DescribeAlarmsForMetric, DescribeAlarmHistory, DeleteAlarms, SetAlarmState, EnableAlarmActions, DisableAlarmActions, TagResource, UntagResource, ListTagsForResource, PutMetricData, GetMetricStatistics, GetMetricData (MetricStat queries), ListMetrics. Metric data is in memory; alarms evaluate as data arrives and publish to SNS actions
- **Route53**: CreateHostedZone, GetHostedZone, ListHostedZones, ListHostedZonesByName, GetHostedZoneCount, UpdateHostedZoneComment, DeleteHostedZone, AssociateVPCWithHostedZone, DisassociateVPCFromHostedZone, GetDNSSEC, ChangeResourceRecordSets (atomic batches; simple, alias, weighted, latency, failover, geolocation and multivalue records), ListResourceRecordSets, GetChange, Create/Get/List/DeleteReusableDelegationSet, Create/Get/Update/Delete/ListHealthChecks, GetHealthCheckCount, ChangeTagsForResource, ListTagsForResource, ListTagsForResources
- **SSM**: PutParameter (SecureString via KMS), GetParameter, GetParameters, GetParametersByPath, DescribeParameters, GetParameterHistory, Label/UnlabelParameterVersion, DeleteParameter(s), AddTagsToResource, RemoveTagsFromResource, ListTagsForResource
- **Secrets Manager**: CreateSecret, DescribeSecret, UpdateSecret, GetSecretValue, PutSecretValue, UpdateSecretVersionStage, ListSecretVersionIds, ListSecrets, DeleteSecret, RestoreSecret, Get/Put/Delete/ValidateResourcePolicy, Tag/UntagResource, GetRandomPassword
- **EventBridge**: CreateEventBus, DescribeEventBus, UpdateEventBus, ListEventBuses, DeleteEventBus, PutRule, DescribeRule, ListRules, DeleteRule, EnableRule, DisableRule, PutTargets, RemoveTargets, ListTargetsByRule, ListRuleNamesByTarget, PutEvents (pattern matching and delivery to SQS/SNS targets), TestEventPattern, TagResource, UntagResource, ListTagsForResource; scheduled rules (`rate()`, `cron()`) fire in-process. Recent deliveries: `GET /_opensnack/v1/events/deliveries?namespace=`
- **API Gateway v2 (HTTP APIs)**: Create/Get/Update/Delete/List for APIs (including quick create), routes, integrations, stages (with auto-deploy), deployments, route and integration responses; DeleteCorsConfiguration, DeleteAccessLogSettings, DeleteRouteSettings, DeleteRouteRequestParameter; GetTags, TagResource, UntagResource. Data plane on `{api_id}.execute-api.{region}.amazonaws.com`: route matching, stages, CORS, HTTP_PROXY to `OPENSNACK_APIGW_ALLOWED_TARGETS` only, AWS_PROXY (payload 1.0/2.0) through a Lambda invoker; optional Caddy front door via `OPENSNACK_CADDY_ADMIN_URL`
- **CloudFront**: Create(WithTags)/Get/GetConfig/Update/Delete/List distributions (ETag and If-Match, disable before delete); origin access controls, cache policies and origin request policies (plus the AWS managed policies); CreateInvalidation, GetInvalidation, ListInvalidations; ListTagsForResource, TagResource, UntagResource. Data plane on `{id}.cloudfront.net`: S3 origins, execute-api origins, HTTP origins on `OPENSNACK_CLOUDFRONT_ALLOWED_ORIGINS` only, default root object, ordered cache behaviours, custom error responses, and an in-memory cache (objects up to 512KB) cleared by invalidations

Load tests for a subset live in [k6/README.md](k6/README.md).


## License

OpenSnack is licensed under the Mozilla Public License 2.0 (MPL-2.0).

You may use, modify, and distribute this software commercially.
Modifications to OpenSnack source files must remain open.

## Security

Please report security issues privately via email or GitHub Security Advisories.
Do not open public issues for vulnerabilities.


