// Demo users: the ones `ohs seed --demo` and POST /v1/admin/tenants/{id}/demo-user create, whose
// records are synthetic. The API keeps no flag for them; their default external ids name them
// (demo.DefaultExternalRef in internal/demo/personas.go): demo-user for the mix persona and
// demo-<persona> for the others. A demo user seeded under another --external-ref is not caught.

/** The default external ids of the demo personas: mix, runner, sleep and weight. */
export const DEMO_REFS: ReadonlySet<string> = new Set(["demo-user", "demo-runner", "demo-sleep", "demo-weight"]);

/** True for a user the demo seed created under its default external id. */
export function isDemoUser(user: { external_ref: string | null }): boolean {
  return user.external_ref !== null && DEMO_REFS.has(user.external_ref);
}
