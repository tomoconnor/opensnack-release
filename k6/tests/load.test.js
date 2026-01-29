/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// Load Testing Scenario for OpenSnack
// Simulates multiple Terraform operations running in parallel (like multiple developers)
// NOTE: This test always cleans up resources (ignores skipCleanup) to avoid DB bloat

import http from 'k6/http';
import { sleep, group } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { config, getUserAgent } from '../config.js';
import { buildJsonRequest, buildQueryRequest } from '../lib/aws.js';

// Custom metrics for Terraform-like operations
const terraformApplyCounter = new Counter('terraform_apply_total');
const terraformDestroyCounter = new Counter('terraform_destroy_total');
const resourceCreateRate = new Rate('resource_create_success');
const resourceDeleteRate = new Rate('resource_delete_success');
const apiLatency = new Trend('api_latency_ms');

export const options = {
  scenarios: {
    // Simulate steady state - continuous Terraform operations
    steady_state: {
      executor: 'constant-vus',
      vus: 5,
      duration: '2m',
      tags: { scenario: 'steady_state' },
    },
    // Simulate spike - many developers running terraform apply simultaneously
    spike: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 20 },  // Ramp up
        { duration: '1m', target: 20 },   // Stay at peak
        { duration: '30s', target: 0 },   // Ramp down
      ],
      startTime: '2m30s',  // Start after steady state
      tags: { scenario: 'spike' },
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.1'],
    http_req_duration: ['p(95)<2000'],
    resource_create_success: ['rate>0.9'],
    resource_delete_success: ['rate>0.9'],
    api_latency_ms: ['p(95)<1000'],
  },
};

const BASE_URL = config.endpoint;

// Generate unique resource ID per iteration
function resourceId() {
  return `load-${__VU}-${__ITER}-${Date.now()}`;
}

// Helper for JSON API requests
function jsonApiPost(url, target, body, tags) {
  return http.post(url, JSON.stringify(body), {
    headers: {
      'Content-Type': 'application/x-amz-json-1.1',
      'X-Amz-Target': target,
      'User-Agent': getUserAgent(),
    },
    tags,
  });
}

// ─── S3 ────────────────────────────────────────────────────────────────────────
function s3Lifecycle() {
  const bucketName = `k6-load-bucket-${resourceId()}`;
  const start = Date.now();

  const createRes = http.put(`${BASE_URL}/${bucketName}`, null, {
    headers: { 'User-Agent': getUserAgent(), 'Content-Length': '0' },
    tags: { operation: 's3_create_bucket' },
  });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    http.head(`${BASE_URL}/${bucketName}`, {
      headers: { 'User-Agent': getUserAgent() },
      tags: { operation: 's3_head_bucket' },
    });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteRes = http.del(`${BASE_URL}/${bucketName}`, null, {
      headers: { 'User-Agent': getUserAgent() },
      tags: { operation: 's3_delete_bucket' },
    });
    const deleteSuccess = deleteRes.status === 204 || deleteRes.status === 200;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── DynamoDB ──────────────────────────────────────────────────────────────────
function dynamodbLifecycle() {
  const tableName = `k6-load-table-${resourceId()}`;
  const dynamoUrl = `${BASE_URL}/dynamodb`;
  const start = Date.now();

  const createReq = buildJsonRequest('DynamoDB_20120810.CreateTable', {
    TableName: tableName,
    AttributeDefinitions: [{ AttributeName: 'id', AttributeType: 'S' }],
    KeySchema: [{ AttributeName: 'id', KeyType: 'HASH' }],
    BillingMode: 'PAY_PER_REQUEST',
  });

  const createRes = http.post(dynamoUrl, createReq.body, {
    headers: createReq.headers,
    tags: { operation: 'dynamodb_create_table' },
  });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const describeReq = buildJsonRequest('DynamoDB_20120810.DescribeTable', { TableName: tableName });
    http.post(dynamoUrl, describeReq.body, { headers: describeReq.headers, tags: { operation: 'dynamodb_describe_table' } });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteReq = buildJsonRequest('DynamoDB_20120810.DeleteTable', { TableName: tableName });
    const deleteRes = http.post(dynamoUrl, deleteReq.body, { headers: deleteReq.headers, tags: { operation: 'dynamodb_delete_table' } });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── SQS ───────────────────────────────────────────────────────────────────────
function sqsLifecycle() {
  const queueName = `k6-load-queue-${resourceId()}`;
  const sqsUrl = `${BASE_URL}/sqs`;
  const start = Date.now();

  const createReq = buildQueryRequest('CreateQueue', { QueueName: queueName }, '2012-11-05');
  const createRes = http.post(sqsUrl, createReq.body, { headers: createReq.headers, tags: { operation: 'sqs_create_queue' } });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const match = createRes.body.match(/<QueueUrl>([^<]+)<\/QueueUrl>/);
    const queueUrl = match ? match[1] : '';

    if (queueUrl) {
      const getReq = buildQueryRequest('GetQueueAttributes', { QueueUrl: queueUrl, 'AttributeName.1': 'All' }, '2012-11-05');
      http.post(sqsUrl, getReq.body, { headers: getReq.headers, tags: { operation: 'sqs_get_attributes' } });
      sleep(0.1);

      const deleteStart = Date.now();
      const deleteReq = buildQueryRequest('DeleteQueue', { QueueUrl: queueUrl }, '2012-11-05');
      const deleteRes = http.post(sqsUrl, deleteReq.body, { headers: deleteReq.headers, tags: { operation: 'sqs_delete_queue' } });
      const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
      resourceDeleteRate.add(deleteSuccess);
      apiLatency.add(Date.now() - deleteStart);
      if (deleteSuccess) terraformDestroyCounter.add(1);
    }
  }
}

// ─── SNS ───────────────────────────────────────────────────────────────────────
function snsLifecycle() {
  const topicName = `k6-load-topic-${resourceId()}`;
  const snsUrl = `${BASE_URL}/sns`;
  const start = Date.now();

  const createReq = buildQueryRequest('CreateTopic', { Name: topicName }, '2010-03-31');
  const createRes = http.post(snsUrl, createReq.body, { headers: createReq.headers, tags: { operation: 'sns_create_topic' } });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const match = createRes.body.match(/<TopicArn>([^<]+)<\/TopicArn>/);
    const topicArn = match ? match[1] : '';

    if (topicArn) {
      const getReq = buildQueryRequest('GetTopicAttributes', { TopicArn: topicArn }, '2010-03-31');
      http.post(snsUrl, getReq.body, { headers: getReq.headers, tags: { operation: 'sns_get_attributes' } });
      sleep(0.1);

      const deleteStart = Date.now();
      const deleteReq = buildQueryRequest('DeleteTopic', { TopicArn: topicArn }, '2010-03-31');
      const deleteRes = http.post(snsUrl, deleteReq.body, { headers: deleteReq.headers, tags: { operation: 'sns_delete_topic' } });
      const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
      resourceDeleteRate.add(deleteSuccess);
      apiLatency.add(Date.now() - deleteStart);
      if (deleteSuccess) terraformDestroyCounter.add(1);
    }
  }
}

