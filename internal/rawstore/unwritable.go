// SPDX-License-Identifier: AGPL-3.0-only

package rawstore

import (
	"errors"
	"io/fs"
	"syscall"
)

// Unwritable reports whether err means the store refused a write for want
// of room or of permission: a full disk, a quota, a read-only or
// unwritable directory. An operator has to act, and the same part can be
// sent again afterwards, so an upload refused for this reason answers 503
// with Retry-After instead of a 500 the client may give up on.
func Unwritable(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) ||
		errors.Is(err, syscall.EROFS) || errors.Is(err, fs.ErrPermission)
}
