import type { KeyboardEvent, ReactNode } from "react";
import { Link, useNavigate } from "react-router";

import { routerPath } from "../lib/basename";
import { Icon } from "./Icon";

export interface Column<T> {
  key: string;
  header: string;
  cell: (row: T) => ReactNode;
  /** Extra class for the header and the cells, for a width or an alignment. */
  className?: string;
  /** The row's buttons: the cell stays in view while a wide table scrolls under it. */
  actions?: boolean;
}

export interface DataTableProps<T> {
  columns: readonly Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  onSelect?: (row: T) => void;
  selectedKey?: string | undefined;
  /** Where a row opens: its first cell is a link there, and Enter or a double click go there too. */
  to?: (row: T) => string;
  /** Shown instead of the table when there is no row. */
  empty: ReactNode;
}

/**
 * A table the keyboard drives: Tab to it, arrows move the selection, Enter opens the row.
 * A click on a row selects it (for the inspector). When rows open, the first cell is a real
 * link, so a click, a Cmd-click or the context menu opens the row like any link.
 * The selected row is marked with aria-current: aria-selected is only valid inside a grid.
 */
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

  const cellClass = (c: Column<T>) => [c.className, c.actions ? "row-actions-cell" : undefined].filter(Boolean).join(" ") || undefined;

  // The wrapper scrolls the table inside its card: on a phone every column and action stays reachable.
  return (
    <div className="table-scroll">
      <table className="data-table" tabIndex={0} onKeyDown={onKeyDown}>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} scope="col" className={cellClass(c)}>
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
                aria-current={key === selectedKey ? "true" : undefined}
                className={key === selectedKey ? "selected" : undefined}
                onClick={(e) => {
                  onSelect?.(row);
                  e.currentTarget.closest("table")?.focus();
                }}
                onDoubleClick={() => {
                  open(row);
                }}
              >
                {columns.map((c, i) => (
                  <td key={c.key} className={cellClass(c)}>
                    {to && i === 0 ? (
                      <Link className="row-link" to={routerPath(to(row))} tabIndex={-1}>
                        {c.cell(row)}
                        <Icon name="chevron" size="sm" />
                      </Link>
                    ) : (
                      c.cell(row)
                    )}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
