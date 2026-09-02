import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { RouterProvider, useQueryParam, useRouter } from "./router";

function wrapper({ children }: { children: ReactNode }) {
  return <RouterProvider>{children}</RouterProvider>;
}

describe("a filter kept in the address", () => {
  it("arrives set when the address carries it", () => {
    window.history.replaceState(null, "", "/activity?principal=key%3Aabc&hours=1");
    const { result } = renderHook(() => useQueryParam("principal"), { wrapper });
    expect(result.current[0]).toBe("key:abc");
  });

  it("writes into the address without leaving the page or adding history", () => {
    window.history.replaceState(null, "", "/activity");
    const before = window.history.length;
    const { result } = renderHook(() => useQueryParam("outcome"), { wrapper });
    act(() => result.current[1]("denied"));
    expect(window.location.pathname).toBe("/activity");
    expect(window.location.search).toBe("?outcome=denied");
    expect(result.current[0]).toBe("denied");
    expect(window.history.length).toBe(before);
  });

  // "No filter" is the plain address, not one carrying an empty parameter.
  it("removes the key rather than leaving it empty", () => {
    window.history.replaceState(null, "", "/activity?outcome=denied&hours=1");
    const { result } = renderHook(() => useQueryParam("outcome"), { wrapper });
    act(() => result.current[1](""));
    expect(window.location.search).toBe("?hours=1");
  });

  it("is carried by navigate and dropped when a link leaves it off", () => {
    window.history.replaceState(null, "", "/");
    const { result } = renderHook(() => useRouter(), { wrapper });
    act(() => result.current.navigate("/approvals?state=indeterminate"));
    expect(result.current.path).toBe("/approvals");
    expect(result.current.search).toBe("?state=indeterminate");
    act(() => result.current.navigate("/approvals"));
    expect(result.current.search).toBe("");
  });
});
