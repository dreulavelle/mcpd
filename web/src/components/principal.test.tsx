import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type ApiKey } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { usePrincipalNames } from "./principal";

function key(name: string, id: string): ApiKey {
  return {
    id, name, role: "role_operator", role_name: "Operator",
    grants: [], reaches: [], permissions: [], groups: [],
    status: "active", created_by: "user:admin@example.com",
    created_at: "2026-08-01T00:00:00Z",
  };
}

function Probe({ actors }: { actors: [string, string?][] }) {
  const name = usePrincipalNames();
  return (
    <ul>
      {actors.map(([actor, resolved]) => (
        <li key={actor}>{name(actor, resolved)}</li>
      ))}
    </ul>
  );
}

function mount(actors: [string, string?][], keys: ApiKey[] = []) {
  vi.spyOn(api, "keys").mockResolvedValue({ keys, count: keys.length });
  return renderWith(<Probe actors={actors} />, { session: sessionFor("admin") });
}

describe("naming a principal", () => {
  /**
   * `renderName` on the server falls back to the identifier itself, so a name
   * equal to the identifier is no answer at all. Preferring it blindly would
   * put `svc:chatgpt:work` into prose on the page that exists to keep machine
   * strings out of it.
   */
  it("prefers a resolved name and ignores one that is only the identifier", async () => {
    mount([
      ["user:alice@example.com", "Alice Doe"],
      ["svc:chatgpt:work", "svc:chatgpt:work"],
      ["system:policy", undefined],
    ]);

    expect(await screen.findByText("Alice Doe")).toBeInTheDocument();
    expect(screen.getByText("ChatGPT (work)")).toBeInTheDocument();
    expect(screen.getByText("a standing rule")).toBeInTheDocument();
  });

  // Nothing server-side resolves a key, so this is the one lookup left.
  it("names a key, and does not say key twice", async () => {
    mount(
      [["key:k1", undefined], ["key:k2", undefined], ["key:k3", undefined]],
      [key("Grafana", "k1"), key("api key", "k2"), key("API-Key", "k3")],
    );

    expect(await screen.findByText("the Grafana key")).toBeInTheDocument();
    expect(screen.getByText("the api key")).toBeInTheDocument();
    expect(screen.getByText("the API-Key")).toBeInTheDocument();
  });

  // The word boundary is the point: a key called Monkey ends in those three
  // letters and is not a key called "key".
  it("keeps the noun on a name that merely ends in those letters", async () => {
    mount([["key:k1", undefined]], [key("Monkey", "k1")]);
    expect(await screen.findByText("the Monkey key")).toBeInTheDocument();
  });

  // A name is a convenience; losing the lookup leaves the words that needed
  // no request.
  it("falls back to words when the accounts cannot be read", async () => {
    vi.spyOn(api, "keys").mockRejectedValue(new Error("nope"));
    renderWith(<Probe actors={[["key:k1", undefined]]} />, {
      session: sessionFor("admin"),
    });
    expect(await screen.findByText("a key")).toBeInTheDocument();
  });
});
