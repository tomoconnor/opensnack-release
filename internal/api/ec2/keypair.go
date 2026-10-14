// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"crypto/ed25519"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

var keyPairActions = map[string]action{
	"CreateKeyPair":    createKeyPair,
	"ImportKeyPair":    importKeyPair,
	"DeleteKeyPair":    deleteKeyPair,
	"DescribeKeyPairs": describeKeyPairs,
}

func (k *KeyPair) objectID() string { return k.KeyPairId }
func (k *KeyPair) tagList() *[]Tag  { return &k.Tags }
func (k *KeyPair) filterValues() map[string][]string {
	return map[string][]string{
		"key-pair-id": {k.KeyPairId}, "key-name": {k.KeyName}, "fingerprint": {k.KeyFingerprint}, "key-type": {k.KeyType},
	}
}

func colonHex(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

func (c *call) keyPairByName(name string) (*KeyPair, error) {
	keys, err := all[KeyPair](c, tKeyPair)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.KeyName == name {
			return k, nil
		}
	}
	return nil, errorf("InvalidKeyPair.NotFound", "The key pair '%s' does not exist", name)
}

func (c *call) checkKeyName(name string) error {
	if name == "" {
		return missing("KeyName")
	}
	if len(name) > 255 {
		return invalidParam("KeyName", name)
	}
	if _, err := c.keyPairByName(name); err == nil {
		return errorf("InvalidKeyPair.Duplicate", "The keypair already exists")
	}
	return nil
}

func createKeyPair(c *call) (any, error) {
	name := c.p.get("KeyName")
	if err := c.checkKeyName(name); err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("key-pair")
	if err != nil {
		return nil, err
	}
	keyType := c.p.get("KeyType")
	if keyType == "" {
		keyType = "rsa"
	}
	var material, fingerprint, public string
	switch keyType {
	case "rsa":
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		der := x509.MarshalPKCS1PrivateKey(priv)
		material = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
		// AWS fingerprints keys it creates by the SHA-1 of the PKCS#8 key.
		pk8, _ := x509.MarshalPKCS8PrivateKey(priv)
		sum := sha1.Sum(pk8)
		fingerprint = colonHex(sum[:])
		sshPub, _ := ssh.NewPublicKey(&priv.PublicKey)
		public = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	case "ed25519":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "")
		if err != nil {
			return nil, err
		}
		material = string(pem.EncodeToMemory(block))
		sshPub, _ := ssh.NewPublicKey(pub)
		sum := sha256.Sum256(sshPub.Marshal())
		fingerprint = base64.StdEncoding.EncodeToString(sum[:])
		public = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	default:
		return nil, invalidParam("KeyType", keyType)
	}
	k := &KeyPair{KeyPairId: newID("key"), KeyName: name, KeyFingerprint: fingerprint, KeyType: keyType,
		CreateTime: now(), PublicKey: public + " " + name, Tags: tags}
	if err := c.put(tKeyPair, k); err != nil {
		return nil, err
	}
	return struct {
		KeyName        string `xml:"keyName"`
		KeyFingerprint string `xml:"keyFingerprint"`
		KeyMaterial    string `xml:"keyMaterial"`
		KeyPairId      string `xml:"keyPairId"`
		Tags           []Tag  `xml:"tagSet>item,omitempty"`
	}{k.KeyName, k.KeyFingerprint, material, k.KeyPairId, k.Tags}, nil
}

// importFingerprint is what AWS reports for an imported public key: the
// MD5 of the DER public key for RSA (openssl rsa -pubout -outform DER |
// openssl md5 -c), base64 SHA-256 of the key blob for ED25519.
func importFingerprint(pub ssh.PublicKey) (string, string, error) {
	switch pub.Type() {
	case ssh.KeyAlgoRSA:
		der, err := x509.MarshalPKIXPublicKey(pub.(ssh.CryptoPublicKey).CryptoPublicKey())
		if err != nil {
			return "", "", errorf("InvalidKey.Format", "Key is not in valid OpenSSH public key format")
		}
		sum := md5.Sum(der)
		return "rsa", colonHex(sum[:]), nil
	case ssh.KeyAlgoED25519:
		sum := sha256.Sum256(pub.Marshal())
		return "ed25519", base64.StdEncoding.EncodeToString(sum[:]), nil
	}
	return "", "", errorf("InvalidKey.Format", "Key is not in valid OpenSSH public key format")
}

func importKeyPair(c *call) (any, error) {
	name := c.p.get("KeyName")
	if err := c.checkKeyName(name); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(c.p.get("PublicKeyMaterial"))
	if err != nil {
		raw = []byte(c.p.get("PublicKeyMaterial"))
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return nil, errorf("InvalidKey.Format", "Key is not in valid OpenSSH public key format")
	}
	keyType, fingerprint, err := importFingerprint(pub)
	if err != nil {
		return nil, err
	}
	tags, err := c.tagSpec("key-pair")
	if err != nil {
		return nil, err
	}
	k := &KeyPair{KeyPairId: newID("key"), KeyName: name, KeyFingerprint: fingerprint, KeyType: keyType,
		CreateTime: now(), PublicKey: strings.TrimSpace(string(raw)), Tags: tags}
	if err := c.put(tKeyPair, k); err != nil {
		return nil, err
	}
	return struct {
		KeyName        string `xml:"keyName"`
		KeyFingerprint string `xml:"keyFingerprint"`
		KeyPairId      string `xml:"keyPairId"`
		Tags           []Tag  `xml:"tagSet>item,omitempty"`
	}{k.KeyName, k.KeyFingerprint, k.KeyPairId, k.Tags}, nil
}

// DeleteKeyPair succeeds whether or not the key exists, as in AWS.
func deleteKeyPair(c *call) (any, error) {
	var k *KeyPair
	if id := c.p.get("KeyPairId"); id != "" {
		k = new(KeyPair)
		if ok, err := c.get(tKeyPair, id, k); err != nil || !ok {
			return struct {
				Return bool `xml:"return"`
			}{true}, err
		}
	} else if found, err := c.keyPairByName(c.p.get("KeyName")); err == nil {
		k = found
	}
	if k == nil {
		return nil, nil
	}
	if err := c.remove(tKeyPair, k.KeyPairId); err != nil {
		return nil, err
	}
	return struct {
		Return    bool   `xml:"return"`
		KeyPairId string `xml:"keyPairId"`
	}{true, k.KeyPairId}, nil
}

func describeKeyPairs(c *call) (any, error) {
	keys, err := describe[KeyPair](c, tKeyPair, c.p.list("KeyPairId"), "InvalidKeyPair.NotFound")
	if err != nil {
		return nil, err
	}
	if names := c.p.list("KeyName"); len(names) > 0 {
		var picked []*KeyPair
		for _, n := range names {
			i := slices.IndexFunc(keys, func(k *KeyPair) bool { return k.KeyName == n })
			if i < 0 {
				return nil, errorf("InvalidKeyPair.NotFound", "The key pair '%s' does not exist", n)
			}
			picked = append(picked, keys[i])
		}
		keys = picked
	}
	if !c.p.boolean("IncludePublicKey") {
		for _, k := range keys {
			k.PublicKey = ""
		}
	}
	return struct {
		Keys []*KeyPair `xml:"keySet>item"`
	}{keys}, nil
}
