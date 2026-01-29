/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// AWS Protocol Helpers for k6 tests
// Simulates how Terraform AWS provider communicates with AWS services

import { config, getBaseHeaders } from '../config.js';

// Query API format (used by STS, IAM, SQS, SNS, EC2)
export function buildQueryRequest(action, params = {}, version) {
  // k6 doesn't have URLSearchParams, so build manually
  const parts = [];
  parts.push(`Action=${encodeURIComponent(action)}`);
  parts.push(`Version=${encodeURIComponent(version)}`);

  for (const key of Object.keys(params)) {
    const value = params[key];
    if (value !== undefined && value !== null) {
      parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
    }
  }

  return {
    body: parts.join('&'),
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'User-Agent': `k6/opensnack-test custom-${config.namespace}`,
    },
  };
}

// JSON API format (used by DynamoDB, Lambda, CloudWatch Logs, KMS)
export function buildJsonRequest(target, body = {}) {
  return {
    body: JSON.stringify(body),
    headers: {
      ...getBaseHeaders('json'),
      'X-Amz-Target': target,
      'Content-Type': 'application/x-amz-json-1.0',
    },
  };
}

// REST API format (used by S3, Route53)
export function buildRestRequest(method, body = null, contentType = null) {
  const headers = {
    'User-Agent': `k6/opensnack-test custom-${config.namespace}`,
  };

  if (contentType) {
    headers['Content-Type'] = contentType;
  }

  return {
    method,
    body: body ? (typeof body === 'string' ? body : JSON.stringify(body)) : null,
    headers,
  };
}

// S3 XML body builder
export function buildS3Xml(rootElement, content) {
  let xml = `<?xml version="1.0" encoding="UTF-8"?>`;
  xml += `<${rootElement} xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`;
  xml += content;
  xml += `</${rootElement}>`;
  return xml;
}
