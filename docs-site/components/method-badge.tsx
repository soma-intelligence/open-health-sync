const methodClass: Record<string, string> = {
  GET: "get",
  POST: "post",
  DELETE: "delete",
  PUT: "put",
  PATCH: "patch",
};

/** GET, POST and DELETE in their state colours; the short form keeps the names aligned. */
export function MethodBadge({ method }: { method: string }) {
  return (
    <span className="ohs-method" data-method={methodClass[method] ?? "other"}>
      {method === "DELETE" ? "DEL" : method}
    </span>
  );
}
