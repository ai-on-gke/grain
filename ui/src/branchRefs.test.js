import { afterEach, describe, expect, it, vi } from "vitest";
import { branchRefs, clearBranchRefCache } from "./branchRefs.js";
import api from "./api.js";

vi.mock("./api.js", () => ({ default: vi.fn() }));

describe("branchRefs", () => {
  afterEach(() => {
    api.mockReset();
    clearBranchRefCache();
  });

  it("asks GitHub, through grain, for the repo's refs", async () => {
    api.mockResolvedValue(["main", "grain/task-12"]);

    expect(await branchRefs("acme/widgets")).toEqual(["main", "grain/task-12"]);
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/github-branches");
  });

  it("asks once for however many readers want the same repo", async () => {
    api.mockResolvedValue(["main"]);

    await Promise.all([branchRefs("acme/widgets"), branchRefs("acme/widgets")]);
    await branchRefs("acme/widgets");

    expect(api).toHaveBeenCalledTimes(1);
  });

  // "grain cannot tell" is not "the branch is not there": a caller
  // deciding whether to offer a branch comparison has to be able to tell
  // the two apart, so a failed read is raised rather than answered with
  // an empty list -- and not kept, so the next reader asks again.
  it("raises a failed read, and asks again next time", async () => {
    api.mockRejectedValueOnce(new Error("no credential"));
    await expect(branchRefs("acme/widgets")).rejects.toThrow("no credential");

    api.mockResolvedValueOnce(["main"]);
    expect(await branchRefs("acme/widgets")).toEqual(["main"]);
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("asks about a host-qualified repo with its host in the owner segment", async () => {
    api.mockResolvedValue(["main"]);

    await branchRefs("git.forge.example/experimental/bsalmon-test");

    expect(api).toHaveBeenCalledWith(
      "/api/repos/git.forge.example%2Fexperimental/bsalmon-test/github-branches",
    );
  });

  it("percent-encodes the repo it asks about", async () => {
    api.mockResolvedValue([]);

    await branchRefs("acme/wid gets");

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/wid%20gets/github-branches",
    );
  });
});
