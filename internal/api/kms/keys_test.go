// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms_test

import (
	"bytes"
	"net/http"
	"testing"

	"opensnack/internal/api/kms"
	"opensnack/internal/resource"
)

func ok(t *testing.T, h *kms.Handler, action string, input, out any) {
	t.Helper()
	rec := call(t, h, action, input)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d: %s", action, rec.Code, rec.Body.String())
	}
	if out != nil {
		decode(t, rec, out)
	}
}

func TestGenerateDataKeyDecryptsToPlaintext(t *testing.T) {
	h := kms.NewHandler(resource.NewMemStore())
	meta := createKey(t, h)
	ok(t, h, "CreateAlias", kms.CreateAliasInput{AliasName: "alias/data", TargetKeyID: meta.KeyID}, nil)

	ctx := map[string]string{"bucket": "packages"}
	var gen kms.GenerateDataKeyOutput
	ok(t, h, "GenerateDataKey", map[string]any{
		"KeyId": "alias/data", "KeySpec": "AES_256", "EncryptionContext": ctx,
	}, &gen)

	if len(gen.Plaintext) != 32 {
		t.Fatalf("expected a 32-byte data key, got %d", len(gen.Plaintext))
	}
	if gen.KeyID != meta.ARN {
		t.Fatalf("expected KeyId %s, got %s", meta.ARN, gen.KeyID)
	}

	var dec kms.DecryptOutput
	ok(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: gen.CiphertextBlob, EncryptionContext: ctx}, &dec)
	if !bytes.Equal(dec.Plaintext, gen.Plaintext) {
		t.Fatal("Decrypt did not return the data key")
	}

	// The context is bound to the blob
	if got := errType(t, call(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: gen.CiphertextBlob})); got != "InvalidCiphertextException" {
		t.Fatalf("expected InvalidCiphertextException without the context, got %s", got)
	}

	var bare kms.GenerateDataKeyOutput
	rec := call(t, h, "GenerateDataKeyWithoutPlaintext", map[string]any{"KeyId": meta.ARN, "NumberOfBytes": 16})
	decode(t, rec, &bare)
	if bytes.Contains(rec.Body.Bytes(), []byte(`"Plaintext"`)) {
		t.Fatalf("GenerateDataKeyWithoutPlaintext returned a plaintext: %s", rec.Body.String())
	}
	ok(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: bare.CiphertextBlob}, &dec)
	if len(dec.Plaintext) != 16 {
		t.Fatalf("expected a 16-byte data key, got %d", len(dec.Plaintext))
	}

	for name, in := range map[string]map[string]any{
		"neither":  {"KeyId": meta.KeyID},
		"both":     {"KeyId": meta.KeyID, "KeySpec": "AES_128", "NumberOfBytes": 16},
		"too many": {"KeyId": meta.KeyID, "NumberOfBytes": 2048},
	} {
		if got := errType(t, call(t, h, "GenerateDataKey", in)); got != "ValidationException" {
			t.Fatalf("%s: expected ValidationException, got %s", name, got)
		}
	}
}

func TestManagedAliasCreatedOnFirstUse(t *testing.T) {
	h := kms.NewHandler(resource.NewMemStore())

	var gen kms.GenerateDataKeyOutput
	ok(t, h, "GenerateDataKey", map[string]any{"KeyId": "alias/aws/s3", "KeySpec": "AES_256"}, &gen)

	var desc kms.DescribeKeyOutput
	ok(t, h, "DescribeKey", kms.DescribeKeyInput{KeyID: "alias/aws/s3"}, &desc)
	if desc.KeyMetadata.KeyManager != "AWS" || desc.KeyMetadata.ARN != gen.KeyID {
		t.Fatalf("unexpected managed key: %+v", desc.KeyMetadata)
	}

	// The account cannot change it
	if got := errType(t, call(t, h, "ScheduleKeyDeletion", kms.ScheduleKeyDeletionInput{KeyID: desc.KeyMetadata.KeyID})); got != "AccessDeniedException" {
		t.Fatalf("expected AccessDeniedException, got %s", got)
	}
	if got := errType(t, call(t, h, "CreateAlias", kms.CreateAliasInput{AliasName: "alias/aws/mine", TargetKeyID: desc.KeyMetadata.KeyID})); got != "NotAuthorizedException" {
		t.Fatalf("expected NotAuthorizedException, got %s", got)
	}
}

