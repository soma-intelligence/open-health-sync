import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

type Tone = "ok" | "bad";
interface Item {
  id: number;
  message: string;
  tone: Tone;
}

const TOAST_MS = 4500;

const ToastContext = createContext<((message: string, tone?: Tone) => void) | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Item[]>([]);
  const next = useRef(0);
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());

  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach(clearTimeout);
      pending.clear();
    };
  }, []);

  const push = useCallback((message: string, tone: Tone = "ok") => {
    const id = next.current++;
    setItems((all) => [...all, { id, message, tone }]);
    const timer = setTimeout(() => {
      timers.current.delete(timer);
      setItems((all) => all.filter((i) => i.id !== id));
    }, TOAST_MS);
    timers.current.add(timer);
  }, []);

  const value = useMemo(() => push, [push]);
  return (
    <ToastContext value={value}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((i) => (
          <p key={i.id} className={`toast ${i.tone}`}>
            {i.message}
          </p>
        ))}
      </div>
    </ToastContext>
  );
}

export function useToast(): (message: string, tone?: Tone) => void {
  const push = useContext(ToastContext);
  if (!push) throw new Error("ToastProvider is missing");
  return push;
}
