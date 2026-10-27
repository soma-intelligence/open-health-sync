// SPDX-License-Identifier: AGPL-3.0-only

// Package vault holds the sealing of health data at rest: the row codec, the
// descriptor of every sealed table and, in later files, the keys and the AEAD
// that seal them (see docs/specs/vault.md).
//
// This file set covers the plaintext of a sealed row. A Table lists the columns
// that go into the one sealed column of a row; every other column stays in clear
// for SQL to filter, sort and join on.
package vault
