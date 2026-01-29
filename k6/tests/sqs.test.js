/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// SQS Terraform-like k6 Tests
// Tests SQS queue lifecycle operations similar to Terraform aws_sqs_queue

import http from 'k6/http';
import { sleep, group, check } from 'k6';
import { config, uniqueName } from '../config.js';
import { buildQueryRequest } from '../lib/aws.js';
import { checkSuccess, checkBodyContains, checkResourceNotFound } from '../lib/checks.js';

export const options = {
  scenarios: {
    sqs_lifecycle: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
  },
};

const SQS_URL = `${config.endpoint}${config.services.sqs}`;
const SQS_VERSION = '2012-11-05';

export default function () {
  const queueName = uniqueName('k6-test-queue');
  let queueUrl = '';

  group('SQS Queue Lifecycle', () => {
    // 1. CREATE QUEUE (terraform apply - aws_sqs_queue)
    group('Create Queue', () => {
      const createReq = buildQueryRequest('CreateQueue', {
        QueueName: queueName,
        'Attribute.1.Name': 'VisibilityTimeout',
        'Attribute.1.Value': '30',
        'Attribute.2.Name': 'DelaySeconds',
        'Attribute.2.Value': '0',
        'Tag.1.Key': 'Environment',
        'Tag.1.Value': 'test',
      }, SQS_VERSION);

      const res = http.post(SQS_URL, createReq.body, {
        headers: createReq.headers,
      });

      checkSuccess(res, 'CreateQueue');
      checkBodyContains(res, 'QueueUrl', 'CreateQueue response');

      // Extract queue URL from response
      const match = res.body.match(/<QueueUrl>([^<]+)<\/QueueUrl>/);
      if (match) {
        queueUrl = match[1];
      }
    });

    sleep(0.5);

    // 2. LIST QUEUES (terraform data source)
    group('List Queues', () => {
      const listReq = buildQueryRequest('ListQueues', {
        QueueNamePrefix: 'k6-test',
      }, SQS_VERSION);

      const res = http.post(SQS_URL, listReq.body, {
        headers: listReq.headers,
      });

      checkSuccess(res, 'ListQueues');
    });

    // 3. GET QUEUE URL (terraform reads queue URL)
    group('Get Queue URL', () => {
      const getUrlReq = buildQueryRequest('GetQueueUrl', {
        QueueName: queueName,
      }, SQS_VERSION);

      const res = http.post(SQS_URL, getUrlReq.body, {
        headers: getUrlReq.headers,
      });

      checkSuccess(res, 'GetQueueUrl');
      checkBodyContains(res, queueName, 'Queue URL contains name');
    });

    // 4. GET QUEUE ATTRIBUTES (terraform reads state)
    group('Get Queue Attributes', () => {
      const getAttrsReq = buildQueryRequest('GetQueueAttributes', {
        QueueUrl: queueUrl,
        'AttributeName.1': 'All',
      }, SQS_VERSION);

      const res = http.post(SQS_URL, getAttrsReq.body, {
        headers: getAttrsReq.headers,
      });

      checkSuccess(res, 'GetQueueAttributes');
    });

    // 5. SET QUEUE ATTRIBUTES (terraform updates)
    group('Set Queue Attributes', () => {
      const setAttrsReq = buildQueryRequest('SetQueueAttributes', {
        QueueUrl: queueUrl,
        'Attribute.1.Name': 'VisibilityTimeout',
        'Attribute.1.Value': '60',
      }, SQS_VERSION);

      const res = http.post(SQS_URL, setAttrsReq.body, {
        headers: setAttrsReq.headers,
      });

      checkSuccess(res, 'SetQueueAttributes');
    });

    sleep(0.5);
  });

  group('SQS Message Operations', () => {
    // 6. SEND MESSAGE
    group('Send Message', () => {
      const sendReq = buildQueryRequest('SendMessage', {
        QueueUrl: queueUrl,
        MessageBody: JSON.stringify({ test: 'message', timestamp: Date.now() }),
      }, SQS_VERSION);

      const res = http.post(SQS_URL, sendReq.body, {
        headers: sendReq.headers,
      });

      checkSuccess(res, 'SendMessage');
      checkBodyContains(res, 'MessageId', 'SendMessage response');
    });

    // 7. RECEIVE MESSAGE
    group('Receive Message', () => {
      const receiveReq = buildQueryRequest('ReceiveMessage', {
        QueueUrl: queueUrl,
        MaxNumberOfMessages: '1',
        WaitTimeSeconds: '1',
      }, SQS_VERSION);

      const res = http.post(SQS_URL, receiveReq.body, {
        headers: receiveReq.headers,
      });

      checkSuccess(res, 'ReceiveMessage');
    });

    sleep(0.5);
  });

  // Cleanup (skipped by default - set K6_CLEANUP=true to enable)
  if (!config.skipCleanup) {
    group('SQS Cleanup', () => {
      // 8. PURGE QUEUE (optional - clear all messages)
      group('Purge Queue', () => {
        const purgeReq = buildQueryRequest('PurgeQueue', {
          QueueUrl: queueUrl,
        }, SQS_VERSION);

        const res = http.post(SQS_URL, purgeReq.body, {
          headers: purgeReq.headers,
        });

        // Purge may return success even if queue doesn't support it
        check(res, {
          'PurgeQueue completed': (r) => r.status >= 200 && r.status < 500,
        });
      });

      // 9. DELETE QUEUE (terraform destroy)
      group('Delete Queue', () => {
        const deleteReq = buildQueryRequest('DeleteQueue', {
          QueueUrl: queueUrl,
        }, SQS_VERSION);

        const res = http.post(SQS_URL, deleteReq.body, {
          headers: deleteReq.headers,
        });

        checkSuccess(res, 'DeleteQueue');
      });

      sleep(0.5);

      // 10. VERIFY QUEUE DELETED
      group('Verify Queue Deleted', () => {
        const getUrlReq = buildQueryRequest('GetQueueUrl', {
          QueueName: queueName,
        }, SQS_VERSION);

        const res = http.post(SQS_URL, getUrlReq.body, {
          headers: getUrlReq.headers,
        });

        checkResourceNotFound(res, 'Queue');
      });
    });
  } else {
    console.log(`Skipping cleanup - queue "${queueName}" retained for inspection`);
  }
}
