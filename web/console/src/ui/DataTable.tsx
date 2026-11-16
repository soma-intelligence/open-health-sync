import type { KeyboardEvent, ReactNode } from "react";
import { useNavigate } from "react-router";

import { routerPath } from "../lib/basename";

export interface Column<T> {
  key: string;
  header: string;
  cell: (row: T) => ReactNode;
  /** Extra class for the header and the cells, for a width or an alignment. */
  className?: string;
}

export interface DataTableProps<T> {
  columns: readonly Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  onSelect?: (row: T) => void;
  selectedKey?: string | undefined;
  /** Where Enter, or a double click, takes a row. */
  to?: (row: T) => string;
  /** Shown instead of the table when there is no row. */
  empty: ReactNode;
}

/** A table the keyboard drives: Tab to it, arrows move the selection, Enter opens the row. */
export function DataTable<T>({ columns, rows, rowKey, onSelect, selectedKey, to, empty }: DataTableProps<T>) {
  const navigate = useNavigate();
  if (rows.length === 0) return <>{empty}</>;

  const index = rows.findIndex((r) => rowKey(r) === selectedKey);
  const open = (row: T) => {
    if (to) void navigate(routerPath(to(row)));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTableElement>) => {
    const move = (to: number) => {
      const row = rows[Math.min(rows.length - 1, Math.max(0, to))];
      if (row !== undefined) onSelect?.(row);
    };
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        move(index + 1);
        break;
      case "ArrowUp":
        e.preventDefault();
        move(index < 0 ? 0 : index - 1);
        break;
      case "Home":
        e.preventDefault();
        move(0);
        break;
      case "End":
        e.preventDefault();
        move(rows.length - 1);
        break;
      case "Enter": {
        const row = rows[index];
        if (row !== undefined) {
          e.preventDefault();
          open(row);
        }
        break;
      }
    }
  };

  return (
    <table className="data-table" tabIndex={0} onKeyDown={onKeyDown}>
      <thead>
        <tr>
          {columns.map((c) => (
            <th key={c.key} scope="col" className={c.className}>
              {c.header}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const key = rowKey(row);
          return (
            <tr
              key={key}
              aria-selected={key === selectedKey}
              className={key === selectedKey ? "selected" : undefined}
              onClick={(e) => {
                onSelect?.(row);
                e.currentTarget.closest("table")?.focus();
              }}
              onDoubleClick={() => {
                open(row);
              }}
            >
              {columns.map((c) => (
                <td key={c.key} className={c.className}>
                  {c.cell(row)}
                </td>
              ))}
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
