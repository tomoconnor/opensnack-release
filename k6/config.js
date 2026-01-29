/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// OpenSnack k6 Test Configuration
// Mirrors the Terraform provider configuration in opentofu/main.tf

export const config = {
  // Base endpoint - uses nip.io for DNS resolution
  endpoint: __ENV.OPENSNACK_ENDPOINT || 'http://127.0.0.1.nip.io:4566',

  // AWS credentials (fake for local testing)
  credentials: {
    accessKeyId: 'test',
    secretAccessKey: 'test',
    region: 'us-east-1',
  },

  // Service endpoints (matching opentofu/main.tf)
  services: {
    s3: '',  // Root path
    dynamodb: '/dynamodb',
    sqs: '/sqs',
    sns: '/sns',
    iam: '/iam',
    sts: '/sts',
    logs: '/logs',
    lambda: '/lambda',
    kms: '/kms',
    ec2: '/ec2',
    elasticache: '/elasticache',
    secretsmanager: '/secretsmanager',
    ssm: '/ssm',
    route53: '/route53',
    s3control: '/s3-control',
  },

  // Test namespacing (allows parallel test runs)
  namespace: __ENV.K6_NAMESPACE || `k6-${Date.now()}`,

  // Skip cleanup to allow manual DB inspection (default: true)
  skipCleanup: __ENV.K6_CLEANUP !== 'true',
};

// AWS-style headers for different service types
export function getBaseHeaders(service = 'json') {
  const headers = {
    'Content-Type': service === 'xml' ? 'application/xml' : 'application/x-amz-json-1.0',
    'X-Amz-Date': new Date().toISOString().replace(/[:-]|\.\d{3}/g, ''),
    'User-Agent': `k6/opensnack-test custom-${config.namespace}`,
  };
  return headers;
}

// User-Agent string for namespace isolation (Terraform can't set custom headers)
export function getUserAgent() {
  return `k6/opensnack-test custom-${config.namespace}`;
}

// Helper to generate unique resource names
export function uniqueName(prefix) {
  return `${prefix}-${config.namespace}-${Date.now()}`;
}