// ─── IAM ───────────────────────────────────────────────────────────────────────
function iamLifecycle() {
  const userName = `k6-load-user-${resourceId()}`;
  const iamUrl = `${BASE_URL}/iam`;
  const start = Date.now();

  const createReq = buildQueryRequest('CreateUser', { UserName: userName }, '2010-05-08');
  const createRes = http.post(iamUrl, createReq.body, { headers: createReq.headers, tags: { operation: 'iam_create_user' } });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const getReq = buildQueryRequest('GetUser', { UserName: userName }, '2010-05-08');
    http.post(iamUrl, getReq.body, { headers: getReq.headers, tags: { operation: 'iam_get_user' } });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteReq = buildQueryRequest('DeleteUser', { UserName: userName }, '2010-05-08');
    const deleteRes = http.post(iamUrl, deleteReq.body, { headers: deleteReq.headers, tags: { operation: 'iam_delete_user' } });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── EC2 ───────────────────────────────────────────────────────────────────────
function ec2Lifecycle() {
  const ec2Url = `${BASE_URL}/ec2`;
  const start = Date.now();

  const createReq = buildQueryRequest('RunInstances', {
    ImageId: 'ami-12345678',
    InstanceType: 't3.micro',
    MinCount: '1',
    MaxCount: '1',
  }, '2016-11-15');
  const createRes = http.post(ec2Url, createReq.body, { headers: createReq.headers, tags: { operation: 'ec2_run_instances' } });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const match = createRes.body.match(/<instanceId>([^<]+)<\/instanceId>/);
    const instanceId = match ? match[1] : '';

    const describeReq = buildQueryRequest('DescribeInstances', {}, '2016-11-15');
    http.post(ec2Url, describeReq.body, { headers: describeReq.headers, tags: { operation: 'ec2_describe_instances' } });
    sleep(0.1);

    if (instanceId) {
      const deleteStart = Date.now();
      const deleteReq = buildQueryRequest('TerminateInstances', { 'InstanceId.1': instanceId }, '2016-11-15');
      const deleteRes = http.post(ec2Url, deleteReq.body, { headers: deleteReq.headers, tags: { operation: 'ec2_terminate_instances' } });
      const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
      resourceDeleteRate.add(deleteSuccess);
      apiLatency.add(Date.now() - deleteStart);
      if (deleteSuccess) terraformDestroyCounter.add(1);
    }
  }
}

