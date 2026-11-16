import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter, useLocation } from "react-router";

import { ApiError } from "../api/client";
import { ConfirmDialog } from "./ConfirmDialog";
import { DataTable, type Column } from "./DataTable";
import { Dialog } from "./Dialog";

describe("Dialog", () => {
  function Host({ onClose }: { onClose: () => void }) {
    return (
      <Dialog title="First" open onClose={onClose}>
        <p>inside</p>
      </Dialog>
    );
  }

  it("shows its title and content when open, nothing when closed", () => {
    const { rerender } = render(
      <Dialog title="Hello" open={false} onClose={() => undefined}>
        <p>inside</p>
      </Dialog>,
    );
    expect(screen.queryByText("inside")).not.toBeInTheDocument();
    rerender(
      <Dialog title="Hello" open onClose={() => undefined}>
        <p>inside</p>
      </Dialog>,
    );
    expect(screen.getByRole("dialog", { name: "Hello" })).toBeInTheDocument();
    expect(screen.getByText("inside")).toBeInTheDocument();
  });

  it("asks to close on Escape", () => {
    const onClose = vi.fn();
    render(<Host onClose={onClose} />);
    const dialog = screen.getByRole("dialog", { name: "First" });
    act(() => {
      dialog.dispatchEvent(new Event("cancel", { cancelable: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("asks to close on a click on the backdrop, not on the content", async () => {
    const onClose = vi.fn();
    render(<Host onClose={onClose} />);
    await userEvent.click(screen.getByText("inside"));
    expect(onClose).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("dialog", { name: "First" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("gives two open dialogs their own labels", () => {
    render(
      <>
        <Dialog title="One" open onClose={() => undefined}>
          <p>a</p>
        </Dialog>
        <Dialog title="Two" open onClose={() => undefined}>
          <p>b</p>
        </Dialog>
      </>,
    );
    expect(screen.getByRole("dialog", { name: "One" })).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Two" })).toBeInTheDocument();
  });
});

describe("ConfirmDialog", () => {
  it("with typed keeps the button disabled until the text matches", async () => {
    const onConfirm = vi.fn(() => Promise.resolve(null));
    render(<ConfirmDialog title="Delete user" body="This cannot be undone." confirmLabel="Delete user" danger typed="p-17" onConfirm={onConfirm} onClose={() => undefined} />);
    const button = screen.getByRole("button", { name: "Delete user" });
    expect(button).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Type p-17 to confirm/), "p-1");
    expect(button).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Type p-17 to confirm/), "7");
    expect(button).toBeEnabled();
    await userEvent.click(button);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("closes after a confirmation that worked", async () => {
    const onClose = vi.fn();
    render(<ConfirmDialog title="Go" body="Sure?" confirmLabel="Go on" onConfirm={() => Promise.resolve(null)} onClose={onClose} />);
    await userEvent.click(screen.getByRole("button", { name: "Go on" }));
    await waitFor(() => {
      expect(onClose).toHaveBeenCalledTimes(1);
    });
  });

  it("shows the API error and stays open", async () => {
    const onClose = vi.fn();
    const onConfirm = vi.fn(() => Promise.reject(new ApiError(409, "a user with this external id exists", null, null)));
    render(<ConfirmDialog title="Go" body="Sure?" confirmLabel="Go on" onConfirm={onConfirm} onClose={onClose} />);
    await userEvent.click(screen.getByRole("button", { name: "Go on" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("a user with this external id exists");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Go on" })).toBeEnabled();
  });
});

interface Row {
  id: string;
  name: string;
}
const rows: Row[] = [
  { id: "a", name: "Alpha" },
  { id: "b", name: "Beta" },
  { id: "c", name: "Gamma" },
];
const columns: Column<Row>[] = [{ key: "name", header: "Name", cell: (r) => r.name }];

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

describe("DataTable", () => {
  it("moves the selection with the arrow keys and opens with Enter", async () => {
    function Host() {
      const [key, setKey] = useState<string | undefined>(undefined);
      return (
        <MemoryRouter initialEntries={["/users"]}>
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.id}
            selectedKey={key}
            onSelect={(r) => {
              setKey(r.id);
            }}
            to={(r) => `/users/${r.id}`}
            empty={<p>none</p>}
          />
          <Where />
        </MemoryRouter>
      );
    }
    render(<Host />);
    const table = screen.getByRole("table");
    table.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(screen.getByRole("row", { name: /Alpha/ })).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{ArrowDown}");
    expect(screen.getByRole("row", { name: /Beta/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("row", { name: /Alpha/ })).toHaveAttribute("aria-selected", "false");
    await userEvent.keyboard("{ArrowUp}");
    expect(screen.getByRole("row", { name: /Alpha/ })).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");
    expect(screen.getByRole("row", { name: /Gamma/ })).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Enter}");
    expect(screen.getByTestId("where")).toHaveTextContent("/users/c");
  });

  it("shows the empty node when there are no rows", () => {
    render(
      <MemoryRouter>
        <DataTable columns={columns} rows={[]} rowKey={(r) => r.id} empty={<p>none here</p>} />
      </MemoryRouter>,
    );
    expect(screen.getByText("none here")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
