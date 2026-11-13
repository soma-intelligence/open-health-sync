// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestParseStreamNotification(t *testing.T) {
	tenant := uuid.New()
	got, seq, ok := ParseStreamNotification(tenant.String() + ":42")
	assert.True(t, ok)
	assert.Equal(t, tenant, got)
	assert.Equal(t, int64(42), seq)

	for _, bad := range []string{"", "42", tenant.String(), tenant.String() + ":", tenant.String() + ":0",
		tenant.String() + ":-3", tenant.String() + ":x", "nope:42"} {
		_, _, ok := ParseStreamNotification(bad)
		assert.False(t, ok, "%q", bad)
	}
}
