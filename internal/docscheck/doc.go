// SPDX-License-Identifier: AGPL-3.0-only

// Package docscheck holds repository-wide checks that run as tests: that
// every Go package has a package comment, that the hosting platform
// templates (render.yaml, railway.json, deploy/fly) parse and stay in step
// with the compose stack, and that the documented API key scopes are the
// ones the server knows.
package docscheck
