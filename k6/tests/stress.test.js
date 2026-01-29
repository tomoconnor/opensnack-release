/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// Extreme Stress Test for OpenSnack
// Purpose: Find the breaking point of the system
// WARNING: This will hammer the server hard - use with caution!

import http from 'k6/http';
import { sleep, group } from 'k6';
import { Counter, Rate, Trend, Gauge } from 'k6/metrics';
import { config, getUserAgent } from '../config.js';
import { buildJsonRequest, buildQueryRequest } from '../lib/aws.js';

// Metrics to track breaking points
const requestsTotal = new Counter('requests_total');
const errorsTotal = new Counter('errors_total');
const successRate = new Rate('success_rate');
const responseTime = new Trend('response_time_ms');
const activeVUs = new Gauge('active_vus');

// Error tracking by type
const error4xx = new Counter('errors_4xx');
const error5xx = new Counter('errors_5xx');
const errorTimeout = new Counter('errors_timeout');
const errorConnection = new Counter('errors_connection');

export const options = {
  scenarios: {
    // Stage 1: Warm up
    warmup: {
      executor: 'constant-vus',
      vus: 10,
      duration: '30s',
      tags: { stage: 'warmup' },
    },
    // Stage 2: Ramp to moderate load
    ramp_up: {
      executor: 'ramping-vus',
      startVUs: 10,
      stages: [
        { duration: '1m', target: 50 },
        { duration: '2m', target: 50 },
      ],
      startTime: '30s',
      tags: { stage: 'ramp_up' },
    },
    // Stage 3: Push to high load
    high_load: {
      executor: 'ramping-vus',
      startVUs: 50,
      stages: [
        { duration: '1m', target: 100 },
        { duration: '2m', target: 100 },
      ],
      startTime: '3m30s',
      tags: { stage: 'high_load' },
    },
    // Stage 4: Extreme stress - find breaking point
    stress: {
      executor: 'ramping-vus',
      startVUs: 100,
      stages: [
        { duration: '1m', target: 200 },
        { duration: '1m', target: 300 },
        { duration: '1m', target: 500 },
        { duration: '2m', target: 500 },
      ],
      startTime: '6m30s',
      tags: { stage: 'stress' },
    },
    // Stage 5: Spike test - sudden extreme load
    spike: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: 1000 },
        { duration: '1m', target: 1000 },
        { duration: '10s', target: 0 },
      ],
      startTime: '11m30s',
      tags: { stage: 'spike' },
    },
    // Stage 6: Recovery - can it come back?
    recovery: {
      executor: 'constant-vus',
      vus: 20,
      duration: '1m',
      startTime: '13m',
      tags: { stage: 'recovery' },
    },
  },
  thresholds: {
    // These are intentionally loose - we want to see failures
    'success_rate': ['rate>0.5'],              // At least 50% success (expect failures)
    'response_time_ms{stage:warmup}': ['p(95)<500'],
    'response_time_ms{stage:stress}': ['p(95)<5000'],
  },
};

const BASE_URL = config.endpoint;

function resourceId() {
  return `stress-${__VU}-${__ITER}-${Date.now()}`;
}

function jsonApiPost(url, target, body, tags) {
  return http.post(url, JSON.stringify(body), {
    headers: {
      'Content-Type': 'application/x-amz-json-1.1',
      'X-Amz-Target': target,
      'User-Agent': getUserAgent(),
    },
    tags,
    timeout: '10s',
  });
}

function trackResponse(res, operation) {
  requestsTotal.add(1);
  activeVUs.add(__VU);

  if (res.timings && res.timings.duration) {
    responseTime.add(res.timings.duration);
  }

  if (res.status >= 200 && res.status < 300) {
    successRate.add(true);
  } else {
    successRate.add(false);
    errorsTotal.add(1);

    if (res.status >= 400 && res.status < 500) {
      error4xx.add(1);
    } else if (res.status >= 500) {
      error5xx.add(1);
    } else if (res.status === 0) {
      // Connection error or timeout
      if (res.error && res.error.includes('timeout')) {
        errorTimeout.add(1);
      } else {
        errorConnection.add(1);
      }
    }
  }

  return res.status >= 200 && res.status < 300;
}

// ─── Fast S3 Operations ────────────────────────────────────────────────────────
function s3Fast() {
  const bucketName = `stress-bucket-${resourceId()}`;

  const createRes = http.put(`${BASE_URL}/${bucketName}`, null, {
    headers: { 'User-Agent': getUserAgent(), 'Content-Length': '0' },
    timeout: '5s',
  });
  const created = trackResponse(createRes, 's3_create');

  if (created) {
    // Quick read
    const headRes = http.head(`${BASE_URL}/${bucketName}`, {
      headers: { 'User-Agent': getUserAgent() },
      timeout: '5s',
    });
    trackResponse(headRes, 's3_head');

    // Immediate delete
    const deleteRes = http.del(`${BASE_URL}/${bucketName}`, null, {
      headers: { 'User-Agent': getUserAgent() },
      timeout: '5s',
    });
    trackResponse(deleteRes, 's3_delete');
  }
}

