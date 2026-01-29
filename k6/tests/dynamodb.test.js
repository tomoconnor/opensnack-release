/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// DynamoDB Terraform-like k6 Tests
// Tests DynamoDB table lifecycle operations similar to Terraform aws_dynamodb_table

import http from 'k6/http';
import { sleep, group, check } from 'k6';
import { config, uniqueName } from '../config.js';
import { buildJsonRequest } from '../lib/aws.js';
import { checkStatus, checkSuccess, checkJsonField, checkResourceNotFound } from '../lib/checks.js';

export const options = {
  scenarios: {
    dynamodb_lifecycle: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<1000'],
  },
};

const DYNAMODB_URL = `${config.endpoint}${config.services.dynamodb}`;

export default function () {
  const tableName = uniqueName('k6-test-table');

  group('DynamoDB Table Lifecycle', () => {
    // 1. CREATE TABLE (terraform apply - aws_dynamodb_table)
    group('Create Table', () => {
      const createRequest = buildJsonRequest('DynamoDB_20120810.CreateTable', {
        TableName: tableName,
        AttributeDefinitions: [
          { AttributeName: 'pk', AttributeType: 'S' },
          { AttributeName: 'sk', AttributeType: 'S' },
        ],
        KeySchema: [
          { AttributeName: 'pk', KeyType: 'HASH' },
          { AttributeName: 'sk', KeyType: 'RANGE' },
        ],
        BillingMode: 'PAY_PER_REQUEST',
        Tags: [
          { Key: 'Environment', Value: 'test' },
          { Key: 'ManagedBy', Value: 'k6' },
        ],
      });

      const res = http.post(DYNAMODB_URL, createRequest.body, {
        headers: createRequest.headers,
      });

      checkSuccess(res, 'CreateTable');
      checkJsonField(res, 'TableDescription.TableName', tableName, 'CreateTable response');
    });

    sleep(0.5);

    // 2. DESCRIBE TABLE (terraform reads state after create)
    group('Describe Table', () => {
      const describeRequest = buildJsonRequest('DynamoDB_20120810.DescribeTable', {
        TableName: tableName,
      });

      const res = http.post(DYNAMODB_URL, describeRequest.body, {
        headers: describeRequest.headers,
      });

      checkSuccess(res, 'DescribeTable');
      checkJsonField(res, 'Table.TableStatus', 'ACTIVE', 'Table status');
      checkJsonField(res, 'Table.TableName', tableName, 'Table name');
    });

    // 3. LIST TABLES (terraform data source)
    group('List Tables', () => {
      const listRequest = buildJsonRequest('DynamoDB_20120810.ListTables', {});

      const res = http.post(DYNAMODB_URL, listRequest.body, {
        headers: listRequest.headers,
      });

      checkSuccess(res, 'ListTables');
      check(res, {
        'table in list': (r) => {
          try {
            const json = JSON.parse(r.body);
            return json.TableNames && json.TableNames.includes(tableName);
          } catch {
            return false;
          }
        },
      });
    });

    // 4. DESCRIBE TIME TO LIVE (terraform reads TTL config)
    group('Describe Time To Live', () => {
      const ttlRequest = buildJsonRequest('DynamoDB_20120810.DescribeTimeToLive', {
        TableName: tableName,
      });

      const res = http.post(DYNAMODB_URL, ttlRequest.body, {
        headers: ttlRequest.headers,
      });

      checkSuccess(res, 'DescribeTimeToLive');
    });

    // 5. UPDATE TIME TO LIVE (terraform aws_dynamodb_table ttl block)
    group('Update Time To Live', () => {
      const updateTtlRequest = buildJsonRequest('DynamoDB_20120810.UpdateTimeToLive', {
        TableName: tableName,
        TimeToLiveSpecification: {
          AttributeName: 'ttl',
          Enabled: true,
        },
      });

      const res = http.post(DYNAMODB_URL, updateTtlRequest.body, {
        headers: updateTtlRequest.headers,
      });

      checkSuccess(res, 'UpdateTimeToLive');
    });

    // 6. LIST TAGS (terraform reads tags)
    group('List Tags', () => {
      const listTagsRequest = buildJsonRequest('DynamoDB_20120810.ListTagsOfResource', {
        ResourceArn: `arn:aws:dynamodb:us-east-1:000000000000:table/${tableName}`,
      });

      const res = http.post(DYNAMODB_URL, listTagsRequest.body, {
        headers: listTagsRequest.headers,
      });

      checkSuccess(res, 'ListTagsOfResource');
    });

    // 7. TAG RESOURCE (terraform updates tags)
    group('Tag Resource', () => {
      const tagRequest = buildJsonRequest('DynamoDB_20120810.TagResource', {
        ResourceArn: `arn:aws:dynamodb:us-east-1:000000000000:table/${tableName}`,
        Tags: [
          { Key: 'UpdatedBy', Value: 'k6-test' },
        ],
      });

      const res = http.post(DYNAMODB_URL, tagRequest.body, {
        headers: tagRequest.headers,
      });

      checkSuccess(res, 'TagResource');
    });

    // 8. DESCRIBE CONTINUOUS BACKUPS (terraform reads PITR status)
    group('Describe Continuous Backups', () => {
      const backupsRequest = buildJsonRequest('DynamoDB_20120810.DescribeContinuousBackups', {
        TableName: tableName,
      });

      const res = http.post(DYNAMODB_URL, backupsRequest.body, {
        headers: backupsRequest.headers,
      });

      checkSuccess(res, 'DescribeContinuousBackups');
    });

    // 9. UPDATE TABLE (terraform applies changes)
    group('Update Table', () => {
      const updateRequest = buildJsonRequest('DynamoDB_20120810.UpdateTable', {
        TableName: tableName,
        DeletionProtectionEnabled: false,
      });

      const res = http.post(DYNAMODB_URL, updateRequest.body, {
        headers: updateRequest.headers,
      });

      checkSuccess(res, 'UpdateTable');
    });

    sleep(0.5);
  });

  group('DynamoDB Item Operations', () => {
    // 10. PUT ITEM (data operations after table creation)
    group('Put Item', () => {
      const putItemRequest = buildJsonRequest('DynamoDB_20120810.PutItem', {
        TableName: tableName,
        Item: {
          pk: { S: 'user#123' },
          sk: { S: 'profile' },
          name: { S: 'Test User' },
          email: { S: 'test@example.com' },
        },
      });

      const res = http.post(DYNAMODB_URL, putItemRequest.body, {
        headers: putItemRequest.headers,
      });

      checkSuccess(res, 'PutItem');
    });

    // 11. GET ITEM
    group('Get Item', () => {
      const getItemRequest = buildJsonRequest('DynamoDB_20120810.GetItem', {
        TableName: tableName,
        Key: {
          pk: { S: 'user#123' },
          sk: { S: 'profile' },
        },
      });

      const res = http.post(DYNAMODB_URL, getItemRequest.body, {
        headers: getItemRequest.headers,
      });

      checkSuccess(res, 'GetItem');
    });

    // 12. QUERY
    group('Query', () => {
      const queryRequest = buildJsonRequest('DynamoDB_20120810.Query', {
        TableName: tableName,
        KeyConditionExpression: 'pk = :pk',
        ExpressionAttributeValues: {
          ':pk': { S: 'user#123' },
        },
      });

      const res = http.post(DYNAMODB_URL, queryRequest.body, {
        headers: queryRequest.headers,
      });

      checkSuccess(res, 'Query');
    });

    // 13. SCAN
    group('Scan', () => {
      const scanRequest = buildJsonRequest('DynamoDB_20120810.Scan', {
        TableName: tableName,
      });

      const res = http.post(DYNAMODB_URL, scanRequest.body, {
        headers: scanRequest.headers,
      });

      checkSuccess(res, 'Scan');
    });

    // 14. DELETE ITEM
    group('Delete Item', () => {
      const deleteItemRequest = buildJsonRequest('DynamoDB_20120810.DeleteItem', {
        TableName: tableName,
        Key: {
          pk: { S: 'user#123' },
          sk: { S: 'profile' },
        },
      });

      const res = http.post(DYNAMODB_URL, deleteItemRequest.body, {
        headers: deleteItemRequest.headers,
      });

      checkSuccess(res, 'DeleteItem');
    });
  });

  // Cleanup (skipped by default - set K6_CLEANUP=true to enable)
  if (!config.skipCleanup) {
    group('DynamoDB Cleanup', () => {
      // 15. DELETE TABLE (terraform destroy)
      group('Delete Table', () => {
        const deleteRequest = buildJsonRequest('DynamoDB_20120810.DeleteTable', {
          TableName: tableName,
        });

        const res = http.post(DYNAMODB_URL, deleteRequest.body, {
          headers: deleteRequest.headers,
        });

        checkSuccess(res, 'DeleteTable');
      });

      sleep(0.5);

      // 16. VERIFY TABLE DELETED
      group('Verify Table Deleted', () => {
        const describeRequest = buildJsonRequest('DynamoDB_20120810.DescribeTable', {
          TableName: tableName,
        });

        const res = http.post(DYNAMODB_URL, describeRequest.body, {
          headers: describeRequest.headers,
        });

        checkResourceNotFound(res, 'Table');
      });
    });
  } else {
    console.log(`Skipping cleanup - table "${tableName}" retained for inspection`);
  }
}
