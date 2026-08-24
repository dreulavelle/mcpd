import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Plugin } from "@/lib/api";
import { ReachPicker } from "./ReachPicker";

function plugin(name: string, title = ""): Plugin {
  return {
    name, type: name, version: "1", title, description: "",
    endpoint: `/${name}`, connect_url: `https://host/${name}`,
    health: "healthy", tools: [], mutations: [], required: false,
    settings: [],
  };
}

function stub(plugins: Plugin[]) {
  vi.spyOn(api, "plugins").mockResolvedValue({ plugins, count: plugins.length });
}

/** Renders the picker and reports what it last handed back. */
function mount(value: string[] = []) {
  const onChange = vi.fn();
  render(<ReachPicker id="reach" value={value} onChange={onChange} subject="this key" />);
  return onChange;
}

beforeEach(() => { vi.restoreAllMocks(); });

/**
 * This used to be a text box asking for a comma-separated list.
 *
 * That asks somebody to know a plugin's exact name and spell it, and to find
 * out they were wrong only when the grant silently reaches nothing — a name
 * matching no plugin is not an error, it is a grant to a system that does not
 * exist.
 */
describe("choosing what a grant reaches", () => {
  it("offers the systems this host actually has", async () => {
    stub([plugin("cnmaestro", "Cambium cnMaestro"), plugin("echo")]);
    mount();

    expect(await screen.findByRole("checkbox", { name: /Cambium cnMaestro/ }))
      .toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /echo/ })).toBeInTheDocument();
    // The name as well as the title: the name is what the grant stores and
    // what an audit record will say.
    expect(screen.getByText("cnmaestro")).toBeInTheDocument();
  });

  it("hands back the plugin's name, not its title", async () => {
    stub([plugin("cnmaestro", "Cambium cnMaestro")]);
    const onChange = mount();

    await userEvent.click(await screen.findByRole("checkbox", { name: /Cambium/ }));
    expect(onChange).toHaveBeenCalledWith(["cnmaestro"]);
  });

  it("sends the wildcard the server understands for everything", async () => {
    stub([plugin("echo")]);
    const onChange = mount();

    await userEvent.selectOptions(screen.getByLabelText("Can reach"), "all");
    expect(onChange).toHaveBeenCalledWith(["*"]);
  });

  // Otherwise a grant would claim to be both "everything" and "these two",
  // and which one the server honoured would be a question about its parser.
  it("drops the wildcard once a system is chosen", async () => {
    stub([plugin("echo")]);
    const onChange = mount(["*"]);

    await userEvent.selectOptions(screen.getByLabelText("Can reach"), "some");
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it("unticks back off the list", async () => {
    stub([plugin("echo"), plugin("cnmaestro")]);
    const onChange = mount(["echo", "cnmaestro"]);

    await userEvent.click(await screen.findByRole("checkbox", { name: /echo/ }));
    expect(onChange).toHaveBeenCalledWith(["cnmaestro"]);
  });

  // An empty box would read as a host with no systems, which is a different
  // and much more alarming fact than a grant nobody has narrowed yet.
  it("says why there is nothing to choose", async () => {
    stub([]);
    mount();

    expect(await screen.findByText(/no systems to grant yet/i)).toBeInTheDocument();
  });

  it("shows nothing to choose rather than a wall of boxes while it asks", () => {
    stub([plugin("echo")]);
    mount();

    expect(screen.getByText(/Looking up this host's systems/)).toBeInTheDocument();
  });
});
