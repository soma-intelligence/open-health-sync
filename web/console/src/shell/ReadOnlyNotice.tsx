import { useSession } from "../session/SessionProvider";
import { Icon } from "../ui/Icon";

/**
 * A read-only console hides every write. Without a word, an operator looks for a button that is not
 * there; this says why, once, above every page.
 */
export function ReadOnlyNotice() {
  const { session } = useSession();
  if (!session?.read_only) return null;
  return (
    <div className="notice-bar readonly-notice" role="note">
      <Icon name="lock" size="sm" />
      <p>
        This console is read only (<span className="mono">OHS_CONSOLE=read-only</span>): you can look at everything, and the
        buttons that change data are hidden.
      </p>
    </div>
  );
}