func TestAliasesListByKey(t *testing.T) {
	h := kms.NewHandler(resource.NewMemStore())
	k1, k2 := createKey(t, h), createKey(t, h)

	ok(t, h, "CreateAlias", kms.CreateAliasInput{AliasName: "alias/one", TargetKeyID: k1.KeyID}, nil)
	ok(t, h, "CreateAlias", kms.CreateAliasInput{AliasName: "alias/two", TargetKeyID: k1.ARN}, nil)
	ok(t, h, "CreateAlias", kms.CreateAliasInput{AliasName: "alias/three", TargetKeyID: k2.KeyID}, nil)

	if got := errType(t, call(t, h, "CreateAlias", kms.CreateAliasInput{AliasName: "alias/one", TargetKeyID: k2.KeyID})); got != "AlreadyExistsException" {
		t.Fatalf("expected AlreadyExistsException, got %s", got)
	}

	var list kms.ListAliasesOutput
	ok(t, h, "ListAliases", kms.ListAliasesInput{KeyID: k1.ARN}, &list)
	if len(list.Aliases) != 2 || list.Aliases[0].AliasName != "alias/one" || list.Aliases[1].AliasName != "alias/two" {
		t.Fatalf("unexpected aliases for k1: %+v", list.Aliases)
	}
	if list.Aliases[0].TargetKeyID != k1.KeyID || list.Aliases[0].AliasArn != "arn:aws:kms:us-east-1:000000000000:alias/one" {
		t.Fatalf("unexpected alias entry: %+v", list.Aliases[0])
	}

	ok(t, h, "UpdateAlias", kms.UpdateAliasInput{AliasName: "alias/two", TargetKeyID: k2.KeyID}, nil)
	ok(t, h, "ListAliases", kms.ListAliasesInput{KeyID: k2.KeyID}, &list)
	if len(list.Aliases) != 2 {
		t.Fatalf("expected two aliases on k2 after UpdateAlias, got %+v", list.Aliases)
	}

	// Paging
	ok(t, h, "ListAliases", kms.ListAliasesInput{Limit: 2}, &list)
	if len(list.Aliases) != 2 || !list.Truncated {
		t.Fatalf("expected a truncated first page, got %+v", list)
	}
	ok(t, h, "ListAliases", kms.ListAliasesInput{Limit: 2, Marker: list.NextMarker}, &list)
	if len(list.Aliases) != 1 || list.Truncated {
		t.Fatalf("expected a final page of one, got %+v", list)
	}

	ok(t, h, "DeleteAlias", kms.DeleteAliasInput{AliasName: "alias/one"}, nil)
	if got := errType(t, call(t, h, "DeleteAlias", kms.DeleteAliasInput{AliasName: "alias/one"})); got != "NotFoundException" {
		t.Fatalf("expected NotFoundException, got %s", got)
	}
}