// ─── Fast DynamoDB Operations ──────────────────────────────────────────────────
function dynamodbFast() {
  const tableName = `stress-table-${resourceId()}`;
  const dynamoUrl = `${BASE_URL}/dynamodb`;

  const createReq = buildJsonRequest('DynamoDB_20120810.CreateTable', {
    TableName: tableName,
    AttributeDefinitions: [{ AttributeName: 'id', AttributeType: 'S' }],
    KeySchema: [{ AttributeName: 'id', KeyType: 'HASH' }],
    BillingMode: 'PAY_PER_REQUEST',
  });

  const createRes = http.post(dynamoUrl, createReq.body, {
    headers: createReq.headers,
    timeout: '5s',
  });
  const created = trackResponse(createRes, 'dynamodb_create');

  if (created) {
    const deleteReq = buildJsonRequest('DynamoDB_20120810.DeleteTable', { TableName: tableName });
    const deleteRes = http.post(dynamoUrl, deleteReq.body, {
      headers: deleteReq.headers,
      timeout: '5s',
    });
    trackResponse(deleteRes, 'dynamodb_delete');
  }
}

// ─── Fast SQS Operations ───────────────────────────────────────────────────────
function sqsFast() {
  const queueName = `stress-queue-${resourceId()}`;
  const sqsUrl = `${BASE_URL}/sqs`;

  const createReq = buildQueryRequest('CreateQueue', { QueueName: queueName }, '2012-11-05');
  const createRes = http.post(sqsUrl, createReq.body, {
    headers: createReq.headers,
    timeout: '5s',
  });
  const created = trackResponse(createRes, 'sqs_create');

  if (created) {
    const match = createRes.body.match(/<QueueUrl>([^<]+)<\/QueueUrl>/);
    if (match) {
      const deleteReq = buildQueryRequest('DeleteQueue', { QueueUrl: match[1] }, '2012-11-05');
      const deleteRes = http.post(sqsUrl, deleteReq.body, {
        headers: deleteReq.headers,
        timeout: '5s',
      });
      trackResponse(deleteRes, 'sqs_delete');
    }
  }
}

// ─── Fast SNS Operations ───────────────────────────────────────────────────────
function snsFast() {
  const topicName = `stress-topic-${resourceId()}`;
  const snsUrl = `${BASE_URL}/sns`;

  const createReq = buildQueryRequest('CreateTopic', { Name: topicName }, '2010-03-31');
  const createRes = http.post(snsUrl, createReq.body, {
    headers: createReq.headers,
    timeout: '5s',
  });
  const created = trackResponse(createRes, 'sns_create');

  if (created) {
    const match = createRes.body.match(/<TopicArn>([^<]+)<\/TopicArn>/);
    if (match) {
      const deleteReq = buildQueryRequest('DeleteTopic', { TopicArn: match[1] }, '2010-03-31');
      const deleteRes = http.post(snsUrl, deleteReq.body, {
        headers: deleteReq.headers,
        timeout: '5s',
      });
      trackResponse(deleteRes, 'sns_delete');
    }
  }
}

// ─── Fast IAM Operations ───────────────────────────────────────────────────────
function iamFast() {
  const userName = `stress-user-${resourceId()}`;
  const iamUrl = `${BASE_URL}/iam`;

  const createReq = buildQueryRequest('CreateUser', { UserName: userName }, '2010-05-08');
  const createRes = http.post(iamUrl, createReq.body, {
    headers: createReq.headers,
    timeout: '5s',
  });
  const created = trackResponse(createRes, 'iam_create');

  if (created) {
    const deleteReq = buildQueryRequest('DeleteUser', { UserName: userName }, '2010-05-08');
    const deleteRes = http.post(iamUrl, deleteReq.body, {
      headers: deleteReq.headers,
      timeout: '5s',
    });
    trackResponse(deleteRes, 'iam_delete');
  }
}

// ─── Fast Lambda Operations ────────────────────────────────────────────────────
function lambdaFast() {
  const functionName = `stress-fn-${resourceId()}`;
  const lambdaUrl = `${BASE_URL}/lambda`;

  const createRes = jsonApiPost(lambdaUrl, 'AWSLambda_20150331.CreateFunction', {
    FunctionName: functionName,
    Runtime: 'nodejs18.x',
    Role: 'arn:aws:iam::000000000000:role/test',
    Handler: 'index.handler',
    Code: { ZipFile: 'UEsDBBQAAAAIAA==' },
  }, {});
  const created = trackResponse(createRes, 'lambda_create');

  if (created) {
    const deleteRes = jsonApiPost(lambdaUrl, 'AWSLambda_20150331.DeleteFunction', {
      FunctionName: functionName,
    }, {});
    trackResponse(deleteRes, 'lambda_delete');
  }
}

