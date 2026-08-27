import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Markdown } from "./markdown";

// The real thing, as GitHub returns it for v0.4.0.
const RELEASE_NOTES = `## [0.4.0](https://github.com/dreulavelle/mcpd/compare/v0.3.0...v0.4.0) (2026-08-27)


### Features

* measure what a call costs, and stop paying twice ([#84](https://github.com/dreulavelle/mcpd/issues/84)) ([ec00c61](https://github.com/dreulavelle/mcpd/commit/ec00c618b65e5d59507777230b49eccd24cb2950))
`;

describe("release notes", () => {
  /**
   * They arrived as markdown and were shown as markdown: hashes, asterisks and
   * bracketed URLs, in a monospace block. Everything needed was on screen and
   * none of it was readable.
   */
  it("renders a changelog as headings, bullets and links", () => {
    render(<Markdown text={RELEASE_NOTES} />);

    // The version heading, with its compare link intact.
    const compare = screen.getByRole("link", { name: "0.4.0" });
    expect(compare).toHaveAttribute(
      "href",
      "https://github.com/dreulavelle/mcpd/compare/v0.3.0...v0.4.0",
    );
    expect(screen.getByText(/2026-08-27/)).toBeInTheDocument();
    expect(screen.getByText("Features")).toBeInTheDocument();

    // The change itself is a list item, not a line of asterisks.
    const item = screen.getByRole("listitem");
    expect(item).toHaveTextContent("measure what a call costs, and stop paying twice");

    // Both of the links release-please appends survive.
    expect(screen.getByRole("link", { name: "#84" })).toHaveAttribute(
      "href", "https://github.com/dreulavelle/mcpd/issues/84",
    );
    expect(screen.getByRole("link", { name: "ec00c61" })).toBeInTheDocument();

    // And none of the syntax is left on screen.
    expect(screen.queryByText(/^##/)).not.toBeInTheDocument();
  });

  it("opens links in a new tab without handing over the opener", () => {
    render(<Markdown text={RELEASE_NOTES} />);
    const link = screen.getByRole("link", { name: "#84" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toContain("noopener");
  });

  /**
   * These notes come from the GitHub API, which is content this host did not
   * write. A link that runs is the reason the renderer builds elements rather
   * than HTML, and the reason a scheme it will not follow is shown as text.
   */
  it("will not render a link that runs", () => {
    render(<Markdown text="* [click me](javascript:alert(1))" />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    // Shown as it was written, so the note is not silently missing something.
    expect(screen.getByRole("listitem")).toHaveTextContent("[click me](javascript:alert(1))");
  });

  it("treats markup in the source as text, never as elements", () => {
    const { container } = render(
      <Markdown text={"A note with <script>alert(1)</script> in it."} />,
    );
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
  });

  it("renders bold and code without leaving their markers behind", () => {
    render(<Markdown text="Set **max_items** with `--limit` first." />);
    expect(screen.getByText("max_items").tagName).toBe("STRONG");
    expect(screen.getByText("--limit").tagName).toBe("CODE");
    expect(screen.queryByText(/\*\*/)).not.toBeInTheDocument();
  });

  /**
   * A document outside the subset must be ugly rather than missing. Dropping
   * what it could not parse would lose the one thing somebody opened the panel
   * to read.
   */
  it("keeps what it cannot parse", () => {
    render(<Markdown text={"| a | b |\n| - | - |\n| 1 | 2 |"} />);
    expect(screen.getByText(/\| a \| b \|/)).toBeInTheDocument();
  });
});
