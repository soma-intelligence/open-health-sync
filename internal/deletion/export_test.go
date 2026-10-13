// SPDX-License-Identifier: AGPL-3.0-only

package deletion

// ChildTables exposes childTables to the integration tests: table name to
// its foreign key column and parent table.
func ChildTables() map[string][2]string {
	out := make(map[string][2]string, len(childTables))
	for t, c := range childTables {
		out[t] = [2]string{c.fk, c.parent}
	}
	return out
}