// ─── ElastiCache ───────────────────────────────────────────────────────────────
function elasticacheLifecycle() {
  const clusterId = `k6-cache-${resourceId()}`.substring(0, 40); // Max 40 chars
  const elasticacheUrl = `${BASE_URL}/elasticache`;
  const start = Date.now();

  const createReq = buildQueryRequest('CreateCacheCluster', {
    CacheClusterId: clusterId,
    Engine: 'redis',
    CacheNodeType: 'cache.t2.micro',
    NumCacheNodes: '1',
  }, '2015-02-02');
  const createRes = http.post(elasticacheUrl, createReq.body, { headers: createReq.headers, tags: { operation: 'elasticache_create_cluster' } });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const describeReq = buildQueryRequest('DescribeCacheClusters', { CacheClusterId: clusterId }, '2015-02-02');
    http.post(elasticacheUrl, describeReq.body, { headers: describeReq.headers, tags: { operation: 'elasticache_describe_clusters' } });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteReq = buildQueryRequest('DeleteCacheCluster', { CacheClusterId: clusterId }, '2015-02-02');
    const deleteRes = http.post(elasticacheUrl, deleteReq.body, { headers: deleteReq.headers, tags: { operation: 'elasticache_delete_cluster' } });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── KMS ───────────────────────────────────────────────────────────────────────
function kmsLifecycle() {
  const kmsUrl = `${BASE_URL}/kms`;
  const start = Date.now();

  const createRes = jsonApiPost(kmsUrl, 'TrentService.CreateKey', {
    Description: `Load test key ${resourceId()}`,
    KeyUsage: 'ENCRYPT_DECRYPT',
  }, { operation: 'kms_create_key' });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    let keyId = '';
    try {
      const json = JSON.parse(createRes.body);
      if (json.KeyMetadata) keyId = json.KeyMetadata.KeyId;
    } catch (e) { }

    if (keyId) {
      jsonApiPost(kmsUrl, 'TrentService.DescribeKey', { KeyId: keyId }, { operation: 'kms_describe_key' });
      sleep(0.1);

      const deleteStart = Date.now();
      const deleteRes = jsonApiPost(kmsUrl, 'TrentService.ScheduleKeyDeletion', {
        KeyId: keyId,
        PendingWindowInDays: 7,
      }, { operation: 'kms_schedule_key_deletion' });
      const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
      resourceDeleteRate.add(deleteSuccess);
      apiLatency.add(Date.now() - deleteStart);
      if (deleteSuccess) terraformDestroyCounter.add(1);
    }
  }
}

