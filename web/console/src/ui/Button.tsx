import type { ButtonHTMLAttributes } from "react";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  kind?: "primary" | "ghost" | "danger";
  size?: "sm";
}

export function Button({ kind, size, className, type = "button", ...rest }: ButtonProps) {
  const classes = ["btn", kind, size, className].filter(Boolean).join(" ");
  return <button type={type} className={classes} {...rest} />;
}
