/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// IAM Terraform-like k6 Tests
// Tests IAM user and policy lifecycle operations similar to Terraform aws_iam_user

import http from 'k6/http';
import { sleep, group, check } from 'k6';
import { config, uniqueName } from '../config.js';
import { buildQueryRequest } from '../lib/aws.js';
import { checkSuccess, checkBodyContains, checkResourceNotFound } from '../lib/checks.js';

export const options = {
  scenarios: {
    iam_lifecycle: {
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

const IAM_URL = `${config.endpoint}${config.services.iam}`;
const IAM_VERSION = '2010-05-08';

export default function () {
  const userName = uniqueName('k6-test-user');
  const policyName = uniqueName('k6-test-policy');
  let policyArn = '';

  group('IAM User Lifecycle', () => {
    // 1. CREATE USER (terraform apply - aws_iam_user)
    group('Create User', () => {
      const createReq = buildQueryRequest('CreateUser', {
        UserName: userName,
        Path: '/',
      }, IAM_VERSION);

      const res = http.post(IAM_URL, createReq.body, {
        headers: createReq.headers,
      });

      checkSuccess(res, 'CreateUser');
      checkBodyContains(res, userName, 'CreateUser response');
    });

    sleep(0.5);

    // 2. GET USER (terraform reads state)
    group('Get User', () => {
      const getReq = buildQueryRequest('GetUser', {
        UserName: userName,
      }, IAM_VERSION);

      const res = http.post(IAM_URL, getReq.body, {
        headers: getReq.headers,
      });

      checkSuccess(res, 'GetUser');
      checkBodyContains(res, userName, 'GetUser response');
    });

    // 3. LIST USERS (terraform data source)
    group('List Users', () => {
      const listReq = buildQueryRequest('ListUsers', {
        PathPrefix: '/',
      }, IAM_VERSION);

      const res = http.post(IAM_URL, listReq.body, {
        headers: listReq.headers,
      });

      checkSuccess(res, 'ListUsers');
    });
  });

  group('IAM Policy Lifecycle', () => {
    // 4. CREATE POLICY (terraform apply - aws_iam_policy)
    group('Create Policy', () => {
      const policyDocument = JSON.stringify({
        Version: '2012-10-17',
        Statement: [{
          Effect: 'Allow',
          Action: ['s3:ListBucket', 's3:GetObject'],
          Resource: '*',
        }],
      });

      const createReq = buildQueryRequest('CreatePolicy', {
        PolicyName: policyName,
        PolicyDocument: policyDocument,
        Path: '/',
        Description: 'Test policy created by k6',
      }, IAM_VERSION);

      const res = http.post(IAM_URL, createReq.body, {
        headers: createReq.headers,
      });

      checkSuccess(res, 'CreatePolicy');

      // Extract policy ARN from response
      const match = res.body.match(/<Arn>([^<]+)<\/Arn>/);
      if (match) {
        policyArn = match[1];
      }
    });

    sleep(0.5);

    // 5. GET POLICY (terraform reads state)
    group('Get Policy', () => {
      if (!policyArn) {
        console.log('Skipping GetPolicy - no policy ARN');
        return;
      }

      const getReq = buildQueryRequest('GetPolicy', {
        PolicyArn: policyArn,
      }, IAM_VERSION);

      const res = http.post(IAM_URL, getReq.body, {
        headers: getReq.headers,
      });

      checkSuccess(res, 'GetPolicy');
    });

    // 6. LIST POLICIES (terraform data source)
    group('List Policies', () => {
      const listReq = buildQueryRequest('ListPolicies', {
        Scope: 'Local',
      }, IAM_VERSION);

      const res = http.post(IAM_URL, listReq.body, {
        headers: listReq.headers,
      });

      checkSuccess(res, 'ListPolicies');
    });
  });

  group('IAM Attachment Lifecycle', () => {
    // 7. ATTACH USER POLICY (terraform aws_iam_user_policy_attachment)
    group('Attach User Policy', () => {
      if (!policyArn) {
        console.log('Skipping AttachUserPolicy - no policy ARN');
        return;
      }

      const attachReq = buildQueryRequest('AttachUserPolicy', {
        UserName: userName,
        PolicyArn: policyArn,
      }, IAM_VERSION);

      const res = http.post(IAM_URL, attachReq.body, {
        headers: attachReq.headers,
      });

      checkSuccess(res, 'AttachUserPolicy');
    });

    sleep(0.5);

    // 8. LIST ATTACHED USER POLICIES (terraform reads state)
    group('List Attached User Policies', () => {
      const listReq = buildQueryRequest('ListAttachedUserPolicies', {
        UserName: userName,
      }, IAM_VERSION);

      const res = http.post(IAM_URL, listReq.body, {
        headers: listReq.headers,
      });

      checkSuccess(res, 'ListAttachedUserPolicies');
    });

    // 9. DETACH USER POLICY (terraform destroy attachment)
    group('Detach User Policy', () => {
      if (!policyArn) {
        console.log('Skipping DetachUserPolicy - no policy ARN');
        return;
      }

      const detachReq = buildQueryRequest('DetachUserPolicy', {
        UserName: userName,
        PolicyArn: policyArn,
      }, IAM_VERSION);

      const res = http.post(IAM_URL, detachReq.body, {
        headers: detachReq.headers,
      });

      checkSuccess(res, 'DetachUserPolicy');
    });
  });

  // Cleanup (skipped by default - set K6_CLEANUP=true to enable)
  if (!config.skipCleanup) {
    group('IAM Cleanup', () => {
      // 10. DELETE POLICY (terraform destroy policy)
      group('Delete Policy', () => {
        if (!policyArn) {
          console.log('Skipping DeletePolicy - no policy ARN');
          return;
        }

        const deleteReq = buildQueryRequest('DeletePolicy', {
          PolicyArn: policyArn,
        }, IAM_VERSION);

        const res = http.post(IAM_URL, deleteReq.body, {
          headers: deleteReq.headers,
        });

        checkSuccess(res, 'DeletePolicy');
      });

      // 11. DELETE USER (terraform destroy user)
      group('Delete User', () => {
        const deleteReq = buildQueryRequest('DeleteUser', {
          UserName: userName,
        }, IAM_VERSION);

        const res = http.post(IAM_URL, deleteReq.body, {
          headers: deleteReq.headers,
        });

        checkSuccess(res, 'DeleteUser');
      });

      sleep(0.5);

      // 12. VERIFY USER DELETED
      group('Verify User Deleted', () => {
        const getReq = buildQueryRequest('GetUser', {
          UserName: userName,
        }, IAM_VERSION);

        const res = http.post(IAM_URL, getReq.body, {
          headers: getReq.headers,
        });

        checkResourceNotFound(res, 'User');
      });
    });
  } else {
    console.log(`Skipping cleanup - user "${userName}" and policy retained for inspection`);
  }
}
