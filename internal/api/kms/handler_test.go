// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/api/kms"
	"opensnack/internal/resource"
)

//
// MockStore (same pattern as the SQS/SNS/S3 tests)
//

type MockStore struct {
	data map[string]resource.Resource
}

func NewMockStore() *MockStore {
	return &MockStore{data: map[string]resource.Resource{}}
}

func key(id, ns string) string { return ns + "|" + id }

func (m *MockStore) Create(r *resource.Resource) error {
	m.data[key(r.ID, r.Namespace)] = *r
	return nil
}

func (m *MockStore) Update(r *resource.Resource) error {
	m.data[key(r.ID, r.Namespace)] = *r
	return nil
}

func (m *MockStore) Get(id, service, typ, ns string) (*resource.Resource, error) {
	v, ok := m.data[key(id, ns)]
	if !ok {
		return nil, errors.New("not found")
	}
	return &v, nil
}

func (m *MockStore) List(service, typ, ns string) ([]resource.Resource, error) {
	out := []resource.Resource{}
	for _, v := range m.data {
		if v.Service == service && v.Type == typ && v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *MockStore) Delete(id, service, typ, ns string) error {
	delete(m.data, key(id, ns))
	return nil
}

//
// Helpers
//

// call dispatches one KMS action and returns the recorder.
func call(t *testing.T, h *kms.Handler, action string, input any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal %s input: %v", action, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/kms", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService."+action)

	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	return rec
}

// callRaw dispatches one KMS action with a hand-written JSON body.
func callRaw(t *testing.T, h *kms.Handler, action, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/kms", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService."+action)

	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("invalid JSON response: %s", rec.Body.String())
	}
}

// createKey creates a key and returns its metadata.
func createKey(t *testing.T, h *kms.Handler) kms.KeyMetadata {
	t.Helper()

	rec := call(t, h, "CreateKey", kms.CreateKeyInput{Description: "test key"})
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateKey: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var out kms.CreateKeyOutput
	decode(t, rec, &out)
	return out.KeyMetadata
}

// errType returns the __type of an error response.
func errType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Type string `json:"__type"`
	}
	decode(t, rec, &body)
	return body.Type
}

//
// TESTS
//

func TestCreateKeyPersistsKeyMaterial(t *testing.T) {
	store := NewMockStore()
	h := kms.NewHandler(store)

	meta := createKey(t, h)

	res, err := store.Get(meta.KeyID, "kms", "key", "default")
	if err != nil {
		t.Fatalf("key not stored: %v", err)
	}

	var entry struct {
		KeyMaterial []byte `json:"key_material"`
	}
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		t.Fatalf("decode stored entry: %v", err)
	}

	if len(entry.KeyMaterial) != 32 {
		t.Fatalf("expected 32 bytes of key material, got %d", len(entry.KeyMaterial))
	}
}

