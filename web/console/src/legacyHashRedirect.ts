// Runs before the router module is evaluated (first import of main.tsx): the
// browser router reads the address once, when it is created.
import { legacyHashPath } from "./lib/legacyHash";

const legacy = legacyHashPath(window.location.hash);
if (legacy !== null) window.history.replaceState(null, "", legacy);