// ─── Fast SSM Operations ───────────────────────────────────────────────────────
function ssmFast() {
  const paramName = `/stress/param-${resourceId()}`;
  const ssmUrl = `${BASE_URL}/ssm`;

  const createRes = jsonApiPost(ssmUrl, 'AmazonSSM.PutParameter', {
    Name: paramName,
    Value: 'stress-test-value',
    Type: 'String',
  }, {});
  const created = trackResponse(createRes, 'ssm_create');

  if (created) {
    const deleteRes = jsonApiPost(ssmUrl, 'AmazonSSM.DeleteParameter', {
      Name: paramName,
    }, {});
    trackResponse(deleteRes, 'ssm_delete');
  }
}

// ─── Fast Logs Operations ──────────────────────────────────────────────────────
function logsFast() {
  const logGroupName = `/stress/logs-${resourceId()}`;
  const logsUrl = `${BASE_URL}/logs`;

  const createRes = jsonApiPost(logsUrl, 'Logs_20140328.CreateLogGroup', {
    logGroupName,
  }, {});
  const created = trackResponse(createRes, 'logs_create');

  if (created) {
    const deleteRes = jsonApiPost(logsUrl, 'Logs_20140328.DeleteLogGroup', {
      logGroupName,
    }, {});
    trackResponse(deleteRes, 'logs_delete');
  }
}

// ─── Read-Only Burst (lighter load) ────────────────────────────────────────────
function readOnlyBurst() {
  const stsUrl = `${BASE_URL}/sts`;

  // STS GetCallerIdentity - very lightweight
  const req = buildQueryRequest('GetCallerIdentity', {}, '2011-06-15');
  const res = http.post(stsUrl, req.body, {
    headers: req.headers,
    timeout: '5s',
  });
  trackResponse(res, 'sts_identity');
}

// ─── Main ──────────────────────────────────────────────────────────────────────
export default function () {
  // Weight the operations - mix of heavy and light
  const operations = [
    { fn: s3Fast, weight: 20 },
    { fn: dynamodbFast, weight: 15 },
    { fn: sqsFast, weight: 15 },
    { fn: snsFast, weight: 10 },
    { fn: iamFast, weight: 10 },
    { fn: lambdaFast, weight: 5 },
    { fn: ssmFast, weight: 10 },
    { fn: logsFast, weight: 5 },
    { fn: readOnlyBurst, weight: 10 },
  ];

  // Build weighted selection
  const weighted = [];
  for (const op of operations) {
    for (let i = 0; i < op.weight; i++) {
      weighted.push(op.fn);
    }
  }

  // Pick random operation
  const operation = weighted[Math.floor(Math.random() * weighted.length)];

  group('Stress Operation', () => {
    operation();
  });

  // NO SLEEP during stress stages - maximum pressure
  // Only add tiny sleep to prevent CPU lockup
  if (Math.random() < 0.1) {
    sleep(0.01);
  }
}

// Called at the end of the test
export function handleSummary(data) {
  const summary = {
    timestamp: new Date().toISOString(),
    total_requests: data.metrics.requests_total ? data.metrics.requests_total.values.count : 0,
    total_errors: data.metrics.errors_total ? data.metrics.errors_total.values.count : 0,
    success_rate: data.metrics.success_rate ? (data.metrics.success_rate.values.rate * 100).toFixed(2) + '%' : 'N/A',
    response_time_p95: data.metrics.response_time_ms ? data.metrics.response_time_ms.values['p(95)'].toFixed(2) + 'ms' : 'N/A',
    response_time_max: data.metrics.response_time_ms ? data.metrics.response_time_ms.values.max.toFixed(2) + 'ms' : 'N/A',
    errors_4xx: data.metrics.errors_4xx ? data.metrics.errors_4xx.values.count : 0,
    errors_5xx: data.metrics.errors_5xx ? data.metrics.errors_5xx.values.count : 0,
    errors_timeout: data.metrics.errors_timeout ? data.metrics.errors_timeout.values.count : 0,
    errors_connection: data.metrics.errors_connection ? data.metrics.errors_connection.values.count : 0,
  };

  console.log('\n' + '='.repeat(60));
  console.log('STRESS TEST SUMMARY');
  console.log('='.repeat(60));
  console.log(`Total Requests:     ${summary.total_requests}`);
  console.log(`Total Errors:       ${summary.total_errors}`);
  console.log(`Success Rate:       ${summary.success_rate}`);
  console.log(`Response Time P95:  ${summary.response_time_p95}`);
  console.log(`Response Time Max:  ${summary.response_time_max}`);
  console.log(`4xx Errors:         ${summary.errors_4xx}`);
  console.log(`5xx Errors:         ${summary.errors_5xx}`);
  console.log(`Timeout Errors:     ${summary.errors_timeout}`);
  console.log(`Connection Errors:  ${summary.errors_connection}`);
  console.log('='.repeat(60) + '\n');

  return {
    'stdout': JSON.stringify(summary, null, 2),
    'stress-results.json': JSON.stringify(data, null, 2),
  };
}
