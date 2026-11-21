// SPDX-License-Identifier: AGPL-3.0-only

// Package settings stores the runtime settings the admin console saves, in
// ohs_core.instance_settings. Secret values are sealed with the encryption
// key and bound to their name; every write moves the revision a running
// process compares with the one it loaded.
package settings
