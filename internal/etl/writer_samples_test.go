// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// The supersede step stays inside the job's tenant and user and matches one
// figure of one provider record: provider, external id and quantity type.
func TestQuantitySupersedeStatement(t *testing.T) {
	assert.Contains(t, quantitySupersede, "q.tenant_id = $1 AND q.user_id = $2")
	assert.Contains(t, quantitySupersede, "q.provider = n.provider")
	assert.Contains(t, quantitySupersede, `q.external_id COLLATE "C" = n.external_id`, "on the index of migration 0014")
	assert.Contains(t, quantitySupersede, "q.quantity_type = n.quantity_type")
	assert.Contains(t, quantitySupersede, "q.start_date <> n.start_date", "the row the upsert targets stays")
}

// Of several copies of one figure in a batch the last is kept; samples
// without the flag, without an external id or not Replaceable are left alone,
// however many share an external id.
func TestSupersedeMovedSplit(t *testing.T) {
	sc := scope{provider: "whoop"}
	s := func(id, qt string, v float64, flag bool) model.QuantitySample {
		return model.QuantitySample{ExternalID: id, QuantityType: qt, Value: v, Replaceable: true, SupersedeMoved: flag}
	}
	in := []model.QuantitySample{
		s("c-1_strain", "strain", 1, true),
		s("series", "heart_rate", 60, false),
		s("c-1_strain", "strain", 2, true),
		s("series", "heart_rate", 61, false),
		s("c-1_strain", "heart_rate", 70, true),
		s("", "strain", 3, true),
		{ExternalID: "c-2_strain", QuantityType: "strain", Value: 4, SupersedeMoved: true},
		{ExternalID: "c-2_strain", QuantityType: "strain", Value: 5, SupersedeMoved: true},
		// Another provider's record of the same id is another figure.
		{Provider: "other", ExternalID: "c-1_strain", QuantityType: "strain", Value: 6, Replaceable: true, SupersedeMoved: true},
	}
	out, dropped := supersedeMovedSplit(sc, in)
	assert.Equal(t, 1, dropped)
	var values []float64
	for _, o := range out {
		values = append(values, o.Value)
	}
	assert.Equal(t, []float64{60, 2, 61, 70, 3, 4, 5, 6}, values)

	plain := []model.QuantitySample{s("a", "strain", 1, false)}
	out, dropped = supersedeMovedSplit(sc, plain)
	assert.Zero(t, dropped)
	assert.Equal(t, plain, out)
}
