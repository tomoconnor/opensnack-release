// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import "strings"

// managedPolicy is one of the AWS managed cache or origin request policies,
// which every account sees under the same well-known ID.
type managedPolicy struct {
	id     string
	config string
}

func (m managedPolicy) record() *record {
	return &record{
		ID:        m.id,
		ETag:      "E23ZP02F085DFQ",
		Config:    m.config,
		Modified:  "2020-07-21T00:00:00.000Z",
		AccountID: "managed",
	}
}

func cachePolicyXML(name, comment string, min, def, max string, gzip bool, headers, cookies, queries string) string {
	b := map[bool]string{true: "true", false: "false"}
	return `<CachePolicyConfig><Comment>` + comment + `</Comment><Name>` + name + `</Name>` +
		`<DefaultTTL>` + def + `</DefaultTTL><MaxTTL>` + max + `</MaxTTL><MinTTL>` + min + `</MinTTL>` +
		`<ParametersInCacheKeyAndForwardedToOrigin>` +
		`<EnableAcceptEncodingGzip>` + b[gzip] + `</EnableAcceptEncodingGzip>` +
		`<EnableAcceptEncodingBrotli>` + b[gzip] + `</EnableAcceptEncodingBrotli>` +
		headers + cookies + queries +
		`</ParametersInCacheKeyAndForwardedToOrigin></CachePolicyConfig>`
}

func names(wrapper, item string, values ...string) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<" + wrapper + "><Quantity>" + itoa(len(values)) + "</Quantity><Items>")
	for _, v := range values {
		b.WriteString("<" + item + ">" + v + "</" + item + ">")
	}
	b.WriteString("</Items></" + wrapper + ">")
	return b.String()
}

func headersConfig(behavior string, hs ...string) string {
	return "<HeadersConfig><HeaderBehavior>" + behavior + "</HeaderBehavior>" + names("Headers", "Name", hs...) + "</HeadersConfig>"
}

func cookiesConfig(behavior string) string {
	return "<CookiesConfig><CookieBehavior>" + behavior + "</CookieBehavior></CookiesConfig>"
}

func queryStringsConfig(behavior string) string {
	return "<QueryStringsConfig><QueryStringBehavior>" + behavior + "</QueryStringBehavior></QueryStringsConfig>"
}

var managedCachePolicies = []managedPolicy{
	{"658327ea-f89d-4fab-a63d-7e88639e58f6", cachePolicyXML("Managed-CachingOptimized",
		"Policy with caching enabled. Supports Gzip and Brotli compression.", "1", "86400", "31536000", true,
		headersConfig("none"), cookiesConfig("none"), queryStringsConfig("none"))},
	{"4135ea2d-6df8-44a3-9df3-4b5a84be39ad", cachePolicyXML("Managed-CachingDisabled",
		"Policy with caching disabled", "0", "0", "0", false,
		headersConfig("none"), cookiesConfig("none"), queryStringsConfig("none"))},
	{"b2884449-e4de-46a7-ac36-70bc7f1ddd6d", cachePolicyXML("Managed-CachingOptimizedForUncompressedObjects",
		"Default policy when compression is disabled", "1", "86400", "31536000", false,
		headersConfig("none"), cookiesConfig("none"), queryStringsConfig("none"))},
	{"83da9c7e-98b4-4e11-a168-04f0df8e2c65", cachePolicyXML("UseOriginCacheControlHeaders",
		"Policy for origins that return Cache-Control headers. Query strings are not included in the cache key.", "0", "0", "31536000", true,
		headersConfig("whitelist", "host", "origin", "x-http-method-override", "x-http-method", "x-method-override"),
		cookiesConfig("all"), queryStringsConfig("none"))},
	{"4cc15a8a-d715-48a4-82b8-cc0b614638fe", cachePolicyXML("UseOriginCacheControlHeaders-QueryStrings",
		"Policy for origins that return Cache-Control headers and serve different content based on values present in the query string.", "0", "0", "31536000", true,
		headersConfig("whitelist", "host", "origin", "x-http-method-override", "x-http-method", "x-method-override"),
		cookiesConfig("all"), queryStringsConfig("all"))},
}

func originRequestPolicyXML(name, comment, headers, cookies, queries string) string {
	return `<OriginRequestPolicyConfig><Comment>` + comment + `</Comment><Name>` + name + `</Name>` +
		headers + cookies + queries + `</OriginRequestPolicyConfig>`
}

var managedOriginRequestPolicies = []managedPolicy{
	{"216adef6-5c7f-47e4-b989-5492eafa07d3", originRequestPolicyXML("Managed-AllViewer",
		"Policy to forward all parameters in viewer requests",
		headersConfig("allViewer"), cookiesConfig("all"), queryStringsConfig("all"))},
	{"88a5eaf4-2fd4-4709-b370-b4c650ea3fcf", originRequestPolicyXML("Managed-CORS-S3Origin",
		"Policy for S3 origin with CORS",
		headersConfig("whitelist", "origin", "access-control-request-headers", "access-control-request-method"),
		cookiesConfig("none"), queryStringsConfig("none"))},
	{"59781a5b-3903-41f3-afcb-af62929ccde1", originRequestPolicyXML("Managed-CORS-CustomOrigin",
		"Policy for custom origin with CORS",
		headersConfig("whitelist", "origin"), cookiesConfig("none"), queryStringsConfig("none"))},
	{"b689b0a8-53d0-40ab-baf2-68738e2966ac", originRequestPolicyXML("Managed-AllViewerExceptHostHeader",
		"Policy to forward all parameters in viewer requests except for the Host header",
		headersConfig("allExcept", "host"), cookiesConfig("all"), queryStringsConfig("all"))},
	{"acba4595-bd28-49b8-b9fe-13317c0390fa", originRequestPolicyXML("Managed-UserAgentRefererHeaders",
		"Policy to forward user-agent and referer headers to origin",
		headersConfig("whitelist", "user-agent", "referer"), cookiesConfig("none"), queryStringsConfig("none"))},
}
