// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package awsresponses

import (
	"fmt"
	"sync/atomic"
)

var requestCounter uint64 = 1

func NextRequestID() string {
	n := atomic.AddUint64(&requestCounter, 1)
	return fmt.Sprintf("REQ-%06d", n)
}
