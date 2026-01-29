/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// All Services Terraform-like k6 Tests
// Comprehensive test that exercises ALL supported services in a Terraform-like lifecycle

import http from 'k6/http';
import { sleep, group, check } from 'k6';
import { config, getUserAgent } from '../config.js';
import { buildQueryRequest, buildJsonRequest, buildRestRequest } from '../lib/aws.js';
import { checkSuccess, checkBodyContains, checkStatus } from '../lib/checks.js';

export const options = {
  scenarios: {
    all_services: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.10'],
    http_req_duration: ['p(95)<3000'],
  },
};

const BASE_URL = config.endpoint;

// Helper for JSON API requests (KMS, SSM, SecretsManager, Lambda, Logs)
function jsonApiRequest(url, target, body) {
  return http.post(url, JSON.stringify(body), {
    headers: {
      'Content-Type': 'application/x-amz-json-1.1',
      'X-Amz-Target': target,
      'User-Agent': getUserAgent(),
    },
  });
}

export default function () {
  const testId = Date.now();

  // Resource names for all services
  const bucketName = `k6-all-bucket-${testId}`;
  const tableName = `k6-all-table-${testId}`;
  const queueName = `k6-all-queue-${testId}`;
  const topicName = `k6-all-topic-${testId}`;
  const userName = `k6-all-user-${testId}`;
  const instanceImageId = 'ami-12345678';
  const cacheClusterId = `k6-cache-${testId}`;
  const kmsKeyAlias = `k6-key-${testId}`;
  const lambdaFunctionName = `k6-lambda-${testId}`;
  const logGroupName = `/k6/test/${testId}`;
  const ssmParameterName = `/k6/test/param-${testId}`;
  const secretName = `k6-secret-${testId}`;
  const hostedZoneName = `k6-${testId}.example.com`;

  // Store ARNs/URLs/IDs for cleanup
  let queueUrl = '';
  let topicArn = '';
  let instanceId = '';
  let kmsKeyId = '';
  let hostedZoneId = '';

  console.log(`Running all-services test with ID: ${testId}`);

  //
  // ─── S3 ──────────────────────────────────────────────────────────────────────
  //
  group('S3 Service', () => {
    group('Create Bucket', () => {
      const res = http.put(`${BASE_URL}/${bucketName}`, null, {
        headers: {
          'User-Agent': getUserAgent(),
          'Content-Length': '0',
        },
      });
      checkStatus(res, 200, 'S3:CreateBucket');
    });

    group('Put Object', () => {
      const res = http.put(`${BASE_URL}/${bucketName}/test.txt`, 'Hello from k6!', {
        headers: {
          'Content-Type': 'text/plain',
          'User-Agent': getUserAgent(),
        },
      });
      checkSuccess(res, 'S3:PutObject');
    });

    group('Get Object', () => {
      const res = http.get(`${BASE_URL}/${bucketName}/test.txt`, {
        headers: { 'User-Agent': getUserAgent() },
      });
      checkSuccess(res, 'S3:GetObject');
    });
  });

  sleep(0.3);

  //
  // ─── DynamoDB ────────────────────────────────────────────────────────────────
  //
  group('DynamoDB Service', () => {
    const dynamoUrl = `${BASE_URL}/dynamodb`;

    group('Create Table', () => {
      const req = buildJsonRequest('DynamoDB_20120810.CreateTable', {
        TableName: tableName,
        AttributeDefinitions: [{ AttributeName: 'id', AttributeType: 'S' }],
        KeySchema: [{ AttributeName: 'id', KeyType: 'HASH' }],
        BillingMode: 'PAY_PER_REQUEST',
      });
      const res = http.post(dynamoUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'DynamoDB:CreateTable');
    });

    group('Describe Table', () => {
      const req = buildJsonRequest('DynamoDB_20120810.DescribeTable', {
        TableName: tableName,
      });
      const res = http.post(dynamoUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'DynamoDB:DescribeTable');
    });
  });

  sleep(0.3);

  //
  // ─── SQS ─────────────────────────────────────────────────────────────────────
  //
  group('SQS Service', () => {
    const sqsUrl = `${BASE_URL}/sqs`;

    group('Create Queue', () => {
      const req = buildQueryRequest('CreateQueue', { QueueName: queueName }, '2012-11-05');
      const res = http.post(sqsUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'SQS:CreateQueue');
      const match = res.body.match(/<QueueUrl>([^<]+)<\/QueueUrl>/);
      if (match) queueUrl = match[1];
    });

    group('Send Message', () => {
      if (!queueUrl) return;
      const req = buildQueryRequest('SendMessage', {
        QueueUrl: queueUrl,
        MessageBody: 'Test message',
      }, '2012-11-05');
      const res = http.post(sqsUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'SQS:SendMessage');
    });
  });

  sleep(0.3);

  //
  // ─── SNS ─────────────────────────────────────────────────────────────────────
  //
  group('SNS Service', () => {
    const snsUrl = `${BASE_URL}/sns`;

    group('Create Topic', () => {
      const req = buildQueryRequest('CreateTopic', { Name: topicName }, '2010-03-31');
      const res = http.post(snsUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'SNS:CreateTopic');
      const match = res.body.match(/<TopicArn>([^<]+)<\/TopicArn>/);
      if (match) topicArn = match[1];
    });

    group('Publish', () => {
      if (!topicArn) return;
      const req = buildQueryRequest('Publish', {
        TopicArn: topicArn,
        Message: 'Test message',
      }, '2010-03-31');
      const res = http.post(snsUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'SNS:Publish');
    });
  });

  sleep(0.3);

  //
  // ─── IAM ─────────────────────────────────────────────────────────────────────
  //
  group('IAM Service', () => {
    const iamUrl = `${BASE_URL}/iam`;

    group('Create User', () => {
      const req = buildQueryRequest('CreateUser', { UserName: userName }, '2010-05-08');
      const res = http.post(iamUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'IAM:CreateUser');
    });

    group('Get User', () => {
      const req = buildQueryRequest('GetUser', { UserName: userName }, '2010-05-08');
      const res = http.post(iamUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'IAM:GetUser');
    });
  });

  sleep(0.3);

  //
  // ─── STS ─────────────────────────────────────────────────────────────────────
  //
  group('STS Service', () => {
    group('Get Caller Identity', () => {
      const req = buildQueryRequest('GetCallerIdentity', {}, '2011-06-15');
      const res = http.post(`${BASE_URL}/sts`, req.body, { headers: req.headers });
      checkSuccess(res, 'STS:GetCallerIdentity');
    });
  });

  sleep(0.3);

  //
  // ─── EC2 ─────────────────────────────────────────────────────────────────────
  //
  group('EC2 Service', () => {
    const ec2Url = `${BASE_URL}/ec2`;

    group('Run Instances', () => {
      const req = buildQueryRequest('RunInstances', {
        ImageId: instanceImageId,
        InstanceType: 't3.micro',
        MinCount: '1',
        MaxCount: '1',
      }, '2016-11-15');
      const res = http.post(ec2Url, req.body, { headers: req.headers });
      checkSuccess(res, 'EC2:RunInstances');
      // Extract instance ID
      const match = res.body.match(/<instanceId>([^<]+)<\/instanceId>/);
      if (match) instanceId = match[1];
    });

    group('Describe Instances', () => {
      const req = buildQueryRequest('DescribeInstances', {}, '2016-11-15');
      const res = http.post(ec2Url, req.body, { headers: req.headers });
      checkSuccess(res, 'EC2:DescribeInstances');
    });
  });

  sleep(0.3);

  //
  // ─── ElastiCache ─────────────────────────────────────────────────────────────
  //
  group('ElastiCache Service', () => {
    const elasticacheUrl = `${BASE_URL}/elasticache`;

    group('Create Cache Cluster', () => {
      const req = buildQueryRequest('CreateCacheCluster', {
        CacheClusterId: cacheClusterId,
        Engine: 'redis',
        CacheNodeType: 'cache.t2.micro',
        NumCacheNodes: '1',
      }, '2015-02-02');
      const res = http.post(elasticacheUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'ElastiCache:CreateCacheCluster');
    });

    group('Describe Cache Clusters', () => {
      const req = buildQueryRequest('DescribeCacheClusters', {
        CacheClusterId: cacheClusterId,
      }, '2015-02-02');
      const res = http.post(elasticacheUrl, req.body, { headers: req.headers });
      checkSuccess(res, 'ElastiCache:DescribeCacheClusters');
    });
  });

  sleep(0.3);

  //
  // ─── KMS ─────────────────────────────────────────────────────────────────────
  //
  group('KMS Service', () => {
    const kmsUrl = `${BASE_URL}/kms`;

    group('Create Key', () => {
      const res = jsonApiRequest(kmsUrl, 'TrentService.CreateKey', {
        Description: `Test key ${testId}`,
        KeyUsage: 'ENCRYPT_DECRYPT',
      });
      checkSuccess(res, 'KMS:CreateKey');
      try {
        const json = JSON.parse(res.body);
        if (json.KeyMetadata) kmsKeyId = json.KeyMetadata.KeyId;
      } catch (e) { }
    });

    group('Describe Key', () => {
      if (!kmsKeyId) return;
      const res = jsonApiRequest(kmsUrl, 'TrentService.DescribeKey', {
        KeyId: kmsKeyId,
      });
      checkSuccess(res, 'KMS:DescribeKey');
    });
  });

  sleep(0.3);

  //
  // ─── Lambda ──────────────────────────────────────────────────────────────────
  //
  group('Lambda Service', () => {
    const lambdaUrl = `${BASE_URL}/lambda`;

    group('Create Function', () => {
      const res = jsonApiRequest(lambdaUrl, 'AWSLambda_20150331.CreateFunction', {
        FunctionName: lambdaFunctionName,
        Runtime: 'nodejs18.x',
        Role: 'arn:aws:iam::000000000000:role/test-role',
        Handler: 'index.handler',
        Code: { ZipFile: 'UEsDBBQAAAAIAA==' }, // Minimal valid zip
      });
      checkSuccess(res, 'Lambda:CreateFunction');
    });

    group('Get Function', () => {
      const res = jsonApiRequest(lambdaUrl, 'AWSLambda_20150331.GetFunction', {
        FunctionName: lambdaFunctionName,
      });
      checkSuccess(res, 'Lambda:GetFunction');
    });
  });

  sleep(0.3);

  //
  // ─── CloudWatch Logs ─────────────────────────────────────────────────────────
  //
  group('CloudWatch Logs Service', () => {
    const logsUrl = `${BASE_URL}/logs`;

    group('Create Log Group', () => {
      const res = jsonApiRequest(logsUrl, 'Logs_20140328.CreateLogGroup', {
        logGroupName: logGroupName,
      });
      checkSuccess(res, 'Logs:CreateLogGroup');
    });

    group('Describe Log Groups', () => {
      const res = jsonApiRequest(logsUrl, 'Logs_20140328.DescribeLogGroups', {});
      checkSuccess(res, 'Logs:DescribeLogGroups');
    });
  });

  sleep(0.3);

  //
  // ─── SSM (Parameter Store) ───────────────────────────────────────────────────
  //
  group('SSM Service', () => {
    const ssmUrl = `${BASE_URL}/ssm`;

    group('Put Parameter', () => {
      const res = jsonApiRequest(ssmUrl, 'AmazonSSM.PutParameter', {
        Name: ssmParameterName,
        Value: 'test-value-from-k6',
        Type: 'String',
      });
      checkSuccess(res, 'SSM:PutParameter');
    });

    group('Get Parameter', () => {
      const res = jsonApiRequest(ssmUrl, 'AmazonSSM.GetParameter', {
        Name: ssmParameterName,
      });
      checkSuccess(res, 'SSM:GetParameter');
    });
  });

  sleep(0.3);

  //
  // ─── Secrets Manager ─────────────────────────────────────────────────────────
  //
  group('SecretsManager Service', () => {
    const secretsUrl = `${BASE_URL}/secretsmanager`;

    group('Create Secret', () => {
      const res = jsonApiRequest(secretsUrl, 'secretsmanager.CreateSecret', {
        Name: secretName,
        SecretString: JSON.stringify({ username: 'admin', password: 'secret123' }),
      });
      checkSuccess(res, 'SecretsManager:CreateSecret');
    });

    group('Get Secret Value', () => {
      const res = jsonApiRequest(secretsUrl, 'secretsmanager.GetSecretValue', {
        SecretId: secretName,
      });
      checkSuccess(res, 'SecretsManager:GetSecretValue');
    });
  });

  sleep(0.3);

  //
  // ─── Route53 ─────────────────────────────────────────────────────────────────
  //
  group('Route53 Service', () => {
    const route53Url = `${BASE_URL}/route53/2013-04-01`;

    group('Create Hosted Zone', () => {
      const xmlBody = `<?xml version="1.0" encoding="UTF-8"?>
<CreateHostedZoneRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <Name>${hostedZoneName}</Name>
  <CallerReference>k6-test-${testId}</CallerReference>
</CreateHostedZoneRequest>`;

      const res = http.post(`${route53Url}/hostedzone`, xmlBody, {
        headers: {
          'Content-Type': 'application/xml',
          'User-Agent': getUserAgent(),
        },
      });
      checkSuccess(res, 'Route53:CreateHostedZone');

      // Extract hosted zone ID
      const match = res.body.match(/<Id>\/hostedzone\/([^<]+)<\/Id>/);
      if (match) hostedZoneId = match[1];
    });

    group('Get Hosted Zone', () => {
      if (!hostedZoneId) return;
      const res = http.get(`${route53Url}/hostedzone/${hostedZoneId}`, {
        headers: { 'User-Agent': getUserAgent() },
      });
      checkSuccess(res, 'Route53:GetHostedZone');
    });

    group('List Hosted Zones', () => {
      const res = http.get(`${route53Url}/hostedzone`, {
        headers: { 'User-Agent': getUserAgent() },
      });
      checkSuccess(res, 'Route53:ListHostedZones');
    });
  });

  sleep(0.5);

  //
  // ─── CLEANUP ─────────────────────────────────────────────────────────────────
  //
  if (!config.skipCleanup) {
    group('Cleanup', () => {
      // Route53
      group('Delete Hosted Zone', () => {
        if (hostedZoneId) {
          http.del(`${BASE_URL}/route53/2013-04-01/hostedzone/${hostedZoneId}`, null, {
            headers: { 'User-Agent': getUserAgent() },
          });
        }
      });

      // SecretsManager
      group('Delete Secret', () => {
        jsonApiRequest(`${BASE_URL}/secretsmanager`, 'secretsmanager.DeleteSecret', {
          SecretId: secretName,
          ForceDeleteWithoutRecovery: true,
        });
      });

      // SSM
      group('Delete Parameter', () => {
        jsonApiRequest(`${BASE_URL}/ssm`, 'AmazonSSM.DeleteParameter', {
          Name: ssmParameterName,
        });
      });

      // CloudWatch Logs
      group('Delete Log Group', () => {
        jsonApiRequest(`${BASE_URL}/logs`, 'Logs_20140328.DeleteLogGroup', {
          logGroupName: logGroupName,
        });
      });

      // Lambda
      group('Delete Function', () => {
        jsonApiRequest(`${BASE_URL}/lambda`, 'AWSLambda_20150331.DeleteFunction', {
          FunctionName: lambdaFunctionName,
        });
      });

      // KMS (schedule deletion)
      group('Schedule Key Deletion', () => {
        if (kmsKeyId) {
          jsonApiRequest(`${BASE_URL}/kms`, 'TrentService.ScheduleKeyDeletion', {
            KeyId: kmsKeyId,
            PendingWindowInDays: 7,
          });
        }
      });

      // ElastiCache
      group('Delete Cache Cluster', () => {
        const req = buildQueryRequest('DeleteCacheCluster', {
          CacheClusterId: cacheClusterId,
        }, '2015-02-02');
        http.post(`${BASE_URL}/elasticache`, req.body, { headers: req.headers });
      });

      // EC2
      group('Terminate Instance', () => {
        if (instanceId) {
          const req = buildQueryRequest('TerminateInstances', {
            'InstanceId.1': instanceId,
          }, '2016-11-15');
          http.post(`${BASE_URL}/ec2`, req.body, { headers: req.headers });
        }
      });

      // IAM
      group('Delete User', () => {
        const req = buildQueryRequest('DeleteUser', { UserName: userName }, '2010-05-08');
        http.post(`${BASE_URL}/iam`, req.body, { headers: req.headers });
      });

      // SNS
      group('Delete Topic', () => {
        if (topicArn) {
          const req = buildQueryRequest('DeleteTopic', { TopicArn: topicArn }, '2010-03-31');
          http.post(`${BASE_URL}/sns`, req.body, { headers: req.headers });
        }
      });

      // SQS
      group('Delete Queue', () => {
        if (queueUrl) {
          const req = buildQueryRequest('DeleteQueue', { QueueUrl: queueUrl }, '2012-11-05');
          http.post(`${BASE_URL}/sqs`, req.body, { headers: req.headers });
        }
      });

      // DynamoDB
      group('Delete Table', () => {
        const req = buildJsonRequest('DynamoDB_20120810.DeleteTable', { TableName: tableName });
        http.post(`${BASE_URL}/dynamodb`, req.body, { headers: req.headers });
      });

      // S3
      group('Delete S3 Resources', () => {
        http.del(`${BASE_URL}/${bucketName}/test.txt`, null, {
          headers: { 'User-Agent': getUserAgent() },
        });
        http.del(`${BASE_URL}/${bucketName}`, null, {
          headers: { 'User-Agent': getUserAgent() },
        });
      });
    });
  } else {
    console.log(`Skipping cleanup - resources retained for inspection:`);
    console.log(`  - S3 bucket: ${bucketName}`);
    console.log(`  - DynamoDB table: ${tableName}`);
    console.log(`  - SQS queue: ${queueName}`);
    console.log(`  - SNS topic: ${topicName}`);
    console.log(`  - IAM user: ${userName}`);
    console.log(`  - EC2 instance: ${instanceId}`);
    console.log(`  - ElastiCache cluster: ${cacheClusterId}`);
    console.log(`  - KMS key: ${kmsKeyId}`);
    console.log(`  - Lambda function: ${lambdaFunctionName}`);
    console.log(`  - CloudWatch log group: ${logGroupName}`);
    console.log(`  - SSM parameter: ${ssmParameterName}`);
    console.log(`  - Secret: ${secretName}`);
    console.log(`  - Route53 hosted zone: ${hostedZoneId}`);
  }

  console.log(`All-services test completed for ID: ${testId}`);
}
