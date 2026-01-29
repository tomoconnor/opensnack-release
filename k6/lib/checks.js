/**
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

// Common check helpers for k6 tests
import { check } from 'k6';

// Check that response status matches expected
export function checkStatus(response, expected, name = 'status') {
  return check(response, {
    [`${name} is ${expected}`]: (r) => r.status === expected,
  });
}

// Check that response is successful (2xx)
export function checkSuccess(response, name = 'request') {
  return check(response, {
    [`${name} succeeded`]: (r) => r.status >= 200 && r.status < 300,
  });
}

// Check that response body contains expected content
export function checkBodyContains(response, expected, name = 'body') {
  return check(response, {
    [`${name} contains "${expected}"`]: (r) => r.body && r.body.includes(expected),
  });
}

// Check that JSON response has expected field
export function checkJsonField(response, field, expectedValue = undefined, name = 'json') {
  return check(response, {
    [`${name} has field "${field}"`]: (r) => {
      try {
        const json = JSON.parse(r.body);
        const value = field.split('.').reduce((obj, key) => obj && obj[key], json);
        if (expectedValue !== undefined) {
          return value === expectedValue;
        }
        return value !== undefined;
      } catch {
        return false;
      }
    },
  });
}

// Check for Terraform-style resource creation (returns ARN or ID)
export function checkResourceCreated(response, resourceType) {
  return check(response, {
    [`${resourceType} created successfully`]: (r) => r.status >= 200 && r.status < 300,
  });
}

// Check for resource not found (expected on delete verification)
export function checkResourceNotFound(response, resourceType) {
  return check(response, {
    [`${resourceType} not found (expected)`]: (r) =>
      r.status === 404 ||
      r.status === 400 ||
      (r.body && (r.body.includes('NotFound') || r.body.includes('NoSuch'))),
  });
}
