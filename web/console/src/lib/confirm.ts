/** True when what the operator typed names what goes. */
export function confirmMatches(typed: string | null | undefined, expected: string): boolean {
  return typeof typed === "string" && typed.trim() !== "" && typed.trim() === expected;
}
