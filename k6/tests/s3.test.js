/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// S3 Terraform-like k6 Tests
// Tests S3 bucket and object lifecycle operations similar to Terraform aws_s3_bucket

import http from 'k6/http';
import { sleep, group } from 'k6';
import { config, uniqueName, getUserAgent } from '../config.js';
import { buildRestRequest, buildS3Xml } from '../lib/aws.js';
import { checkStatus, checkSuccess, checkBodyContains, checkResourceNotFound } from '../lib/checks.js';

export const options = {
  scenarios: {
    s3_lifecycle: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.07'],
    http_req_duration: ['p(95)<500'],
  },
};

const BASE_URL = config.endpoint;

export default function () {
  const bucketName = uniqueName('k6-test-bucket');
  const objectKey = 'test-object.txt';
  const objectContent = 'Hello from k6 Terraform-like test!';

  group('S3 Bucket Lifecycle', () => {
    // 1. CREATE BUCKET (terraform apply - aws_s3_bucket)
    group('Create Bucket', () => {
      const createReq = buildRestRequest('PUT');
      const res = http.put(
        `${BASE_URL}/${bucketName}`,
        null,
        {
          headers: {
            ...createReq.headers,
            'Content-Length': '0',
          },
        }
      );

      checkSuccess(res, 'CreateBucket');
      checkStatus(res, 200, 'CreateBucket');
    });

    sleep(0.5);

    // 2. HEAD BUCKET (terraform plan - data source check)
    group('Head Bucket', () => {
      const headReq = buildRestRequest('HEAD');
      const res = http.head(
        `${BASE_URL}/${bucketName}`,
        { headers: headReq.headers }
      );

      checkStatus(res, 200, 'HeadBucket');
    });

    // 3. GET BUCKET LOCATION (terraform reads this during apply)
    group('Get Bucket Location', () => {
      const getReq = buildRestRequest('GET');
      const res = http.get(
        `${BASE_URL}/${bucketName}?location`,
        { headers: getReq.headers }
      );

      checkSuccess(res, 'GetBucketLocation');
    });

    // 4. PUT BUCKET VERSIONING (terraform aws_s3_bucket_versioning)
    group('Put Bucket Versioning', () => {
      const versioningXml = buildS3Xml('VersioningConfiguration', '<Status>Enabled</Status>');
      const res = http.put(
        `${BASE_URL}/${bucketName}?versioning`,
        versioningXml,
        {
          headers: {
            'Content-Type': 'application/xml',
            'User-Agent': getUserAgent(),
          },
        }
      );

      checkSuccess(res, 'PutBucketVersioning');
    });

    // 5. GET BUCKET VERSIONING (terraform reads state)
    group('Get Bucket Versioning', () => {
      const getReq = buildRestRequest('GET');
      const res = http.get(
        `${BASE_URL}/${bucketName}?versioning`,
        { headers: getReq.headers }
      );

      checkSuccess(res, 'GetBucketVersioning');
    });

    // 6. PUT BUCKET ACL (terraform aws_s3_bucket_acl)
    group('Put Bucket ACL', () => {
      const res = http.put(
        `${BASE_URL}/${bucketName}?acl`,
        null,
        {
          headers: {
            'x-amz-acl': 'private',
            'User-Agent': getUserAgent(),
          },
        }
      );

      checkSuccess(res, 'PutBucketAcl');
    });

    // 7. GET BUCKET ACL (terraform reads state)
    group('Get Bucket ACL', () => {
      const getReq = buildRestRequest('GET');
      const res = http.get(
        `${BASE_URL}/${bucketName}?acl`,
        { headers: getReq.headers }
      );

      checkSuccess(res, 'GetBucketAcl');
    });

    // 8. PUT BUCKET POLICY (terraform aws_s3_bucket_policy)
    group('Put Bucket Policy', () => {
      const policy = JSON.stringify({
        Version: '2012-10-17',
        Statement: [{
          Effect: 'Allow',
          Principal: '*',
          Action: 's3:GetObject',
          Resource: `arn:aws:s3:::${bucketName}/*`,
        }],
      });

      const res = http.put(
        `${BASE_URL}/${bucketName}?policy`,
        policy,
        {
          headers: {
            'Content-Type': 'application/json',
            'User-Agent': getUserAgent(),
          },
        }
      );

      checkSuccess(res, 'PutBucketPolicy');
    });

    // 9. GET BUCKET POLICY (terraform reads state)
    group('Get Bucket Policy', () => {
      const getReq = buildRestRequest('GET');
      const res = http.get(
        `${BASE_URL}/${bucketName}?policy`,
        { headers: getReq.headers }
      );

      checkSuccess(res, 'GetBucketPolicy');
    });

    sleep(0.5);
  });

  group('S3 Object Lifecycle', () => {
    // 10. PUT OBJECT (terraform aws_s3_object)
    group('Put Object', () => {
      const res = http.put(
        `${BASE_URL}/${bucketName}/${objectKey}`,
        objectContent,
        {
          headers: {
            'Content-Type': 'text/plain',
            'User-Agent': getUserAgent(),
          },
        }
      );

      checkSuccess(res, 'PutObject');
    });

    // 11. HEAD OBJECT (terraform checks if object exists)
    group('Head Object', () => {
      const headReq = buildRestRequest('HEAD');
      const res = http.head(
        `${BASE_URL}/${bucketName}/${objectKey}`,
        { headers: headReq.headers }
      );

      checkStatus(res, 200, 'HeadObject');
    });

    // 12. GET OBJECT (terraform data source)
    group('Get Object', () => {
      const getReq = buildRestRequest('GET');
      const res = http.get(
        `${BASE_URL}/${bucketName}/${objectKey}`,
        { headers: getReq.headers }
      );

      checkSuccess(res, 'GetObject');
      checkBodyContains(res, 'Hello from k6', 'GetObject content');
    });

    // 13. DELETE OBJECT (terraform destroy) - only if cleanup enabled
    if (!config.skipCleanup) {
      group('Delete Object', () => {
        const deleteReq = buildRestRequest('DELETE');
        const res = http.del(
          `${BASE_URL}/${bucketName}/${objectKey}`,
          null,
          { headers: deleteReq.headers }
        );

        // S3 returns 204 on successful delete
        checkStatus(res, 204, 'DeleteObject');
      });
    }

    sleep(0.5);
  });

  // Cleanup (skipped by default - set K6_CLEANUP=true to enable)
  if (!config.skipCleanup) {
    group('S3 Cleanup', () => {
      // 14. DELETE BUCKET (terraform destroy)
      group('Delete Bucket', () => {
        const deleteReq = buildRestRequest('DELETE');
        const res = http.del(
          `${BASE_URL}/${bucketName}`,
          null,
          { headers: deleteReq.headers }
        );

        checkStatus(res, 204, 'DeleteBucket');
      });

      // 15. VERIFY BUCKET DELETED (terraform verifies destruction)
      group('Verify Bucket Deleted', () => {
        const headReq = buildRestRequest('HEAD');
        const res = http.head(
          `${BASE_URL}/${bucketName}`,
          { headers: headReq.headers }
        );

        checkResourceNotFound(res, 'Bucket');
      });
    });
  } else {
    console.log(`Skipping cleanup - bucket "${bucketName}" retained for inspection`);
  }
}