func TestKeyPolicyRotationTagsRoundTrip(t *testing.T) {
	h := kms.NewHandler(resource.NewMemStore())
	meta := createKey(t, h)

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"kms:*","Resource":"*"}]}`
	ok(t, h, "PutKeyPolicy", kms.PutKeyPolicyInput{KeyID: meta.KeyID, Policy: policy, PolicyName: "default"}, nil)
	var pol kms.GetKeyPolicyOutput
	ok(t, h, "GetKeyPolicy", kms.GetKeyPolicyInput{KeyID: meta.ARN, PolicyName: "default"}, &pol)
	if pol.Policy != policy {
		t.Fatalf("policy not returned exactly: %s", pol.Policy)
	}

	period := int32(180)
	ok(t, h, "EnableKeyRotation", kms.EnableKeyRotationInput{KeyID: meta.KeyID, RotationPeriodInDays: &period}, nil)
	var rot kms.GetKeyRotationStatusOutput
	ok(t, h, "GetKeyRotationStatus", kms.GetKeyRotationStatusInput{KeyID: meta.KeyID}, &rot)
	if !rot.KeyRotationEnabled || rot.RotationPeriodInDays == nil || *rot.RotationPeriodInDays != 180 {
		t.Fatalf("unexpected rotation status: %+v", rot)
	}
	ok(t, h, "DisableKeyRotation", kms.KeyIDInput{KeyID: meta.KeyID}, nil)
	rot = kms.GetKeyRotationStatusOutput{}
	ok(t, h, "GetKeyRotationStatus", kms.GetKeyRotationStatusInput{KeyID: meta.KeyID}, &rot)
	if rot.KeyRotationEnabled || rot.RotationPeriodInDays != nil {
		t.Fatalf("rotation still on: %+v", rot)
	}

	ok(t, h, "TagResource", kms.TagResourceInput{KeyID: meta.KeyID, Tags: []kms.Tag{{"b", "1"}, {"a", "2"}}}, nil)
	ok(t, h, "TagResource", kms.TagResourceInput{KeyID: meta.KeyID, Tags: []kms.Tag{{"b", "3"}}}, nil)
	ok(t, h, "UntagResource", kms.UntagResourceInput{KeyID: meta.KeyID, TagKeys: []string{"a"}}, nil)
	var tags kms.ListResourceTagsOutput
	ok(t, h, "ListResourceTags", kms.ListResourceTagsInput{KeyID: meta.KeyID}, &tags)
	if len(tags.Tags) != 1 || tags.Tags[0] != (kms.Tag{TagKey: "b", TagValue: "3"}) {
		t.Fatalf("unexpected tags: %+v", tags.Tags)
	}

	ok(t, h, "UpdateKeyDescription", kms.UpdateKeyDescriptionInput{KeyID: meta.KeyID, Description: "new"}, nil)
	ok(t, h, "DisableKey", kms.KeyIDInput{KeyID: meta.KeyID}, nil)
	var desc kms.DescribeKeyOutput
	ok(t, h, "DescribeKey", kms.DescribeKeyInput{KeyID: meta.KeyID}, &desc)
	if desc.KeyMetadata.Description != "new" || desc.KeyMetadata.Enabled || desc.KeyMetadata.KeyState != "Disabled" {
		t.Fatalf("unexpected metadata: %+v", desc.KeyMetadata)
	}
	if got := errType(t, call(t, h, "Encrypt", kms.EncryptInput{KeyID: meta.KeyID, Plaintext: []byte("x")})); got != "DisabledException" {
		t.Fatalf("expected DisabledException, got %s", got)
	}
}

func TestScheduleAndCancelKeyDeletion(t *testing.T) {
	h := kms.NewHandler(resource.NewMemStore())
	meta := createKey(t, h)

	if got := errType(t, call(t, h, "ScheduleKeyDeletion", kms.ScheduleKeyDeletionInput{KeyID: meta.KeyID, PendingWindowInDays: 3})); got != "ValidationException" {
		t.Fatalf("expected ValidationException for a 3 day window, got %s", got)
	}

	var sched kms.ScheduleKeyDeletionOutput
	ok(t, h, "ScheduleKeyDeletion", kms.ScheduleKeyDeletionInput{KeyID: meta.KeyID, PendingWindowInDays: 7}, &sched)
	if sched.KeyState != "PendingDeletion" || sched.PendingWindowInDays != 7 || sched.KeyID != meta.ARN {
		t.Fatalf("unexpected ScheduleKeyDeletion output: %+v", sched)
	}

	// Still described, so the provider can see the state; changes are refused
	var desc kms.DescribeKeyOutput
	ok(t, h, "DescribeKey", kms.DescribeKeyInput{KeyID: meta.KeyID}, &desc)
	if desc.KeyMetadata.KeyState != "PendingDeletion" || desc.KeyMetadata.DeletionDate == 0 {
		t.Fatalf("unexpected metadata: %+v", desc.KeyMetadata)
	}
	rec := call(t, h, "ScheduleKeyDeletion", kms.ScheduleKeyDeletionInput{KeyID: meta.KeyID})
	if got := errType(t, rec); got != "KMSInvalidStateException" || !bytes.Contains(rec.Body.Bytes(), []byte("is pending deletion")) {
		t.Fatalf("expected KMSInvalidStateException ... is pending deletion, got %s", rec.Body.String())
	}

	ok(t, h, "CancelKeyDeletion", kms.KeyIDInput{KeyID: meta.KeyID}, nil)
	desc = kms.DescribeKeyOutput{}
	ok(t, h, "DescribeKey", kms.DescribeKeyInput{KeyID: meta.KeyID}, &desc)
	if desc.KeyMetadata.KeyState != "Disabled" || desc.KeyMetadata.DeletionDate != 0 {
		t.Fatalf("expected a disabled key after cancel, got %+v", desc.KeyMetadata)
	}
}
