import { THEME_CHOICES, useTheme, type ThemeChoice } from "./theme";

const LABEL: Record<ThemeChoice, string> = { light: "Light", dark: "Dark", auto: "Auto" };

export function ThemeControl() {
  const [choice, setChoice] = useTheme();
  return (
    <div className="segmented" role="group" aria-label="Theme">
      {THEME_CHOICES.map((c) => (
        <button
          key={c}
          type="button"
          aria-pressed={choice === c}
          className={choice === c ? "on" : undefined}
          onClick={() => {
            setChoice(c);
          }}
        >
          {LABEL[c]}
        </button>
      ))}
    </div>
  );
}
