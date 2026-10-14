// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"opensnack/internal/api/sns"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

// snsNotifier delivers CloudWatch alarm actions through the SNS handler's
// own Publish path, as an in-process Query request on the alarm owner's
// behalf, so CloudWatch needs nothing from the SNS package but this.
type snsNotifier struct{ h *sns.Handler }

func (n snsNotifier) Publish(scope, accountID, topicArn, subject, message string) error {
	ns, region := resource.SplitScope(scope)
	form := url.Values{
		"Action":   {"Publish"},
		"Version":  {sns.APIVersion},
		"TopicArn": {topicArn},
		"Subject":  {subject},
		"Message":  {message},
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(util.WithIdentity(r.Context(), &util.Identity{
		Namespace: ns, AccountID: accountID, Region: region, SigningService: "sns",
	}))
	rec := httptest.NewRecorder()
	n.h.Dispatch(rec, r)
	if rec.Code != http.StatusOK {
		return fmt.Errorf("sns publish: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	return nil
}
