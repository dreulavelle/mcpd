import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Grant, type Plugin } from "@/lib/api";
import { GrantsPicker } from "./GrantsPicker";

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
function mount(value: Grant[] = []) {
  const onChange = vi.fn();
  render(<GrantsPicker id="reach" value={value} onChange={onChange} subject="this key" />);
  return onChange;
}

beforeEach(() => { vi.restoreAllMocks(); });

/**
 * This used to be a text box asking for a comma-separated list.
 *
 * That asks somebody to know a plugin's exact name and spell it, and to find
 * out they were wrong only when the grant silently reaches nothing -- a name
 * matching no plugin is not an error, it is a grant to a system that does not
 * exist. Now each system is offered by name, held at none, read or write, so
 * "read-only" lives per system rather than as one flag over the whole grant.
 */
describe("choosing what a grant reaches", () => {
  it("offers the systems this host actually has", async () => {
    stub([plugin("cnmaestro", "Cambium cnMaestro"), plugin("echo")]);
    mount();

    expect(await screen.findByLabelText(/Cambium cnMaestro access/)).toBeInTheDocument();
    expect(screen.getByLabelText(/echo access/)).toBeInTheDocument();
    // The name as well as the title: the name is what the grant stores and
    // what an audit record will say.
    expect(screen.getByText("cnmaestro")).toBeInTheDocument();
  });

  it("hands back the plugin's name at the level chosen, not its title", async () => {
    stub([plugin("cnmaestro", "Cambium cnMaestro")]);
    const onChange = mount();

    await userEvent.selectOptions(await screen.findByLabelText(/Cambium cnMaestro access/), "write");
    expect(onChange).toHaveBeenCalledWith([{ plugin: "cnmaestro", level: "write" }]);
  });

  it("can hold a system at read only, independently of the others", async () => {
    stub([plugin("cnmaestro"), plugin("echo")]);
    const onChange = mount([{ plugin: "echo", level: "write" }]);

    await userEvent.selectOptions(await screen.findByLabelText(/cnmaestro access/), "read");
    expect(onChange).toHaveBeenCalledWith([
      { plugin: "echo", level: "write" },
      { plugin: "cnmaestro", level: "read" },
    ]);
  });

  it("sends the wildcard the server understands for everything, read and write", async () => {
    stub([plugin("echo")]);
    const onChange = mount();

    await userEvent.selectOptions(screen.getByLabelText("Can reach"), "write");
    expect(onChange).toHaveBeenCalledWith([{ plugin: "*", level: "write" }]);
  });

  it("offers the wildcard at read only too", async () => {
    stub([plugin("echo")]);
    const onChange = mount();

    await userEvent.selectOptions(screen.getByLabelText("Can reach"), "read");
    expect(onChange).toHaveBeenCalledWith([{ plugin: "*", level: "read" }]);
  });

  // Otherwise a grant would claim to be both "everything" and "these two",
  // and which one the server honoured would be a question about its parser.
  it("drops the wildcard once systems are chosen by hand again", async () => {
    stub([plugin("echo")]);
    const onChange = mount([{ plugin: "*", level: "write" }]);

    await userEvent.selectOptions(screen.getByLabelText("Can reach"), "some");
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it("clears one system's access without touching the others", async () => {
    stub([plugin("echo"), plugin("cnmaestro")]);
    const onChange = mount([
      { plugin: "echo", level: "write" }, { plugin: "cnmaestro", level: "write" },
    ]);

    await userEvent.selectOptions(await screen.findByLabelText(/echo access/), "");
    expect(onChange).toHaveBeenCalledWith([{ plugin: "cnmaestro", level: "write" }]);
  });

  // An empty box would read as a host with no systems, which is a different
  // and much more alarming fact than a grant nobody has narrowed yet.
  it("says why there is nothing to choose", async () => {
    stub([]);
    mount();

    expect(await screen.findByText(/no systems to grant yet/i)).toBeInTheDocument();
  });

  it("shows nothing to choose rather than a wall of selects while it asks", () => {
    vi.spyOn(api, "plugins").mockReturnValue(new Promise(() => {}));
    mount();

    expect(screen.getByText(/Looking up this host's systems/)).toBeInTheDocument();
  });

  // Nothing chosen is the safe default rather than an incomplete form, so it
  // has to say so rather than leave the fieldset looking abandoned.
  it("says that choosing nothing reaches nothing", async () => {
    stub([plugin("echo")]);
    mount();

    expect(await screen.findByText(/Nothing chosen reaches nothing/i)).toBeInTheDocument();
  });
});
