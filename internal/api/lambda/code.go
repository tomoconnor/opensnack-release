// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"opensnack/internal/api/s3"
	"opensnack/internal/util"
)

//
// CODE STORAGE
//
// A function's zip is a blob file under the tenant's object directory (the
// same one S3 uses, so deleting the namespace removes it too):
//
//	$OPENSNACK_OBJECT_ROOT/<sha256(namespace)[:32]>/lambda/<blob>.zip
//
// $LATEST and each published version name their blob in the store. A version
// shares the blob of the $LATEST it was published from; a blob is removed
// once nothing names it.
//

const maxZipSize = 50 << 20    // direct upload
const maxS3ZipSize = 250 << 20 // from S3 (AWS bounds the unzipped size)

func codeDir(namespace string) string {
	return filepath.Join(s3.NamespaceDir(namespace), "lambda")
}

var blobName = regexp.MustCompile(`^[0-9a-f]{32}$`)

func codePath(namespace, blob string) string {
	return filepath.Join(codeDir(namespace), blob+".zip")
}

func writeCode(namespace string, body []byte) (string, error) {
	blob := util.RandomHex(16)
	path := codePath(namespace, blob)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	return blob, os.Rename(tmp, path)
}

// ReadCode returns a stored zip by blob name.
func ReadCode(namespace, blob string) ([]byte, error) {
	if !blobName.MatchString(blob) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(codePath(namespace, blob))
}

func removeCode(r *http.Request, blob string) {
	if blobName.MatchString(blob) {
		_ = os.Remove(codePath(util.GlobalNamespace(r), blob))
	}
}

func codeSha256(zip []byte) string {
	sum := sha256.Sum256(zip)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// fetchCode returns the zip a FunctionCode names: inline bytes or an S3
// object in the caller's own namespace. Image functions have no zip.
func (h *Handler) fetchCode(r *http.Request, code *FunctionCode, packageType string) ([]byte, error) {
	if packageType == "Image" {
		if code.ImageUri == "" {
			return nil, errInvalid("ImageUri must be specified for functions with the Image package type.")
		}
		return nil, nil
	}
	var body []byte
	switch {
	case len(code.ZipFile) > 0:
		if code.S3Bucket != "" || code.S3Key != "" {
			return nil, errInvalid("Please provide a source for function code: either ZipFile or S3Bucket and S3Key, not both.")
		}
		if len(code.ZipFile) > maxZipSize {
			return nil, &apiError{http.StatusRequestEntityTooLarge, "RequestEntityTooLargeException", "Request must be smaller than 69905067 bytes for the CreateFunction operation"}
		}
		body = code.ZipFile
	case code.S3Bucket != "" && code.S3Key != "":
		ns := util.GlobalNamespace(r)
		if _, err := h.Store.Get(code.S3Bucket, "s3", "bucket", ns); err != nil {
			return nil, errInvalid("Error occurred while GetObject. S3 Error Code: NoSuchBucket. S3 Error Message: The specified bucket does not exist")
		}
		b, err := s3.NewHandler(h.Store).ReadObject(ns, code.S3Bucket, code.S3Key)
		if err != nil {
			return nil, errInvalid("Error occurred while GetObject. S3 Error Code: NoSuchKey. S3 Error Message: The specified key does not exist.")
		}
		if len(b) > maxS3ZipSize {
			return nil, errInvalid("Unzipped size must be smaller than 262144000 bytes")
		}
		body = b
	case code.ImageUri != "":
		return nil, errInvalid("Please don't provide ImageUri when updating a function with packageType Zip.")
	default:
		return nil, errInvalid("Please provide a source for function code.")
	}
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err != nil {
		return nil, errInvalid("Could not unzip uploaded file. Please check your file, then try to upload again.")
	}
	return body, nil
}

// setCode stores zip as fn's $LATEST code (or records the image URI).
func (h *Handler) setCode(r *http.Request, fn *functionRecord, body []byte, code *FunctionCode) error {
	if fn.Config.PackageType == "Image" {
		fn.ImageUri = code.ImageUri
		sum := sha256.Sum256([]byte(code.ImageUri))
		fn.Config.CodeSha256 = hex.EncodeToString(sum[:])
		fn.Config.CodeSize = 0
		return nil
	}
	blob, err := writeCode(util.GlobalNamespace(r), body)
	if err != nil {
		return err
	}
	fn.CodeBlob = blob
	fn.Config.CodeSha256 = codeSha256(body)
	fn.Config.CodeSize = int64(len(body))
	return nil
}

// blobInUse reports whether $LATEST or any version of the function still
// names blob.
func (h *Handler) blobInUse(r *http.Request, name, blob string) bool {
	if blob == "" {
		return true
	}
	if fn, err := h.loadFunction(r, name); err == nil && fn.CodeBlob == blob {
		return true
	}
	for _, v := range h.versions(r, name) {
		if v.CodeBlob == blob {
			return true
		}
	}
	return false
}

//
// CODE DOWNLOAD
//
// GetFunction's Code.Location is a presigned-style URL served by opensnack:
//
//	{base}/_opensnack/lambda-code/{blob}?ns=...&expires=...&signature=...
//
// It sits under the internal prefix, outside SigV4, like a real presigned S3
// URL: the signature is an HMAC over the namespace, blob and expiry with a
// server secret, so only a caller who got the URL from an authenticated
// GetFunction in that namespace can use it, and only for 10 minutes. The
// secret is OPENSNACK_LAMBDA_CODE_SECRET, or random per process (URLs then
// die with a restart).
//

const CodeDownloadPrefix = "/_opensnack/lambda-code/"

const codeURLLifetime = 10 * time.Minute

var codeSecret = func() []byte {
	if s := os.Getenv("OPENSNACK_LAMBDA_CODE_SECRET"); s != "" {
		return []byte(s)
	}
	b := make([]byte, 32)
	rand.Read(b)
	return b
}()

func codeSignature(namespace, blob string, expires int64) string {
	mac := hmac.New(sha256.New, codeSecret)
	mac.Write([]byte(namespace + "\n" + blob + "\n" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func codeDownloadURL(r *http.Request, blob string) string {
	if blob == "" {
		return ""
	}
	namespace := util.GlobalNamespace(r)
	expires := time.Now().Add(codeURLLifetime).Unix()
	q := url.Values{}
	q.Set("ns", namespace)
	q.Set("expires", strconv.FormatInt(expires, 10))
	q.Set("signature", codeSignature(namespace, blob, expires))
	return util.BaseURL(r) + CodeDownloadPrefix + blob + "?" + q.Encode()
}

// ServeCodeDownload serves a Code.Location URL. The router mounts it at
// CodeDownloadPrefix, outside AWS authentication; the URL's own signature is
// the authorisation.
func (h *Handler) ServeCodeDownload(w http.ResponseWriter, r *http.Request) {
	deny := func(status int, code, msg string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		w.Write([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>" + code + "</Code><Message>" + msg + "</Message></Error>"))
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		deny(http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed against this resource.")
		return
	}
	blob := filepath.Base(r.URL.Path)
	q := r.URL.Query()
	namespace := q.Get("ns")
	expires, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err != nil || !blobName.MatchString(blob) || namespace == "" ||
		!hmac.Equal([]byte(q.Get("signature")), []byte(codeSignature(namespace, blob, expires))) {
		deny(http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	}
	if time.Now().Unix() > expires {
		deny(http.StatusForbidden, "AccessDenied", "Request has expired")
		return
	}
	f, err := os.Open(codePath(namespace, blob))
	if err != nil {
		deny(http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/zip")
	http.ServeContent(w, r, "", st.ModTime(), f)
}
