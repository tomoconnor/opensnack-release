// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package util

import (
	"encoding/xml"

	"github.com/labstack/echo/v4"
)

func WriteXML(c echo.Context, v interface{}) error {
	c.Response().Header().Set(echo.HeaderContentType, "application/xml")

	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	_, err = c.Response().Write([]byte(xml.Header))
	if err != nil {
		return err
	}

	_, err = c.Response().Write(out)
	return err
}