func TestKeyMaterialNeverLeavesTheStore(t *testing.T) {
	store := NewMockStore()
	h := kms.NewHandler(store)

	meta := createKey(t, h)

	for _, tc := range []struct {
		action string
		input  any
	}{
		{"CreateKey", kms.CreateKeyInput{}},
		{"DescribeKey", kms.DescribeKeyInput{KeyID: meta.KeyID}},
		{"ListKeys", map[string]any{}},
	} {
		rec := call(t, h, tc.action, tc.input)
		if strings.Contains(rec.Body.String(), "key_material") {
			t.Fatalf("%s leaked key material: %s", tc.action, rec.Body.String())
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	plaintext := []byte("super secret payload")

	rec := call(t, h, "Encrypt", kms.EncryptInput{KeyID: meta.KeyID, Plaintext: plaintext})
	if rec.Code != http.StatusOK {
		t.Fatalf("Encrypt: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var enc kms.EncryptOutput
	decode(t, rec, &enc)

	if enc.KeyID != meta.ARN {
		t.Fatalf("expected key ARN %q, got %q", meta.ARN, enc.KeyID)
	}
	if enc.EncryptionAlgorithm != "SYMMETRIC_DEFAULT" {
		t.Fatalf("unexpected algorithm: %s", enc.EncryptionAlgorithm)
	}
	if bytes.Contains(enc.CiphertextBlob, plaintext) {
		t.Fatal("ciphertext blob contains the plaintext")
	}

	rec = call(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob})
	if rec.Code != http.StatusOK {
		t.Fatalf("Decrypt: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var dec kms.DecryptOutput
	decode(t, rec, &dec)

	if !bytes.Equal(dec.Plaintext, plaintext) {
		t.Fatalf("round trip mismatch: got %q, want %q", dec.Plaintext, plaintext)
	}
	if dec.KeyID != meta.ARN {
		t.Fatalf("expected key ARN %q, got %q", meta.ARN, dec.KeyID)
	}
}

func TestEncryptIsNotDeterministic(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	in := kms.EncryptInput{KeyID: meta.KeyID, Plaintext: []byte("same input")}

	var first, second kms.EncryptOutput
	decode(t, call(t, h, "Encrypt", in), &first)
	decode(t, call(t, h, "Encrypt", in), &second)

	if bytes.Equal(first.CiphertextBlob, second.CiphertextBlob) {
		t.Fatal("two encryptions of the same plaintext produced the same blob")
	}
}

func TestEncryptAcceptsKeyARN(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	rec := call(t, h, "Encrypt", kms.EncryptInput{KeyID: meta.ARN, Plaintext: []byte("hello")})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for ARN key id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEncryptionContextIsAuthenticated(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	ctx := map[string]string{"purpose": "test", "tenant": "acme"}

	var enc kms.EncryptOutput
	decode(t, call(t, h, "Encrypt", kms.EncryptInput{
		KeyID:             meta.KeyID,
		Plaintext:         []byte("context bound"),
		EncryptionContext: ctx,
	}), &enc)

	// Matching context decrypts
	rec := call(t, h, "Decrypt", kms.DecryptInput{
		CiphertextBlob:    enc.CiphertextBlob,
		EncryptionContext: ctx,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("matching context: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var dec kms.DecryptOutput
	decode(t, rec, &dec)
	if string(dec.Plaintext) != "context bound" {
		t.Fatalf("unexpected plaintext: %q", dec.Plaintext)
	}

	// Wrong and missing contexts do not
	for name, bad := range map[string]map[string]string{
		"wrong value": {"purpose": "test", "tenant": "other"},
		"missing key": {"purpose": "test"},
		"absent":      nil,
	} {
		rec := call(t, h, "Decrypt", kms.DecryptInput{
			CiphertextBlob:    enc.CiphertextBlob,
			EncryptionContext: bad,
		})
		if rec.Code == http.StatusOK {
			t.Fatalf("%s: expected decrypt to fail", name)
		}
		if got := errType(t, rec); got != "InvalidCiphertextException" {
			t.Fatalf("%s: expected InvalidCiphertextException, got %s", name, got)
		}
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	var enc kms.EncryptOutput
	decode(t, call(t, h, "Encrypt", kms.EncryptInput{
		KeyID:     meta.KeyID,
		Plaintext: []byte("tamper me"),
	}), &enc)

	tampered := bytes.Clone(enc.CiphertextBlob)
	tampered[len(tampered)-1] ^= 0xff

	rec := call(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: tampered})
	if rec.Code == http.StatusOK {
		t.Fatal("expected tampered ciphertext to be rejected")
	}
	if got := errType(t, rec); got != "InvalidCiphertextException" {
		t.Fatalf("expected InvalidCiphertextException, got %s", got)
	}
}

func TestDecryptRejectsMismatchedKeyID(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	first := createKey(t, h)
	second := createKey(t, h)

	var enc kms.EncryptOutput
	decode(t, call(t, h, "Encrypt", kms.EncryptInput{
		KeyID:     first.KeyID,
		Plaintext: []byte("bound to first key"),
	}), &enc)

	rec := call(t, h, "Decrypt", kms.DecryptInput{
		CiphertextBlob: enc.CiphertextBlob,
		KeyID:          second.KeyID,
	})
	if got := errType(t, rec); got != "IncorrectKeyException" {
		t.Fatalf("expected IncorrectKeyException, got %s (%d): %s", got, rec.Code, rec.Body.String())
	}
}

func TestDecryptRejectsGarbage(t *testing.T) {
	h := kms.NewHandler(NewMockStore())

	for name, blob := range map[string][]byte{
		"empty":        {},
		"too short":    {0x01},
		"bad version":  {0x02, 0x01, 'a', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"truncated id": {0x01, 0x40, 'a', 'b'},
	} {
		rec := call(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: blob})
		if got := errType(t, rec); got != "InvalidCiphertextException" {
			t.Fatalf("%s: expected InvalidCiphertextException, got %s", name, got)
		}
	}
}

func TestEncryptUnknownKey(t *testing.T) {
	h := kms.NewHandler(NewMockStore())

	rec := call(t, h, "Encrypt", kms.EncryptInput{
		KeyID:     "11111111-2222-3333-4444-555555555555",
		Plaintext: []byte("nope"),
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := errType(t, rec); got != "NotFoundException" {
		t.Fatalf("expected NotFoundException, got %s", got)
	}
}

func TestEncryptValidation(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	cases := map[string]struct {
		input   kms.EncryptInput
		errType string
	}{
		"missing key id": {
			kms.EncryptInput{Plaintext: []byte("x")},
			"InvalidParameterException",
		},
		"empty plaintext": {
			kms.EncryptInput{KeyID: meta.KeyID},
			"ValidationException",
		},
		"oversized plaintext": {
			kms.EncryptInput{KeyID: meta.KeyID, Plaintext: bytes.Repeat([]byte("a"), 4097)},
			"ValidationException",
		},
		"unsupported algorithm": {
			kms.EncryptInput{
				KeyID:               meta.KeyID,
				Plaintext:           []byte("x"),
				EncryptionAlgorithm: "RSAES_OAEP_SHA_256",
			},
			"InvalidParameterException",
		},
	}

	for name, tc := range cases {
		rec := call(t, h, "Encrypt", tc.input)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", name, rec.Code, rec.Body.String())
		}
		if got := errType(t, rec); got != tc.errType {
			t.Fatalf("%s: expected %s, got %s", name, tc.errType, got)
		}
	}

	// The maximum size is accepted
	rec := call(t, h, "Encrypt", kms.EncryptInput{
		KeyID:     meta.KeyID,
		Plaintext: bytes.Repeat([]byte("a"), 4096),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("4096 bytes: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCryptoRejectsKeyPendingDeletion(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	var enc kms.EncryptOutput
	decode(t, call(t, h, "Encrypt", kms.EncryptInput{
		KeyID:     meta.KeyID,
		Plaintext: []byte("before deletion"),
	}), &enc)

	rec := call(t, h, "ScheduleKeyDeletion", kms.ScheduleKeyDeletionInput{KeyID: meta.KeyID})
	if rec.Code != http.StatusOK {
		t.Fatalf("ScheduleKeyDeletion: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"Encrypt": call(t, h, "Encrypt", kms.EncryptInput{KeyID: meta.KeyID, Plaintext: []byte("x")}),
		"Decrypt": call(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob}),
	} {
		if got := errType(t, rec); got != "KMSInvalidStateException" {
			t.Fatalf("%s: expected KMSInvalidStateException, got %s (%d)", name, got, rec.Code)
		}
	}
}

// A key stored before key material was persisted at creation is backfilled on
// first use rather than failing every Encrypt.
func TestKeyMaterialBackfilledForLegacyKey(t *testing.T) {
	store := NewMockStore()
	h := kms.NewHandler(store)

	meta := createKey(t, h)

	res, err := store.Get(meta.KeyID, "kms", "key", "default")
	if err != nil {
		t.Fatalf("key not stored: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		t.Fatalf("decode stored entry: %v", err)
	}
	delete(entry, "key_material")

	res.Attributes, _ = json.Marshal(entry)
	if err := store.Update(res); err != nil {
		t.Fatalf("update: %v", err)
	}

	var enc kms.EncryptOutput
	rec := call(t, h, "Encrypt", kms.EncryptInput{KeyID: meta.KeyID, Plaintext: []byte("legacy")})
	if rec.Code != http.StatusOK {
		t.Fatalf("Encrypt on legacy key: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &enc)

	// The backfilled material is persisted, so a later Decrypt still works
	var dec kms.DecryptOutput
	rec = call(t, h, "Decrypt", kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob})
	if rec.Code != http.StatusOK {
		t.Fatalf("Decrypt on backfilled key: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &dec)

	if string(dec.Plaintext) != "legacy" {
		t.Fatalf("unexpected plaintext: %q", dec.Plaintext)
	}
}

func TestDispatchUnknownAction(t *testing.T) {
	h := kms.NewHandler(NewMockStore())

	rec := call(t, h, "GenerateDataKey", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if got := errType(t, rec); got != "InvalidAction" {
		t.Fatalf("expected InvalidAction, got %s", got)
	}
}

// The KMS wire format carries blobs as base64 strings. The other tests marshal
// through the same structs, so they would not notice these fields regressing to
// a plain string; this drives the handler with raw JSON instead.
func TestBlobsAreBase64OnTheWire(t *testing.T) {
	h := kms.NewHandler(NewMockStore())
	meta := createKey(t, h)

	plaintext := []byte("wire format check")
	encoded := base64.StdEncoding.EncodeToString(plaintext)

	rec := callRaw(t, h, "Encrypt",
		`{"KeyId":"`+meta.KeyID+`","Plaintext":"`+encoded+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("Encrypt: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var enc struct {
		CiphertextBlob string `json:"CiphertextBlob"`
	}
	decode(t, rec, &enc)

	blob, err := base64.StdEncoding.DecodeString(enc.CiphertextBlob)
	if err != nil {
		t.Fatalf("CiphertextBlob is not base64: %q", enc.CiphertextBlob)
	}
	if bytes.Contains(blob, plaintext) {
		t.Fatal("ciphertext blob contains the plaintext")
	}

	rec = callRaw(t, h, "Decrypt", `{"CiphertextBlob":"`+enc.CiphertextBlob+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("Decrypt: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var dec struct {
		Plaintext string `json:"Plaintext"`
	}
	decode(t, rec, &dec)

	if dec.Plaintext != encoded {
		t.Fatalf("expected base64 plaintext %q, got %q", encoded, dec.Plaintext)
	}
}
