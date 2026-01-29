/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// SNS Terraform-like k6 Tests
// Tests SNS topic lifecycle operations similar to Terraform aws_sns_topic

import http from 'k6/http';
import { sleep, group, check } from 'k6';
import { config, uniqueName } from '../config.js';
import { buildQueryRequest } from '../lib/aws.js';
import { checkSuccess, checkBodyContains, checkResourceNotFound } from '../lib/checks.js';

export const options = {
  scenarios: {
    sns_lifecycle: {
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

const SNS_URL = `${config.endpoint}${config.services.sns}`;
const SNS_VERSION = '2010-03-31';

export default function () {
  const topicName = uniqueName('k6-test-topic');
  let topicArn = '';

  group('SNS Topic Lifecycle', () => {
    // 1. CREATE TOPIC (terraform apply - aws_sns_topic)
    group('Create Topic', () => {
      const createReq = buildQueryRequest('CreateTopic', {
        Name: topicName,
        'Tag.member.1.Key': 'Environment',
        'Tag.member.1.Value': 'test',
      }, SNS_VERSION);

      const res = http.post(SNS_URL, createReq.body, {
        headers: createReq.headers,
      });

      checkSuccess(res, 'CreateTopic');
      checkBodyContains(res, 'TopicArn', 'CreateTopic response');

      // Extract topic ARN from response
      const match = res.body.match(/<TopicArn>([^<]+)<\/TopicArn>/);
      if (match) {
        topicArn = match[1];
      }
    });

    sleep(0.5);

    // 2. LIST TOPICS (terraform data source)
    group('List Topics', () => {
      const listReq = buildQueryRequest('ListTopics', {}, SNS_VERSION);

      const res = http.post(SNS_URL, listReq.body, {
        headers: listReq.headers,
      });

      checkSuccess(res, 'ListTopics');
    });

    // 3. GET TOPIC ATTRIBUTES (terraform reads state)
    group('Get Topic Attributes', () => {
      if (!topicArn) {
        console.log('Skipping GetTopicAttributes - no topic ARN');
        return;
      }

      const getReq = buildQueryRequest('GetTopicAttributes', {
        TopicArn: topicArn,
      }, SNS_VERSION);

      const res = http.post(SNS_URL, getReq.body, {
        headers: getReq.headers,
      });

      checkSuccess(res, 'GetTopicAttributes');
    });

    // 4. SET TOPIC ATTRIBUTES (terraform updates)
    group('Set Topic Attributes', () => {
      if (!topicArn) {
        console.log('Skipping SetTopicAttributes - no topic ARN');
        return;
      }

      const setReq = buildQueryRequest('SetTopicAttributes', {
        TopicArn: topicArn,
        AttributeName: 'DisplayName',
        AttributeValue: 'K6 Test Topic',
      }, SNS_VERSION);

      const res = http.post(SNS_URL, setReq.body, {
        headers: setReq.headers,
      });

      checkSuccess(res, 'SetTopicAttributes');
    });

    // 5. LIST TAGS FOR RESOURCE (terraform reads tags)
    group('List Tags For Resource', () => {
      if (!topicArn) {
        console.log('Skipping ListTagsForResource - no topic ARN');
        return;
      }

      const listTagsReq = buildQueryRequest('ListTagsForResource', {
        ResourceArn: topicArn,
      }, SNS_VERSION);

      const res = http.post(SNS_URL, listTagsReq.body, {
        headers: listTagsReq.headers,
      });

      checkSuccess(res, 'ListTagsForResource');
    });

    // 6. TAG RESOURCE (terraform updates tags)
    group('Tag Resource', () => {
      if (!topicArn) {
        console.log('Skipping TagResource - no topic ARN');
        return;
      }

      const tagReq = buildQueryRequest('TagResource', {
        ResourceArn: topicArn,
        'Tags.member.1.Key': 'UpdatedBy',
        'Tags.member.1.Value': 'k6-test',
      }, SNS_VERSION);

      const res = http.post(SNS_URL, tagReq.body, {
        headers: tagReq.headers,
      });

      checkSuccess(res, 'TagResource');
    });

    sleep(0.5);
  });

  group('SNS Publish Operations', () => {
    // 7. PUBLISH MESSAGE (common operation after topic creation)
    group('Publish Message', () => {
      if (!topicArn) {
        console.log('Skipping Publish - no topic ARN');
        return;
      }

      const publishReq = buildQueryRequest('Publish', {
        TopicArn: topicArn,
        Message: JSON.stringify({ test: 'message', timestamp: Date.now() }),
        Subject: 'K6 Test Message',
      }, SNS_VERSION);

      const res = http.post(SNS_URL, publishReq.body, {
        headers: publishReq.headers,
      });

      checkSuccess(res, 'Publish');
      checkBodyContains(res, 'MessageId', 'Publish response');
    });
  });

  group('SNS Subscription Lifecycle', () => {
    // 8. SUBSCRIBE (terraform aws_sns_topic_subscription)
    let subscriptionArn = '';

    group('Subscribe', () => {
      if (!topicArn) {
        console.log('Skipping Subscribe - no topic ARN');
        return;
      }

      const subscribeReq = buildQueryRequest('Subscribe', {
        TopicArn: topicArn,
        Protocol: 'https',
        Endpoint: 'https://example.com/webhook',
      }, SNS_VERSION);

      const res = http.post(SNS_URL, subscribeReq.body, {
        headers: subscribeReq.headers,
      });

      checkSuccess(res, 'Subscribe');

      // Extract subscription ARN
      const match = res.body.match(/<SubscriptionArn>([^<]+)<\/SubscriptionArn>/);
      if (match) {
        subscriptionArn = match[1];
      }
    });

    // 9. LIST SUBSCRIPTIONS BY TOPIC (terraform reads state)
    group('List Subscriptions By Topic', () => {
      if (!topicArn) {
        console.log('Skipping ListSubscriptionsByTopic - no topic ARN');
        return;
      }

      const listSubsReq = buildQueryRequest('ListSubscriptionsByTopic', {
        TopicArn: topicArn,
      }, SNS_VERSION);

      const res = http.post(SNS_URL, listSubsReq.body, {
        headers: listSubsReq.headers,
      });

      checkSuccess(res, 'ListSubscriptionsByTopic');
    });

    // 10. UNSUBSCRIBE (terraform destroy subscription)
    group('Unsubscribe', () => {
      if (!subscriptionArn || subscriptionArn === 'pending confirmation') {
        console.log('Skipping Unsubscribe - no valid subscription ARN');
        return;
      }

      const unsubscribeReq = buildQueryRequest('Unsubscribe', {
        SubscriptionArn: subscriptionArn,
      }, SNS_VERSION);

      const res = http.post(SNS_URL, unsubscribeReq.body, {
        headers: unsubscribeReq.headers,
      });

      checkSuccess(res, 'Unsubscribe');
    });

    sleep(0.5);
  });

  // Cleanup (skipped by default - set K6_CLEANUP=true to enable)
  if (!config.skipCleanup) {
    group('SNS Cleanup', () => {
      // 11. DELETE TOPIC (terraform destroy)
      group('Delete Topic', () => {
        if (!topicArn) {
          console.log('Skipping DeleteTopic - no topic ARN');
          return;
        }

        const deleteReq = buildQueryRequest('DeleteTopic', {
          TopicArn: topicArn,
        }, SNS_VERSION);

        const res = http.post(SNS_URL, deleteReq.body, {
          headers: deleteReq.headers,
        });

        checkSuccess(res, 'DeleteTopic');
      });

      sleep(0.5);

      // 12. VERIFY TOPIC DELETED
      group('Verify Topic Deleted', () => {
        if (!topicArn) {
          console.log('Skipping verification - no topic ARN');
          return;
        }

        const getReq = buildQueryRequest('GetTopicAttributes', {
          TopicArn: topicArn,
        }, SNS_VERSION);

        const res = http.post(SNS_URL, getReq.body, {
          headers: getReq.headers,
        });

        checkResourceNotFound(res, 'Topic');
      });
    });
  } else {
    console.log(`Skipping cleanup - topic "${topicName}" retained for inspection`);
  }
}
