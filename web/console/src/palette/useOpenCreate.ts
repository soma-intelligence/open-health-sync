import { useEffect } from "react";
import { useLocation, useNavigate } from "react-router";

import { useCanWrite } from "../session/SessionProvider";

/**
 * Runs onOpen when the page was reached with location.state.open === "create"
 * (the command palette does that), then clears the state so a reload or back
 * does not open it again. A read only session never opens the dialog.
 */
export function useOpenCreate(onOpen: () => void): void {
  const location = useLocation();
  const navigate = useNavigate();
  const canWrite = useCanWrite();
  const requested = (location.state as { open?: unknown } | null)?.open === "create";
  const { pathname, search, hash } = location;

  useEffect(() => {
    if (!requested) return;
    if (canWrite) onOpen();
    void navigate({ pathname, search, hash }, { replace: true, state: null });
    // onOpen only sets state; it is not a dependency of the request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requested, canWrite, pathname, search, hash, navigate]);
}