// ─── Lambda ────────────────────────────────────────────────────────────────────
function lambdaLifecycle() {
  const functionName = `k6-load-lambda-${resourceId()}`;
  const lambdaUrl = `${BASE_URL}/lambda`;
  const start = Date.now();

  const createRes = jsonApiPost(lambdaUrl, 'AWSLambda_20150331.CreateFunction', {
    FunctionName: functionName,
    Runtime: 'nodejs18.x',
    Role: 'arn:aws:iam::000000000000:role/test-role',
    Handler: 'index.handler',
    Code: { ZipFile: 'UEsDBBQAAAAIAA==' },
  }, { operation: 'lambda_create_function' });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    jsonApiPost(lambdaUrl, 'AWSLambda_20150331.GetFunction', { FunctionName: functionName }, { operation: 'lambda_get_function' });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteRes = jsonApiPost(lambdaUrl, 'AWSLambda_20150331.DeleteFunction', { FunctionName: functionName }, { operation: 'lambda_delete_function' });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── CloudWatch Logs ───────────────────────────────────────────────────────────
function logsLifecycle() {
  const logGroupName = `/k6/load/${resourceId()}`;
  const logsUrl = `${BASE_URL}/logs`;
  const start = Date.now();

  const createRes = jsonApiPost(logsUrl, 'Logs_20140328.CreateLogGroup', { logGroupName }, { operation: 'logs_create_log_group' });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    jsonApiPost(logsUrl, 'Logs_20140328.DescribeLogGroups', {}, { operation: 'logs_describe_log_groups' });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteRes = jsonApiPost(logsUrl, 'Logs_20140328.DeleteLogGroup', { logGroupName }, { operation: 'logs_delete_log_group' });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── SSM ───────────────────────────────────────────────────────────────────────
function ssmLifecycle() {
  const paramName = `/k6/load/param-${resourceId()}`;
  const ssmUrl = `${BASE_URL}/ssm`;
  const start = Date.now();

  const createRes = jsonApiPost(ssmUrl, 'AmazonSSM.PutParameter', {
    Name: paramName,
    Value: 'test-value',
    Type: 'String',
  }, { operation: 'ssm_put_parameter' });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    jsonApiPost(ssmUrl, 'AmazonSSM.GetParameter', { Name: paramName }, { operation: 'ssm_get_parameter' });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteRes = jsonApiPost(ssmUrl, 'AmazonSSM.DeleteParameter', { Name: paramName }, { operation: 'ssm_delete_parameter' });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── Secrets Manager ───────────────────────────────────────────────────────────
function secretsManagerLifecycle() {
  const secretName = `k6-load-secret-${resourceId()}`;
  const secretsUrl = `${BASE_URL}/secretsmanager`;
  const start = Date.now();

  const createRes = jsonApiPost(secretsUrl, 'secretsmanager.CreateSecret', {
    Name: secretName,
    SecretString: JSON.stringify({ password: 'test123' }),
  }, { operation: 'secretsmanager_create_secret' });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    jsonApiPost(secretsUrl, 'secretsmanager.GetSecretValue', { SecretId: secretName }, { operation: 'secretsmanager_get_secret' });
    sleep(0.1);

    const deleteStart = Date.now();
    const deleteRes = jsonApiPost(secretsUrl, 'secretsmanager.DeleteSecret', {
      SecretId: secretName,
      ForceDeleteWithoutRecovery: true,
    }, { operation: 'secretsmanager_delete_secret' });
    const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
    resourceDeleteRate.add(deleteSuccess);
    apiLatency.add(Date.now() - deleteStart);
    if (deleteSuccess) terraformDestroyCounter.add(1);
  }
}

// ─── Route53 ───────────────────────────────────────────────────────────────────
function route53Lifecycle() {
  const zoneName = `k6-${resourceId()}.example.com`;
  const route53Url = `${BASE_URL}/route53/2013-04-01`;
  const start = Date.now();

  const xmlBody = `<?xml version="1.0" encoding="UTF-8"?>
<CreateHostedZoneRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <Name>${zoneName}</Name>
  <CallerReference>k6-load-${Date.now()}</CallerReference>
</CreateHostedZoneRequest>`;

  const createRes = http.post(`${route53Url}/hostedzone`, xmlBody, {
    headers: { 'Content-Type': 'application/xml', 'User-Agent': getUserAgent() },
    tags: { operation: 'route53_create_hosted_zone' },
  });

  const createSuccess = createRes.status >= 200 && createRes.status < 300;
  resourceCreateRate.add(createSuccess);
  apiLatency.add(Date.now() - start);

  if (createSuccess) {
    terraformApplyCounter.add(1);
    const match = createRes.body.match(/<Id>\/hostedzone\/([^<]+)<\/Id>/);
    const hostedZoneId = match ? match[1] : '';

    if (hostedZoneId) {
      http.get(`${route53Url}/hostedzone/${hostedZoneId}`, {
        headers: { 'User-Agent': getUserAgent() },
        tags: { operation: 'route53_get_hosted_zone' },
      });
      sleep(0.1);

      const deleteStart = Date.now();
      const deleteRes = http.del(`${route53Url}/hostedzone/${hostedZoneId}`, null, {
        headers: { 'User-Agent': getUserAgent() },
        tags: { operation: 'route53_delete_hosted_zone' },
      });
      const deleteSuccess = deleteRes.status >= 200 && deleteRes.status < 300;
      resourceDeleteRate.add(deleteSuccess);
      apiLatency.add(Date.now() - deleteStart);
      if (deleteSuccess) terraformDestroyCounter.add(1);
    }
  }
}

// ─── Main ──────────────────────────────────────────────────────────────────────
export default function () {
  // All 13 service lifecycle functions (excluding STS which is read-only)
  const services = [
    s3Lifecycle,
    dynamodbLifecycle,
    sqsLifecycle,
    snsLifecycle,
    iamLifecycle,
    ec2Lifecycle,
    elasticacheLifecycle,
    kmsLifecycle,
    lambdaLifecycle,
    logsLifecycle,
    ssmLifecycle,
    secretsManagerLifecycle,
    route53Lifecycle,
  ];

  // Randomly choose which service to test (simulating diverse Terraform configs)
  const randomService = services[Math.floor(Math.random() * services.length)];

  group('Terraform Lifecycle', () => {
    randomService();
  });

  // Small sleep between iterations to prevent overwhelming
  sleep(Math.random() * 0.5 + 0.5); // 0.5-1s
}
